package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExaSearchNotRegisteredWithoutKey(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	if r.GetFunc("exa_search") != nil {
		t.Fatalf("exa_search should not be registered by default")
	}
}

func TestExaSearchRegistersWhenKeyProvided(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterExaSearch(r, "test-key")
	if r.GetFunc("exa_search") == nil {
		t.Fatalf("exa_search should be registered after RegisterExaSearch")
	}
}

func TestExaSearchRequiresQuery(t *testing.T) {
	fn := makeExaSearchTool("test-key")
	_, err := fn(context.Background(), json.RawMessage(`{"query": ""}`))
	if err == nil {
		t.Fatalf("expected error for empty query")
	}
	if !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExaSearchSendsIntegrationHeaderAndParsesResults(t *testing.T) {
	var gotReq exaSearchRequest
	var gotAPIKey, gotIntegration string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotIntegration = r.Header.Get(exaIntegrationHeader)
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"requestId": "abc",
			"results": [
				{
					"title": "Hello",
					"url": "https://example.com/1",
					"publishedDate": "2025-01-01",
					"highlights": ["first highlight", "second highlight"]
				},
				{
					"title": "World",
					"url": "https://example.com/2",
					"summary": "A short summary"
				}
			]
		}`))
	}))
	defer srv.Close()

	fn := makeExaSearchToolWithEndpoint("my-key", srv.URL)
	out, err := fn(context.Background(), json.RawMessage(`{
		"query": "golang testing",
		"num_results": 3,
		"type": "neural",
		"category": "research paper",
		"include_domains": ["example.com"],
		"start_published_date": "2024-01-01",
		"content_mode": "highlights"
	}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAPIKey != "my-key" {
		t.Errorf("expected x-api-key=my-key, got %q", gotAPIKey)
	}
	if gotIntegration != exaIntegrationValue {
		t.Errorf("expected %s=%s, got %q", exaIntegrationHeader, exaIntegrationValue, gotIntegration)
	}
	if gotReq.Query != "golang testing" || gotReq.NumResults != 3 || gotReq.Type != "neural" {
		t.Errorf("request body not serialized correctly: %+v", gotReq)
	}
	if gotReq.Category != "research paper" {
		t.Errorf("category not forwarded: %q", gotReq.Category)
	}
	if len(gotReq.IncludeDomains) != 1 || gotReq.IncludeDomains[0] != "example.com" {
		t.Errorf("include_domains not forwarded: %+v", gotReq.IncludeDomains)
	}
	if gotReq.StartPublishedDate != "2024-01-01" {
		t.Errorf("start_published_date not forwarded: %q", gotReq.StartPublishedDate)
	}
	if gotReq.Contents == nil || gotReq.Contents.Highlights == nil {
		t.Errorf("expected highlights in contents, got %+v", gotReq.Contents)
	}

	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Errorf("output missing titles: %s", out)
	}
	if !strings.Contains(out, "first highlight") {
		t.Errorf("highlights not rendered: %s", out)
	}
	if !strings.Contains(out, "A short summary") {
		t.Errorf("summary not rendered: %s", out)
	}
}

func TestExaSearchNumResultsBounds(t *testing.T) {
	var gotReq exaSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = w.Write([]byte(`{"requestId":"x","results":[]}`))
	}))
	defer srv.Close()

	fn := makeExaSearchToolWithEndpoint("k", srv.URL)

	cases := []struct {
		in   int
		want int
	}{
		{0, 5},   // default
		{-1, 5},  // negative → default
		{50, 50}, // passthrough
		{500, 100}, // clamped
	}
	for _, tc := range cases {
		args, _ := json.Marshal(map[string]interface{}{"query": "x", "num_results": tc.in})
		_, err := fn(context.Background(), args)
		if err != nil {
			t.Fatalf("unexpected error for in=%d: %v", tc.in, err)
		}
		if gotReq.NumResults != tc.want {
			t.Errorf("in=%d: got numResults=%d, want %d", tc.in, gotReq.NumResults, tc.want)
		}
	}
}

func TestExaSearchContentModes(t *testing.T) {
	var gotReq exaSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = w.Write([]byte(`{"requestId":"x","results":[]}`))
	}))
	defer srv.Close()

	fn := makeExaSearchToolWithEndpoint("k", srv.URL)

	cases := []struct {
		mode        string
		wantText    bool
		wantHighlts bool
		wantSummary bool
	}{
		{"", false, true, false},
		{"highlights", false, true, false},
		{"text", true, false, false},
		{"summary", false, false, true},
		{"full", true, true, true},
	}
	for _, tc := range cases {
		gotReq = exaSearchRequest{}
		args, _ := json.Marshal(map[string]interface{}{"query": "x", "content_mode": tc.mode})
		_, err := fn(context.Background(), args)
		if err != nil {
			t.Fatalf("mode=%q: unexpected error: %v", tc.mode, err)
		}
		c := gotReq.Contents
		if c == nil {
			t.Fatalf("mode=%q: expected contents to be set", tc.mode)
		}
		if (c.Text != nil) != tc.wantText {
			t.Errorf("mode=%q: Text presence mismatch", tc.mode)
		}
		if (c.Highlights != nil) != tc.wantHighlts {
			t.Errorf("mode=%q: Highlights presence mismatch", tc.mode)
		}
		if (c.Summary != nil) != tc.wantSummary {
			t.Errorf("mode=%q: Summary presence mismatch", tc.mode)
		}
	}
}

func TestExaSearchHandlesNonOKResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	fn := makeExaSearchToolWithEndpoint("bad", srv.URL)
	_, err := fn(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil {
		t.Fatalf("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected status code in error, got: %v", err)
	}
}

func TestExaSearchEmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"requestId":"x","results":[]}`))
	}))
	defer srv.Close()

	fn := makeExaSearchToolWithEndpoint("k", srv.URL)
	out, err := fn(context.Background(), json.RawMessage(`{"query":"nothing here"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No results found") {
		t.Fatalf("expected empty-results message, got: %s", out)
	}
}

func TestExtractExaSnippetCascade(t *testing.T) {
	cases := []struct {
		name string
		in   exaResult
		want string
	}{
		{"summary wins", exaResult{Summary: "sum", Highlights: []string{"h"}, Text: "t"}, "sum"},
		{"highlights over text", exaResult{Highlights: []string{"h1", "h2"}, Text: "t"}, "h1 ... h2"},
		{"text fallback", exaResult{Text: "t"}, "t"},
		{"none", exaResult{}, ""},
	}
	for _, tc := range cases {
		got := extractExaSnippet(tc.in)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
