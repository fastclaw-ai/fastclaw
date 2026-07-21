package setup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
)

// Wire round-trip: the JSON a real API client would POST decodes into
// the expected chatRequest fields, including the new Attachments shape.
func TestChatRequestJSONRoundTrip(t *testing.T) {
	body := []byte(`{
		"agentId": "a1",
		"sessionId": "s1",
		"message": "look at this",
		"imageUrls": ["https://x/foo.png"],
		"attachments": [
			{"url": "data:application/pdf;base64,AAA", "name": "report.pdf"},
			{"url": "https://x/file.zip"}
		]
	}`)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.ImageURLs) != 1 || req.ImageURLs[0] != "https://x/foo.png" {
		t.Errorf("ImageURLs = %v", req.ImageURLs)
	}
	if len(req.Attachments) != 2 {
		t.Fatalf("Attachments len = %d, want 2", len(req.Attachments))
	}
	if req.Attachments[0].Name != "report.pdf" || req.Attachments[1].Name != "" {
		t.Errorf("Attachments = %+v", req.Attachments)
	}
	// And the derived slices route correctly:
	if got := req.inlineImageURLs(); len(got) != 1 {
		t.Errorf("inlineImageURLs len = %d, want 1 (Attachments must be excluded)", len(got))
	}
	if got := req.allAttachments(); len(got) != 3 {
		t.Errorf("allAttachments len = %d, want 3", len(got))
	}
}

// inlineImageURLs must include legacy image-only fields and must NOT
// include the general-purpose Attachments field. A PDF URL leaking
// into PhotoURLs would be wrapped as `image_url` content part and
// sink the whole turn upstream.
func TestInlineImageURLsExcludesAttachments(t *testing.T) {
	req := chatRequest{
		Images:    []string{"data:image/png;base64,AAA"},
		ImageURLs: []string{"https://x/photo.jpg"},
		Attachments: []attachmentRequest{
			{URL: "data:application/pdf;base64,BBB", Name: "report.pdf"},
		},
	}
	got := req.inlineImageURLs()
	want := []string{"data:image/png;base64,AAA", "https://x/photo.jpg"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// allAttachments must include all three sources so the workspace gets
// every byte the caller sent (regardless of whether vision inlines it).
func TestAllAttachmentsCoversAllSources(t *testing.T) {
	req := chatRequest{
		Images:    []string{"u1"},
		ImageURLs: []string{"u2"},
		Attachments: []attachmentRequest{
			{URL: "u3", Name: "a.pdf"},
		},
	}
	got := req.allAttachments()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].URL != "u1" || got[1].URL != "u2" || got[2].URL != "u3" {
		t.Errorf("URLs out of order: %+v", got)
	}
	if got[2].Name != "a.pdf" {
		t.Errorf("Name not propagated: %+v", got[2])
	}
	if got[0].Name != "" || got[1].Name != "" {
		t.Errorf("legacy entries should have empty Name: %+v", got)
	}
}

// Empty input round-trips to nil rather than empty slice, so the
// downstream `len(imageURLs)==0` short-circuit fires the same way it
// did before this refactor.
func TestInlineImageURLsEmptyIsNil(t *testing.T) {
	req := chatRequest{
		Attachments: []attachmentRequest{{URL: "u", Name: "a.pdf"}},
	}
	if got := req.inlineImageURLs(); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

// VisionGate zips inline candidates against WriteSessionAttachments
// results by INDEX, which is only correct because inlineImageURLs() is
// an index-aligned prefix of allAttachments(). Pin that contract: if a
// future refactor reorders either helper, this test fails before the
// misalignment silently drops or mislabels vision images.
func TestInlineURLsArePrefixOfAllAttachments(t *testing.T) {
	req := chatRequest{
		Images:    []string{"i0", "i1"},
		ImageURLs: []string{"u2"},
		Attachments: []attachmentRequest{
			{URL: "a3", Name: "report.pdf"},
		},
	}
	inline := req.inlineImageURLs()
	all := req.allAttachments()
	if len(inline) > len(all) {
		t.Fatalf("inline longer than all: %d > %d", len(inline), len(all))
	}
	for i, u := range inline {
		if all[i].URL != u {
			t.Fatalf("index %d misaligned: inline=%q all=%q", i, u, all[i].URL)
		}
	}
}

// Handler flow for issue #106: a downgraded image (declared image/* but
// not actually a supported format) is excluded from vision inline URLs
// yet still materialized — its filename shows up in the breadcrumb
// paths handed to annotateMessageWithAttachments.
func TestVisionGateExcludesDowngradedButKeepsBreadcrumb(t *testing.T) {
	results := []agent.AttachmentResult{
		{Index: 0, Path: "image_ab12_0.svg", VisionSafe: false, URL: "data:image/svg+xml;base64,PHN2Zy8+"},
		{Index: 1, Path: "report.pdf", VisionSafe: false, URL: "data:application/pdf;base64,AAA"},
	}
	inline := agent.VisionGate([]string{"data:image/svg+xml;base64,PHN2Zy8+"}, results)
	if len(inline) != 0 {
		t.Errorf("downgraded svg must not inline, got %v", inline)
	}
	paths := attachmentPaths(results)
	if len(paths) != 2 || paths[0] != "image_ab12_0.svg" {
		t.Errorf("breadcrumb paths = %v", paths)
	}
	msg := annotateMessageWithAttachments("what is this", paths)
	if !strings.HasPrefix(msg, "[Attached: /workspace/image_ab12_0.svg]\n[Attached: /workspace/report.pdf]\n") {
		t.Errorf("breadcrumb missing downgraded file: %q", msg)
	}
}
