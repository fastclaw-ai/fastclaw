// Package websearch bundles the built-in web_search providers. Each one is a
// plain Go struct — no subprocess IPC — so multi-tenant calls are ordinary
// function calls. Per-request credentials come from toolproviders.Request.Config
// so providers hold no tenant state.
package websearch

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// Category is the tool category these providers plug into.
const Category = "web_search"

// RegisterAll adds every built-in web_search provider to r. Providers advertise
// themselves unconditionally; whether a given provider is actually used at
// runtime depends on whether the Chain's GetConfig returns a usable key/endpoint.
func RegisterAll(r *toolproviders.Registry) {
	r.Register(&Brave{})
	r.Register(&BrightData{})
	r.Register(&Exa{})
	r.Register(&SearxNG{})
	r.Register(&None{})
}

// --- shared arg parsing ---

type args struct {
	Query string
	Count int
}

func parseArgs(raw map[string]any) (args, error) {
	var out args
	if q, ok := raw["query"].(string); ok {
		out.Query = strings.TrimSpace(q)
	}
	if out.Query == "" {
		return out, fmt.Errorf("query is required")
	}
	// JSON numbers decode as float64 through map[string]any.
	switch v := raw["count"].(type) {
	case float64:
		out.Count = int(v)
	case int:
		out.Count = v
	}
	if out.Count <= 0 {
		out.Count = 5
	}
	if out.Count > 20 {
		out.Count = 20
	}
	return out, nil
}

// resultItem is the internal shape every provider normalizes to before
// rendering. Keeps the LLM-visible output identical regardless of backend.
type resultItem struct {
	Title   string
	URL     string
	Snippet string
}

// snippetTagRe matches HTML tags that some search backends (notably
// Baidu / Sogou via SearXNG) leak into the `content` field — typically
// <strong>…</strong> wrappers around matched query terms. They look
// like "dirty data" to the model and trigger a "let me fetch the real
// article to verify" reflex even when the snippet itself already
// answers the question. Stripping them up front makes the snippet
// look like authoritative plain text.
var snippetTagRe = regexp.MustCompile(`<[^>]+>`)

func cleanSnippet(s string) string {
	s = snippetTagRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	// Collapse multi-space artifacts left after tag removal.
	s = regexp.MustCompile(`[ \t]+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func render(query string, items []resultItem) string {
	if len(items) == 0 {
		return "No results found for: " + query
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Search results for: %s\n\n", query)
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. %s\n   URL: %s\n   %s\n\n", i+1, it.Title, it.URL, cleanSnippet(it.Snippet))
	}
	return sb.String()
}
