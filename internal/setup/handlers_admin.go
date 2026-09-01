package setup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/session"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// --- Login / logout / me ---

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil || s.authResolver == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "auth not configured"})
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	acct, err := s.accounts.Authenticate(r.Context(), req.Login, req.Password)
	if err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid credentials"})
		return
	}
	cookie, err := s.authResolver.IssueSession(r.Context(), acct.ID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	http.SetCookie(w, cookie)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.authResolver != nil {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			_ = s.authResolver.RevokeSession(r.Context(), c.Value)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:   auth.SessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	acct, err := s.accounts.Get(r.Context(), ident.UserID)
	if err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// deployMode lets the frontend show or hide local-only conveniences
	// (open-in-Finder for the workspace folder, future "edit SOUL.md in
	// $EDITOR" hooks, etc). One source of truth here so we don't read
	// the env var in 5 different handlers; the frontend can cache it
	// alongside the user profile since it doesn't change at runtime.
	deployMode := "self-hosted"
	if buildinfo.IsHostedDeploy() {
		deployMode = "hosted"
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok":          true,
		"user":        acct,
		"authMethod":  ident.AuthMethod,
		"actAsUserId": ident.ActAsUserID,
		"readOnly":    ident.ReadOnly(),
		"deployMode":  deployMode,
	})
}

// --- Self-service profile ---

// maxAvatarBytes caps the size of a base64-encoded avatar payload. ~256KB
// is enough for a reasonable square (e.g. 256×256 PNG); anything larger
// pushes the users row into TOAST territory on Postgres and slows /api/me.
// Frontend should resize/compress before upload — this is just the wall.
const maxAvatarBytes = 256 * 1024

type updateMeReq struct {
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

// handleUpdateMe lets the logged-in user edit their own display name and
// avatar. Avatar must be empty (clears) or a data: URL — full-blown HTTP
// URLs would let a malicious paste exfiltrate user data via referer when
// rendered, so we constrain to inline images only.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	var req updateMeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.AvatarURL != "" {
		if !strings.HasPrefix(req.AvatarURL, "data:image/") {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "avatar must be a data:image/* URL"})
			return
		}
		if len(req.AvatarURL) > maxAvatarBytes {
			jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "avatar too large (max 256KB)"})
			return
		}
	}
	acct, err := s.accounts.UpdateProfile(r.Context(), ident.UserID, req.DisplayName, req.AvatarURL)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct})
}

func (s *Server) handleUploadMyAvatar(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no file"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if !strings.HasPrefix(contentType, "image/") {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "avatar must be an image"})
		return
	}
	avatarURL := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
	if len(avatarURL) > maxAvatarBytes {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "avatar too large (max 256KB)"})
		return
	}

	current, err := s.accounts.Get(r.Context(), ident.UserID)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	acct, err := s.accounts.UpdateProfile(r.Context(), ident.UserID, current.DisplayName, avatarURL)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct})
}

type changePasswordReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleChangeMyPassword is the self-service variant of admin's password
// reset — requires the current password before accepting a new one. Min
// length matches the implicit default elsewhere; we don't enforce strong
// rules because the install is single-tenant and we don't want to be
// the place that rejects "correcthorse" with a regex.
func (s *Server) handleChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	if ident.Role == users.RoleAppUser {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "app_user has no password"})
		return
	}
	var req changePasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.NewPassword == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "new password required"})
		return
	}
	if err := s.accounts.VerifyPassword(r.Context(), ident.UserID, req.OldPassword); err != nil {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "current password incorrect"})
		return
	}
	if err := s.accounts.SetPassword(r.Context(), ident.UserID, req.NewPassword); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// --- Onboard ---

