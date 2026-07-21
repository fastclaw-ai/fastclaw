package agent

// Integration tests for the imageproc wiring (issue #106): uploaded
// images are sniffed, policy-gated and compressed before storage and
// before vision inlining; downgraded images are materialized to the
// workspace but excluded from `image_url` content parts.

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

// noisyJPEGDataURL builds a data URL carrying an incompressible w×h
// JPEG, large enough to trip the compression ladder.
func noisyJPEGDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(7))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Intn(256))
		if i%4 == 3 {
			img.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func tinyPNGDataURL(t *testing.T) string {
	t.Helper()
	// 1x1 transparent PNG.
	const b64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	if _, err := base64.StdEncoding.DecodeString(b64); err != nil {
		t.Fatalf("fixture not base64: %v", err)
	}
	return "data:image/png;base64," + b64
}

// --- processImageAttachment ---

func TestProcessImageAttachmentCompressesAndFixesExt(t *testing.T) {
	u := noisyJPEGDataURL(t, 4000, 3000)
	payload, err := base64.StdEncoding.DecodeString(strings.SplitN(u, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	out, ext, proc := processImageAttachment(payload, "image/jpeg", ".jpg")
	if proc.Decision.String() != "accept" {
		t.Fatalf("decision = %v note=%q", proc.Decision, proc.Note)
	}
	if !proc.Reencoded {
		t.Fatal("expected re-encode for 4000x3000 noisy jpeg")
	}
	if len(out) >= len(payload) {
		t.Errorf("stored bytes not compressed: %d >= %d", len(out), len(payload))
	}
	if ext != ".jpg" {
		t.Errorf("ext = %q, want .jpg", ext)
	}
}

func TestProcessImageAttachmentMismatchGetsEffectiveExt(t *testing.T) {
	// Declared .png, actually JPEG: sniffed format wins, so the stored
	// file must get the JPEG extension.
	u := noisyJPEGDataURL(t, 4000, 3000)
	payload, _ := base64.StdEncoding.DecodeString(strings.SplitN(u, ",", 2)[1])
	_, ext, proc := processImageAttachment(payload, "image/png", ".png")
	if proc.MIME != "image/jpeg" {
		t.Fatalf("effective mime = %s, want image/jpeg", proc.MIME)
	}
	if ext != ".jpg" {
		t.Errorf("ext = %q, want .jpg (sniffed format wins)", ext)
	}
}

func TestProcessImageAttachmentDowngradeKeepsOriginal(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	out, ext, proc := processImageAttachment(svg, "image/svg+xml", ".svg")
	if proc.Decision.String() != "downgrade" {
		t.Fatalf("decision = %v, want downgrade", proc.Decision)
	}
	if !bytes.Equal(out, svg) {
		t.Error("downgraded bytes must pass through untouched")
	}
	if ext != ".svg" {
		t.Errorf("ext = %q, want .svg", ext)
	}
}

func TestProcessImageAttachmentNonImageUntouched(t *testing.T) {
	pdf := []byte("%PDF-1.4 hello")
	out, ext, proc := processImageAttachment(pdf, "application/pdf", ".pdf")
	if proc.Decision.String() != "downgrade" {
		t.Fatalf("non-image decision = %v, want downgrade", proc.Decision)
	}
	if !bytes.Equal(out, pdf) || ext != ".pdf" {
		t.Errorf("non-image attachment changed: ext=%q", ext)
	}
}

// --- WriteSessionAttachments end to end ---

func TestWriteSessionAttachmentsStoresAndFlags(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{workspacePath: dir}

	svgURL := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	results := a.WriteSessionAttachments(context.Background(), "s1", "", []Attachment{
		{URL: tinyPNGDataURL(t)},               // 0: accepted, vision-safe
		{URL: svgURL},                          // 1: downgraded
		{URL: noisyJPEGDataURL(t, 4000, 3000)}, // 2: accepted + compressed
	})
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}

	// Every item — including the downgraded SVG — is materialized to the
	// workspace, so the agent can still reach it via tools.
	for _, r := range results {
		if _, err := os.Stat(filepath.Join(dir, r.Path)); err != nil {
			t.Errorf("result %d (%s) not on disk: %v", r.Index, r.Path, err)
		}
	}

	if !results[0].VisionSafe {
		t.Error("tiny png should be vision-safe")
	}
	if results[1].VisionSafe {
		t.Error("svg must NOT be vision-safe")
	}
	if results[2].VisionSafe != true || results[2].URL == "" {
		t.Errorf("compressed jpeg should be vision-safe with rewritten URL: %+v", results[2])
	}

	// The compressed image landed on disk compressed, and its inline URL
	// carries the same smaller bytes.
	stored, err := os.ReadFile(filepath.Join(dir, results[2].Path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(results[2].URL, "data:image/jpeg;base64,") {
		t.Errorf("inline URL not rewritten: %.40s", results[2].URL)
	}
	inlineBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(results[2].URL, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, inlineBytes) {
		t.Error("stored bytes and inline URL bytes differ")
	}
	// Result indexes align with input order (VisionGate's contract).
	for i, r := range results {
		if r.Index != i {
			t.Errorf("result %d has Index %d", i, r.Index)
		}
	}
}

// --- VisionGate ---

func TestVisionGate(t *testing.T) {
	candidates := []string{"u0", "u1", "u2", "u3"}
	results := []AttachmentResult{
		{Index: 0, Path: "a.png", VisionSafe: true, URL: "u0"},
		{Index: 1, Path: "b.svg", VisionSafe: false, URL: "u1"},
		{Index: 2, Path: "c.jpg", VisionSafe: true, URL: "data:image/jpeg;base64,COMPRESSED"},
		// Index 3 absent: decode failed server-side → passes through.
	}
	got := VisionGate(candidates, results)
	want := []string{"u0", "data:image/jpeg;base64,COMPRESSED", "u3"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if VisionGate(nil, results) != nil {
		t.Error("nil candidates should yield nil")
	}
}

// --- buildUserMessage vision gate ---

func TestBuildUserMessageDropsDowngradedImages(t *testing.T) {
	svgURL := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	msg := buildUserMessage(bus.InboundMessage{
		Text:      "look at these",
		PhotoURLs: []string{tinyPNGDataURL(t), svgURL},
	})
	var images, texts int
	for _, p := range msg.ContentParts {
		switch p.Type {
		case "image_url":
			images++
			if strings.Contains(p.ImageURL.URL, "svg") {
				t.Error("downgraded svg leaked into image_url part")
			}
		case "text":
			texts++
		}
	}
	if images != 1 {
		t.Errorf("image_url parts = %d, want 1 (only the valid png)", images)
	}
	if texts != 1 {
		t.Errorf("text parts = %d, want 1", texts)
	}
}

func TestBuildUserMessageCompressesInlineDataURL(t *testing.T) {
	big := noisyJPEGDataURL(t, 4000, 3000)
	msg := buildUserMessage(bus.InboundMessage{PhotoURL: big})
	if len(msg.ContentParts) != 1 || msg.ContentParts[0].Type != "image_url" {
		t.Fatalf("parts = %+v", msg.ContentParts)
	}
	got := msg.ContentParts[0].ImageURL.URL
	if len(got) >= len(big) {
		t.Errorf("inline data URL not compressed: %d >= %d", len(got), len(big))
	}
	if !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Errorf("unexpected url prefix: %.40s", got)
	}
}

func TestBuildUserMessageAllImagesDowngraded(t *testing.T) {
	svgURL := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	msg := buildUserMessage(bus.InboundMessage{PhotoURLs: []string{svgURL}})
	if len(msg.ContentParts) != 0 {
		t.Errorf("downgraded-only send must emit no content parts, got %+v", msg.ContentParts)
	}
	// No breadcrumb was added by the caller here; the message degrades
	// to an empty plain user message rather than a bogus vision message.
	if msg.Content != "" {
		t.Errorf("content = %q, want empty", msg.Content)
	}
}

func TestBuildUserMessageHTTPURLPassesThrough(t *testing.T) {
	// http(s) URLs can't be sniffed without a fetch; they pass the
	// buildUserMessage gate untouched (server-side materialization via
	// VisionGate already filtered known-bad ones).
	msg := buildUserMessage(bus.InboundMessage{PhotoURLs: []string{"https://x/photo.jpg"}})
	if len(msg.ContentParts) != 1 || msg.ContentParts[0].ImageURL.URL != "https://x/photo.jpg" {
		t.Errorf("http url mangled: %+v", msg.ContentParts)
	}
}
