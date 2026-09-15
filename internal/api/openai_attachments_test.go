package api

import (
	"encoding/json"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
)

// Wire round-trip for the OpenAI-compat endpoint: a client posts a body
// using all three attachment shapes and the helpers split them into the
// right buckets.
func TestChatCompletionRequestAttachmentsRoundTrip(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-7",
		"messages": [{"role": "user", "content": "hi"}],
		"images":    ["data:image/png;base64,AAA"],
		"imageUrls": ["https://x/photo.jpg"],
		"attachments": [
			{"url": "data:application/pdf;base64,BBB", "name": "Q4-report.pdf"},
			{"url": "https://x/archive.zip"}
		]
	}`)
	var req chatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Attachments) != 2 || req.Attachments[0].Name != "Q4-report.pdf" {
		t.Errorf("Attachments = %+v", req.Attachments)
	}

	inline := req.inlineImageURLs()
	if len(inline) != 2 {
		t.Fatalf("inline len = %d, want 2 (Images + ImageURLs only)", len(inline))
	}
	if inline[0] != "data:image/png;base64,AAA" || inline[1] != "https://x/photo.jpg" {
		t.Errorf("inline order/values wrong: %v", inline)
	}
	// Critical: a PDF / zip data URL must not leak into the inline-vision
	// slice — that would be wrapped as an image_url content block and
	// upstream providers reject the whole turn.
	for _, u := range inline {
		if u == "data:application/pdf;base64,BBB" || u == "https://x/archive.zip" {
			t.Errorf("non-image URL leaked into inline: %q", u)
		}
	}

	all := req.allAttachments()
	if len(all) != 4 {
		t.Errorf("allAttachments len = %d, want 4 (Images+ImageURLs+Attachments)", len(all))
	}
}

// VisionGate zips inline candidates against materialization results by
// INDEX; that contract only holds because inlineImageURLs() is an
// index-aligned prefix of allAttachments(). Pin it, and prove the
// downgrade flow: a sniff-rejected image stays out of the inline slice
// while its breadcrumb still names the workspace file.
func TestChatCompletionVisionGateFlow(t *testing.T) {
	req := chatCompletionRequest{
		Images:    []string{"data:image/svg+xml;base64,PHN2Zy8+"},
		ImageURLs: []string{"https://x/photo.jpg"},
		Attachments: []attachmentRequest{
			{URL: "data:application/pdf;base64,BBB", Name: "r.pdf"},
		},
	}
	inline := req.inlineImageURLs()
	all := req.allAttachments()
	for i, u := range inline {
		if all[i].URL != u {
			t.Fatalf("index %d misaligned: inline=%q all=%q", i, u, all[i].URL)
		}
	}

	// Server materialization decided: svg downgraded (sniff mismatch),
	// remote jpg accepted, pdf irrelevant to vision.
	results := []agent.AttachmentResult{
		{Index: 0, Path: "image_ab12_0.svg", VisionSafe: false},
		{Index: 1, Path: "image_ab12_1.jpg", VisionSafe: true, URL: "https://x/photo.jpg"},
		{Index: 2, Path: "r.pdf", VisionSafe: false},
	}
	got := agent.VisionGate(inline, results)
	if len(got) != 1 || got[0] != "https://x/photo.jpg" {
		t.Errorf("gated inline = %v, want only the accepted jpg", got)
	}
}

// imageUrls alone (without the Images alias) must still flow through —
// this was the original bug C: the OpenAI endpoint silently dropped it.
func TestChatCompletionImageUrlsAliasAlone(t *testing.T) {
	body := []byte(`{
		"model": "x",
		"messages": [],
		"imageUrls": ["https://x/a.png"]
	}`)
	var req chatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := req.inlineImageURLs(); len(got) != 1 || got[0] != "https://x/a.png" {
		t.Errorf("inlineImageURLs = %v", got)
	}
	if got := req.allAttachments(); len(got) != 1 || got[0].URL != "https://x/a.png" {
		t.Errorf("allAttachments = %+v", got)
	}
}
