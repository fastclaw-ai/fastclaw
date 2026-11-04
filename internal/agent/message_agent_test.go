package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

func TestMessageAgentRefusesSelfAndDeepChains(t *testing.T) {
	a := &Agent{agentID: "agt_a", name: "a"}
	resolveSelf := func(context.Context, string, string) (*Agent, error) { return a, nil }
	if _, err := a.messageAgent(context.Background(), resolveSelf, "u", "a", "hi"); err == nil || !strings.Contains(err.Error(), "yourself") {
		t.Fatalf("self message err = %v", err)
	}
	deep := context.WithValue(context.Background(), messageAgentDepthKey{}, maxMessageAgentDepth)
	if _, err := a.messageAgent(deep, resolveSelf, "u", "b", "hi"); err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("deep chain err = %v", err)
	}
	if _, err := a.messageAgent(context.Background(), resolveSelf, "u", "b", "  "); err == nil {
		t.Fatal("empty message should be rejected")
	}
}

func TestGroupTurnsDropSideChannelTools(t *testing.T) {
	defs := []provider.Tool{{Function: provider.ToolFunction{Name: "exec"}}, {Function: provider.ToolFunction{Name: "message"}}, {Function: provider.ToolFunction{Name: "message_agent"}}}
	group := withoutGroupSideChannels(defs, bus.InboundMessage{Params: map[string]any{"__fastclawGroupChat": map[string]any{}}})
	if len(group) != 1 || group[0].Function.Name != "exec" {
		t.Fatalf("group tools = %+v", group)
	}
	if direct := withoutGroupSideChannels(defs, bus.InboundMessage{}); len(direct) != 3 {
		t.Fatalf("direct turn lost tools: %+v", direct)
	}
}
