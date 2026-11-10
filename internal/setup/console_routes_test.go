package setup

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestLegacyConsoleRedirects(t *testing.T) {
	h := spaHandler{fs: fstest.MapFS{
		"index.html":                               {Data: []byte("root")},
		"channels/telegram.svg":                    {Data: []byte("svg")},
		"console/models/index.html":                {Data: []byte("models")},
		"agents/default/chat/index.html":           {Data: []byte("chat")},
		"console/agents/default/skills/index.html": {Data: []byte("agent skills")},
	}}
	cases := []struct {
		path, want string
	}{
		{"/overview/", "/console/"},
		{"/models/", "/console/models/"},
		{"/apikeys", "/console/apikeys/"},
		{"/channels-config/", "/console/channels-config/"},
		{"/tools/", "/admin/tools/"},
		{"/agents/?manage=1", "/console/agents/"},
		{"/agents/?manage=1&x=1", "/console/agents/?x=1"},
		{"/agents/agt_1/skills/", "/console/agents/agt_1/skills/"},
		{"/agents/agt_1/channels/?tab=wechat", "/console/agents/agt_1/channels/?tab=wechat"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != c.want {
			t.Errorf("%s → %d %q, want redirect to %q", c.path, rec.Code, rec.Header().Get("Location"), c.want)
		}
	}
	// Chat routes, static assets and the user Tools page are not redirected.
	for _, path := range []string{"/agents/agt_1/chat/", "/channels/telegram.svg", "/agents/", "/console/tools/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusFound {
			t.Errorf("%s must not redirect (got %q)", path, rec.Header().Get("Location"))
		}
	}
}

func TestConsoleAgentPagesUsePlaceholder(t *testing.T) {
	h := spaHandler{fs: fstest.MapFS{
		"index.html":                               {Data: []byte("root")},
		"console/agents/default/index.html":        {Data: []byte("agent root")},
		"console/agents/default/skills/index.html": {Data: []byte("agent skills")},
		"console/agents/default/skills/index.txt":  {Data: []byte("rsc")},
		"agents/default/chat/_/index.html":         {Data: []byte("session")},
	}}
	for path, want := range map[string]string{
		"/console/agents/agt_9/":                 "agent root",
		"/console/agents/agt_9/skills/":          "agent skills",
		"/console/agents/agt_9/skills/index.txt": "rsc",
		"/agents/agt_9/chat/sess_1/?actAs=u_1":   "session", // audit links keep the long form
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}

// Conversations live at /chat/<sessionId>; the old per-agent and per-group
// URLs redirect there, and the static export's chat/_ placeholder serves
// any session id.
func TestChatSessionRoutes(t *testing.T) {
	h := spaHandler{fs: fstest.MapFS{
		"index.html":                       {Data: []byte("root")},
		"chat/index.html":                  {Data: []byte("legacy chat")},
		"chat/_/index.html":                {Data: []byte("session")},
		"chat/_/index.txt":                 {Data: []byte("rsc")},
		"agents/default/chat/_/index.html": {Data: []byte("agent session")},
	}}
	for path, want := range map[string]string{
		"/agents/agt_1/chat/s-1-abc/":     "/chat/s-1-abc/",
		"/agents/agt_1/chat/s-1-abc":      "/chat/s-1-abc/",
		"/teams/tm-1/chat/g-1-abc/":       "/chat/g-1-abc/",
		"/agents/agt_1/chat/s-1-abc/?x=1": "/chat/s-1-abc/?x=1",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s → %d %q, want redirect to %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	// New chats, audit links and RSC payloads keep their URLs.
	for _, path := range []string{
		"/agents/agt_1/chat/",
		"/agents/agt_1/chat/s-1-abc/?actAs=u_x",
		"/agents/agt_1/chat/s-1-abc/index.txt",
		"/agents/agt_1/chat/_/",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusFound {
			t.Errorf("%s must not redirect (got %q)", path, rec.Header().Get("Location"))
		}
	}
	for path, want := range map[string]string{
		"/chat/s-1-abc/":          "session",
		"/chat/g-1-abc/index.txt": "rsc",
		"/chat/":                  "legacy chat",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("%s → %d %q, want %q", path, rec.Code, rec.Body.String(), want)
		}
	}
}
