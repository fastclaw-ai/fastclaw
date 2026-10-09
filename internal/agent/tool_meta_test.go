package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
)

func TestExtractToolMetaJSON(t *testing.T) {
	raw := tools.WithMeta("A button is now shown.\nSecond line.", map[string]any{
		"endsTurn":         true,
		"connectorRequest": map[string]any{"id": "r1", "connector": "notion"},
	})
	text, meta := extractToolMeta(raw)
	if text != "A button is now shown.\nSecond line." {
		t.Fatalf("text = %q", text)
	}
	if meta["endsTurn"] != true {
		t.Fatalf("meta = %v", meta)
	}
	req, _ := meta["connectorRequest"].(map[string]any)
	if req["id"] != "r1" {
		t.Fatalf("connectorRequest = %v", meta["connectorRequest"])
	}
	// Plain results and the sandbox marker are unchanged.
	if text, meta := extractToolMeta("plain"); text != "plain" || meta != nil {
		t.Fatalf("plain = %q %v", text, meta)
	}
	if _, meta := extractToolMeta(tools.MetaSandboxPrefix + "x"); meta["sandbox"] != true {
		t.Fatalf("sandbox = %v", meta)
	}
}
