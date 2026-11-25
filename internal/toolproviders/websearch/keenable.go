package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// Keenable endpoints. The public endpoint needs no API key; when an API key is
// configured the keyed endpoint is used with the key in X-API-Key.
var (
	keenablePublicURL = "https://api.keenable.ai/v1/search/public"
	keenableKeyedURL  = "https://api.keenable.ai/v1/search"
)

// Keenable calls api.keenable.ai. It works without a key (CredentialFree), so
// an admin can pick it in the UI without typing a fake API key; a key raises
// the rate limits.
type Keenable struct{}

func (Keenable) Category() string     { return Category }
func (Keenable) Name() string         { return "keenable" }
func (Keenable) CredentialFree() bool { return true }

func (k *Keenable) Execute(ctx context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	a, err := parseArgs(req.Args)
	if err != nil {
		return toolproviders.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	buf, _ := json.Marshal(map[string]any{"query": a.Query, "max_results": a.Count})
	endpoint := keenablePublicURL
	apiKey := strings.TrimSpace(req.Config.APIKey)
	if apiKey != "" {
		endpoint = keenableKeyedURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return toolproviders.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Keenable-Title", "fastclaw")
	if apiKey != "" {
		httpReq.Header.Set("X-API-Key", apiKey)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("keenable request: %w", err))
	}
	defer resp.Body.Close()
	if err := retriableHTTP("keenable", resp); err != nil {
		return toolproviders.Response{}, err
	}
	var out struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Snippet     string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return toolproviders.Response{}, fmt.Errorf("keenable decode: %w", err)
	}
	items := make([]resultItem, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		// Prefer the short description; the snippet can be a long page excerpt.
		snippet := r.Description
		if snippet == "" {
			snippet = truncate(r.Snippet, 280)
		}
		items = append(items, resultItem{Title: r.Title, URL: r.URL, Snippet: snippet})
	}
	if len(items) == 0 {
		return toolproviders.Response{}, toolproviders.ErrNoResults
	}
	return toolproviders.Response{Text: render(a.Query, items)}, nil
}
