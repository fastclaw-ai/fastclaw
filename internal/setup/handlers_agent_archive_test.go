package setup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Export from one agent, import into another: identity files, skills and
// prompt settings carry over; files the archive lacks are cleared.
func TestAgentArchiveRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, resolver, _, user := newAuthTestServer(t, ctx)
	t.Setenv("FASTCLAW_HOME", t.TempDir())

	for _, id := range []string{"agt_src", "agt_dst"} {
		if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: id, UserID: user.ID, Name: "Menu Bot"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.dataStore.SaveAgentFile(ctx, "agt_src", user.ID, "SOUL.md", []byte("be kind"))
	_ = s.dataStore.SaveAgentFile(ctx, "agt_src", user.ID, "MEMORY.md", []byte("private"))
	_ = s.dataStore.SaveAgentFile(ctx, "agt_dst", user.ID, "BOOTSTRAP.md", []byte("old"))
	if err := s.applyAgentScopeDefaultsPatch(httptest.NewRequest("GET", "/", nil), "agt_src", map[string]any{"promptMode": "customize"}); err != nil {
		t.Fatal(err)
	}
	srcHome, _ := config.AgentHomeDir("agt_src")
	writeFile(t, filepath.Join(srcHome, "skills", "menu", "SKILL.md"), "---\nname: menu\n---\n")
	writeFile(t, filepath.Join(srcHome, "skills", "menu", "scripts", "run.sh"), "echo hi")
	dstHome, _ := config.AgentHomeDir("agt_dst")
	writeFile(t, filepath.Join(dstHome, "skills", "stale", "SKILL.md"), "old")

	rr := httptest.NewRecorder()
	s.authMiddleware(s.handleExportAgentArchive)(rr, withPath(authTestRequest(t, ctx, resolver, "GET", "/api/agents/agt_src/archive", user.ID), "agt_src"))
	if rr.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rr.Code, rr.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"fastclaw-agent.json", "SOUL.md", "skills/menu/SKILL.md", "skills/menu/scripts/run.sh"} {
		if !names[want] {
			t.Errorf("export missing %s (have %v)", want, names)
		}
	}
	if names["MEMORY.md"] {
		t.Error("export must not include MEMORY.md")
	}

	importReq := func(query string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "menu.zip")
		fw.Write(rr.Body.Bytes())
		mw.Close()
		req := authTestRequest(t, ctx, resolver, "POST", "/api/agents/agt_dst/archive"+query, user.ID)
		req.Body = http.NoBody
		req2 := httptest.NewRequest("POST", "/api/agents/agt_dst/archive"+query, &body)
		req2.Header.Set("Content-Type", mw.FormDataContentType())
		for _, c := range req.Cookies() {
			req2.AddCookie(c)
		}
		out := httptest.NewRecorder()
		s.authMiddleware(s.handleImportAgentArchive)(out, withPath(req2, "agt_dst"))
		return out
	}

	preview := importReq("?dryRun=1")
	if preview.Code != http.StatusOK {
		t.Fatalf("dry run: %d %s", preview.Code, preview.Body.String())
	}
	var p struct {
		Format string   `json:"format"`
		Files  []string `json:"files"`
		Skills []string `json:"skills"`
	}
	json.Unmarshal(preview.Body.Bytes(), &p)
	if p.Format != "fastclaw-agent" || len(p.Files) != 1 || len(p.Skills) != 1 {
		t.Fatalf("preview = %s", preview.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dstHome, "skills", "stale")); err != nil {
		t.Fatal("dry run must not touch skills")
	}

	if res := importReq(""); res.Code != http.StatusOK {
		t.Fatalf("import: %d %s", res.Code, res.Body.String())
	}
	if data, _ := s.dataStore.GetAgentFileExact(ctx, "agt_dst", user.ID, "SOUL.md"); string(data) != "be kind" {
		t.Errorf("SOUL.md = %q", data)
	}
	if _, err := s.dataStore.GetAgentFileExact(ctx, "agt_dst", user.ID, "BOOTSTRAP.md"); err == nil {
		t.Error("BOOTSTRAP.md absent from archive should be cleared")
	}
	if _, err := os.Stat(filepath.Join(dstHome, "skills", "menu", "scripts", "run.sh")); err != nil {
		t.Error("imported skill resource missing")
	}
	if _, err := os.Stat(filepath.Join(dstHome, "skills", "stale")); !os.IsNotExist(err) {
		t.Error("stale skill should be replaced")
	}
	if got := s.agentScopePromptMode(httptest.NewRequest("GET", "/", nil), "agt_dst"); got != "customize" {
		t.Errorf("promptMode = %q", got)
	}
}

// A plain workspace folder (no manifest, wrapped in one directory) imports
// too; anything that isn't an identity file or skill is reported, not applied.
func TestParseAgentArchiveWorkspaceLayout(t *testing.T) {
	p, err := parseAgentArchive(map[string][]byte{
		"my-agent/SOUL.md":               []byte("soul"),
		"my-agent/USER.md":               []byte("me"),
		"my-agent/skills/a/SKILL.md":     []byte("a"),
		"my-agent/skills/b/notes.txt":    []byte("no SKILL.md"),
		"my-agent/sessions/s1/chat.json": []byte("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest != nil || string(p.Files["SOUL.md"]) != "soul" || len(p.Files) != 1 {
		t.Fatalf("files = %v", p.Files)
	}
	if len(p.Skills) != 1 || p.Skills["a"] == nil {
		t.Fatalf("skills = %v", p.Skills)
	}
	if len(p.Ignored) != 3 {
		t.Fatalf("ignored = %v", p.Ignored)
	}
	if _, err := parseAgentArchive(map[string][]byte{"notes.txt": []byte("x")}); err == nil {
		t.Fatal("archive with nothing importable should fail")
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withPath(r *http.Request, id string) *http.Request {
	r.SetPathValue("id", id)
	return r
}
