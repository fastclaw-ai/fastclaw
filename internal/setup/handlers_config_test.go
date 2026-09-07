package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Round-trip regression tests for the masked-secret write-back path of
// POST /api/config: the dashboard GETs masked values ("sk-1****abcd")
// and POSTs them back verbatim on save. mergeSkillEntry must resolve
// those placeholders to the stored originals — before it was wired in,
// a no-op save replaced real keys with literal "****" strings.

func TestUpdateConfigMaskedGlobalSkillEntryKeepsStoredSecret(t *testing.T) {
	ctx := context.Background()
	s, resolver, adminUser, _ := newAuthTestServer(t, ctx)

	post := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		s.authMiddleware(s.handleUpdateConfig)(rr, configTestRequest(t, ctx, resolver, http.MethodPost, "/api/config", adminUser.ID, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("POST /api/config status = %d, body = %s", rr.Code, rr.Body.String())
		}
		return rr
	}
	storedEntry := func() config.SkillEntryCfg {
		t.Helper()
		rec, err := s.dataStore.GetConfigByName(ctx, store.KindSetting, "", "", "skills.entries")
		if err != nil || rec == nil {
			t.Fatalf("GetConfigByName: rec=%v err=%v", rec, err)
		}
		blob, _ := json.Marshal(rec.Data)
		var entries map[string]config.SkillEntryCfg
		if err := json.Unmarshal(blob, &entries); err != nil {
			t.Fatalf("decode entries: %v", err)
		}
		return entries["my-skill"]
	}

	post(map[string]any{"skills": map[string]any{"entries": map[string]any{
		"my-skill": map[string]any{
			"enabled": true,
			"apiKey":  "real-secret-key-123456",
			"env":     map[string]any{"NORMAL_VAR": "plain", "API_TOKEN": "real-env-secret-987654"},
		},
	}}})
	if got := storedEntry(); got.APIKey != "real-secret-key-123456" || got.Env["API_TOKEN"] != "real-env-secret-987654" {
		t.Fatalf("initial save lost secrets: %+v", got)
	}

	// Simulate the dashboard: GET (masked) → POST the masked entry back.
	get := httptest.NewRecorder()
	s.authMiddleware(s.handleGetConfig)(get, configTestRequest(t, ctx, resolver, http.MethodGet, "/api/config", adminUser.ID, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET /api/config status = %d", get.Code)
	}
	var view struct {
		Skills struct {
			Entries map[string]config.SkillEntryCfg `json:"entries"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	maskedEntry := view.Skills.Entries["my-skill"]
	if maskedEntry.APIKey == "real-secret-key-123456" || maskedEntry.Env["API_TOKEN"] == "real-env-secret-987654" {
		t.Fatalf("GET leaked plaintext secrets: %+v", maskedEntry)
	}

	post(map[string]any{"skills": map[string]any{"entries": map[string]any{"my-skill": maskedEntry}}})
	if got := storedEntry(); got.APIKey != "real-secret-key-123456" {
		t.Fatalf("masked write-back clobbered apiKey: got %q", got.APIKey)
	}
	if got := storedEntry(); got.Env["API_TOKEN"] != "real-env-secret-987654" {
		t.Fatalf("masked write-back clobbered env API_TOKEN: got %q", got.Env["API_TOKEN"])
	}
	if got := storedEntry(); got.Env["NORMAL_VAR"] != "plain" || !got.Enabled {
		t.Fatalf("non-secret fields should round-trip unchanged: %+v", got)
	}
}

func TestUpdateConfigMaskedAgentSkillEntryKeepsStoredSecret(t *testing.T) {
	ctx := context.Background()
	s, resolver, adminUser, _ := newAuthTestServer(t, ctx)

	if err := s.dataStore.SaveAgent(ctx, &store.AgentRecord{ID: "agent-1", UserID: adminUser.ID, Name: "Test Agent"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	post := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		s.authMiddleware(s.handleUpdateConfig)(rr, configTestRequest(t, ctx, resolver, http.MethodPost, "/api/config", adminUser.ID, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("POST /api/config status = %d, body = %s", rr.Code, rr.Body.String())
		}
		return rr
	}
	storedEntry := func() config.SkillEntryCfg {
		t.Helper()
		rec, err := s.dataStore.GetConfigByName(ctx, store.KindSetting, "", "agent-1", "skills.entries")
		if err != nil || rec == nil {
			t.Fatalf("GetConfigByName: rec=%v err=%v", rec, err)
		}
		blob, _ := json.Marshal(rec.Data)
		var entries map[string]config.SkillEntryCfg
		if err := json.Unmarshal(blob, &entries); err != nil {
			t.Fatalf("decode entries: %v", err)
		}
		return entries["my-skill"]
	}

	post(map[string]any{"skills": map[string]any{"agentEntries": map[string]any{
		"agent-1": map[string]any{
			"my-skill": map[string]any{
				"apiKey": "agent-secret-key-abcdef",
				"env":    map[string]any{"DB_PASSWORD": "real-db-pass-123456"},
			},
		},
	}}})
	if got := storedEntry(); got.APIKey != "agent-secret-key-abcdef" || got.Env["DB_PASSWORD"] != "real-db-pass-123456" {
		t.Fatalf("initial save lost secrets: %+v", got)
	}

	get := httptest.NewRecorder()
	s.authMiddleware(s.handleGetConfig)(get, configTestRequest(t, ctx, resolver, http.MethodGet, "/api/config", adminUser.ID, nil))
	var view struct {
		Skills struct {
			AgentEntries map[string]map[string]config.SkillEntryCfg `json:"agentEntries"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	maskedEntry := view.Skills.AgentEntries["agent-1"]["my-skill"]
	if maskedEntry.APIKey == "agent-secret-key-abcdef" {
		t.Fatalf("GET leaked plaintext apiKey: %+v", maskedEntry)
	}

	post(map[string]any{"skills": map[string]any{"agentEntries": map[string]any{
		"agent-1": map[string]any{"my-skill": maskedEntry},
	}}})
	if got := storedEntry(); got.APIKey != "agent-secret-key-abcdef" {
		t.Fatalf("masked write-back clobbered apiKey: got %q", got.APIKey)
	}
	if got := storedEntry(); got.Env["DB_PASSWORD"] != "real-db-pass-123456" {
		t.Fatalf("masked write-back clobbered env DB_PASSWORD: got %q", got.Env["DB_PASSWORD"])
	}
}

func TestMergeSkillEntry(t *testing.T) {
	existing := config.SkillEntryCfg{
		Enabled: true,
		APIKey:  "real-secret-key-123456",
		Env:     map[string]string{"API_TOKEN": "real-env-secret-987654", "NORMAL_VAR": "old"},
	}
	in := config.SkillEntryCfg{
		Enabled: false,
		APIKey:  maskAPIKey("real-secret-key-123456"),
		Env:     map[string]string{"API_TOKEN": maskAPIKey("real-env-secret-987654"), "NORMAL_VAR": "new"},
	}
	out := mergeSkillEntry(existing, in)
	if out.APIKey != existing.APIKey {
		t.Errorf("masked apiKey should resolve to stored original, got %q", out.APIKey)
	}
	if out.Env["API_TOKEN"] != existing.Env["API_TOKEN"] {
		t.Errorf("masked env value should resolve to stored original, got %q", out.Env["API_TOKEN"])
	}
	if out.Env["NORMAL_VAR"] != "new" {
		t.Errorf("plain env value should follow the request, got %q", out.Env["NORMAL_VAR"])
	}
	if out.Enabled != in.Enabled {
		t.Errorf("enabled should follow the request, got %v", out.Enabled)
	}

	// A genuinely new value (no mask) must replace the stored one.
	out = mergeSkillEntry(existing, config.SkillEntryCfg{APIKey: "brand-new-key-000000"})
	if out.APIKey != "brand-new-key-000000" {
		t.Errorf("unmasked apiKey should replace the stored one, got %q", out.APIKey)
	}
}

func TestAdminConfigExplicitUserAndSystemScopesStaySeparate(t *testing.T) {
	ctx := context.Background()
	s, resolver, adminUser, _ := newAuthTestServer(t, ctx)

	post := func(path, model string) {
		t.Helper()
		body := map[string]any{
			"agents": map[string]any{
				"defaults": map[string]any{"model": model},
			},
		}
		rr := httptest.NewRecorder()
		s.authMiddleware(s.handleUpdateConfig)(
			rr,
			configTestRequest(t, ctx, resolver, http.MethodPost, path, adminUser.ID, body),
		)
		if rr.Code != http.StatusOK {
			t.Fatalf("POST %s status = %d, body = %s", path, rr.Code, rr.Body.String())
		}
	}

	post("/api/config?scope=system", "system/provider")
	post("/api/config?scope=user", "personal/provider")

	readModel := func(userID string) string {
		t.Helper()
		rec, err := s.dataStore.GetConfigByName(
			ctx,
			store.KindSetting,
			userID,
			"",
			"agents.defaults",
		)
		if err != nil || rec == nil {
			t.Fatalf("GetConfigByName user=%q: rec=%v err=%v", userID, rec, err)
		}
		blob, _ := json.Marshal(rec.Data)
		var defaults config.AgentDefaults
		if err := json.Unmarshal(blob, &defaults); err != nil {
			t.Fatalf("decode defaults user=%q: %v", userID, err)
		}
		return defaults.Model
	}

	if got := readModel(""); got != "system/provider" {
		t.Fatalf("system model = %q, want %q", got, "system/provider")
	}
	if got := readModel(adminUser.ID); got != "personal/provider" {
		t.Fatalf("admin personal model = %q, want %q", got, "personal/provider")
	}

	getModel := func(path string) string {
		t.Helper()
		rr := httptest.NewRecorder()
		s.authMiddleware(s.handleGetConfig)(
			rr,
			configTestRequest(t, ctx, resolver, http.MethodGet, path, adminUser.ID, nil),
		)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, rr.Code, rr.Body.String())
		}
		var view struct {
			Agents config.AgentsConfig `json:"agents"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		return view.Agents.Defaults.Model
	}

	if got := getModel("/api/config?scope=system"); got != "system/provider" {
		t.Fatalf("system view model = %q", got)
	}
	if got := getModel("/api/config?scope=user"); got != "personal/provider" {
		t.Fatalf("user view model = %q", got)
	}
}

func configTestRequest(t *testing.T, ctx context.Context, resolver *auth.Resolver, method, path, userID string, body map[string]any) *http.Request {
	t.Helper()

	cookie, err := resolver.IssueSession(ctx, userID)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	var reader *bytes.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(blob)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(cookie)
	return req
}
