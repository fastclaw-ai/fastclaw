package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Streamable HTTP servers may answer JSON-RPC over SSE frames and expect
// "Accept: application/json, text/event-stream" on every request.

func TestExtractSSEData(t *testing.T) {
	body := []byte("event: message\r\n" +
		"data: {\"a\":1}\r\n\r\n" +
		"data:{\"b\":2}\n\n" +
		"data:\n\n" +
		"data: {\"c\":3}\n\n")
	got := extractSSEData(body)
	if string(got) != `{"c":3}` {
		t.Fatalf("extractSSEData = %q, want last non-empty payload {\"c\":3}", got)
	}
	if extractSSEData([]byte("event: message\n\n")) != nil {
		t.Fatal("extractSSEData on SSE body without data lines should return nil")
	}
}

func TestHTTPClientHandlesSSEResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json, text/event-stream" {
			t.Errorf("Accept header = %q, want application/json, text/event-stream", got)
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
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":%s}\n\n", req.ID, result)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, nil)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v, want one echo tool", tools)
	}
}
