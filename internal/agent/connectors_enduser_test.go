package agent

import "testing"

func TestConnectorsURLParam(t *testing.T) {
	for raw, want := range map[string]string{
		"https://weclaw.im/devices":      "https://weclaw.im/devices",
		" http://localhost:3000/devices": "http://localhost:3000/devices",
		"javascript:alert(1)":            "",
		"https://u:p@weclaw.im/x":        "",
		"/devices":                       "",
		"":                               "",
	} {
		if got := urlParam(map[string]any{ConnectorsURLParam: raw}, ConnectorsURLParam); got != want {
			t.Errorf("urlParam(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := urlParam(nil, ConnectorsURLParam); got != "" {
		t.Errorf("nil params = %q", got)
	}
}

func TestConnectLinks(t *testing.T) {
	if l := connectLinks(map[string]any{}); l != nil {
		t.Fatalf("no page: want cards, got %+v", l)
	}
	l := connectLinks(map[string]any{ConnectorsURLParam: "https://weclaw.im/connectors"})
	if l == nil || l.ReturnURL != "https://weclaw.im/connectors" {
		t.Fatalf("return defaults to the page: %+v", l)
	}
	l = connectLinks(map[string]any{
		ConnectorsURLParam:       "https://weclaw.im/connectors",
		ConnectorsReturnURLParam: "https://weclaw.im/connectors/done",
	})
	if l == nil || l.ReturnURL != "https://weclaw.im/connectors/done" || l.PageURL != "https://weclaw.im/connectors" {
		t.Fatalf("got %+v", l)
	}
}
