package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
)

type fakeBuiltin struct{ calls []agl.BuiltinCall }

func (f *fakeBuiltin) BuiltinTools(context.Context, agl.LocalMachine) []agl.BuiltinTool { return nil }

func (f *fakeBuiltin) CallBuiltin(_ context.Context, c agl.BuiltinCall) (string, error) {
	f.calls = append(f.calls, c)
	return `{"ok":true}`, nil
}

func TestRegisterComputerToolsMountsOffered(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	svc := &fakeBuiltin{}
	offered := map[string]agl.BuiltinTool{
		"read": {Name: "read", Description: "Read.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}},
	}
	chat := agl.ToolChat{Channel: "wechat", AccountID: "bot", ChatID: "wx_alice"}
	RegisterComputerTools(r, svc, "u_alice", chat, nil, offered, []string{"Mini", "Studio"})
	if !r.HasTool("computer_read") || r.HasTool("computer_exec") || r.HasTool("computer_write") {
		t.Fatal("wrong tools mounted")
	}
	// The cached definition is not modified by the per-turn machine param.
	if _, ok := offered["read"].InputSchema["properties"].(map[string]any)["machine"]; ok {
		t.Fatal("machine param leaked into the shared schema")
	}
	out, err := r.Execute(context.Background(), "computer_read", `{"path":"~/a.txt","machine":"Studio"}`)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("execute = %q, %v", out, err)
	}
	if len(svc.calls) != 1 || svc.calls[0].UserID != "u_alice" || svc.calls[0].Machine != "Studio" || svc.calls[0].Name != "read" || svc.calls[0].Chat != chat {
		t.Fatalf("calls = %+v", svc.calls)
	}
	var args map[string]any
	_ = json.Unmarshal(svc.calls[0].Args, &args)
	if args["path"] != "~/a.txt" {
		t.Fatalf("args = %v", args)
	}
}

func TestComputerToolsNote(t *testing.T) {
	readOnly := ComputerToolsNote([]string{"Mini"}, []string{"Mini"}, map[string]agl.BuiltinTool{"read": {}})
	for _, want := range []string{"computer_read", "re-linked with more access", "no coding agent"} {
		if !strings.Contains(readOnly, want) {
			t.Errorf("read-only note lacks %q: %s", want, readOnly)
		}
	}
	full := ComputerToolsNote([]string{"MacBook"}, nil, map[string]agl.BuiltinTool{"read": {}, "write": {}, "edit": {}, "exec": {}})
	for _, want := range []string{"computer_exec", "run_local_agent", "quick look-ups", "hand the whole task to run_local_agent"} {
		if !strings.Contains(full, want) {
			t.Errorf("note lacks %q: %s", want, full)
		}
	}
	if strings.Contains(full, "re-linked") || strings.Contains(full, "no coding agent") {
		t.Fatalf("note = %q", full)
	}
}
