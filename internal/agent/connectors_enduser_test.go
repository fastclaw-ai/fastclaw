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
		if got := connectorsURLParam(map[string]any{ConnectorsURLParam: raw}); got != want {
			t.Errorf("connectorsURLParam(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := connectorsURLParam(nil); got != "" {
		t.Errorf("nil params = %q", got)
	}
}
