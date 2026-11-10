package tools

import "testing"

// The fallback when a one-time link can't be minted: the product's page,
// opened on the platform — no credential.
func TestConnectorsPageLink(t *testing.T) {
	tool := &connectorsTool{links: &ConnectLinks{PageURL: "https://weclaw.im/devices?tab=connectors"}}
	if got := tool.pageLink("notion"); got != "https://weclaw.im/devices?connector=notion&tab=connectors" {
		t.Fatalf("page link: %s", got)
	}
	tool = &connectorsTool{links: &ConnectLinks{}}
	if got := tool.pageLink("notion"); got != "" {
		t.Fatalf("no page: %s", got)
	}
}
