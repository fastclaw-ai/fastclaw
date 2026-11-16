package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// A product's agent creator adds skills to the agent: only the agent's owner
// (the product, which authorizes its creator) may, a creator skill can't
// take a product, system or bundled name, and its keys are its own.
func TestCreatorSkills(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("FASTCLAW_HOME", home)
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: "agt_1", UserID: "u_owner", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	for dir, name := range map[string]string{
		filepath.Join(home, "skills"):                      "image-tool",
		filepath.Join(home, "users", "u_owner", "skills"): "brand",
	} {
		os.MkdirAll(filepath.Join(dir, name), 0o755)
		os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(skillMD(name, "x")), 0o644)
	}
	s := NewServer(0)
	s.dataStore = st

	owner := auth.Identity{UserID: "u_owner", Role: users.RoleUser, AuthMethod: "apikey", APIKeyID: "k1"}
	endUser := auth.Identity{UserID: "u_alice", Role: users.RoleAppUser, AuthMethod: "apikey", APIKeyID: "k1", AppUserID: "u_owner", EndUser: "user:u_alice"}
	stranger := auth.Identity{UserID: "u_x", Role: users.RoleUser, AuthMethod: "session"}

	upload := func(id auth.Identity, name string) (int, string) {
		body, ct := mySkillBody(t, name+".zip", map[string]string{
			name + "/SKILL.md":       "---\nname: " + name + "\ndescription: d\nenv:\n  - name: MY_KEY\n---\nRun scripts/run.py\n",
			name + "/scripts/run.py": "print('hi')\n",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/agents/agt_1/skills/upload", body)
		req.Header.Set("Content-Type", ct)
		req.SetPathValue("id", "agt_1")
		rec := httptest.NewRecorder()
		s.handleUploadCreatorSkill(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		return rec.Code, rec.Body.String()
	}
	env := func(id auth.Identity, method, name, body string) (int, string) {
		req := httptest.NewRequest(method, "/api/agents/agt_1/skills/"+name+"/env", bytes.NewBufferString(body))
		req.SetPathValue("id", "agt_1")
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		h := s.handleGetCreatorSkillEnv
		if method == http.MethodPut {
			h = s.handlePutCreatorSkillEnv
		}
		h(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		return rec.Code, rec.Body.String()
	}

	for _, id := range []auth.Identity{endUser, stranger} {
		if code, body := upload(id, "weather"); code != http.StatusForbidden && code != http.StatusNotFound {
			t.Fatalf("%s uploaded: %d %s", id.UserID, code, body)
		}
	}
	for _, taken := range []string{"image-tool", "brand"} {
		if code, body := upload(owner, taken); code != http.StatusConflict || !strings.Contains(body, "name_taken") {
			t.Fatalf("%s: %d %s", taken, code, body)
		}
	}
	if names := agent.BundledSkillNames(); len(names) > 0 {
		if code, _ := upload(owner, names[0]); code != http.StatusConflict {
			t.Fatalf("bundled %s: %d", names[0], code)
		}
	}
	code, body := upload(owner, "weather")
	if code != http.StatusOK || !strings.Contains(body, "MY_KEY") {
		t.Fatalf("upload: %d %s", code, body)
	}
	agentHome, _ := config.AgentHomeDir("agt_1")
	dir := filepath.Join(agentHome, "skills", "weather")
	if _, err := os.Stat(filepath.Join(dir, "scripts", "run.py")); err != nil {
		t.Fatal("scripts are kept: ", err)
	}
	if _, err := os.Stat(filepath.Join(dir, agent.CreatorSkillMarker)); err != nil {
		t.Fatal("not marked as a creator skill")
	}

	// Keys: the creator's own, masked on the way back; bad names refused.
	for _, bad := range []string{`{"env":{"PATH":"x"}}`, `{"env":{"LD_PRELOAD":"x"}}`, `{"env":{"PYTHONPATH":"x"}}`, `{"env":{"lower":"x"}}`} {
		if code, body := env(owner, http.MethodPut, "weather", bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, code, body)
		}
	}
	if code, body := env(endUser, http.MethodPut, "weather", `{"env":{"MY_KEY":"sk-creator-secret-1234"}}`); code == http.StatusOK {
		t.Fatalf("end user set keys: %s", body)
	}
	if code, body := env(owner, http.MethodPut, "weather", `{"env":{"MY_KEY":"sk-creator-secret-1234","REGION":"sg"}}`); code != http.StatusOK || strings.Contains(body, "creator-secret") {
		t.Fatalf("set: %d %s", code, body)
	}
	_, body = env(owner, http.MethodGet, "weather", "")
	var got struct {
		Env map[string]string `json:"env"`
	}
	json.Unmarshal([]byte(body), &got)
	if strings.Contains(body, "creator-secret") || got.Env["MY_KEY"] == "" {
		t.Fatalf("get: %s", body)
	}
	// Saving the masked value back keeps the key; leaving a name out drops it.
	env(owner, http.MethodPut, "weather", `{"env":{"MY_KEY":"`+got.Env["MY_KEY"]+`"}}`)
	entries, _ := loadAgentSkillEntries(ctx, st, "agt_1")
	if e := entries["weather"].Env; e["MY_KEY"] != "sk-creator-secret-1234" || e["REGION"] != "" {
		t.Fatalf("stored: %v", e)
	}
	// Only creator skills take keys this way.
	if code, _ := env(owner, http.MethodPut, "brand", `{"env":{"MY_KEY":"x"}}`); code != http.StatusNotFound {
		t.Fatalf("non-creator skill: %d", code)
	}

	// Deleting the skill drops its keys; ".." never reaches RemoveAll.
	del := func(name string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/agents/agt_1/skills/x", nil)
		req.SetPathValue("id", "agt_1")
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		s.handleDeleteAgentSkill(rec, req.WithContext(auth.WithIdentity(req.Context(), owner)))
		return rec.Code
	}
	if code := del(".."); code != http.StatusBadRequest {
		t.Fatalf("..: %d", code)
	}
	if _, err := os.Stat(agentHome); err != nil {
		t.Fatal("agent home removed")
	}
	if code := del("weather"); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if entries, _ := loadAgentSkillEntries(ctx, st, "agt_1"); entries["weather"].Env != nil {
		t.Fatalf("keys kept: %v", entries)
	}
}
