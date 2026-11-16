package agent

import (
	"reflect"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func inlineImage(url string) provider.ContentPart {
	return provider.ContentPart{
		Type:     "image_url",
		ImageURL: &provider.ImageURL{URL: url},
	}
}

func TestModelMessagesWithRecentImagesKeepsOnlyCurrentTurnImages(t *testing.T) {
	messages := []provider.Message{
		{
			Role: "user",
			ContentParts: []provider.ContentPart{
				{Type: "text", Text: "[Attached: /workspace/old.png]\nold"},
				inlineImage("data:image/png;base64,OLD"),
			},
		},
		{Role: "assistant", Content: "old reply"},
		{
			Role: "user",
			ContentParts: []provider.ContentPart{
				{Type: "text", Text: "[Attached: /workspace/new.png]\nnew"},
				inlineImage("data:image/png;base64,NEW"),
			},
		},
	}
	original := append([]provider.ContentPart(nil), messages[0].ContentParts...)

	got := modelMessagesWithRecentImages(messages)
	if messageHasInlineImage(got[0]) {
		t.Fatal("older image turn still contains an inline image")
	}
	if len(got[0].ContentParts) != 2 || got[0].ContentParts[0].Text == "" || got[0].ContentParts[1].Text != earlierImageOmittedText {
		t.Fatalf("older image turn should keep its breadcrumb plus a placeholder: %#v", got[0])
	}
	if !messageHasInlineImage(got[2]) {
		t.Fatal("current turn's image was stripped")
	}
	if !reflect.DeepEqual(messages[0].ContentParts, original) {
		t.Fatal("input history was mutated")
	}
}

// A later question doesn't carry an earlier picture along: it becomes a
// placeholder (one 1 MB picture used to ride on every following turn).
func TestModelMessagesWithRecentImagesDropsImageOnTextFollowUp(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", ContentParts: []provider.ContentPart{inlineImage("data:image/png;base64,ONE")}},
		{Role: "assistant", Content: "description"},
		{Role: "user", Content: "what about the icon?"},
	}

	got := modelMessagesWithRecentImages(messages)
	if messageHasInlineImage(got[0]) || got[0].Content != earlierImageOmittedText {
		t.Fatalf("earlier image should be a placeholder: %#v", got[0])
	}
}

// A burst sent together (picture, picture, "continue") is one turn: its
// pictures all go out, even with no reply between them.
func TestModelMessagesWithRecentImagesKeepsBurstImages(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", ContentParts: []provider.ContentPart{inlineImage("data:image/png;base64,A")}},
		{Role: "user", ContentParts: []provider.ContentPart{inlineImage("data:image/png;base64,B")}},
		{Role: "user", Content: "继续"},
	}

	got := modelMessagesWithRecentImages(messages)
	if !messageHasInlineImage(got[2]) || !messageHasInlineImage(got[3]) {
		t.Fatal("pictures of the current burst were stripped")
	}
}

func TestModelMessagesWithRecentImagesReplacesImageOnlyOlderTurn(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", ContentParts: []provider.ContentPart{inlineImage("data:image/png;base64,OLD")}},
		{Role: "assistant", Content: "first"},
		{Role: "user", ContentParts: []provider.ContentPart{inlineImage("data:image/png;base64,NEW")}},
	}

	got := modelMessagesWithRecentImages(messages)
	if got[0].Content != earlierImageOmittedText || len(got[0].ContentParts) != 0 {
		t.Fatalf("image-only older turn was not replaced safely: %#v", got[0])
	}
	if !messageHasInlineImage(got[2]) {
		t.Fatal("latest image turn was stripped")
	}
}

func TestModelMessagesWithRecentImagesCapsCurrentImagePayload(t *testing.T) {
	largeURL := "data:image/png;base64," + string(make([]byte, maxInlineImagePayloadBytes))
	messages := []provider.Message{{
		Role: "user",
		ContentParts: []provider.ContentPart{
			inlineImage("data:image/png;base64,ONE"),
			inlineImage(largeURL),
		},
	}}

	got := modelMessagesWithRecentImages(messages)
	imageCount := 0
	foundOmissionNote := false
	for _, part := range got[0].ContentParts {
		if part.Type == "image_url" {
			imageCount++
		}
		if part.Type == "text" && part.Text == oversizedImagesOmittedText {
			foundOmissionNote = true
		}
	}
	if imageCount != 1 || !foundOmissionNote {
		t.Fatalf("payload cap result = %#v; want one image plus omission note", got[0].ContentParts)
	}
}

func TestModelMessagesWithRecentImagesCapsImageCount(t *testing.T) {
	parts := make([]provider.ContentPart, 0, maxInlineImagesPerModelCall+1)
	for i := 0; i < maxInlineImagesPerModelCall+1; i++ {
		parts = append(parts, inlineImage("https://example.com/image.png"))
	}

	got := modelMessagesWithRecentImages([]provider.Message{{Role: "user", ContentParts: parts}})
	imageCount := 0
	for _, part := range got[0].ContentParts {
		if part.Type == "image_url" {
			imageCount++
		}
	}
	if imageCount != maxInlineImagesPerModelCall {
		t.Fatalf("kept %d images, want %d", imageCount, maxInlineImagesPerModelCall)
	}
}
