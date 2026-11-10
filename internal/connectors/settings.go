package connectors

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// SettingsNamespace is the system-scope setting row (kind=setting) an
// admin saves from System → Tools → Connectors. It takes precedence over
// FASTCLAW_CONNANY_URL / FASTCLAW_CONNANY_API_KEY, which remain the
// fallback when nothing is saved.
const SettingsNamespace = "connectors"

// Settings is the saved Connany configuration.
type Settings struct {
	URL    string
	APIKey string
}

// LoadSettings reads the saved configuration; ok=false when none is saved.
func LoadSettings(ctx context.Context, st store.Store) (Settings, bool, error) {
	return loadSettings(ctx, st, "")
}

// loadSettings reads the system row (userID "") or a user's own row.
func loadSettings(ctx context.Context, st store.Store, userID string) (Settings, bool, error) {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, "", SettingsNamespace)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec == nil) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, err
	}
	url, _ := rec.Data["url"].(string)
	key, _ := rec.Data["apiKey"].(string)
	if url == "" && key == "" {
		return Settings{}, false, nil
	}
	return Settings{URL: url, APIKey: key}, true, nil
}

// SaveSettings stores the configuration (system scope).
func SaveSettings(ctx context.Context, st store.Store, s Settings) error {
	return saveSettings(ctx, st, "", s)
}

func saveSettings(ctx context.Context, st store.Store, userID string, s Settings) error {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, "", SettingsNamespace)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if rec == nil {
		rec = &store.ConfigRecord{Kind: store.KindSetting, UserID: userID, Name: SettingsNamespace, Enabled: true}
	}
	rec.Data = map[string]interface{}{"url": s.URL, "apiKey": s.APIKey}
	return st.SaveConfig(ctx, rec)
}

// ClearSettings removes the saved configuration (env vars apply again).
func ClearSettings(ctx context.Context, st store.Store) error {
	return clearSettings(ctx, st, "")
}

func clearSettings(ctx context.Context, st store.Store, userID string) error {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, userID, "", SettingsNamespace)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rec == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	return st.DeleteConfig(ctx, rec.ID)
}

var envFallback Settings

// SetEnvFallback records FASTCLAW_CONNANY_URL / _API_KEY, read at boot
// (the env is scrubbed afterwards), for when no settings are saved.
func SetEnvFallback(s Settings) {
	defaultMu.Lock()
	envFallback = s
	defaultMu.Unlock()
}

// EnvFallback returns the env configuration recorded at boot.
func EnvFallback() Settings {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return envFallback
}

// Apply loads the effective configuration — saved settings, else the env
// fallback — and (re)configures the service.
func Apply(ctx context.Context, st store.Store) (source string, err error) {
	db, ok := st.(DB)
	if !ok {
		return "", nil
	}
	defaultMu.Lock()
	settingsStore, settingsDB = st, db
	defaultMu.Unlock()
	saved, has, err := LoadSettings(ctx, st)
	if err != nil {
		return "", err
	}
	if has {
		return "settings", Configure(ctx, saved.URL, saved.APIKey, db)
	}
	env := EnvFallback()
	if env.URL != "" && env.APIKey != "" {
		return "env", Configure(ctx, env.URL, env.APIKey, db)
	}
	return "", Configure(ctx, "", "", db)
}

// ── A user's own Connany (Console → Tools → Connectors) ────────────────
//
// Like models and skills: a person may bring their own Connany project
// key, used for their accounts instead of the deployment's; without one
// they use the deployment's service (System → Tools), if any.

var (
	settingsStore store.Store
	settingsDB    DB

	userMu  sync.Mutex
	userSvc = map[string]userEntry{}
)

type userEntry struct {
	at  time.Time
	set Settings // zero = the user has none
	svc *Service
}

// newUserService builds a user's own service (a var so tests can reach a
// local fake, which the public-address guard refuses).
var newUserService = func(ctx context.Context, baseURL, apiKey string, db DB) (*Service, error) {
	return newService(ctx, baseURL, apiKey, db, true)
}

// A pod sees another pod's save within this long.
const userSettingsTTL = 30 * time.Second

// LoadUserSettings reads a user's own configuration; ok=false when none.
func LoadUserSettings(ctx context.Context, st store.Store, userID string) (Settings, bool, error) {
	if userID == "" {
		return Settings{}, false, nil
	}
	return loadSettings(ctx, st, userID)
}

// SaveUserSettings stores a user's own configuration and applies it.
func SaveUserSettings(ctx context.Context, st store.Store, userID string, s Settings) error {
	if userID == "" {
		return newError("invalid_request")
	}
	if err := saveSettings(ctx, st, userID, s); err != nil {
		return err
	}
	forgetUser(userID)
	return nil
}

// ClearUserSettings removes a user's own configuration: they fall back to
// the deployment's service.
func ClearUserSettings(ctx context.Context, st store.Store, userID string) error {
	if userID == "" {
		return nil
	}
	if err := clearSettings(ctx, st, userID); err != nil {
		return err
	}
	forgetUser(userID)
	return nil
}

func forgetUser(userID string) {
	userMu.Lock()
	delete(userSvc, userID)
	userMu.Unlock()
}

// ProbePublic is Probe for an address a user typed in (see NewPublicClient).
func ProbePublic(ctx context.Context, baseURL, apiKey string) error {
	client, err := NewPublicClient(baseURL, apiKey)
	if err != nil {
		return newError("invalid_url")
	}
	return probe(ctx, client)
}

// For returns the service that holds userID's accounts: their own Connany
// when they saved one, else the deployment's (Get). nil when neither.
func For(ctx context.Context, userID string) *Service {
	svc, _ := ForWithSource(ctx, userID)
	return svc
}

// ForWithSource is For plus where the service comes from: "user",
// "system" or "" (none).
func ForWithSource(ctx context.Context, userID string) (*Service, string) {
	if svc := userService(ctx, userID); svc != nil {
		return svc, "user"
	}
	if svc := Get(); svc != nil {
		return svc, "system"
	}
	return nil, ""
}

func userService(ctx context.Context, userID string) *Service {
	if userID == "" {
		return nil
	}
	userMu.Lock()
	hit, ok := userSvc[userID]
	userMu.Unlock()
	if ok && time.Since(hit.at) < userSettingsTTL {
		return hit.svc
	}
	defaultMu.RLock()
	st, db := settingsStore, settingsDB
	defaultMu.RUnlock()
	if st == nil || db == nil {
		return nil
	}
	set, has, err := LoadUserSettings(ctx, st, userID)
	if err != nil {
		// Keep what we had rather than switch services on a DB hiccup.
		if ok {
			return hit.svc
		}
		return nil
	}
	entry := userEntry{at: time.Now()}
	if has && set.URL != "" && set.APIKey != "" {
		entry.set = set
		if ok && hit.svc != nil && hit.set == set {
			entry.svc = hit.svc // unchanged: keep its caches
		} else if svc, err := newUserService(ctx, set.URL, set.APIKey, db); err == nil {
			entry.svc = svc
		}
	}
	userMu.Lock()
	userSvc[userID] = entry
	userMu.Unlock()
	return entry.svc
}
