// Package agentcli provides the data-layer operations that fastclaw's
// `agents …` CLI subcommands run against the operator's own FastClaw
// store. The CLI is a thin convenience wrapper over the same store the
// gateway and dashboard use — agents created here are indistinguishable
// from agents created via the web UI.
package agentcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// validateName mirrors the dashboard's only check: non-empty after trim.
// Anything stricter would reject agent names the web UI accepts and break
// the "CLI ≡ dashboard" contract.
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("agent name is required")
	}
	return nil
}

// InitOptions controls Init behavior.
type InitOptions struct {
	Description string
	// AgentID overrides the auto-derived id. Set this to update an agent
	// originally created via the dashboard.
	AgentID string

	Provider  string
	Model     string
	APIKeyEnv string
	APIBase   string
	APIType   string
	AuthType  string

	// Username pins the agent to a specific account. When omitted, Init
	// uses the existing agent's owner, or the first super_admin, or
	// creates an admin if the DB is empty.
	Username    string
	Email       string
	Password    string
	DisplayName string
}

// InitResult describes what Init created or updated.
type InitResult struct {
	Agent             store.AgentRecord
	OwnerUsername     string
	Created           bool
	OwnerCreated      bool
	GeneratedPassword string
	ProviderSaved     bool
	ModelSaved        bool
}

// Init creates a new agent or updates an existing one in the operator's
// store. It writes the same tables the dashboard does — the agent is a
// peer of dashboard-created agents.
func Init(ctx context.Context, st store.Store, name string, opts InitOptions) (*InitResult, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	displayName := strings.TrimSpace(name)

	var (
		providerName string
		fullModel    string
		pcfg         config.ProviderConfig
		saveProvider bool
	)
	if opts.Provider != "" || opts.Model != "" {
		var modelID string
		var err error
		providerName, modelID, fullModel, err = normalizeProviderModel(opts.Provider, opts.Model)
		if err != nil {
			return nil, err
		}
		pcfg, err = providerConfigFromOptions(ctx, st, providerName, modelID, opts)
		if err != nil {
			return nil, err
		}
		saveProvider = true
	}

	existing, err := lookupAgent(ctx, st, displayName, opts)
	if err != nil {
		return nil, err
	}
	// Without --id we treat init as create-only: a name collision is a
	// mistake (likely a forgotten --id), not an implicit update. --id
	// remains the documented update path for dashboard-created agents.
	if existing != nil && opts.AgentID == "" {
		return nil, fmt.Errorf("agent %q already exists (id %s); use a different name, or pass --id %s to update it", displayName, existing.ID, existing.ID)
	}

	// Updating an existing agent (--id) without --username preserves the
	// current owner instead of looking up the default admin (which may
	// not be the agent's owner).
	var owner *users.Account
	var ownerCreated bool
	var generatedPassword string
	if existing != nil && opts.Username == "" {
		owner, err = loadAccount(ctx, st, existing.UserID)
		if err != nil {
			return nil, err
		}
	} else {
		owner, ownerCreated, generatedPassword, err = ensureOwner(ctx, st, opts)
		if err != nil {
			return nil, err
		}
	}

	rec, created, err := writeAgent(ctx, st, existing, displayName, owner, opts)
	if err != nil {
		return nil, err
	}

	res := &InitResult{
		Agent:             *rec,
		OwnerUsername:     owner.Username,
		Created:           created,
		OwnerCreated:      ownerCreated,
		GeneratedPassword: generatedPassword,
	}

	if saveProvider {
		if err := scope.SaveProvider(ctx, st, "", "", providerName, pcfg); err != nil {
			return nil, err
		}
		res.ProviderSaved = true
		if fullModel != "" {
			data := map[string]interface{}{}
			if cur, err := st.GetConfigByName(ctx, store.KindSetting, "", rec.ID, "agents.defaults"); err == nil && cur != nil && cur.Data != nil {
				data = cur.Data
			} else if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			data["model"] = fullModel
			if err := scope.SaveSetting(ctx, st, "", rec.ID, "agents.defaults", data); err != nil {
				return nil, err
			}
			res.ModelSaved = true
		}
	}
	return res, nil
}

