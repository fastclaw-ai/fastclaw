package tools

import (
	"strings"
	"testing"
)

// Where the chat can't show a card, request_connection hands the model
// the product's connect page, opened on the platform — no credential.
func TestConnectorsLinkFor(t *testing.T) {
	tool := &connectorsTool{connectURL: "https://weclaw.im/devices?tab=connectors"}
	got := tool.linkFor("notion", "Notion", "connect")
	if !strings.Contains(got, "https://weclaw.im/devices?connector=notion&tab=connectors") {
		t.Fatalf("link missing: %s", got)
	}
	if strings.Contains(got, "\x1fFC_META") {
		t.Fatalf("link mode must not end the turn with a card: %q", got)
	}
}
