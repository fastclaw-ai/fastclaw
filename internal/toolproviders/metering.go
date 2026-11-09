package toolproviders

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// Usage metering for provider-backed tools (docs/usage-metering.md). The
// category names are the subpackages' Category constants; they are spelled
// out here because those packages import this one.
const (
	categoryImageGen  = "image_gen"
	categoryTTS       = "tts"
	categoryWebSearch = "web_search"
	categoryWebFetch  = "web_fetch"
)

// authorizeCall asks the billing hook before a costly call (image_gen,
// tts). A denial comes back as an error whose text is the hook's message.
func authorizeCall(ctx context.Context, category, provider, model string, args map[string]any) error {
	switch category {
	case categoryImageGen:
		return billing.AuthorizeTool(ctx, billing.KindImage, provider, model,
			billing.Estimate{Units: float64(requestedImages(args)), Unit: "image"})
	case categoryTTS:
		return billing.AuthorizeTool(ctx, billing.KindTTS, provider, model,
			billing.Estimate{Units: float64(ttsChars(args)), Unit: "char"})
	}
	return nil
}

// meterCall records one successful provider call.
func meterCall(ctx context.Context, category, provider, model string, args map[string]any, resp Response) {
	ev := billing.Event{Provider: provider, Model: model}
	switch category {
	case categoryImageGen:
		n := countImages(resp.Text)
		if n == 0 {
			n = requestedImages(args)
		}
		ev.Kind, ev.Units, ev.Unit = billing.KindImage, float64(n), "image"
		detail := map[string]any{}
		if s, _ := args["size"].(string); strings.TrimSpace(s) != "" {
			detail["size"] = strings.TrimSpace(s)
		}
		if q, _ := args["quality"].(string); strings.TrimSpace(q) != "" {
			detail["quality"] = strings.TrimSpace(q)
		}
		ev.Detail = detail
	case categoryTTS:
		ev.Kind, ev.Units, ev.Unit = billing.KindTTS, float64(ttsChars(args)), "char"
	case categoryWebSearch:
		ev.Kind, ev.Units, ev.Unit = billing.KindSearch, 1, "call"
	case categoryWebFetch:
		ev.Kind, ev.Units, ev.Unit = billing.KindFetch, 1, "call"
	default:
		return
	}
	billing.Record(ctx, ev)
}

var imageTag = regexp.MustCompile(`!\[image \d+\]\(`)

// countImages counts the images an image_gen provider returned (each is
// rendered as one "![image N](…)" tag).
func countImages(text string) int { return len(imageTag.FindAllStringIndex(text, -1)) }

// requestedImages mirrors imagegen's n handling: default 1, max 4.
func requestedImages(args map[string]any) int {
	n := 1
	switch v := args["n"].(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	}
	if n <= 0 {
		n = 1
	}
	if n > 4 {
		n = 4
	}
	return n
}

// ttsChars is the number of characters a tts call synthesizes.
func ttsChars(args map[string]any) int {
	s, _ := args["text"].(string)
	return utf8.RuneCountInString(strings.TrimSpace(s))
}
