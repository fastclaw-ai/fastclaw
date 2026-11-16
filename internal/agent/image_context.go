package agent

import "github.com/fastclaw-ai/fastclaw/internal/provider"

const (
	earlierImageOmittedText     = "[An image the person sent earlier. It is no longer attached; if you need to see it again, ask them to send it again.]"
	oversizedImagesOmittedText  = "[Some current images were omitted from inline model context because the combined image payload was too large]"
	maxInlineImagesPerModelCall = 4
	maxInlineImagePayloadBytes  = 6 * 1024 * 1024
)

// modelMessagesWithRecentImages sends the model only the images of the
// current turn: the user messages at the end of the history, after the
// last reply (several of them when a burst like "picture, picture,
// continue" arrives at once). Every earlier image is replaced with a
// short placeholder. Replaying old data URLs made every later turn carry
// them — one 1 MB picture rode along on each question for the rest of
// the conversation — which bloats the request, the cost and memory.
//
// The persisted/archive history used by the chat UI is unchanged. Text
// parts and attachment breadcrumbs stay, so the model still knows a
// picture was there and what it already said about it. The input slice
// and its messages are never mutated.
func modelMessagesWithRecentImages(messages []provider.Message) []provider.Message {
	currentStart := len(messages)
	for currentStart > 0 && messages[currentStart-1].Role == "user" {
		currentStart--
	}

	var result []provider.Message
	inlineImageCount := 0
	inlineImageBytes := 0
	for i, message := range messages {
		if message.Role != "user" || !messageHasInlineImage(message) {
			continue
		}
		current := i >= currentStart

		parts := make([]provider.ContentPart, 0, len(message.ContentParts))
		omitted := 0
		for _, part := range message.ContentParts {
			if part.Type != "image_url" || part.ImageURL == nil || part.ImageURL.URL == "" {
				parts = append(parts, part)
				continue
			}
			urlBytes := len(part.ImageURL.URL)
			keep := current &&
				inlineImageCount < maxInlineImagesPerModelCall &&
				inlineImageBytes+urlBytes <= maxInlineImagePayloadBytes
			if !keep {
				omitted++
				continue
			}
			parts = append(parts, part)
			inlineImageCount++
			inlineImageBytes += urlBytes
		}
		if omitted == 0 {
			continue
		}
		if result == nil {
			result = make([]provider.Message, len(messages))
			copy(result, messages)
		}
		switch {
		case current:
			parts = append(parts, provider.ContentPart{Type: "text", Text: oversizedImagesOmittedText})
		case len(parts) == 0 && message.Content == "":
			message.Content = earlierImageOmittedText
		default:
			parts = append(parts, provider.ContentPart{Type: "text", Text: earlierImageOmittedText})
		}
		message.ContentParts = parts
		result[i] = message
	}
	if result == nil {
		return messages
	}
	return result
}

func messageHasInlineImage(message provider.Message) bool {
	for _, part := range message.ContentParts {
		if part.Type == "image_url" && part.ImageURL != nil && part.ImageURL.URL != "" {
			return true
		}
	}
	return false
}
