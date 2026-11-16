package share

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func newTestService(t *testing.T, now time.Time) *Service {
	t.Helper()
	s := New(workspace.NewLocalFS(t.TempDir()), "https://cloud.example.com/")
	s.now = func() time.Time { return now }
	return s
}

func serve(s *Service, link string) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("GET /f/{token}/{name}", s)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, strings.TrimPrefix(link, "https://cloud.example.com"), nil))
	return rec
}

func TestCreateServeExpireSweep(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	s := newTestService(t, now)

	link, err := s.Create(ctx, []byte("pkg-bytes"), "油价 widget.scripting")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, "https://cloud.example.com/f/20261123") {
		t.Fatalf("link = %s", link)
	}

	rec := serve(s, link)
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 200 || string(body) != "pkg-bytes" {
		t.Fatalf("serve: %d %q", rec.Code, body)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("unknown type should download, got %q", cd)
	}
	if rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("content-type %q", rec.Header().Get("Content-Type"))
	}

	// A guessed token, or a tampered one, finds nothing.
	if rec := serve(s, strings.Replace(link, "/f/20261123", "/f/20261124", 1)); rec.Code != 404 {
		t.Errorf("tampered token served: %d", rec.Code)
	}

	// Still valid on the expiry day, gone the day after.
	s.now = func() time.Time { return time.Date(2026, 11, 23, 23, 0, 0, 0, time.UTC) }
	if rec := serve(s, link); rec.Code != 200 {
		t.Errorf("expiry day: %d", rec.Code)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 24, 0, 30, 0, 0, time.UTC) }
	if rec := serve(s, link); rec.Code != 404 {
		t.Errorf("after expiry: %d", rec.Code)
	}
	s.Sweep(ctx, 5)
	if objs, _ := s.ws.List(ctx, scope, "20261123", ""); len(objs) != 0 {
		t.Errorf("sweep left %d files", len(objs))
	}
}

func TestImagesOpenInline(t *testing.T) {
	s := newTestService(t, time.Now())
	link, err := s.Create(context.Background(), []byte("png"), "a.png")
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(s, link)
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline;") || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("png: %q %q", cd, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Error("no sandbox CSP")
	}
}

func TestNilServiceRefuses(t *testing.T) {
	var s *Service
	if _, err := s.Create(context.Background(), []byte("x"), "a"); err == nil {
		t.Error("nil service created a link")
	}
	if New(nil, "https://x") != nil || New(workspace.NewLocalFS(t.TempDir()), "") != nil {
		t.Error("New without store or base should be nil")
	}
}