type onboardRequest struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`

	Provider string `json:"provider"`
	APIBase  string `json:"apiBase"`
	APIKey   string `json:"apiKey"`
	APIType  string `json:"apiType,omitempty"`
	AuthType string `json:"authType,omitempty"`
	Model    string `json:"model"`

	AgentName string `json:"agentName,omitempty"`

	SandboxEnabled         bool   `json:"sandboxEnabled,omitempty"`
	SandboxBackend         string `json:"sandboxBackend,omitempty"`
	SandboxImage           string `json:"sandboxImage,omitempty"`
	SandboxE2BKey          string `json:"sandboxE2BKey,omitempty"`
	SandboxE2BApiUrl       string `json:"sandboxE2BApiUrl,omitempty"`
	SandboxE2BDomain       string `json:"sandboxE2BDomain,omitempty"`
	SandboxBoxliteURL      string `json:"sandboxBoxliteUrl,omitempty"`
	SandboxBoxliteClientID string `json:"sandboxBoxliteClientId,omitempty"`
	SandboxBoxliteKey      string `json:"sandboxBoxliteKey,omitempty"`
	SandboxBoxlitePrefix   string `json:"sandboxBoxlitePrefix,omitempty"`
}

// handleOnboard creates the first super_admin + first system provider +
// first agent, all in a single logical operation. Only callable when the
// users table is empty; subsequent calls 409.
func (s *Server) handleOnboard(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil || s.accounts == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store not ready"})
		return
	}
	count, err := s.accounts.Count(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if count > 0 {
		jsonResponse(w, http.StatusConflict, map[string]any{"ok": false, "error": "already onboarded"})
		return
	}
	var req onboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "username, email, password required"})
		return
	}
	acct, err := s.accounts.Create(r.Context(), users.CreateInput{
		Username:    req.Username,
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        users.RoleSuperAdmin,
	})
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.Provider != "" && req.APIKey != "" {
		pcfg := config.ProviderConfig{
			APIBase:  req.APIBase,
			APIKey:   req.APIKey,
			APIType:  req.APIType,
			AuthType: req.AuthType,
		}
		// Seed the chosen model into Provider.Models so the Models /
		// Providers admin pages show it right away — without this, users
		// land on an "Edit Provider" dialog with an empty Models list
		// and an inactive Test connection button, even though
		// agents.defaults already names this model.
		if req.Model != "" {
			pcfg.Models = []config.ModelEntry{{ID: req.Model, Name: req.Model}}
		}
		if err := scope.SaveProviderByScope(r.Context(), s.dataStore, scope.System, "", req.Provider, pcfg); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if req.Model != "" {
			defaults := map[string]interface{}{
				"model": req.Provider + "/" + req.Model,
			}
			if err := scope.SaveSettingByScope(r.Context(), s.dataStore, scope.System, "", "agents.defaults", defaults); err != nil {
				jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
				return
			}
		}
	}
	agentID, _ := generateID("agt_")
	agentName := req.AgentName
	if agentName == "" {
		agentName = "default"
	}
	agentRec := &store.AgentRecord{
		ID:     agentID,
		UserID: acct.ID,
		Name:   agentName,
	}
	if err := s.dataStore.SaveAgent(r.Context(), agentRec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if req.SandboxEnabled {
		backend := req.SandboxBackend
		if backend == "" {
			backend = "docker"
		}
		sandbox := map[string]interface{}{
			"enabled": true,
			"backend": backend,
		}
		if req.SandboxImage != "" {
			sandbox["image"] = req.SandboxImage
		}
		if req.SandboxE2BKey != "" {
			sandbox["e2bKey"] = req.SandboxE2BKey
		}
		if req.SandboxE2BApiUrl != "" {
			sandbox["e2bApiUrl"] = req.SandboxE2BApiUrl
		}
		if req.SandboxE2BDomain != "" {
			sandbox["e2bDomain"] = req.SandboxE2BDomain
		}
		if req.SandboxBoxliteURL != "" {
			sandbox["boxliteUrl"] = req.SandboxBoxliteURL
		}
		if req.SandboxBoxliteClientID != "" {
			sandbox["boxliteClientId"] = req.SandboxBoxliteClientID
		}
		if req.SandboxBoxliteKey != "" {
			sandbox["boxliteKey"] = req.SandboxBoxliteKey
		}
		if req.SandboxBoxlitePrefix != "" {
			sandbox["boxlitePrefix"] = req.SandboxBoxlitePrefix
		}
		if err := scope.SaveSettingByScope(r.Context(), s.dataStore, scope.System, "", "sandbox", sandbox); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := s.reloadSystemSandbox(); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	cookie, err := s.authResolver.IssueSession(r.Context(), acct.ID)
	if err == nil {
		http.SetCookie(w, cookie)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "user": acct, "agentId": agentID})
}

// --- Admin: user management ---

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.accounts.List(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// app_user accounts are programmatically provisioned by api_keys on
	// behalf of downstream end-users — they're not humans the admin
	// manages, and their volume can be very large. Hide them by default;
	// admins that need to audit them can pass ?includeAppUsers=1.
	if r.URL.Query().Get("includeAppUsers") != "1" {
		filtered := make([]*users.Account, 0, len(list))
		for _, u := range list {
			if u.Role == users.RoleAppUser {
				continue
			}
			filtered = append(filtered, u)
		}
		list = filtered
	}
	jsonResponse(w, http.StatusOK, map[string]any{"users": list})
}

type createUserReq struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
	// AgentQuota is a pointer so the admin can distinguish "unset →
	// use the default unlimited" from "explicitly 0 → no self-creation".
	AgentQuota *int64 `json:"agentQuota,omitempty"`
	// AvatarURL is an optional inline data:image/* URL (≤256KB). Same
	// shape and cap as the self-service /api/me endpoint.
	AvatarURL string `json:"avatarUrl,omitempty"`
	// ExternalID is the calling app's own user identifier. Combined
	// with the auth-derived apikey_id (NOT taken from the body) it
	// makes provisioning idempotent: the same upstream user always
	// resolves to the same fastclaw user_id. Optional for session
	// callers (web admin clicks); typical for upstream apikey
	// provisioning where the caller wants a stable mapping back to
	// their own user table.
	ExternalID string `json:"externalId,omitempty"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.AvatarURL != "" {
		if !strings.HasPrefix(req.AvatarURL, "data:image/") {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "avatar must be a data:image/* URL"})
			return
		}
		if len(req.AvatarURL) > maxAvatarBytes {
			jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "error": "avatar too large (max 256KB)"})
			return
		}
	}
	// apikey_id is auth-derived, never trusted from the body — that
	// row is what audits a provisioned user back to the key that
	// minted them. Empty for session callers (web admin), populated
	// when an admin apikey hits this endpoint.
	apikeyID := ""
	if ident, ok := auth.FromContext(r.Context()); ok {
		apikeyID = ident.APIKeyID
	}
	role := req.Role
	if role == "" {
		role = users.RoleUser
	}
	acct, err := s.accounts.Create(r.Context(), users.CreateInput{
		Username:    req.Username,
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        role,
		AgentQuota:  req.AgentQuota,
		AvatarURL:   req.AvatarURL,
		APIKeyID:    apikeyID,
		ExternalID:  req.ExternalID,
	})
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"user": acct})
}

