package sandbox

import "testing"

func TestBoxlitePaths(t *testing.T) {
	cases := []struct {
		baseURL, prefix string
		wantCreate      string
		wantAttach      string
	}{
		{defaultBoxliteURL, "", "https://api.boxlite.ai/v1/boxes", "wss://api.boxlite.ai/v1/boxes/b1/executions/x/attach"},
		{"https://api.dev.boxlite.ai/api/v1", "", "https://api.dev.boxlite.ai/api/v1/default/boxes", "wss://api.dev.boxlite.ai/api/v1/default/boxes/b1/executions/x/attach"},
		{"http://localhost:8080/v1/", "/tenant/", "http://localhost:8080/v1/tenant/boxes", "ws://localhost:8080/v1/tenant/boxes/b1/executions/x/attach"},
	}
	for _, c := range cases {
		e := &BoxliteExecutor{baseURL: trimSlash(c.baseURL), prefix: resolveBoxlitePrefix(c.baseURL, c.prefix), boxID: "b1"}
		if got := e.prefixPath("/boxes"); got != c.wantCreate {
			t.Errorf("%s %q: create = %s, want %s", c.baseURL, c.prefix, got, c.wantCreate)
		}
		got, err := e.attachWebsocketURL("x")
		if err != nil || got != c.wantAttach {
			t.Errorf("%s %q: attach = %s (%v), want %s", c.baseURL, c.prefix, got, err, c.wantAttach)
		}
	}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
