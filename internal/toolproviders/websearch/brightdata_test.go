package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// newTestHTTPClient returns an *http.Client whose transport rewrites every
// request to dst. It does not mutate http.DefaultClient.
func newTestHTTPClient(dst string) *http.Client {
	u, err := url.Parse(dst)
	if err != nil {
		panic("bad test server URL: " + err.Error())
	}
	return &http.Client{
		Transport: &rewriteTransport{scheme: u.Scheme, host: u.Host},
	}
}

// rewriteTransport rewrites all HTTP requests to the target scheme/host.
type rewriteTransport struct {
	scheme string
	host   string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.scheme
	req.URL.Host = rt.host
	req.RequestURI = ""
	return http.DefaultTransport.RoundTrip(req)
}

func TestBrightDataExecuteSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/request" {
			t.Errorf("expected /request, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("expected Bearer auth, got %s", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["zone"] != "benchmark" {
			t.Errorf("expected zone benchmark, got %v", body["zone"])
		}
		if body["format"] != "raw" {
			t.Errorf("expected format raw, got %v", body["format"])
		}
		urlStr, _ := body["url"].(string)
		if !strings.Contains(urlStr, "brd_json=1") {
			t.Errorf("expected brd_json=1 in url, got %s", urlStr)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"organic": []map[string]any{
				{"title": "Result 1", "link": "https://example.com/1", "description": "First result"},
				{"title": "Result 2", "link": "https://example.com/2", "description": "Second result"},
			},
		})
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	resp, err := b.Execute(context.Background(), toolproviders.Request{
		Args: map[string]any{"query": "test query", "count": float64(3)},
		Config: toolproviders.ProviderConfig{
			APIKey:  "test-api-key",
			Options: map[string]string{"zone": "benchmark"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "Result 1") || !strings.Contains(resp.Text, "Result 2") {
		t.Errorf("unexpected response text: %s", resp.Text)
	}
}

func TestBrightDataMissingAPIKey(t *testing.T) {
	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("expected missing api key error, got: %v", err)
	}
}

// TestBrightData_MissingZone_IsHardError verifies that a missing zone is a
// non-retriable configuration error. Deleting this guard makes the test fail.
func TestBrightData_MissingZone_IsHardError(t *testing.T) {
	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing serp zone") {
		t.Errorf("expected missing serp zone error, got: %v", err)
	}
	// The error must not be retriable — a missing zone is a config bug,
	// not a transient failure.
	if errors.As(err, new(*toolproviders.RetriableError)) {
		t.Error("missing zone error must not be retriable")
	}
}

func TestBrightDataHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key", Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil {
		t.Fatal("expected error for non-200")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("expected HTTP 401 in error, got: %v", err)
	}
	// Non-retriable — 401 is a config problem.
	if errors.As(err, new(*toolproviders.RetriableError)) {
		t.Error("401 should not be retriable")
	}
}

func TestBrightDataRetriableHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key", Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil {
		t.Fatal("expected error for 429")
	}
	if !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("expected HTTP 429 in error, got: %v", err)
	}
	if !errors.As(err, new(*toolproviders.RetriableError)) {
		t.Error("429 should be retriable")
	}
}

// TestBrightData_PaidFailure_NoOrganic verifies that a billed 200 with a
// missing organic field throws (non-retriable). It never returns null or
// zero results.
func TestBrightData_PaidFailure_NoOrganic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"not_organic": "where are the results?"}`))
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key", Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil {
		t.Fatal("expected error for missing organic")
	}
	if !strings.Contains(err.Error(), "missing organic") {
		t.Errorf("expected missing organic in error, got: %v", err)
	}
	// The error must not be retriable — this is a paid failure, not
	// something to silently fall back from.
	if errors.As(err, new(*toolproviders.RetriableError)) {
		t.Error("paid failure must not be retriable")
	}
}

// TestBrightData_EmptyOrganicIsHardError verifies that a billed 200 with an
// empty organic array returns a non-retriable error. The provider paid for
// this response; reclassifying it as retriable would cause the chain to
// silently try (and pay) another provider.
func TestBrightData_EmptyOrganicIsHardError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"organic": []map[string]any{},
		})
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key", Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil {
		t.Fatal("expected error for empty organic")
	}
	// Must NOT be retriable — this is a paid failure.
	if errors.As(err, new(*toolproviders.RetriableError)) || errors.Is(err, toolproviders.ErrNoResults) {
		t.Error("empty organic must be a non-retriable hard error")
	}
}

// TestBrightData_RedirectBlocked verifies that the provider refuses to
// follow HTTP redirects, preventing credentials and query data from being
// forwarded to an unexpected host.
func TestBrightData_RedirectBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/steal", http.StatusFound)
	}))
	defer srv.Close()

	orig := httpClient
	httpClient = newTestHTTPClient(srv.URL)
	defer func() { httpClient = orig }()

	b := &BrightData{}
	_, err := b.Execute(context.Background(), toolproviders.Request{
		Args:   map[string]any{"query": "test"},
		Config: toolproviders.ProviderConfig{APIKey: "test-api-key", Options: map[string]string{"zone": "benchmark"}},
	})
	if err == nil {
		t.Fatal("expected error for redirect")
	}
	// The error should be retriable (network-level failure).
	if !errors.As(err, new(*toolproviders.RetriableError)) {
		t.Errorf("redirect error should be retriable, got: %v", err)
	}
}

// TestBrightData_ExplicitOnly verifies that the provider reports itself
// as explicit-only, so it is never selected by auto or "all" chains.
func TestBrightData_ExplicitOnly(t *testing.T) {
	b := &BrightData{}
	if !b.ExplicitOnly() {
		t.Error("BrightData must report ExplicitOnly() == true")
	}
}
