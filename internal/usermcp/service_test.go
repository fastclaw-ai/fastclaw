package usermcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// fakeMCP is a Streamable HTTP server with one read-only and one write
// tool. sse answers over an event stream instead of JSON.
func fakeMCP(t *testing.T, sse bool, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "s1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "search", "description": "Find things", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				{"name": "delete", "description": "Delete a thing"},
			}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("ran %v", req.Params["name"])}}}
		}
		resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if sse {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", resp)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestService(t *testing.T, ts *httptest.Server) *Service {
	t.Helper()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := Init(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	prev := httpClient
	httpClient = ts.Client() // the guard refuses the local test server
	t.Cleanup(func() { httpClient = prev })
	return Get()
}

func TestAddListCallDelete(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprintf("sse=%v", sse), func(t *testing.T) {
			var calls atomic.Int32
			ts := fakeMCP(t, sse, &calls)
			svc := newTestService(t, ts)
			ctx := context.Background()

			if _, err := svc.Add(ctx, "u1", "Mine", ts.URL, map[string]string{"Authorization": "Bearer wrong"}, nil); Code(err) != "unauthorized" {
				t.Fatalf("bad key: got %v", err)
			}
			srv, err := svc.Add(ctx, "u1", "Mine", ts.URL, map[string]string{"Authorization": "Bearer secret"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if srv.ToolCount != 2 {
				t.Fatalf("toolCount = %d", srv.ToolCount)
			}
			if _, err := svc.Add(ctx, "u1", "mine", ts.URL, map[string]string{"Authorization": "Bearer secret"}, nil); Code(err) != "name_taken" {
				t.Fatalf("duplicate name: got %v", err)
			}

			// Only the user who added it sees it.
			if other, _ := svc.List(ctx, "u2"); len(other) != 0 {
				t.Fatalf("u2 sees %v", other)
			}
			if err := svc.Delete(ctx, "u2", srv.ID); Code(err) != "not_found" {
				t.Fatalf("u2 deleted u1's server: %v", err)
			}
			list, _ := svc.List(ctx, "u1")
			if len(list) != 1 || list[0].Headers["Authorization"] != "Bearer secret" {
				t.Fatalf("list = %+v", list)
			}

			out, err := svc.Call(ctx, list[0], "u1", "search", nil, nil)
			if err != nil || out != "ran search" {
				t.Fatalf("read call: %q %v", out, err)
			}
			// A tool not marked read-only never runs without an approval.
			if _, err := svc.Call(ctx, list[0], "u1", "delete", nil, nil); Code(err) != "write_not_allowed" {
				t.Fatalf("write without approval: %v", err)
			}
			denied := errors.New("no")
			if _, err := svc.Call(ctx, list[0], "u1", "delete", nil, func(context.Context, ToolDef) error { return denied }); !errors.Is(err, denied) {
				t.Fatalf("declined write: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want only the read", calls.Load())
			}
			if _, err := svc.Call(ctx, list[0], "u1", "delete", nil, func(context.Context, ToolDef) error { return nil }); err != nil {
				t.Fatalf("approved write: %v", err)
			}
			if _, err := svc.Call(ctx, list[0], "u1", "nope", nil, nil); Code(err) != "tool_not_found" {
				t.Fatalf("unknown tool: %v", err)
			}

			if err := svc.Delete(ctx, "u1", srv.ID); err != nil {
				t.Fatal(err)
			}
			if list, _ := svc.List(ctx, "u1"); len(list) != 0 {
				t.Fatalf("after delete: %v", list)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	ok := map[string]string{"Authorization": "Bearer x"}
	cases := []struct {
		name, url string
		headers   map[string]string
		code      string
	}{
		{"A", "https://example.com/mcp", ok, ""},
		{"", "https://example.com/mcp", ok, "invalid_name"},
		{strings.Repeat("x", 41), "https://example.com/mcp", ok, "invalid_name"},
		{"A", "http://example.com/mcp", ok, "invalid_url"},
		{"A", "https://user:pw@example.com/mcp", ok, "invalid_url"},
		{"A", "file:///etc/passwd", ok, "invalid_url"},
		{"A", "https://example.com/mcp", map[string]string{"Host": "x"}, "invalid_headers"},
		{"A", "https://example.com/mcp", map[string]string{"X-A": "a\r\nb: c"}, "invalid_headers"},
		{"A", "https://example.com/mcp", map[string]string{"Bad Name": "x"}, "invalid_headers"},
	}
	for _, c := range cases {
		_, _, _, err := Normalize(c.name, c.url, c.headers)
		got := ""
		if err != nil {
			got = Code(err)
		}
		if got != c.code {
			t.Errorf("Normalize(%q, %q, %v) = %q, want %q", c.name, c.url, c.headers, got, c.code)
		}
	}
	// Values are sent as given: no $ENV expansion.
	_, _, h, _ := Normalize("A", "https://example.com/", map[string]string{"X-Key": "$HOME"})
	if h["X-Key"] != "$HOME" {
		t.Fatalf("header expanded: %q", h["X-Key"])
	}
}

func TestPublicDialRefusesPrivateAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1"} {
		if !blockedAddr(net.ParseIP(ip)) {
			t.Errorf("%s not blocked", ip)
		}
	}
	if blockedAddr(net.ParseIP("8.8.8.8")) {
		t.Error("public address blocked")
	}
	_, err := newPublicHTTPClient().Get("https://127.0.0.1:1/")
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("loopback dial: %v", err)
	}
}

func TestUpdate(t *testing.T) {
	var calls atomic.Int32
	ts := fakeMCP(t, false, &calls)
	svc := newTestService(t, ts)
	ctx := context.Background()
	auth := map[string]string{"Authorization": "Bearer secret"}

	a, err := svc.Add(ctx, "u1", "A", ts.URL, auth, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Add(ctx, "u1", "B", ts.URL, auth, nil); err != nil {
		t.Fatal(err)
	}

	// Someone else's server is not found, and stays unchanged.
	if _, err := svc.Update(ctx, "u2", a.ID, "Hijack", ts.URL, nil); Code(err) != "not_found" {
		t.Fatalf("u2 updated u1's server: %v", err)
	}
	if _, err := svc.Update(ctx, "u1", a.ID, "b", ts.URL, nil); Code(err) != "name_taken" {
		t.Fatalf("rename onto another: %v", err)
	}

	// Rename only: headers nil keeps them.
	got, err := svc.Update(ctx, "u1", a.ID, "Renamed", ts.URL, nil)
	if err != nil || got.Name != "Renamed" || got.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("rename: %+v %v", got, err)
	}
	// An empty value keeps the saved value; a new header is added.
	got, err = svc.Update(ctx, "u1", a.ID, "Renamed", ts.URL, map[string]string{"Authorization": "", "X-Extra": "1"})
	if err != nil || got.Headers["Authorization"] != "Bearer secret" || got.Headers["X-Extra"] != "1" {
		t.Fatalf("keep value: %+v %v", got, err)
	}
	// A wrong key is checked before saving, and nothing changes.
	if _, err := svc.Update(ctx, "u1", a.ID, "Renamed", ts.URL, map[string]string{"Authorization": "Bearer wrong"}); Code(err) != "unauthorized" {
		t.Fatalf("bad key: %v", err)
	}
	// A and B can share a created_at: find A by id, not by position.
	list, _ := svc.List(ctx, "u1")
	var saved Server
	for _, srv := range list {
		if srv.ID == a.ID {
			saved = srv
		}
	}
	if saved.Name != "Renamed" || saved.Headers["Authorization"] != "Bearer secret" || saved.Headers["X-Extra"] != "1" {
		t.Fatalf("saved = %+v", saved)
	}
	// Dropping a header removes it.
	got, err = svc.Update(ctx, "u1", a.ID, "Renamed", ts.URL, map[string]string{"Authorization": ""})
	if err != nil || len(got.Headers) != 1 {
		t.Fatalf("drop header: %+v %v", got, err)
	}
	if _, err := svc.Update(ctx, "u1", "mcp_missing", "X", ts.URL, nil); Code(err) != "not_found" {
		t.Fatalf("missing: %v", err)
	}
}
