package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// Machines: a user's own computers linked through agl ("connect my
// computer", docs/agl-machines.md). Two callers:
//   - an integrating product, for one of its end users: api key +
//     X-Fastclaw-End-User; only that end user's machines are visible.
//   - the dashboard (console → Devices): the logged-in user's session
//     cookie; only their own machines are visible.
// Errors use the flat {"error": "<message>"} body of the WeClaw contract.

// SetAglHub enables /v1/machines and mounts the agl hub under /agl.
func (s *Server) SetAglHub(h *agl.Hub) { s.aglHub = h }

func writeFlatError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// machineStore is the store's machine half (nil when unsupported).
func (s *Server) machineStore() store.MachineStore {
	ms, _ := s.store.(store.MachineStore)
	return ms
}

// requireMachineAPI resolves the user a /v1/machines call acts for
// (ident.UserID): the named end user for an api key, the logged-in user
// for a dashboard session.
func (s *Server) requireMachineAPI(w http.ResponseWriter, r *http.Request) (auth.Identity, store.MachineStore, bool) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.AccountID() == "" || ident.UserID == "" {
		writeFlatError(w, http.StatusUnauthorized, "authentication required")
		return auth.Identity{}, nil, false
	}
	switch ident.AuthMethod {
	case "apikey":
		if ident.EndUser == "" || ident.AppUserID == "" {
			writeFlatError(w, http.StatusBadRequest, "X-Fastclaw-End-User is required")
			return auth.Identity{}, nil, false
		}
		if ident.APIKeyType == users.APIKeyTypeAgent {
			writeFlatError(w, http.StatusForbidden, "this api key cannot manage machines")
			return auth.Identity{}, nil, false
		}
	default:
		// A super_admin browsing as someone else must not see or touch
		// that person's computers: they are private to their owner.
		if ident.IsActingAs() {
			writeFlatError(w, http.StatusForbidden, "computers are private to their owner")
			return auth.Identity{}, nil, false
		}
	}
	ms := s.machineStore()
	if s.aglHub == nil || ms == nil {
		writeFlatError(w, http.StatusServiceUnavailable, "machines are not enabled on this server")
		return auth.Identity{}, nil, false
	}
	return ident, ms, true
}

