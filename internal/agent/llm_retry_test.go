package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func fastBackoff(t *testing.T) {
	t.Helper()
	prev := llmRetryBackoff
	llmRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { llmRetryBackoff = prev })
}

// net/http's header timeout satisfies errors.Is(err, context.DeadlineExceeded).
// It must still be retried: only the caller's own context ending is terminal.
func TestLLMRetryRetriesHeaderTimeout(t *testing.T) {
	fastBackoff(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(time.Second) }))
	defer srv.Close()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 20 * time.Millisecond
	client := &http.Client{Transport: tr}

	calls := 0
	resp, err := llmRetry(context.Background(), "test", func(ctx context.Context) (*provider.Response, error) {
		calls++
		if calls < 3 {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			_, err := client.Do(req)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected a header timeout matching DeadlineExceeded, got %v", err)
			}
			return nil, fmt.Errorf("send request: %w", err)
		}
		return &provider.Response{Content: "ok"}, nil
	})
	if err != nil || resp.Content != "ok" || calls != 3 {
		t.Fatalf("resp=%v err=%v calls=%d, want success on 3rd call", resp, err, calls)
	}
}

func TestLLMRetryStopsOnPermanentError(t *testing.T) {
	fastBackoff(t)
	calls := 0
	_, err := llmRetry(context.Background(), "test", func(context.Context) (*provider.Response, error) {
		calls++
		return nil, errors.New("API error 401: invalid api key")
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want one call", err, calls)
	}
}

func TestLLMRetryStopsWhenCallerCancels(t *testing.T) {
	fastBackoff(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := llmRetry(ctx, "test", func(context.Context) (*provider.Response, error) {
		calls++
		cancel()
		return nil, errors.New("API error 503: busy")
	})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want stop after caller cancel", err, calls)
	}
}

func TestIsRetryableLLMError(t *testing.T) {
	cases := map[string]bool{
		"send request: net/http: timeout awaiting response headers": true,
		"read stream: unexpected EOF":                                true,
		"API error 429: rate limited":                                true,
		"API error 502: bad gateway":                                 true,
		"API error 400: invalid request":                             false,
		"API error 401: unauthorized":                                false,
		"API error 404: model not found":                             false,
	}
	for msg, want := range cases {
		if got := isRetryableLLMError(errors.New(msg)); got != want {
			t.Errorf("isRetryableLLMError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestCapToolResult(t *testing.T) {
	small := "hello"
	if got := capToolResult(small, t.TempDir(), "c1"); got != small {
		t.Fatal("small output must pass through")
	}

	dir := t.TempDir()
	big := strings.Repeat("a", 300<<10) + "TAIL"
	got := capToolResult(big, dir, "call/1")
	if len(got) > maxToolResultBytes+1024 {
		t.Fatalf("capped output is %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "TAIL") || !strings.Contains(got, "output truncated") {
		t.Fatal("capped output should keep the tail and explain the cut")
	}
	saved, err := os.ReadFile(filepath.Join(dir, ".tool-outputs", "tool-call_1.txt"))
	if err != nil || string(saved) != big {
		t.Fatalf("full output not saved: %v", err)
	}

	// Multi-byte text is never cut mid-rune.
	cjk := strings.Repeat("中", 40<<10)
	if out := capToolResult(cjk, "", "c2"); !strings.Contains(out, "not saved") || strings.ContainsRune(out, '�') {
		t.Fatal("CJK output should be cut on rune boundaries")
	}
}