// ensureOwner picks (or creates) the user account the agent belongs to.
// Rules: --username pins the lookup. Without it we prefer "admin", then
// fall back to the oldest active super_admin so deployments that
// provisioned a differently-named admin via the dashboard still work.
// If the named user doesn't exist we create them when the DB is empty
// (and surface the generated password for first-run UX), or fail loudly
// when other users already exist.
func ensureOwner(ctx context.Context, st store.Store, opts InitOptions) (*users.Account, bool, string, error) {
	accts, err := users.NewAccounts(st)
	if err != nil {
		return nil, false, "", err
	}
	username := opts.Username
	explicit := username != ""
	if !explicit {
		username = "admin"
	}

	rec, err := st.GetUserByLogin(ctx, username)
	if err == nil {
		acct, err := accts.Get(ctx, rec.ID)
		return acct, false, "", err
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, "", err
	}

	count, err := st.CountUsers(ctx)
	if err != nil {
		return nil, false, "", err
	}
	if count > 0 {
		if !explicit {
			all, err := accts.List(ctx)
			if err != nil {
				return nil, false, "", err
			}
			for _, a := range all {
				if a.Role == users.RoleSuperAdmin && a.Status == users.StatusActive {
					return a, false, "", nil
				}
			}
			return nil, false, "", errors.New(`no "admin" user and no active super_admin found; pass --username to pick an existing user`)
		}
		return nil, false, "", fmt.Errorf("user %q not found; pass --username to pick an existing user", username)
	}

	password := opts.Password
	generated := ""
	if password == "" {
		password, err = randomPassword()
		if err != nil {
			return nil, false, "", err
		}
		generated = password
	}
	email := defaultStr(opts.Email, username+"@local.fastclaw")
	acct, err := accts.Create(ctx, users.CreateInput{
		Username:    username,
		Email:       email,
		Password:    password,
		DisplayName: opts.DisplayName,
		Role:        users.RoleSuperAdmin,
	})
	return acct, err == nil, generated, err
}

// lookupAgent finds an existing agent record by --id or by display name.
// Returns (nil, nil) when no match exists.
func lookupAgent(ctx context.Context, st store.Store, displayName string, opts InitOptions) (*store.AgentRecord, error) {
	if opts.AgentID != "" {
		rec, err := st.GetAgent(ctx, opts.AgentID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("agent id %q not found", opts.AgentID)
		}
		if err != nil {
			return nil, err
		}
		return rec, nil
	}
	return findAgentByName(ctx, st, displayName)
}

