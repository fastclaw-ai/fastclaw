package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	exaSearchEndpoint    = "https://api.exa.ai/search"
	exaIntegrationHeader = "x-exa-integration"
	exaIntegrationValue  = "fastclaw"
	exaRequestTimeout    = 30 * time.Second
)

type exaSearchArgs struct {
	Query              string   `json:"query"`
	NumResults         int      `json:"num_results,omitempty"`
	Type               string   `json:"type,omitempty"`
	Category           string   `json:"category,omitempty"`
	IncludeDomains     []string `json:"include_domains,omitempty"`
	ExcludeDomains     []string `json:"exclude_domains,omitempty"`
	IncludeText        []string `json:"include_text,omitempty"`
	ExcludeText        []string `json:"exclude_text,omitempty"`
	StartPublishedDate string   `json:"start_published_date,omitempty"`
	EndPublishedDate   string   `json:"end_published_date,omitempty"`
	UserLocation       string   `json:"user_location,omitempty"`
	ContentMode        string   `json:"content_mode,omitempty"` // "highlights", "text", "summary", "full"
}

// exaContents mirrors the "contents" object in the Exa API request.
type exaContents struct {
	Text       *exaText       `json:"text,omitempty"`
	Highlights *exaHighlights `json:"highlights,omitempty"`
	Summary    *exaSummary    `json:"summary,omitempty"`
}

type exaText struct {
	MaxCharacters int `json:"maxCharacters,omitempty"`
}

type exaHighlights struct {
	NumSentences int `json:"numSentences,omitempty"`
}

type exaSummary struct {
	Query string `json:"query,omitempty"`
}

type exaSearchRequest struct {
	Query              string       `json:"query"`
	NumResults         int          `json:"numResults,omitempty"`
	Type               string       `json:"type,omitempty"`
	Category           string       `json:"category,omitempty"`
	IncludeDomains     []string     `json:"includeDomains,omitempty"`
	ExcludeDomains     []string     `json:"excludeDomains,omitempty"`
	IncludeText        []string     `json:"includeText,omitempty"`
	ExcludeText        []string     `json:"excludeText,omitempty"`
	StartPublishedDate string       `json:"startPublishedDate,omitempty"`
	EndPublishedDate   string       `json:"endPublishedDate,omitempty"`
	UserLocation       string       `json:"userLocation,omitempty"`
	Contents           *exaContents `json:"contents,omitempty"`
}

// exaResult is a single result entry returned by the Exa API.
type exaResult struct {
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	ID            string   `json:"id"`
	Author        string   `json:"author"`
	PublishedDate string   `json:"publishedDate"`
	Text          string   `json:"text"`
	Highlights    []string `json:"highlights"`
	Summary       string   `json:"summary"`
}

type exaSearchResponse struct {
	RequestID string      `json:"requestId"`
	Results   []exaResult `json:"results"`
}

// RegisterExaSearch registers the exa_search tool backed by the Exa API.
func RegisterExaSearch(r *Registry, apiKey string) {
	r.Register("exa_search", "Search the web using Exa AI-powered search. Returns high-quality results with AI-extracted highlights, optional full text, or LLM-generated summaries. Supports neural, keyword, and hybrid search types, plus domain/date/text filtering and category scoping (company, research paper, news, personal site, financial report, people).", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The search query",
			},
			"num_results": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return (default 5, max 100)",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Search type: auto (default), neural, fast, deep, deep-lite, deep-reasoning, instant",
			},
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Narrow results to a category: company, research paper, news, personal site, financial report, people",
			},
			"include_domains": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Only return results from these domains",
			},
			"exclude_domains": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Exclude results from these domains",
			},
			"include_text": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Require results to contain these strings",
			},
			"exclude_text": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Exclude results containing these strings",
			},
			"start_published_date": map[string]interface{}{
				"type":        "string",
				"description": "Only return results published after this ISO 8601 date",
			},
			"end_published_date": map[string]interface{}{
				"type":        "string",
				"description": "Only return results published before this ISO 8601 date",
			},
			"user_location": map[string]interface{}{
				"type":        "string",
				"description": "Two-letter ISO country code to bias results (e.g. US)",
			},
			"content_mode": map[string]interface{}{
				"type":        "string",
				"description": "What content to retrieve per result: highlights (default), text, summary, or full (highlights + text + summary)",
			},
		},
		"required": []string{"query"},
	}, makeExaSearchTool(apiKey))
}

