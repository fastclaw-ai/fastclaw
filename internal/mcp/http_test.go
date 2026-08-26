package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Session-enforcing Streamable HTTP servers reject requests that miss the
// Mcp-Session-Id returned by initialize; the client must replay it.
func TestHTTPClientMaintainsSessionID(t *testing.T) {
	issued := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sid := r.Header.Get("Mcp-Session-Id")
		switch {
		case sid == "":
			if issued {
				// A follow-up request arrived without the session id.
				http.Error(w, "missing mcp-session-id", http.StatusBadRequest)
				return
			}
			issued = true
			w.Header().Set("Mcp-Session-Id", "sess-123")
		case sid != "sess-123":
			http.Error(w, "wrong mcp-session-id", http.StatusBadRequest)
			return
		}

		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := `{}`
		if req.Method == "tools/list" {
			result = `{"tools":[{"name":"echo","description":"Echo","inputSchema":{}}]}`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.sessionID != "sess-123" {
		t.Fatalf("sessionID = %q, want sess-123", c.sessionID)
	}
	// The test server 400s any follow-up request without the session id,
	// so this only passes if the id is replayed.
	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want one echo tool", tools)
	}
}
