package imageproc

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net/url"
	"strings"
	"testing"
)

// --- helpers to synthesize test images ---

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, noisyImage(t, w, h), &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

// noisyImage returns an image of incompressible random pixels, so the
// encoded size tracks the pixel count (flat-color images compress to
// nothing and would defeat byte-budget assertions).
func noisyImage(t *testing.T, w, h int) image.Image {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(42))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Intn(256))
		if i%4 == 3 {
			img.Pix[i] = 255 // opaque alpha
		}
	}
	return img
}

func gifBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("gif encode: %v", err)
	}
	return buf.Bytes()
}

// --- sniffing ---

func TestSniffImageFormat(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want Format
	}{
		{"png", pngBytes(t, 4, 4), FormatPNG},
		{"jpeg", jpegBytes(t, 4, 4, 80), FormatJPEG},
		{"gif", gifBytes(t, 4, 4), FormatGIF},
		{"webp-riff-header", append([]byte("RIFF\x00\x00\x00\x00WEBP"), 0x00), FormatWebP},
		{"bmp", []byte("BM........"), FormatUnknown},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), FormatUnknown},
		{"avif", []byte("\x00\x00\x00\x1cftypavif"), FormatUnknown},
		{"pdf", []byte("%PDF-1.4"), FormatUnknown},
		{"empty", nil, FormatUnknown},
		{"short", []byte{0x89}, FormatUnknown},
	}
	for _, c := range cases {
		if got := SniffImageFormat(c.data); got != c.want {
			t.Errorf("%s: SniffImageFormat = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestImageDimensions(t *testing.T) {
	t.Run("png", func(t *testing.T) {
		w, h, ok := ImageDimensions(pngBytes(t, 320, 240), FormatPNG)
		if !ok || w != 320 || h != 240 {
			t.Errorf("png dims = %dx%d ok=%v", w, h, ok)
		}
	})
	t.Run("jpeg", func(t *testing.T) {
		w, h, ok := ImageDimensions(jpegBytes(t, 640, 480, 80), FormatJPEG)
		if !ok || w != 640 || h != 480 {
			t.Errorf("jpeg dims = %dx%d ok=%v", w, h, ok)
		}
	})
	t.Run("gif", func(t *testing.T) {
		w, h, ok := ImageDimensions(gifBytes(t, 100, 50), FormatGIF)
		if !ok || w != 100 || h != 50 {
			t.Errorf("gif dims = %dx%d ok=%v", w, h, ok)
		}
	})
	t.Run("webp-vp8x", func(t *testing.T) {
		// Minimal VP8X container: RIFF size + WEBP + VP8X chunk header,
		// canvas (w-1),(h-1) as 24-bit LE at bytes 24..29.
		b := make([]byte, 30)
		copy(b[0:4], "RIFF")
		copy(b[8:12], "WEBP")
		copy(b[12:16], "VP8X")
		w, h := 1023, 767 // stored minus one
		b[24] = byte(w)
		b[25] = byte(w >> 8)
		b[26] = byte(w >> 16)
		b[27] = byte(h)
		b[28] = byte(h >> 8)
		b[29] = byte(h >> 16)
		gotW, gotH, ok := ImageDimensions(b, FormatWebP)
		if !ok || gotW != 1024 || gotH != 768 {
			t.Errorf("webp vp8x dims = %dx%d ok=%v", gotW, gotH, ok)
		}
	})
	t.Run("webp-vp8l", func(t *testing.T) {
		b := make([]byte, 25)
		copy(b[0:4], "RIFF")
		copy(b[8:12], "WEBP")
		copy(b[12:16], "VP8L")
		b[20] = 0x2F
		v := uint32(299) | uint32(199)<<14 // (w-1) | (h-1)<<14
		binary.LittleEndian.PutUint32(b[21:25], v)
		gotW, gotH, ok := ImageDimensions(b, FormatWebP)
		if !ok || gotW != 300 || gotH != 200 {
			t.Errorf("webp vp8l dims = %dx%d ok=%v", gotW, gotH, ok)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		if _, _, ok := ImageDimensions([]byte{0x89, 'P', 'N', 'G'}, FormatPNG); ok {
			t.Error("truncated png should report ok=false")
		}
	})
	t.Run("unknown-format", func(t *testing.T) {
		if _, _, ok := ImageDimensions([]byte("whatever"), FormatUnknown); ok {
			t.Error("unknown format should report ok=false")
		}
	})
}

// --- policy ---

func TestNormalizeMIME(t *testing.T) {
	cases := map[string]string{
		"Image/PNG":             "image/png",
		"image/jpg":             "image/jpeg",
		"image/jpeg; charset=x": "image/jpeg",
		"  image/webp ":         "image/webp",
		"":                      "",
	}
	for in, want := range cases {
		if got := NormalizeMIME(in); got != want {
			t.Errorf("NormalizeMIME(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcessAllowlist(t *testing.T) {
	resetLimitsForTest()
	t.Run("accept-sniffed-png", func(t *testing.T) {
		res := Process(pngBytes(t, 10, 10), "image/png")
		if res.Decision != DecisionAccept || res.MIME != "image/png" {
			t.Errorf("got %v/%s", res.Decision, res.MIME)
		}
	})
	t.Run("sniff-wins-over-declared", func(t *testing.T) {
		// Declared as avif (not in allowlist) but bytes are PNG → accept.
		res := Process(pngBytes(t, 10, 10), "image/avif")
		if res.Decision != DecisionAccept || res.MIME != "image/png" {
			t.Errorf("sniffed png should win: got %v/%s", res.Decision, res.MIME)
		}
	})
	t.Run("declared-png-but-actually-bmp", func(t *testing.T) {
		res := Process([]byte("BM\x86\x00\x00\x00...."), "image/png")
		if res.Decision != DecisionDowngrade {
			t.Errorf("bmp bytes must downgrade, got %v", res.Decision)
		}
		if string(res.Bytes) != "BM\x86\x00\x00\x00...." {
			t.Error("downgrade must return original bytes untouched")
		}
	})
	t.Run("fake-avif", func(t *testing.T) {
		res := Process([]byte("\x00\x00\x00\x1cftypavif\x00\x00"), "image/avif")
		if res.Decision != DecisionDowngrade {
			t.Errorf("avif must downgrade, got %v", res.Decision)
		}
	})
	t.Run("svg", func(t *testing.T) {
		res := Process([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "image/svg+xml")
		if res.Decision != DecisionDowngrade {
			t.Errorf("svg must downgrade, got %v", res.Decision)
		}
	})
	t.Run("non-image", func(t *testing.T) {
		res := Process([]byte("%PDF-1.4 fake"), "application/pdf")
		if res.Decision != DecisionDowngrade {
			t.Errorf("pdf must downgrade, got %v", res.Decision)
		}
	})
	t.Run("corrupt-image-payload", func(t *testing.T) {
		// Valid PNG magic + plausible small IHDR, then garbage: header
		// claims 4000x3000 so the compression path runs and decode fails.
		b := make([]byte, 64)
		copy(b, "\x89PNG\r\n\x1a\n")
		copy(b[12:16], "IHDR")
		binary.BigEndian.PutUint32(b[16:20], 4000)
		binary.BigEndian.PutUint32(b[20:24], 3000)
		res := Process(b, "image/png")
		if res.Decision != DecisionDowngrade {
			t.Errorf("corrupt payload must downgrade, got %v", res.Decision)
		}
	})
}

// --- passthrough behavior ---

func TestProcessSmallImagePassthrough(t *testing.T) {
	resetLimitsForTest()
	orig := pngBytes(t, 100, 50)
	res := Process(orig, "image/png")
	if res.Decision != DecisionAccept {
		t.Fatalf("decision = %v", res.Decision)
	}
	if res.Reencoded {
		t.Error("small image must not be re-encoded")
	}
	if !bytes.Equal(res.Bytes, orig) {
		t.Error("passthrough bytes differ from original")
	}
	if res.Width != 100 || res.Height != 50 || res.OrigWidth != 100 || res.OrigHeight != 50 {
		t.Errorf("dims = %+v", res)
	}
}

func TestProcessGIFPassthrough(t *testing.T) {
	resetLimitsForTest()
	// Even a GIF whose dimensions exceed the max edge must pass through
	// untouched to preserve animation frames.
	orig := gifBytes(t, 3000, 2000)
	res := Process(orig, "image/gif")
	if res.Decision != DecisionAccept {
		t.Fatalf("decision = %v", res.Decision)
	}
	if res.Reencoded || !bytes.Equal(res.Bytes, orig) {
		t.Error("gif must pass through unchanged")
	}
	if res.MIME != "image/gif" {
		t.Errorf("mime = %s", res.MIME)
	}
}

// --- compression ladder ---

func TestProcessLargeImageShrinks(t *testing.T) {
	resetLimitsForTest()
	// 4000x3000 of pure noise: several MB as JPEG, over the byte budget.
	orig := jpegBytes(t, 4000, 3000, 90)
	if len(orig) <= byteBudget {
		t.Fatalf("test image too small (%d bytes) to exercise the ladder", len(orig))
	}
	res := Process(orig, "image/jpeg")
	if res.Decision != DecisionAccept {
		t.Fatalf("decision = %v note=%q", res.Decision, res.Note)
	}
	if !res.Reencoded {
		t.Fatal("expected re-encode")
	}
	if len(res.Bytes) > byteBudget {
		t.Errorf("still over budget: %d > %d", len(res.Bytes), byteBudget)
	}
	if len(res.Bytes) >= len(orig) {
		t.Errorf("no shrink: %d -> %d", len(orig), len(res.Bytes))
	}
	if max(res.Width, res.Height) > maxEdgePx {
		t.Errorf("edge not clamped: %dx%d", res.Width, res.Height)
	}
	if res.Width != 2000 || res.Height != 1500 {
		t.Errorf("final dims = %dx%d, want 2000x1500", res.Width, res.Height)
	}
	// The output must be a decodable image of the claimed MIME.
	if SniffImageFormat(res.Bytes) != FormatJPEG {
		t.Errorf("output is not jpeg: %v", SniffImageFormat(res.Bytes))
	}
}

func TestProcessLargePNGStaysLosslessWhenItFits(t *testing.T) {
	resetLimitsForTest()
	// A mostly-flat screenshot-like PNG re-encodes tiny as PNG: the
	// ladder must prefer lossless PNG over JPEG for png sources.
	img := image.NewNRGBA(image.Rect(0, 0, 3000, 2100))
	for y := 0; y < 2100; y++ {
		for x := 0; x < 3000; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 240, G: 240, B: 240, A: 255})
		}
	}
	// Draw a "text-like" stripe so it's not a single flat color.
	for y := 100; y < 200; y++ {
		for x := 100; x < 2900; x += 3 {
			img.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	res := Process(buf.Bytes(), "image/png")
	if res.Decision != DecisionAccept || !res.Reencoded {
		t.Fatalf("decision=%v reencoded=%v note=%q", res.Decision, res.Reencoded, res.Note)
	}
	if res.MIME != "image/png" {
		t.Errorf("png source should stay png when it fits, got %s", res.MIME)
	}
	if max(res.Width, res.Height) > maxEdgePx {
		t.Errorf("edge not clamped: %dx%d", res.Width, res.Height)
	}
}

// --- bomb guard ---

// fakePNGHeader builds just enough of a PNG header for the sniffer and
// dimension reader to believe the image is w x h — no pixel payload.
func fakePNGHeader(w, h int) []byte {
	b := make([]byte, 33)
	copy(b, "\x89PNG\r\n\x1a\n")
	copy(b[12:16], "IHDR")
	binary.BigEndian.PutUint32(b[16:20], uint32(w))
	binary.BigEndian.PutUint32(b[20:24], uint32(h))
	return b
}

func TestProcessBombGuardRefusesHugeHeader(t *testing.T) {
	resetLimitsForTest()
	// Header claims 20000x10000 = 200 MP without any real payload; the
	// guard must refuse BEFORE decode is attempted.
	res := Process(fakePNGHeader(20000, 10000), "image/png")
	if res.Decision != DecisionDowngrade {
		t.Fatalf("decision = %v, want downgrade", res.Decision)
	}
	if !strings.Contains(res.Note, "bomb") {
		t.Errorf("note should mention the bomb guard: %q", res.Note)
	}
}

func TestExceedsBombLimits(t *testing.T) {
	cases := []struct {
		dataLen, w, h int
		dimsOK        bool
		want          bool
	}{
		{1024, 100, 100, true, false},
		{maxDecodeInputBytes + 1, 10, 10, true, true},
		{1024, 10001, 10000, true, true},  // 100.01 MP
		{1024, 10000, 10000, true, false}, // exactly 100 MP is allowed
		{1024, 0, 0, false, false},        // unknown dims don't trip the guard
	}
	for _, c := range cases {
		if got := exceedsBombLimits(c.dataLen, c.w, c.h, c.dimsOK); got != c.want {
			t.Errorf("exceedsBombLimits(%d,%d,%d,%v) = %v, want %v",
				c.dataLen, c.w, c.h, c.dimsOK, got, c.want)
		}
	}
}

// --- env overrides ---

func TestEnvOverrides(t *testing.T) {
	resetLimitsForTest()
	t.Cleanup(resetLimitsForTest)
	t.Setenv("FASTCLAW_IMAGE_MAX_EDGE_PX", "500")
	t.Setenv("FASTCLAW_IMAGE_BYTE_BUDGET", "300000")

	orig := jpegBytes(t, 2000, 1500, 90)
	res := Process(orig, "image/jpeg")
	if res.Decision != DecisionAccept || !res.Reencoded {
		t.Fatalf("decision=%v reencoded=%v note=%q", res.Decision, res.Reencoded, res.Note)
	}
	if max(res.Width, res.Height) > 500 {
		t.Errorf("env max edge ignored: %dx%d", res.Width, res.Height)
	}
	if len(res.Bytes) > 300000 {
		t.Errorf("env byte budget ignored: %d", len(res.Bytes))
	}
}

// --- ProcessDataURL ---

func TestProcessDataURLRoundTrip(t *testing.T) {
	resetLimitsForTest()
	orig := jpegBytes(t, 4000, 3000, 90)
	in := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(orig)
	out, res, ok := ProcessDataURL(in)
	if !ok {
		t.Fatal("ok = false")
	}
	if res.Decision != DecisionAccept {
		t.Fatalf("decision = %v", res.Decision)
	}
	if !strings.HasPrefix(out, "data:image/jpeg;base64,") {
		t.Errorf("output url prefix wrong: %.40s", out)
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(out, "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatalf("output not valid base64: %v", err)
	}
	if len(payload) > byteBudget {
		t.Errorf("payload over budget: %d", len(payload))
	}
	if len(payload) >= len(orig) {
		t.Errorf("no shrink through data-url round trip")
	}
}

func TestProcessDataURLDeclaresWrongMIME(t *testing.T) {
	resetLimitsForTest()
	// Declared webp, actually PNG: the sniffed format must win and the
	// re-emitted data URL must carry image/png.
	in := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(pngBytes(t, 10, 10))
	out, res, ok := ProcessDataURL(in)
	if !ok || res.Decision != DecisionAccept {
		t.Fatalf("ok=%v decision=%v", ok, res.Decision)
	}
	if !strings.HasPrefix(out, "data:image/png;base64,") {
		t.Errorf("effective mime not applied: %.40s", out)
	}
}

func TestProcessDataURLDowngradeKeepsOriginal(t *testing.T) {
	resetLimitsForTest()
	in := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	out, res, ok := ProcessDataURL(in)
	if !ok {
		t.Fatal("ok = false (parse succeeded; decision is the downgrade)")
	}
	if res.Decision != DecisionDowngrade {
		t.Fatalf("decision = %v", res.Decision)
	}
	if out != in {
		t.Error("downgrade must return the original data URL unchanged")
	}
}

func TestProcessDataURLURLEncodedPayload(t *testing.T) {
	resetLimitsForTest()
	// Non-base64 payloads arrive urlencoded, mirroring decodeDataURL.
	in := "data:image/png," + url.QueryEscape(string(pngBytes(t, 8, 8)))
	out, res, ok := ProcessDataURL(in)
	if !ok || res.Decision != DecisionAccept {
		t.Fatalf("ok=%v decision=%v note=%q", ok, res.Decision, res.Note)
	}
	if !strings.HasPrefix(out, "data:image/png;base64,") {
		t.Errorf("urlencoded payload should re-emit as canonical base64: %.40s", out)
	}
}

func TestProcessDataURLMalformed(t *testing.T) {
	if _, _, ok := ProcessDataURL("not-a-data-url"); ok {
		t.Error("non data url should report ok=false")
	}
	if _, _, ok := ProcessDataURL("data:image/png;base64,!!!notb64!!!"); ok {
		t.Error("bad base64 should report ok=false")
	}
}
