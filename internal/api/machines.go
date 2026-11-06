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

// Machines: an end user's own computers linked through agl ("connect my
// computer", docs/agl-machines.md). Every call names the end user
// (X-Fastclaw-End-User) and only ever sees that end user's machines.
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

// requireMachineAPI resolves the end user a /v1/machines call acts for.
func (s *Server) requireMachineAPI(w http.ResponseWriter, r *http.Request) (auth.Identity, store.MachineStore, bool) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.AccountID() == "" {
		writeFlatError(w, http.StatusUnauthorized, "authentication required")
		return auth.Identity{}, nil, false
	}
	if ident.AuthMethod != "apikey" || ident.EndUser == "" || ident.AppUserID == "" {
		writeFlatError(w, http.StatusBadRequest, "X-Fastclaw-End-User is required")
		return auth.Identity{}, nil, false
	}
	if ident.APIKeyType == users.APIKeyTypeAgent {
		writeFlatError(w, http.StatusForbidden, "this api key cannot manage machines")
		return auth.Identity{}, nil, false
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
		agents = append(agents, map[string]any{"id": a.ID, "name": a.Name, "available": a.Available})
	}
	lastSeen := rfc3339OrNil(m.LastSeenAt)
	if online {
		lastSeen = time.Now().UTC().Format(time.RFC3339)
	}
	name := m.Name
	if name == "" {
		name = m.Hostname
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
		"allowAgents":   m.AllowAgents,
		"defaultAccess": firstNonEmptyStr(m.DefaultAccess, agl.AccessAsk),
		"defaultCwd":    m.DefaultCwd,
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
	// Each product gets its own hub address (see agl.ProductScope), so
	// its link on a computer doesn't replace another product's.
	scope := agl.ProductScope(ident.AccountID())
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