type updateUserReq struct {
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
	Status      string `json:"status,omitempty"`
	AgentQuota  *int64 `json:"agentQuota,omitempty"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateUserReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	acct, err := s.accounts.Update(r.Context(), id, req.DisplayName, req.Role, req.Status, req.AgentQuota)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"user": acct})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.accounts.Delete(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

type resetPasswordReq struct {
	Password string `json:"password"`
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req resetPasswordReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if err := s.accounts.SetPassword(r.Context(), id, req.Password); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// respondAllAgents returns every agent across every user, with the
// owner's username/email joined in. Backs GET /api/agents?all=true for
// the platform-wide admin view; the auth gate lives in handleListAgents
// (which calls this only after CanAdminPlatform passes).
func (s *Server) respondAllAgents(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store"})
		return
	}
	records, err := s.dataStore.ListAllAgents(r.Context())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// Resolve owner usernames once per unique userID — N agents could
	// belong to a handful of users, so a per-row lookup would re-hit the
	// store for the same id repeatedly.
	ownerCache := map[string]*users.Account{}
	resolveOwner := func(uid string) *users.Account {
		if uid == "" {
			return nil
		}
		if a, ok := ownerCache[uid]; ok {
			return a
		}
		a, _ := s.accounts.Get(r.Context(), uid)
		ownerCache[uid] = a
		return a
	}
	out := make([]map[string]any, 0, len(records))
	for _, ar := range records {
		desc, _ := ar.Config["description"].(string)
		entry := map[string]any{
			"id":          ar.ID,
			"name":        ar.Name,
			"description": desc,
			"userId":      ar.UserID,
			"createdAt":   ar.CreatedAt,
		}
		if owner := resolveOwner(ar.UserID); owner != nil {
			entry["ownerUsername"] = owner.Username
			entry["ownerEmail"] = owner.Email
			if owner.DisplayName != "" {
				entry["ownerDisplayName"] = owner.DisplayName
			}
		}
		out = append(out, entry)
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agents": out})
}

// handleAdminChats returns every chat session across every (user, agent)
// pair, enriched with the owning user's username and the agent's name so
// the platform-wide admin Chats page can render one flat table without
// fanning out per-agent on the client. Super_admin only — registered on
// /api/admin/chats and gated by the admin middleware.
//
// Implementation note: we fan out per (chatter user_id, agent_id) pair
// from the sessions table, NOT per agent. A non-owner who binds their
// own bot to a public agent (or chats with a public agent on the web)
// writes session rows under their own user_id — an iteration keyed by
// agent.owner would miss those sessions entirely. The pair fan-out
// captures every chatter regardless of whether they own the agent. The
// "Owner" column then reflects the chat's actual user, so the actAs
// link in the dashboard can impersonate the real session owner instead
// of the agent owner (who may have no read access to the session).
func (s *Server) handleAdminChats(w http.ResponseWriter, r *http.Request) {
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"error": "no data store"})
		return
	}
	// Pagination: ?page=1&pageSize=30 (1-based, defaults to page 1, 30 per page).
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	offset := (page - 1) * pageSize
	metas, total, err := s.dataStore.ListSessionsPaginated(r.Context(), nil, offset, pageSize)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	ownerCache := map[string]*users.Account{}
	resolveOwner := func(uid string) *users.Account {
		if uid == "" {
			return nil
		}
		if a, ok := ownerCache[uid]; ok {
			return a
		}
		a, _ := s.accounts.Get(r.Context(), uid)
		ownerCache[uid] = a
		return a
	}
	agentCache := map[string]*store.AgentRecord{}
	resolveAgent := func(agentID string) *store.AgentRecord {
		if agentID == "" {
			return nil
		}
		if a, ok := agentCache[agentID]; ok {
			return a
		}
		a, _ := s.dataStore.GetAgent(r.Context(), agentID)
		agentCache[agentID] = a
		return a
	}
	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		ag := resolveAgent(m.AgentID)
		if ag == nil {
			continue
		}
		adapter := session.NewStoreAdapter(s.dataStore, m.UserID)
		ws := adapter.BuildWebSession(r.Context(), m)
		if ws == nil {
			continue
		}
		owner := resolveOwner(m.UserID)
		entry := map[string]any{
			"id":           ws.ID,
			"agentId":      m.AgentID,
			"agentName":    ag.Name,
			"userId":       m.UserID,
			"channel":      ws.Channel,
			"accountId":    ws.AccountID,
			"chatId":       ws.ChatID,
			"projectId":    ws.ProjectID,
			"title":        ws.Title,
			"preview":      ws.Preview,
			"thumbnailUrl": ws.ThumbnailURL,
			"createdAt":    ws.CreatedAt,
			"updatedAt":    ws.UpdatedAt,
		}
		if ws.ChatterUserID != "" {
			entry["chatterUserId"] = ws.ChatterUserID
			if chatter := resolveOwner(ws.ChatterUserID); chatter != nil {
				if chatter.ExternalID != "" {
					entry["chatterExternalId"] = chatter.ExternalID
				}
				if chatter.DisplayName != "" {
					entry["chatterDisplayName"] = chatter.DisplayName
				}
			}
		}
		if owner != nil {
			entry["ownerUsername"] = owner.Username
			entry["ownerEmail"] = owner.Email
			if owner.ExternalID != "" {
				entry["ownerExternalId"] = owner.ExternalID
			}
			if owner.DisplayName != "" {
				entry["ownerDisplayName"] = owner.DisplayName
			}
		}
		out = append(out, entry)
	}
	totalPages := (total + pageSize - 1) / pageSize
	jsonResponse(w, http.StatusOK, map[string]any{
		"sessions":   out,
		"page":       page,
		"pageSize":   pageSize,
		"total":      total,
		"totalPages": totalPages,
	})
}

// --- Admin provisioning (per-user) ---
//
// The handlers below all live under /api/users/{id}/* — admin-or-self
// per requireUserOrAdmin. The admin path bypasses the target user's
// agent_quota (call initiated by the platform); the self path enforces
// it. Quota / fork semantics live inside the relevant handler.

// handleListUserAgents returns the agents owned by the path-resolved
// user. Admin-or-self via requireUserOrAdmin (admin can list any
// user's; non-admin can only list their own). Same response shape as
// the regular agents list so admin tools can reuse rendering.
func (s *Server) handleListUserAgents(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("id")
	if !s.requireUserOrAdmin(w, r, uid) {
		return
	}
	if _, err := s.dataStore.GetUser(r.Context(), uid); err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "user not found"})
		return
	}
	records, err := s.dataStore.ListAgents(r.Context(), uid)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(records))
	for _, ar := range records {
		desc, _ := ar.Config["description"].(string)
		out = append(out, map[string]any{
			"id":          ar.ID,
			"name":        ar.Name,
			"description": desc,
			"userId":      ar.UserID,
			"isPublic":    ar.IsPublic,
			"createdAt":   ar.CreatedAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"agents": out})
}

type adminCreateUserAgentReq struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	// ForkFrom is an optional source agent id. When set, the new agent
	// inherits SOUL.md / IDENTITY.md / AGENTS.md / BOOTSTRAP.md /
	// TOOLS.md / HEARTBEAT.md / agent.json from the source's owner-row,
	// plus the source's agent-scope `agents.defaults` and
	// `skills.entries` config rows. Per-user state (MEMORY.md, USER.md,
	// sessions, cron_jobs) and per-owner routing (channel bindings)
	// are deliberately NOT copied. Fork sources can be any agent the
	// caller (super_admin) can read.
	ForkFrom string `json:"forkFrom,omitempty"`
}

// handleCreateUserAgent creates an agent owned by the path-resolved
// user. Behavior depends on caller:
//   - admin (super_admin / type=admin apikey) → bypass the target's
//     agent_quota; forkFrom is honored (clones an existing agent's
//     identity into the new one).
//   - self (target user calling for themselves) → enforce their own
//     agent_quota; forkFrom is ignored to avoid letting users clone
//     other people's private agents into their namespace through this
//     path.
//
// The created agent is always private; flip via the regular
// PUT /api/agents/{id} flow.
func (s *Server) handleCreateUserAgent(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if !s.requireUserOrAdmin(w, r, targetUserID) {
		return
	}
	if !s.requireWritable(w, r) {
		return
	}
	target, err := s.dataStore.GetUser(r.Context(), targetUserID)
	if err != nil || target == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "user not found"})
		return
	}
	ident, _ := auth.FromContext(r.Context())
	isAdmin := ident.CanAdminPlatform()

	var req adminCreateUserAgentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Quota only applies on the self path. Admin provisioning is
	// initiated by the platform and intentionally bypasses it.
	if !isAdmin && target.AgentQuota >= 0 {
		owned, err := s.dataStore.ListAgents(r.Context(), targetUserID)
		if err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		if int64(len(owned)) >= target.AgentQuota {
			jsonResponse(w, http.StatusForbidden, map[string]any{
				"error": fmt.Sprintf("agent quota reached (%d) — contact your admin to provision more", target.AgentQuota),
			})
			return
		}
	}

	var source *store.AgentRecord
	if isAdmin && strings.TrimSpace(req.ForkFrom) != "" {
		source, err = s.dataStore.GetAgent(r.Context(), req.ForkFrom)
		if err != nil || source == nil {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "forkFrom: source agent not found"})
			return
		}
	}

	name := strings.TrimSpace(req.Name)
	if name == "" && source != nil {
		name = source.Name
	}
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "name required"})
		return
	}
	description := req.Description
	if description == "" && source != nil {
		if d, ok := source.Config["description"].(string); ok {
			description = d
		}
	}

	id, err := generateID("agt_")
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	rec := &store.AgentRecord{
		ID:     id,
		UserID: targetUserID,
		Name:   name,
	}
	if description != "" {
		rec.Config = map[string]interface{}{"description": description}
	}
	if err := s.dataStore.SaveAgent(r.Context(), rec); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	model := req.Model
	if model == "" && source != nil {
		model = s.agentScopeModel(r, source.ID)
	}
	if model != "" {
		if err := s.saveAgentScopeModel(r, id, model); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": "save model: " + err.Error()})
			return
		}
	}

	// Fork content: identity files + agent-scope configs.
	if source != nil {
		if err := s.forkAgentContent(r, source, rec); err != nil {
			jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": "fork content: " + err.Error()})
			return
		}
	}

	s.invalidateUser(targetUserID)
	jsonResponse(w, http.StatusCreated, map[string]any{
		"agent": map[string]any{
			"id":          rec.ID,
			"userId":      rec.UserID,
			"name":        rec.Name,
			"description": description,
			"model":       model,
			"isPublic":    rec.IsPublic,
		},
	})
}

// forkAgentFiles is the allowlist of files copied during fork. These
// are the agent's identity (what it IS / does); per-user state
// (MEMORY.md, USER.md) is intentionally omitted so each chatter starts
// fresh on the new agent.
var forkAgentFiles = []string{
	"SOUL.md", "IDENTITY.md", "AGENTS.md",
	"BOOTSTRAP.md", "TOOLS.md", "HEARTBEAT.md", "KNOWLEDGE.md", "agent.json",
}

// forkAgentScopeConfigs is the allowlist of agent-scope config rows
// copied during fork. Bindings are deliberately excluded — they encode
// the source owner's IM routing (bot tokens, chat ids) and would be
// nonsensical on the new agent under a different owner.
var forkAgentScopeConfigs = map[string]bool{
	"agents.defaults": true,
	"skills.entries":  true,
}

// forkAgentContent copies the source agent's owner-row identity files
// and agent-scope configs into the destination agent. Best-effort per
// file: a missing source file is skipped silently (the destination
// just has no override for it, which the runtime handles via the
// usual fallback paths).
func (s *Server) forkAgentContent(r *http.Request, src, dst *store.AgentRecord) error {
	for _, name := range forkAgentFiles {
		data, err := s.dataStore.GetAgentFileExact(r.Context(), src.ID, src.UserID, name)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		if len(data) == 0 {
			continue
		}
		if err := s.dataStore.SaveAgentFile(r.Context(), dst.ID, dst.UserID, name, data); err != nil {
			return err
		}
	}
	// Knowledge base files ride along with the identity — the fork should
	// answer from the same corpus. Reindex each copy so the destination
	// agent's knowledge_search chunks exist without a re-upload.
	docs, err := s.dataStore.ListAgentKnowledgeDocs(r.Context(), src.ID, src.UserID)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.Content == "" {
			continue
		}
		if err := s.dataStore.SaveAgentFile(r.Context(), dst.ID, dst.UserID, doc.Path, []byte(doc.Content)); err != nil {
			return err
		}
		if err := s.indexKnowledgeFile(r.Context(), dst.ID, dst.UserID, doc.Path, []byte(doc.Content)); err != nil {
			return err
		}
	}
	rows, err := s.dataStore.ListConfigs(r.Context(), store.KindSetting, "", src.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if !forkAgentScopeConfigs[row.Name] {
			continue
		}
		if err := scope.SaveSettingByScope(r.Context(), s.dataStore, scope.Agent, dst.ID, row.Name, row.Data); err != nil {
			return err
		}
	}
	return nil
}

// handleCreateUserAPIKey issues an apikey owned by the path-resolved
// user. Admin-or-self via requireUserOrAdmin:
//   - admin caller may issue user/agent keys for any user
//   - non-admin caller may issue keys only for themselves (id == self)
//
// type=admin is always rejected through this path — admin keys grant
// platform-wide rights and shouldn't be auto-provisioned for a target
// user; admin who needs an admin key issues one for themselves via
// POST /api/users/{self}/apikeys (which becomes self-create and the
// route still requires admin caller anyway).
func (s *Server) handleCreateUserAPIKey(w http.ResponseWriter, r *http.Request) {
	targetUserID := r.PathValue("id")
	if !s.requireUserOrAdmin(w, r, targetUserID) {
		return
	}
	if !s.requireWritable(w, r) {
		return
	}
	target, err := s.dataStore.GetUser(r.Context(), targetUserID)
	if err != nil || target == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "user not found"})
		return
	}
	if target.Role == users.RoleAppUser {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "app_user cannot hold api keys"})
		return
	}
	ident, _ := auth.FromContext(r.Context())
	isAdmin := ident.CanAdminPlatform()

	var req createAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	if req.Type == "" {
		req.Type = users.APIKeyTypeUser
	}
	if !users.IsAPIKeyType(req.Type) {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid type"})
		return
	}
	if req.Type == users.APIKeyTypeAdmin {
		// Admin keys are never minted via this path — they could only
		// originate via super_admin doing POST /api/users/{self}/apikeys
		// for themselves, which would still bypass intent ("here's a
		// platform key for that other user"). If a super_admin needs a
		// fresh admin key for themselves, they self-issue from the
		// settings UI; we don't expose a programmatic admin-key mint.
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "admin keys cannot be issued through this path"})
		return
	}
	if req.Type == users.APIKeyTypeAgent {
		if len(req.AgentIDs) == 0 {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "type=agent requires at least one agentId"})
			return
		}
		for _, aid := range req.AgentIDs {
			rec, err := s.dataStore.GetAgent(r.Context(), aid)
			if err != nil || rec == nil {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "agent not found: " + aid})
				return
			}
			// Self caller: must own each agent.
			// Admin caller: target must own each agent (admin can't
			// bind random user A's agent into user B's apikey).
			if rec.UserID != targetUserID {
				jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "cannot bind agent " + aid + " — not owned by target user"})
				return
			}
		}
	}
	_ = isAdmin // currently no admin-only branches inside; kept for future toggles
	ak, token, err := s.apikeys.Create(r.Context(), targetUserID, req.Name, req.Type, req.AgentIDs)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ak.Key = token
	jsonResponse(w, http.StatusCreated, map[string]any{"apikey": ak, "token": token})
}

// --- Apikey CRUD (per-user) ---

type createAPIKeyReq struct {
	Name     string   `json:"name"`
	Type     string   `json:"type,omitempty"` // "admin" | "user" | "agent"; default "agent"
	AgentIDs []string `json:"agentIds,omitempty"`
}

func (s *Server) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		jsonResponse(w, http.StatusUnauthorized, map[string]any{"ok": false})
		return
	}
	list, err := s.apikeys.List(r.Context(), ident.EffectiveUserID())
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	enriched := make([]map[string]any, 0, len(list))
	for _, ak := range list {
		// Only type=agent keys carry an explicit agent list; user/admin
		// derive scope from ownership at auth time, so an empty array
		// here means "the tier defines the scope, not the row."
		var agents []string
		if ak.Type == users.APIKeyTypeAgent {
			agents, _ = s.apikeys.Agents(r.Context(), ak.ID)
		}
		enriched = append(enriched, map[string]any{
			"id":        ak.ID,
			"userId":    ak.UserID,
			"name":      ak.Name,
			"key":       ak.Key,
			"type":      ak.Type,
			"agents":    agents,
			"createdAt": ak.CreatedAt,
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"apikeys": enriched})
}

// handleCreateAPIKey enforces the role × type policy:
//   - super_admin may issue admin / user / agent keys
//   - regular user may issue user / agent keys (only for their own agents)
//   - app_user (provisioned via apikey) may not issue keys at all
//
// type=agent additionally requires that every agentId resolves to an
// agent the caller is allowed to bind — owners can bind their own,
// super_admins can bind anyone's. This is the authoritative gate; the
// users package only validates shape, not policy.
func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	if ident.Role == users.RoleAppUser {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "app_user cannot issue api keys"})
		return
	}
	var req createAPIKeyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if req.Type == "" {
		req.Type = users.APIKeyTypeAgent
	}
	if !users.IsAPIKeyType(req.Type) {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid type"})
		return
	}
	if req.Type == users.APIKeyTypeAdmin && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "only super_admin may issue admin keys"})
		return
	}
	if req.Type == users.APIKeyTypeAgent {
		if len(req.AgentIDs) == 0 {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "type=agent requires at least one agentId"})
			return
		}
		// Bind only agents the caller controls. Super_admin can bind
		// anyone's; everyone else must own each one.
		if ident.Role != users.RoleSuperAdmin {
			for _, aid := range req.AgentIDs {
				rec, err := s.dataStore.GetAgent(r.Context(), aid)
				if err != nil || rec == nil || rec.UserID != ident.UserID {
					jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cannot bind agent " + aid})
					return
				}
			}
		}
	}
	ak, token, err := s.apikeys.Create(r.Context(), ident.UserID, req.Name, req.Type, req.AgentIDs)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ak.Key = token
	jsonResponse(w, http.StatusCreated, map[string]any{"apikey": ak, "token": token})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	if err := s.apikeys.Delete(r.Context(), id); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	token, err := s.apikeys.Rotate(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"token": token})
}

type setAPIKeyAgentsReq struct {
	AgentIDs []string `json:"agentIds"`
}

func (s *Server) handleSetAPIKeyAgents(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "read-only"})
		return
	}
	id := r.PathValue("id")
	rec, err := s.apikeys.Get(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if rec.UserID != ident.UserID && ident.Role != users.RoleSuperAdmin {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden"})
		return
	}
	var req setAPIKeyAgentsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	if ident.Role != users.RoleSuperAdmin {
		for _, aid := range req.AgentIDs {
			ar, err := s.dataStore.GetAgent(r.Context(), aid)
			if err != nil || ar == nil || ar.UserID != ident.UserID {
				jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "cannot bind agent " + aid})
				return
			}
		}
	}
	if err := s.apikeys.SetAgents(r.Context(), id, req.AgentIDs); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

// generateID returns a random hex id with the given prefix.
func generateID(prefix string) (string, error) {
	id, err := newRandID()
	if err != nil {
		return "", err
	}
	return prefix + id, nil
}

// newRandID is implemented in handlers.go to share with other generators.
func init() {
	// Force a compile reference so unused import warnings stay loud
	// when refactoring; otherwise no-op.
	_ = errors.New
}
