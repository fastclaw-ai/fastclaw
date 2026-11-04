package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// BrightData calls api.brightdata.com. Requires a Bright Data API key and a
// provisioned serp zone. The zone is passed via Options["zone"]. The search
// engine defaults to Google; append brd_json=1 for structured JSON results.
type BrightData struct{}

func (BrightData) Category() string       { return Category }
func (BrightData) Name() string           { return "brightdata" }
func (BrightData) ExplicitOnly() bool     { return true }

// httpClient is the HTTP client used for BrightData API calls.
// It disables automatic redirects so that credentials and query data
// are never forwarded to an unexpected host.
var httpClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return fmt.Errorf("brightdata: redirect blocked")
	},
}

func (b *BrightData) Execute(ctx context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	a, err := parseArgs(req.Args)
	if err != nil {
		return toolproviders.Response{}, err
	}
	if req.Config.APIKey == "" {
		return toolproviders.Response{}, fmt.Errorf("brightdata: missing api key")
	}
	zone := req.Config.Options["zone"]
	if zone == "" {
		return toolproviders.Response{}, fmt.Errorf("brightdata: missing serp zone")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	engineURL := fmt.Sprintf("https://www.google.com/search?q=%s&brd_json=1", url.QueryEscape(a.Query))
	body := map[string]any{
		"zone":   zone,
		"url":    engineURL,
		"format": "raw",
	}
	buf, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brightdata.com/request", bytes.NewReader(buf))
	if err != nil {
		return toolproviders.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("brightdata request: %w", err))
	}
	defer resp.Body.Close()
	if err := retriableHTTP("brightdata", resp); err != nil {
		return toolproviders.Response{}, err
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return toolproviders.Response{}, fmt.Errorf("brightdata read: %w", err)
	}
	var out struct {
		Organic []struct {
			Title       string `json:"title"`
			Link        string `json:"link"`
			Description string `json:"description"`
		} `json:"organic"`
	}
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return toolproviders.Response{}, fmt.Errorf("brightdata decode: %w", err)
	}
	if out.Organic == nil {
		return toolproviders.Response{}, fmt.Errorf("brightdata: missing organic in response")
	}
	items := make([]resultItem, 0, len(out.Organic))
	for _, r := range out.Organic {
		items = append(items, resultItem{Title: r.Title, URL: r.Link, Snippet: truncate(r.Description, 280)})
	}
	if len(items) == 0 {
		return toolproviders.Response{}, fmt.Errorf("brightdata: no results")
	}
	return toolproviders.Response{Text: render(a.Query, items)}, nil
}
