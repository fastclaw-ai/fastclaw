package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// newMachinesHarness is the v1 harness with the agl hub enabled.
func newMachinesHarness(t *testing.T) (*v1Harness, *agl.Hub) {
	h := newV1Harness(t)
	authResolver, err := auth.NewResolver(h.st)
	if err != nil {
		t.Fatal(err)
	}
	hub := agl.NewHub(h.st, "https://fc.example.com")
	srv := NewServer(h.rt, authResolver, nil)
	srv.SetStore(h.st)
	srv.SetAglHub(hub)
	h.mux = http.NewServeMux()
	srv.RegisterRoutes(h.mux)
	return h, hub
}

// addMachine links a machine to endUser directly in the store.
func (h *v1Harness) addMachine(endUser, id string) {
	h.t.Helper()
	// Any end-user request mints the app_user.
	if code, out := h.do("GET", "/v1/machines", h.keyA, nil, "X-Fastclaw-End-User", endUser); code != http.StatusOK {
		h.t.Fatalf("list machines for %s: %d %v", endUser, code, out)
	}
	if err := h.st.CreateMachine(context.Background(), &store.MachineRecord{
		ID: id, UserID: h.appUserID(h.appA, endUser), AglMachineID: id, Hostname: "mac-" + endUser, Name: "mac-" + endUser,
		OS: "macos", Arch: "arm64", Version: "0.3.3", TokenHash: "hash-" + id,
		Agents: []store.MachineAgent{{ID: "claude", Name: "Claude Code", Available: true}},
	}); err != nil {
		h.t.Fatal(err)
	}
}

func flatErr(out map[string]any) string { s, _ := out["error"].(string); return s }

func TestMachinesRequireEndUser(t *testing.T) {
	h, _ := newMachinesHarness(t)
	for _, c := range []struct{ method, path string }{
		{"POST", "/v1/machines/pairing"}, {"POST", "/v1/machines/pairing/status"},
		{"GET", "/v1/machines"}, {"DELETE", "/v1/machines/mch_x"},
	} {
		code, out := h.do(c.method, c.path, h.keyA, map[string]any{})
		if code != http.StatusBadRequest || flatErr(out) == "" {
			t.Errorf("%s %s without end user: %d %v", c.method, c.path, code, out)
		}
	}
}

func TestMachinePairingAPI(t *testing.T) {
	h, _ := newMachinesHarness(t)
	code, out := h.do("POST", "/v1/machines/pairing", h.keyA, map[string]any{}, "X-Fastclaw-End-User", "alice")
	if code != http.StatusOK {
		t.Fatalf("pairing: %d %v", code, out)
	}
	pc, _ := out["code"].(string)
	want := `curl -fsSL "https://fc.example.com/agl/install.sh?code=` + pc + `" | sh`
	if pc == "" || out["installCommand"] != want || out["expiresAt"] == nil {
		t.Fatalf("pairing response: %v", out)
	}
	code, out = h.do("POST", "/v1/machines/pairing/status", h.keyA, map[string]any{"code": pc}, "X-Fastclaw-End-User", "alice")
	if code != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("status: %d %v", code, out)
	}
	// Only the end user who created the code may poll it.
	code, out = h.do("POST", "/v1/machines/pairing/status", h.keyA, map[string]any{"code": pc}, "X-Fastclaw-End-User", "bob")
	if code != http.StatusNotFound {
		t.Fatalf("bob polls alice's code: %d %v", code, out)
	}
}

func TestMachinesIsolatedPerEndUser(t *testing.T) {
	h, _ := newMachinesHarness(t)
	h.addMachine("alice", "mch_alice")
	h.addMachine("bob", "mch_bob")

	code, out := h.do("GET", "/v1/machines", h.keyA, nil, "X-Fastclaw-End-User", "alice")
	list, _ := out["machines"].([]any)
	if code != http.StatusOK || len(list) != 1 {
		t.Fatalf("alice's machines: %d %v", code, out)
	}
	m := list[0].(map[string]any)
	if m["id"] != "mch_alice" || m["name"] != "mac-alice" || m["online"] != false || m["os"] != "macos" {
		t.Fatalf("machine view: %v", m)
	}
	if ags, _ := m["agents"].([]any); len(ags) != 1 || ags[0].(map[string]any)["id"] != "claude" {
		t.Fatalf("machine agents: %v", m["agents"])
	}
	// Bob can't delete (or see) alice's machine.
	if code, out := h.do("DELETE", "/v1/machines/mch_alice", h.keyA, nil, "X-Fastclaw-End-User", "bob"); code != http.StatusNotFound {
		t.Fatalf("bob deletes alice's machine: %d %v", code, out)
	}
	// Bob can't bind an agent to it either.
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{
		"name": "x", "owner": "end_user", "runtime": "agl",
		"runtimeConfig": map[string]any{"machineId": "mch_alice", "aglAgent": "claude"},
	}, "X-Fastclaw-End-User", "bob")
	if code != http.StatusBadRequest {
		t.Fatalf("bob binds alice's machine: %d %v", code, out)
	}
	// Alice revokes hers: gone from the list, token cleared.
	if code, out := h.do("DELETE", "/v1/machines/mch_alice", h.keyA, nil, "X-Fastclaw-End-User", "alice"); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("alice deletes: %d %v", code, out)
	}
	if _, out := h.do("GET", "/v1/machines", h.keyA, nil, "X-Fastclaw-End-User", "alice"); len(out["machines"].([]any)) != 0 {
		t.Fatalf("revoked machine listed: %v", out)
	}
	if rec, _ := h.st.GetMachine(context.Background(), "mch_alice"); rec.TokenHash != "" || !rec.Revoked() {
		t.Fatalf("revoked row: %+v", rec)
	}
	if code, _ := h.do("DELETE", "/v1/machines/mch_alice", h.keyA, nil, "X-Fastclaw-End-User", "alice"); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}