func makeExaSearchTool(apiKey string) ToolFunc {
	return makeExaSearchToolWithEndpoint(apiKey, exaSearchEndpoint)
}

func makeExaSearchToolWithEndpoint(apiKey, endpoint string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args exaSearchArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		if strings.TrimSpace(args.Query) == "" {
			return "", fmt.Errorf("query is required")
		}

		numResults := args.NumResults
		if numResults <= 0 {
			numResults = 5
		}
		if numResults > 100 {
			numResults = 100
		}

		reqBody := exaSearchRequest{
			Query:              args.Query,
			NumResults:         numResults,
			Type:               args.Type,
			Category:           args.Category,
			IncludeDomains:     args.IncludeDomains,
			ExcludeDomains:     args.ExcludeDomains,
			IncludeText:        args.IncludeText,
			ExcludeText:        args.ExcludeText,
			StartPublishedDate: args.StartPublishedDate,
			EndPublishedDate:   args.EndPublishedDate,
			UserLocation:       args.UserLocation,
			Contents:           buildExaContents(args.ContentMode, args.Query),
		}

		payload, err := json.Marshal(reqBody)
		if err != nil {
			return "", fmt.Errorf("encode request: %w", err)
		}

		searchCtx, cancel := context.WithTimeout(ctx, exaRequestTimeout)
		defer cancel()

		req, err := http.NewRequestWithContext(searchCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set(exaIntegrationHeader, exaIntegrationValue)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("search request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return "", fmt.Errorf("Exa Search API returned HTTP %d: %s", resp.StatusCode, string(body))
		}

		var searchResp exaSearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
			return "", fmt.Errorf("parse search response: %w", err)
		}

		return formatExaResults(args.Query, searchResp.Results), nil
	}
}

// buildExaContents converts a simple content_mode string into the Exa contents
// request shape. Defaults to highlights when mode is empty.
func buildExaContents(mode, query string) *exaContents {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		normalized = "highlights"
	}
	c := &exaContents{}
	switch normalized {
	case "text":
		c.Text = &exaText{MaxCharacters: 1000}
	case "summary":
		c.Summary = &exaSummary{Query: query}
	case "full":
		c.Text = &exaText{MaxCharacters: 1000}
		c.Highlights = &exaHighlights{}
		c.Summary = &exaSummary{Query: query}
	default: // highlights
		c.Highlights = &exaHighlights{}
	}
	return c
}

// formatExaResults renders results as a human-readable string. Content fields
// cascade: prefer summary, then highlights, then text — whichever is present.
func formatExaResults(query string, results []exaResult) string {
	if len(results) == 0 {
		return "No results found for: " + query
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for: %s\n\n", query))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   URL: %s\n", i+1, r.Title, r.URL))
		if r.PublishedDate != "" {
			sb.WriteString(fmt.Sprintf("   Published: %s\n", r.PublishedDate))
		}
		if r.Author != "" {
			sb.WriteString(fmt.Sprintf("   Author: %s\n", r.Author))
		}
		if snippet := extractExaSnippet(r); snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", snippet))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// extractExaSnippet picks the most useful content field for display. The Exa
// API may return any combination of summary, highlights, and text depending on
// the request, so we cascade through them.
func extractExaSnippet(r exaResult) string {
	if s := strings.TrimSpace(r.Summary); s != "" {
		return s
	}
	if len(r.Highlights) > 0 {
		joined := strings.Join(r.Highlights, " ... ")
		if s := strings.TrimSpace(joined); s != "" {
			return s
		}
	}
	if s := strings.TrimSpace(r.Text); s != "" {
		if len(s) > 500 {
			return s[:500] + "..."
		}
		return s
	}
	return ""
}
