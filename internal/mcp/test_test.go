package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

func TestTestConnectionHTTPSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "tools/list":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a"},{"name":"b"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{}}`))
		}
	}))
	defer server.Close()

	res := TestConnection(context.Background(), config.MCPServerConfig{Type: "http", URL: server.URL})
	if !res.OK {
		t.Fatalf("want ok, got error %q", res.Error)
	}
	if res.ToolCount != 2 {
		t.Fatalf("want 2 tools, got %d", res.ToolCount)
	}
}

func TestTestConnectionHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`))
	}))
	defer server.Close()

	res := TestConnection(context.Background(), config.MCPServerConfig{Type: "http", URL: server.URL})
	if res.OK {
		t.Fatal("want failure, got ok")
	}
	if res.Error == "" {
		t.Fatal("want error message, got empty")
	}
}

func TestHTTPClientPropagatesSessionID(t *testing.T) {
	const sid = "sess-abc-123"
	var (
		mu        sync.Mutex
		initSeen  bool
		listAuth  string
		listSawID string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			// Assign the session ID on the InitializeResult response.
			w.Header().Set("Mcp-Session-Id", sid)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		case "notifications/initialized":
			mu.Lock()
			initSeen = true
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			// Reject when the client forgot the session header — mirrors a
			// spec-compliant server's "Missing session ID" 400.
			if r.Header.Get("Mcp-Session-Id") == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Missing session ID"}}`))
				return
			}
			mu.Lock()
			listSawID = r.Header.Get("Mcp-Session-Id")
			listAuth = r.Header.Get("Accept")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"a"}]}}`))
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()

	res := TestConnection(context.Background(), config.MCPServerConfig{Type: "http", URL: server.URL})
	if !res.OK {
		t.Fatalf("want ok, got error %q", res.Error)
	}
	if res.ToolCount != 1 {
		t.Fatalf("want 1 tool, got %d", res.ToolCount)
	}
	mu.Lock()
	defer mu.Unlock()
	if !initSeen {
		t.Fatal("client must send notifications/initialized after initialize")
	}
	if listSawID != sid {
		t.Fatalf("tools/list must carry session id %q, server saw %q", sid, listSawID)
	}
	if !strings.Contains(listAuth, "text/event-stream") {
		t.Fatalf("tools/list Accept header lost SSE support: %q", listAuth)
	}
}

func TestTestConnectionRejectsStdio(t *testing.T) {
	res := TestConnection(context.Background(), config.MCPServerConfig{Type: "stdio", Command: "npx"})
	if res.OK {
		t.Fatal("stdio should not be testable")
	}
	if res.Error == "" {
		t.Fatal("want rejection message, got empty")
	}
}

func TestHTTPClientSendsAcceptHeader(t *testing.T) {
	gotAccept := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAccept <- r.Header.Get("Accept"):
		default:
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got := <-gotAccept
	if !strings.Contains(got, "application/json") || !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Accept header must advertise json + sse, got %q", got)
	}
}

func TestHTTPClientParsesSSEResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
		default:
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"tools\":[{\"name\":\"a\"}]}}\n\n"))
		}
	}))
	defer server.Close()

	res := TestConnection(context.Background(), config.MCPServerConfig{Type: "http", URL: server.URL})
	if !res.OK {
		t.Fatalf("want ok, got error %q", res.Error)
	}
	if res.ToolCount != 1 {
		t.Fatalf("want 1 tool from SSE response, got %d", res.ToolCount)
	}
}