func TestAglAgents(t *testing.T) {
	h, _ := newMachinesHarness(t)
	h.addMachine("alice", "mch_alice")
	alice := []string{"X-Fastclaw-End-User", "alice"}
	rc := map[string]any{"machineId": "mch_alice", "aglAgent": "claude", "cwd": "~/code/x"}

	// runtime agl is private-only.
	code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "runtime": "agl", "runtimeConfig": rc}, alice...)
	if code != http.StatusBadRequest {
		t.Fatalf("agl platform agent via header only: %d %v", code, out)
	}
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "owner": "account", "runtime": "agl", "runtimeConfig": rc}, alice...)
	if code != http.StatusBadRequest {
		t.Fatalf("agl platform agent: %d %v", code, out)
	}
	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "runtime": "llama", "owner": "end_user"}, alice...)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown runtime: %d %v", code, out)
	}
	// Validation: unknown agent / access / missing fields.
	for _, bad := range []map[string]any{
		{"machineId": "mch_alice", "aglAgent": "codex"},
		{"machineId": "mch_alice", "aglAgent": "claude", "access": "root"},
		{"machineId": "mch_alice"},
		{"machineId": "mch_nope", "aglAgent": "claude"},
	} {
		if code, out := h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "x", "owner": "end_user", "runtime": "agl", "runtimeConfig": bad}, alice...); code != http.StatusBadRequest {
			t.Errorf("runtimeConfig %v: %d %v", bad, code, out)
		}
	}

	code, out = h.do("POST", "/v1/agents", h.keyA, map[string]any{"name": "My Mac", "description": "d", "owner": "end_user", "runtime": "agl", "runtimeConfig": rc}, alice...)
	if code != http.StatusOK {
		t.Fatalf("create agl agent: %d %v", code, out)
	}
	ag := out["agent"].(map[string]any)
	id := ag["id"].(string)
	got, _ := ag["runtimeConfig"].(map[string]any)
	if ag["owner"] != "end_user" || ag["runtime"] != "agl" || got["machineId"] != "mch_alice" || got["access"] != "ask" || got["cwd"] != "~/code/x" {
		t.Fatalf("agent view: %v", ag)
	}

	// GET: owner 200 with runtime; another end user 404.
	code, out = h.do("GET", "/v1/agents/"+id, h.keyA, nil, alice...)
	if code != http.StatusOK || out["agent"].(map[string]any)["runtime"] != "agl" || out["agent"].(map[string]any)["owner"] != "end_user" {
		t.Fatalf("owner get: %d %v", code, out)
	}
	if code, out := h.do("GET", "/v1/agents/"+id, h.keyA, nil, "X-Fastclaw-End-User", "bob"); code != http.StatusNotFound {
		t.Fatalf("bob gets alice's agl agent: %d %v", code, out)
	}
	// The list carries runtime too.
	_, out = h.do("GET", "/v1/agents", h.keyA, nil, alice...)
	if a := listIDs(out)[id]; a == nil || a["runtime"] != "agl" || a["runtimeConfig"] == nil {
		t.Fatalf("list entry: %v", a)
	}

	// PATCH merges and validates runtimeConfig.
	code, out = h.do("PATCH", "/v1/agents/"+id, h.keyA, map[string]any{"name": "Mac 2", "runtimeConfig": map[string]any{"access": "edit"}}, alice...)
	got, _ = out["agent"].(map[string]any)["runtimeConfig"].(map[string]any)
	if code != http.StatusOK || got["access"] != "edit" || got["machineId"] != "mch_alice" || out["agent"].(map[string]any)["display_name"] != "Mac 2" {
		t.Fatalf("patch: %d %v", code, out)
	}
	if code, out := h.do("PATCH", "/v1/agents/"+id, h.keyA, map[string]any{"runtimeConfig": map[string]any{"access": "sudo"}}, alice...); code != http.StatusBadRequest {
		t.Fatalf("bad patch: %d %v", code, out)
	}
	if code, _ := h.do("PATCH", "/v1/agents/"+id, h.keyA, map[string]any{"runtimeConfig": map[string]any{"access": "full"}}, "X-Fastclaw-End-User", "bob"); code != http.StatusNotFound {
		t.Fatalf("bob patches alice's agent: %d", code)
	}
	// runtimeConfig on an ordinary agent is refused.
	plain := h.createAgent(h.keyA, "plain", nil)
	if code, out := h.do("PATCH", "/v1/agents/"+plain, h.keyA, map[string]any{"runtimeConfig": rc}); code != http.StatusBadRequest || !strings.Contains(errMessage(out), "agl") {
		t.Fatalf("runtimeConfig on a platform agent: %d %v", code, out)
	}
	// Ordinary agents don't grow runtime fields.
	if _, out := h.do("GET", "/v1/agents/"+plain, h.keyA, nil); out["agent"].(map[string]any)["runtime"] != nil {
		t.Fatalf("plain agent view: %v", out)
	}
}

func errMessage(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	m, _ := e["message"].(string)
	return m
}