// writeAgent updates the existing agent or creates a new one with a
// random id. Re-init refuses a silent owner switch when --username
// points at a different account.
func writeAgent(ctx context.Context, st store.Store, existing *store.AgentRecord, displayName string, owner *users.Account, opts InitOptions) (*store.AgentRecord, bool, error) {
	if existing != nil {
		if existing.UserID != "" && existing.UserID != owner.ID && opts.Username != "" {
			return nil, false, fmt.Errorf("agent %q is owned by user %s; pass a matching --username or rename the agent first", displayName, existing.UserID)
		}
		if existing.UserID == "" {
			existing.UserID = owner.ID
		}
		if existing.Config == nil {
			existing.Config = map[string]interface{}{}
		}
		if opts.Description != "" {
			existing.Config["description"] = opts.Description
		}
		existing.Name = displayName
		if err := st.SaveAgent(ctx, existing); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	id, err := generateAgentID()
	if err != nil {
		return nil, false, err
	}
	rec := &store.AgentRecord{
		ID:     id,
		UserID: owner.ID,
		Name:   displayName,
		Config: map[string]interface{}{},
	}
	if opts.Description != "" {
		rec.Config["description"] = opts.Description
	}
	if err := st.SaveAgent(ctx, rec); err != nil {
		return nil, false, err
	}
	return rec, true, nil
}

// loadAccount reads the user account for an existing agent. Used on
// re-init so we can preserve the owner without going through ensureOwner.
func loadAccount(ctx context.Context, st store.Store, userID string) (*users.Account, error) {
	accts, err := users.NewAccounts(st)
	if err != nil {
		return nil, err
	}
	return accts.Get(ctx, userID)
}

// Resolve looks an agent up by exact id and by display name. Agent names
// can legitimately start with "agt_", so the prefix alone is not enough
// to classify a user-supplied reference.
func Resolve(ctx context.Context, st store.Store, ref string) (*store.AgentRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("agent name is required")
	}

	var byID *store.AgentRecord
	if rec, err := st.GetAgent(ctx, ref); err == nil {
		byID = rec
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	byName, err := findAgentByName(ctx, st, ref)
	if err != nil {
		if errors.Is(err, ErrAmbiguousName) && byID != nil {
			return nil, fmt.Errorf("agent reference %q is ambiguous: it matches an id and multiple display names; use the exact agt_ id of the intended agent", ref)
		}
		return nil, err
	}
	if byID != nil && byName != nil && byID.ID != byName.ID {
		return nil, fmt.Errorf("agent reference %q is ambiguous: it matches an id and a different display name; use the exact agt_ id of the intended agent", ref)
	}
	if byID != nil {
		return byID, nil
	}
	if byName != nil {
		return byName, nil
	}
	return nil, fmt.Errorf("agent %q not found", ref)
}

// ErrAmbiguousName signals that a display name resolved to more than
// one agent; callers should ask the user for the agt_ id.
var ErrAmbiguousName = errors.New("multiple agents share that name; use the agt_ id instead")

func findAgentByName(ctx context.Context, st store.Store, name string) (*store.AgentRecord, error) {
	all, err := st.ListAllAgents(ctx)
	if err != nil {
		return nil, err
	}
	var matches []store.AgentRecord
	for _, ag := range all {
		if ag.Name == name {
			matches = append(matches, ag)
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		ag := matches[0]
		return &ag, nil
	default:
		return nil, ErrAmbiguousName
	}
}

// List returns all agents in the operator's store, sorted by display name.
func List(ctx context.Context, st store.Store) ([]store.AgentRecord, error) {
	all, err := st.ListAllAgents(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name == all[j].Name {
			return all[i].ID < all[j].ID
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

// Remove deletes an agent record together with any system files it owns.
// Provider configs in the system scope are left intact; they are shared
// between agents and the dashboard.
func Remove(ctx context.Context, st store.Store, name string) (*store.AgentRecord, error) {
	rec, err := Resolve(ctx, st, name)
	if err != nil {
		return nil, err
	}
	files, err := st.ListAgentFiles(ctx, rec.ID, rec.UserID)
	if err == nil {
		for _, f := range files {
			_ = st.DeleteAgentFile(ctx, rec.ID, rec.UserID, f)
		}
	}
	if err := st.DeleteAgent(ctx, rec.ID); err != nil {
		return nil, err
	}
	return rec, nil
}

// SetConfig writes a provider field, an agent-scope setting (model,
// temperature, …), or a system-scope namespace blob.
func SetConfig(ctx context.Context, st store.Store, agentID, key, rawValue string) error {
	if strings.HasPrefix(key, "provider.") {
		return setProviderField(ctx, st, key, rawValue)
	}
	namespace, path, isAgentScope, err := settingKey(key)
	if err != nil {
		return err
	}
	uid, aid := "", ""
	if isAgentScope {
		aid = agentID
	}
	if len(path) == 0 {
		obj, ok := parseValue(rawValue).(map[string]interface{})
		if !ok {
			return fmt.Errorf("config key %q expects a JSON object value", key)
		}
		return scope.SaveSetting(ctx, st, uid, aid, namespace, obj)
	}
	data := map[string]interface{}{}
	if rec, err := st.GetConfigByName(ctx, store.KindSetting, uid, aid, namespace); err == nil && rec != nil && rec.Data != nil {
		data = rec.Data
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	setNested(data, path, parseValue(rawValue))
	if namespace == "sandbox" && len(path) > 0 && path[0] == "enabled" {
		if enabled, _ := data["enabled"].(bool); enabled {
			if _, ok := data["backend"].(string); !ok {
				data["backend"] = "docker"
			}
		}
	}
	return scope.SaveSetting(ctx, st, uid, aid, namespace, data)
}

// GetConfig returns a single config value or, when key is empty, the
// agent-scope + system-scope view of everything saved.
func GetConfig(ctx context.Context, st store.Store, agentID, key string) (interface{}, error) {
	if key == "" {
		return configDump(ctx, st, agentID)
	}
	if strings.HasPrefix(key, "provider.") {
		return getProviderField(ctx, st, key)
	}
	namespace, path, isAgentScope, err := settingKey(key)
	if err != nil {
		return nil, err
	}
	uid, aid := "", ""
	if isAgentScope {
		aid = agentID
	}
	rec, err := st.GetConfigByName(ctx, store.KindSetting, uid, aid, namespace)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return rec.Data, nil
	}
	return getNested(rec.Data, path), nil
}

// PutFile writes a system file to the agent's row in the agent_files table.
func PutFile(ctx context.Context, st store.Store, agentID, userID, filename string, data []byte) error {
	if err := validateSystemFilename(filename); err != nil {
		return err
	}
	return st.SaveAgentFile(ctx, agentID, userID, filename, data)
}

// GetFile reads a system file.
func GetFile(ctx context.Context, st store.Store, agentID, userID, filename string) ([]byte, error) {
	if err := validateSystemFilename(filename); err != nil {
		return nil, err
	}
	return st.GetAgentFile(ctx, agentID, userID, filename)
}

// ListFiles lists the agent's system files (allowlist-filtered).
func ListFiles(ctx context.Context, st store.Store, agentID, userID string) ([]string, error) {
	files, err := st.ListAgentFiles(ctx, agentID, userID)
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, file := range files {
		if systemFileAllowlist[file] {
			out = append(out, file)
		}
	}
	return out, nil
}

func normalizeProviderModel(providerName, model string) (string, string, string, error) {
	providerName = strings.TrimSpace(providerName)
	model = strings.TrimSpace(model)
	modelID := model
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("invalid model %q: expected <provider>/<model>", model)
		}
		if providerName != "" && providerName != parts[0] {
			return "", "", "", fmt.Errorf("--provider %q does not match model provider prefix %q", providerName, parts[0])
		}
		if providerName == "" {
			providerName = parts[0]
		}
		modelID = parts[1]
	}
	if providerName == "" {
		return "", "", "", errors.New("--provider is required when --model does not include a provider prefix")
	}
	fullModel := ""
	if modelID != "" {
		fullModel = providerName + "/" + modelID
	}
	return providerName, modelID, fullModel, nil
}

func providerConfigFromOptions(ctx context.Context, st store.Store, providerName, modelID string, opts InitOptions) (config.ProviderConfig, error) {
	preset := providerPreset(providerName)
	existing := config.ProviderConfig{}
	if rec, err := st.GetConfigByName(ctx, store.KindProvider, "", "", providerName); err == nil && rec != nil {
		blob, _ := json.Marshal(rec.Data)
		_ = json.Unmarshal(blob, &existing)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return config.ProviderConfig{}, err
	}
	apiBase := firstNonEmpty(opts.APIBase, existing.APIBase, preset.apiBase)
	apiType := firstNonEmpty(opts.APIType, existing.APIType, preset.apiType)
	authType := firstNonEmpty(opts.AuthType, existing.AuthType, preset.authType)
	apiKeyEnv := opts.APIKeyEnv
	if apiKeyEnv == "" {
		apiKeyEnv = preset.apiKeyEnv
	}
	apiKey := existing.APIKey
	if opts.APIKeyEnv != "" || (apiKey == "" && apiKeyEnv != "") {
		apiKey = os.Getenv(apiKeyEnv)
		if apiKey == "" && providerName != "ollama" {
			return config.ProviderConfig{}, fmt.Errorf("environment variable %s is empty", apiKeyEnv)
		}
	}
	if apiKey == "" && providerName == "ollama" {
		apiKey = "ollama"
	}
	cfg := config.ProviderConfig{
		APIKey:   apiKey,
		APIBase:  apiBase,
		APIType:  apiType,
		AuthType: authType,
		Models:   existing.Models,
	}
	if modelID != "" {
		cfg.Models = appendModel(cfg.Models, modelID)
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type providerDefaults struct {
	apiBase   string
	apiType   string
	authType  string
	apiKeyEnv string
}

func providerPreset(name string) providerDefaults {
	switch strings.ToLower(name) {
	case "anthropic":
		return providerDefaults{"https://api.anthropic.com", "anthropic-messages", "api-key", "ANTHROPIC_API_KEY"}
	case "openrouter":
		return providerDefaults{"https://openrouter.ai/api/v1", "openai-chat", "bearer-token", "OPENROUTER_API_KEY"}
	case "evolink":
		return providerDefaults{"https://direct.evolink.ai/v1", "openai-chat", "bearer-token", "EVOLINK_API_KEY"}
	case "ollama":
		return providerDefaults{"http://localhost:11434/v1", "openai-chat", "bearer-token", ""}
	case "groq":
		return providerDefaults{"https://api.groq.com/openai/v1", "openai-chat", "bearer-token", "GROQ_API_KEY"}
	case "deepseek":
		return providerDefaults{"https://api.deepseek.com", "openai-chat", "bearer-token", "DEEPSEEK_API_KEY"}
	case "mistral":
		return providerDefaults{"https://api.mistral.ai/v1", "openai-chat", "bearer-token", "MISTRAL_API_KEY"}
	case "openai":
		fallthrough
	default:
		return providerDefaults{"https://api.openai.com/v1", "openai-chat", "bearer-token", "OPENAI_API_KEY"}
	}
}

func setProviderField(ctx context.Context, st store.Store, key, rawValue string) error {
	parts := strings.Split(strings.TrimPrefix(key, "provider."), ".")
	if len(parts) != 2 {
		return errors.New("provider config key must look like provider.<name>.<field>")
	}
	name, field := parts[0], parts[1]
	pc := config.ProviderConfig{}
	if rec, err := st.GetConfigByName(ctx, store.KindProvider, "", "", name); err == nil && rec != nil {
		blob, _ := json.Marshal(rec.Data)
		_ = json.Unmarshal(blob, &pc)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	} else {
		preset := providerPreset(name)
		pc = config.ProviderConfig{APIBase: preset.apiBase, APIType: preset.apiType, AuthType: preset.authType}
	}
	switch field {
	case "apiKeyEnv":
		pc.APIKey = os.Getenv(rawValue)
		if pc.APIKey == "" {
			return fmt.Errorf("environment variable %s is empty", rawValue)
		}
	case "apiKey":
		pc.APIKey = rawValue
	case "apiBase":
		pc.APIBase = rawValue
	case "apiType":
		pc.APIType = rawValue
	case "authType":
		pc.AuthType = rawValue
	case "model":
		if rawValue == "" {
			return errors.New("provider model id is required; use `provider.<name>.models []` to clear")
		}
		pc.Models = appendModel(pc.Models, rawValue)
	case "models":
		if rawValue != "[]" {
			return errors.New(`only "[]" is accepted; use provider.<name>.model <id> to add entries`)
		}
		pc.Models = nil
	default:
		return fmt.Errorf("unsupported provider field %q", field)
	}
	return scope.SaveProvider(ctx, st, "", "", name, pc)
}

func getProviderField(ctx context.Context, st store.Store, key string) (interface{}, error) {
	parts := strings.Split(strings.TrimPrefix(key, "provider."), ".")
	if len(parts) != 2 {
		return nil, errors.New("provider config key must look like provider.<name>.<field>")
	}
	rec, err := st.GetConfigByName(ctx, store.KindProvider, "", "", parts[0])
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return redactProviderData(rec.Data)[parts[1]], nil
}

func appendModel(models []config.ModelEntry, id string) []config.ModelEntry {
	for _, m := range models {
		if m.ID == id {
			return models
		}
	}
	return append(models, config.ModelEntry{ID: id, Name: id})
}

// agentScopeKeys maps top-level CLI keys onto the namespace+path they
// live in under the agent's row. These are the same keys the dashboard
// exposes per-agent.
var agentScopeKeys = map[string]string{
	"model":                "agents.defaults",
	"maxTokens":            "agents.defaults",
	"temperature":          "agents.defaults",
	"maxToolIterations":    "agents.defaults",
	"maxParallelToolCalls": "agents.defaults",
	"thinking":             "agents.defaults",
	"policy":               "agents.defaults",
	// promptMode selects which framework sections BuildSystemPromptAs
	// emits AND which built-in tools the LLM sees. One of "agent",
	// "chatbot", "customize". Stored as a plain string under
	// agents.defaults.promptMode. The built-in tool set per mode is
	// hardcoded in builtinAllowForMode (internal/agent/loop.go) —
	// custom tools come from Plugin / MCP, not a per-agent allowlist.
	"promptMode": "agents.defaults",
	// splitReplies — per-agent multi-bubble toggle. When true, the
	// dispatcher splits the reply at SplitMessageMarker before sending
	// to any IM channel (WeChat / Telegram / Discord / Slack / LINE /
	// Feishu). System-level fallback is gone — false is the default
	// when the key is absent.
	"splitReplies": "agents.defaults",
	"sandbox":      "sandbox",
}

var systemSettingNamespaces = []string{
	"skills.install",
	"skills.entries",
	"tools.providers",
	"tools.categories",
	"skillsLearner",
	"objectstore",
	"taskqueue",
	"heartbeat",
	"plugins",
	"memory",
	"privacy",
	"hooks",
	"teams",
}

// settingKey resolves a CLI key to (namespace, path-into-data, scope).
// Agent-scope keys cover model/temperature/sandbox; everything else is
// a system-wide namespace. The bool return is "isAgentScope" — true
// means the row's agent_id should be set to the active agentID; false
// means a system row (user_id='', agent_id='').
func settingKey(key string) (string, []string, bool, error) {
	if ns, ok := agentScopeKeys[key]; ok {
		path := []string{key}
		if key == ns { // sandbox -> sandbox: whole namespace, no inner path
			path = nil
		}
		return ns, path, true, nil
	}
	if strings.HasPrefix(key, "sandbox.") {
		return "sandbox", strings.Split(strings.TrimPrefix(key, "sandbox."), "."), true, nil
	}
	for _, ns := range systemSettingNamespaces {
		if key == ns {
			return ns, nil, false, nil
		}
		prefix := ns + "."
		if strings.HasPrefix(key, prefix) {
			path := strings.Split(strings.TrimPrefix(key, prefix), ".")
			if len(path) > 0 && path[0] != "" {
				return ns, path, false, nil
			}
		}
	}
	return "", nil, false, fmt.Errorf("unsupported config key %q", key)
}

func configDump(ctx context.Context, st store.Store, agentID string) (map[string]interface{}, error) {
	out := map[string]interface{}{}

	agentRec, err := st.GetAgent(ctx, agentID)
	if err == nil && agentRec != nil {
		out["agent"] = map[string]interface{}{
			"id":     agentRec.ID,
			"name":   agentRec.Name,
			"userId": agentRec.UserID,
			"config": agentRec.Config,
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	agentSettings := map[string]interface{}{}
	for _, ns := range []string{"agents.defaults", "sandbox"} {
		rec, err := st.GetConfigByName(ctx, store.KindSetting, "", agentID, ns)
		if errors.Is(err, store.ErrNotFound) || rec == nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		agentSettings[ns] = rec.Data
	}
	if len(agentSettings) > 0 {
		out["agentScope"] = agentSettings
	}

	sysSettings := map[string]interface{}{}
	settings, err := st.ListConfigs(ctx, store.KindSetting, "", "")
	if err != nil {
		return nil, err
	}
	for _, rec := range settings {
		sysSettings[rec.Name] = rec.Data
	}
	if len(sysSettings) > 0 {
		out["system"] = sysSettings
	}
	providers, err := st.ListConfigs(ctx, store.KindProvider, "", "")
	if err != nil {
		return nil, err
	}
	if len(providers) > 0 {
		provOut := map[string]interface{}{}
		for _, rec := range providers {
			provOut[rec.Name] = redactProviderData(rec.Data)
		}
		out["providers"] = provOut
	}
	return out, nil
}

func redactProviderData(data map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		if k == "apiKey" {
			if s, _ := v.(string); s != "" {
				out[k] = "<set>"
			} else {
				out[k] = ""
			}
			continue
		}
		out[k] = v
	}
	return out
}

func setNested(data map[string]interface{}, path []string, value interface{}) {
	cur := data
	for _, p := range path[:len(path)-1] {
		next, _ := cur[p].(map[string]interface{})
		if next == nil {
			next = map[string]interface{}{}
			cur[p] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = value
}

func getNested(data map[string]interface{}, path []string) interface{} {
	var cur interface{} = data
	for _, p := range path {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// parseValue turns a raw CLI string into a typed JSON value where
// possible. JSON handles bools/numbers/objects; unquoted strings
// fall through as the raw string.
func parseValue(raw string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		return v
	}
	return raw
}

var systemFileAllowlist = map[string]bool{
	"SOUL.md":      true,
	"IDENTITY.md":  true,
	"USER.md":      true,
	"BOOTSTRAP.md": true,
	"MEMORY.md":    true,
	"HEARTBEAT.md": true,
	"AGENTS.md":    true,
	"TOOLS.md":     true,
	"agent.json":   true,
}

func validateSystemFilename(filename string) error {
	if !systemFileAllowlist[filename] {
		return fmt.Errorf("filename %q is not a supported agent system file", filename)
	}
	return nil
}

func generateAgentID() (string, error) {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "agt_" + hex.EncodeToString(b[:]), nil
}

func randomPassword() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func defaultStr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
