package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

func TestHTTPClientExpandsHeaderEnvValues(t *testing.T) {
	t.Setenv("TOKEN", "expanded-token")
	gotAuth := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, map[string]string{"Authorization": "$TOKEN"})
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case got := <-gotAuth:
		if got != "expanded-token" {
			t.Fatalf("Authorization header: want expanded-token, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive request")
	}
}

func TestNewManagerSkipsStdioOnHostedDeploy(t *testing.T) {
	t.Setenv("FASTCLAW_DEPLOY", "hosted")
	marker := filepath.Join(t.TempDir(), "stdio-ran")

	mgr := NewManager(map[string]config.MCPServerConfig{
		"local": {
			Type:    "stdio",
			Command: "sh",
			Args:    []string{"-c", "touch " + marker},
		},
	})
	defer mgr.Close()

	if mgr.HasTools() {
		t.Fatal("hosted stdio MCP should not register tools")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hosted stdio MCP should not start the configured local process")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
}