func rfc3339OrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) machineView(m *store.MachineRecord) map[string]any {
	online := s.aglHub != nil && s.aglHub.Online(m.ID)
	agents := make([]map[string]any, 0, len(m.Agents))
	for _, a := range m.Agents {
		if agl.HiddenAgent(a.ID) { // stored before it was hidden
			continue
		}
		set := m.AgentSetting(a.ID)
		feats := a.Features
		if feats == nil {
			feats = []string{}
		}
		agents = append(agents, map[string]any{
			"id": a.ID, "name": a.Name, "available": a.Available,
			"status": a.Status, "path": a.Path, "version": a.Version, "features": feats,
			"installCommand": a.InstallCommand, "guide": a.Guide,
			// The owner's choices (PATCH agents).
			"enabled": !set.Disabled, "model": set.Model, "effort": set.Effort,
		})
	}
	lastSeen := rfc3339OrNil(m.LastSeenAt)
	if online {
		lastSeen = time.Now().UTC().Format(time.RFC3339)
	}
	name := m.Name
	if name == "" {
		name = m.Hostname
	}
	latest := ""
	if s.aglHub != nil {
		latest = s.aglHub.LatestVersion()
	}
	return map[string]any{
		"id":         m.ID,
		"name":       name,
		"hostname":   m.Hostname,
		"os":         m.OS,
		"arch":       m.Arch,
		"version":    m.Version,
		"online":     online,
		"lastSeenAt": lastSeen,
		"createdAt":  rfc3339OrNil(m.CreatedAt),
		"agents":     agents,
		// Tool mode settings (LLM agents dispatching to this machine).
		// agl updates: the newest release, and whether this machine's
		// agl takes a one-click update (daemon.update, agl ≥ 0.3.5).
		"latestVersion":   latest,
		"updateAvailable": agl.UpdateAvailable(m.Version, latest),
		"remoteUpdate":    agl.RemoteUpdateSupported(m.Version),
		"allowAgents":     m.AllowAgents,
		"defaultAccess":   firstNonEmptyStr(m.DefaultAccess, agl.AccessAsk),
		"defaultCwd":      m.DefaultCwd,
	}
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

const maxMachineNameLen = 64

// HandleUpdateMachine handles PATCH /v1/machines/{id}: any of name,
// allowAgents, defaultAccess, defaultCwd on the end user's own active
// machine (404 otherwise). Returns the updated Machine.
func (s *Server) HandleUpdateMachine(w http.ResponseWriter, r *http.Request) {
	ident, ms, ok := s.requireMachineAPI(w, r)
	if !ok {
		return
	}
	m, err := ms.GetMachine(r.Context(), r.PathValue("id"))
	if err != nil || m.UserID != ident.UserID || m.Revoked() {
		writeFlatError(w, http.StatusNotFound, "machine not found")
		return
	}
	var body struct {
		Name          *string `json:"name"`
		AllowAgents   *bool   `json:"allowAgents"`
		DefaultAccess *string `json:"defaultAccess"`
		DefaultCwd    *string `json:"defaultCwd"`
		// Per local agent, merged into the stored settings.
		Agents map[string]struct {
			Enabled *bool   `json:"enabled"`
			Model   *string `json:"model"`
			Effort  *string `json:"effort"`
		} `json:"agents"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&body); err != nil {
		writeFlatError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	set := store.MachineSettings{AllowAgents: body.AllowAgents}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			writeFlatError(w, http.StatusBadRequest, "name must not be empty")
			return
		}
		if utf8.RuneCountInString(name) > maxMachineNameLen || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\n\r") {
			writeFlatError(w, http.StatusBadRequest, "name is invalid (at most 64 characters, one line)")
			return
		}
		set.Name = &name
	}
	if body.DefaultAccess != nil {
		access := strings.TrimSpace(*body.DefaultAccess)
		switch access {
		case agl.AccessReadOnly, agl.AccessAsk, agl.AccessEdit, agl.AccessFull:
		default:
			writeFlatError(w, http.StatusBadRequest, `defaultAccess must be "read_only", "ask", "edit" or "full"`)
			return
		}
		set.DefaultAccess = &access
	}
	if body.DefaultCwd != nil {
		cwd := strings.TrimSpace(*body.DefaultCwd)
		if !agl.ValidCwd(cwd) {
			writeFlatError(w, http.StatusBadRequest, "defaultCwd is invalid")
			return
		}
		set.DefaultCwd = &cwd
	}
	if len(body.Agents) > 0 {
		merged := make(map[string]store.MachineAgentSetting, len(m.AgentSettings)+len(body.Agents))
		for id, v := range m.AgentSettings {
			merged[id] = v
		}
		for id, p := range body.Agents {
			id = strings.TrimSpace(id)
			if id == "" || len(id) > 64 || !knownAgent(m, id) {
				writeFlatError(w, http.StatusBadRequest, "unknown agent "+strconvQuote(id))
				return
			}
			cur := merged[id]
			if p.Enabled != nil {
				cur.Disabled = !*p.Enabled
			}
			if p.Model != nil {
				if !validAgentOption(*p.Model) {
					writeFlatError(w, http.StatusBadRequest, "model is invalid")
					return
				}
				cur.Model = strings.TrimSpace(*p.Model)
			}
			if p.Effort != nil {
				if !validAgentOption(*p.Effort) {
					writeFlatError(w, http.StatusBadRequest, "effort is invalid")
					return
				}
				cur.Effort = strings.TrimSpace(*p.Effort)
			}
			if cur == (store.MachineAgentSetting{}) {
				delete(merged, id)
			} else {
				merged[id] = cur
			}
		}
		set.AgentSettings = merged
	}
	if err := ms.UpdateMachineSettings(r.Context(), m.ID, set); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeFlatError(w, http.StatusNotFound, "machine not found")
			return
		}
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m, err = ms.GetMachine(r.Context(), m.ID)
	if err != nil {
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.machineView(m))
}

// HandleCreateMachinePairing handles POST /v1/machines/pairing.
func (s *Server) HandleCreateMachinePairing(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := s.requireMachineAPI(w, r)
	if !ok {
		return
	}
	// Each integrating product (api key account) gets its own hub
	// address (agl.ProductScope), so its link on a computer doesn't
	// replace another product's; Fastclaw's own users keep /agl.
	scope := ""
	if ident.AuthMethod == "apikey" && ident.EndUser != "" {
		scope = agl.ProductScope(ident.AccountID())
	}
	code, exp, err := s.aglHub.CreateScopedPairing(r.Context(), ident.UserID, scope)
	if err != nil {
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":           code,
		"installCommand": s.aglHub.ScopedInstallCommand(r, code, scope),
		"expiresAt":      exp.UTC().Format(time.RFC3339),
	})
}

// HandleMachinePairingStatus handles POST /v1/machines/pairing/status.
func (s *Server) HandleMachinePairingStatus(w http.ResponseWriter, r *http.Request) {
	ident, _, ok := s.requireMachineAPI(w, r)
	if !ok {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || strings.TrimSpace(body.Code) == "" {
		writeFlatError(w, http.StatusBadRequest, "code is required")
		return
	}
	status, machineID, err := s.aglHub.PairingStatus(r.Context(), ident.UserID, body.Code)
	if err != nil {
		if errors.Is(err, agl.ErrPairingNotFound) {
			writeFlatError(w, http.StatusNotFound, "pairing code not found")
			return
		}
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := map[string]any{"status": status}
	if machineID != "" {
		out["machineId"] = machineID
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleListMachines handles GET /v1/machines.
func (s *Server) HandleListMachines(w http.ResponseWriter, r *http.Request) {
	ident, ms, ok := s.requireMachineAPI(w, r)
	if !ok {
		return
	}
	list, err := ms.ListMachines(r.Context(), ident.UserID)
	if err != nil {
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, s.machineView(&list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out})
}

// HandleDeleteMachine handles DELETE /v1/machines/{id}: the token hash is
// deleted, the link closed, and agents bound to it stop running (every
// turn re-checks the machine).
func (s *Server) HandleDeleteMachine(w http.ResponseWriter, r *http.Request) {
	ident, ms, ok := s.requireMachineAPI(w, r)
	if !ok {
		return
	}
	m, err := ms.GetMachine(r.Context(), r.PathValue("id"))
	if err != nil || m.UserID != ident.UserID || m.Revoked() {
		writeFlatError(w, http.StatusNotFound, "machine not found")
		return
	}
	if err := s.aglHub.Revoke(r.Context(), m.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeFlatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func knownAgent(m *store.MachineRecord, id string) bool {
	if agl.HiddenAgent(id) {
		return false
	}
	for _, a := range m.Agents {
		if a.ID == id {
			return true
		}
	}
	return false
}

// validAgentOption: a model or effort id agl passes to the agent CLI.
func validAgentOption(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) <= 128 && utf8.ValidString(v) && !strings.ContainsAny(v, "\x00\n\r")
}

func strconvQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// ownMachine resolves {id} to the caller's active machine (404 otherwise).
func (s *Server) ownMachine(w http.ResponseWriter, r *http.Request) (*store.MachineRecord, bool) {
	ident, ms, ok := s.requireMachineAPI(w, r)
	if !ok {
		return nil, false
	}
	m, err := ms.GetMachine(r.Context(), r.PathValue("id"))
	if err != nil || m.UserID != ident.UserID || m.Revoked() {
		writeFlatError(w, http.StatusNotFound, "machine not found")
		return nil, false
	}
	return m, true
}

func (s *Server) writeMachineLinkError(w http.ResponseWriter, err error) {
	if errors.Is(err, agl.ErrOffline) {
		writeFlatError(w, http.StatusConflict, "the computer is offline")
		return
	}
	writeFlatError(w, http.StatusBadGateway, err.Error())
}

// HandleRefreshMachineAgents handles POST /v1/machines/{id}/agents/refresh:
// re-detect the computer's local agents now. Returns the updated Machine.
func (s *Server) HandleRefreshMachineAgents(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownMachine(w, r)
	if !ok {
		return
	}
	updated, err := s.aglHub.RefreshAgents(r.Context(), m.ID)
	if err != nil {
		s.writeMachineLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.machineView(updated))
}

// HandleMachineAgentModels handles GET /v1/machines/{id}/agents/{agent}/models.
func (s *Server) HandleMachineAgentModels(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownMachine(w, r)
	if !ok {
		return
	}
	agent := r.PathValue("agent")
	if !knownAgent(m, agent) {
		writeFlatError(w, http.StatusNotFound, "agent not found")
		return
	}
	models, err := s.aglHub.AgentModels(r.Context(), m.ID, agent)
	if err != nil {
		s.writeMachineLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// HandleTestMachineAgent handles POST /v1/machines/{id}/agents/{agent}/test
// {model?, effort?}: runs the agent read-only with a one-line prompt.
func (s *Server) HandleTestMachineAgent(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownMachine(w, r)
	if !ok {
		return
	}
	agent := r.PathValue("agent")
	if !knownAgent(m, agent) {
		writeFlatError(w, http.StatusNotFound, "agent not found")
		return
	}
	var body struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeFlatError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if !validAgentOption(body.Model) || !validAgentOption(body.Effort) {
		writeFlatError(w, http.StatusBadRequest, "model or effort is invalid")
		return
	}
	res, err := s.aglHub.TestAgent(r.Context(), m.ID, agent, strings.TrimSpace(body.Model), strings.TrimSpace(body.Effort))
	if err != nil {
		s.writeMachineLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// HandleUpdateMachineAgl handles POST /v1/machines/{id}/update: the
// computer's agl updates itself to the newest release. It never ends a
// running task: {updated:false, busy:N} while one runs. agl < 0.3.5 can't
// be updated this way (409 code "manual_update": run `agl update` there).
func (s *Server) HandleUpdateMachineAgl(w http.ResponseWriter, r *http.Request) {
	m, ok := s.ownMachine(w, r)
	if !ok {
		return
	}
	if !s.aglHub.Online(m.ID) {
		writeFlatError(w, http.StatusConflict, "the computer is offline")
		return
	}
	if !agl.RemoteUpdateSupported(m.Version) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this agl can't be updated remotely; run `agl update` on the computer", "code": "manual_update"})
		return
	}
	res, err := s.aglHub.UpdateAgl(r.Context(), m.ID)
	if err != nil {
		if strings.Contains(err.Error(), "needs a link with more access") {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "this agl can't be updated remotely; run `agl update` on the computer", "code": "manual_update"})
			return
		}
		s.writeMachineLinkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
