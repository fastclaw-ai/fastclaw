package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/connectors"
)

type fakeAsker struct {
	decisions []string
	asked     []agl.ApprovalAsk
	err       error
}

func (f *fakeAsker) Ask(_ context.Context, a agl.ApprovalAsk) (string, error) {
	f.asked = append(f.asked, a)
	if f.err != nil {
		return "", f.err
	}
	d := f.decisions[0]
	f.decisions = f.decisions[1:]
	return d, nil
}

func TestConnectorWritesAskThePerson(t *testing.T) {
	asker := &fakeAsker{decisions: []string{"deny", agl.DecisionTimeout, "allow", "allow_session"}}
	chat := agl.WebToolChat("u1", "agent", "session-approval-test")
	tool := &connectorsTool{userID: "u1", approvals: &ConnectorApprovals{Asker: asker, Chat: chat}}
	acct := &connectors.Account{ID: "c1", Name: "Work"}
	def := connectors.ToolDefinition{Name: "notion.notion-update-page"}
	input := map[string]any{"page_id": "p1", "command": "replace_content", "new_str": ""}
	approve := tool.approver(acct, "Notion", input)
	ctx := context.Background()

	var na *notApproved
	if err := approve(ctx, def); !errors.As(err, &na) || !strings.Contains(na.msg, "declined") {
		t.Fatalf("deny: %v", err)
	}
	// What the person sees: the platform, account, tool and the exact input.
	if got := asker.asked[0]; got.UserID != "u1" || got.Chat != chat || got.Agent != "Notion" ||
		got.Title != `Notion "Work" · notion-update-page · {"command":"replace_content","new_str":"","page_id":"p1"}` {
		t.Fatalf("asked: %+v", got)
	}
	if err := approve(ctx, def); !errors.As(err, &na) || !strings.Contains(na.msg, "in time") {
		t.Fatalf("timeout: %v", err)
	}
	if err := approve(ctx, def); err != nil {
		t.Fatalf("allow: %v", err)
	}
	if err := approve(ctx, def); err != nil {
		t.Fatalf("allow_session: %v", err)
	}
	// Allowed for the session: the same tool on the same account runs
	// without asking again; another tool still asks.
	if err := approve(ctx, def); err != nil || len(asker.asked) != 4 {
		t.Fatalf("granted: %v, asked %d times", err, len(asker.asked))
	}
	asker.decisions = []string{"deny"}
	if err := approve(ctx, connectors.ToolDefinition{Name: "notion.notion-move-pages"}); !errors.As(err, &na) {
		t.Fatalf("other tool: %v", err)
	}
	// A busy chat or a missing chat refuses the write; nothing runs.
	asker.err = &agl.ToolError{Msg: "Another approval is still waiting."}
	if err := tool.approver(acct, "Notion", nil)(ctx, connectors.ToolDefinition{Name: "notion.x"}); !errors.As(err, &na) || !strings.Contains(na.msg, "Nothing was changed") {
		t.Fatalf("busy: %v", err)
	}
	// Without a way to ask, writes are not offered at all.
	if (&connectorsTool{}).approver(acct, "Notion", nil) != nil {
		t.Fatal("approver without approvals")
	}
}
