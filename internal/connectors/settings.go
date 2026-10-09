package connectors

import (
	"context"
	"errors"

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
	rec, err := st.GetConfigByName(ctx, store.KindSetting, "", "", SettingsNamespace)
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
	rec, err := st.GetConfigByName(ctx, store.KindSetting, "", "", SettingsNamespace)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if rec == nil {
		rec = &store.ConfigRecord{Kind: store.KindSetting, Name: SettingsNamespace, Enabled: true}
	}
	rec.Data = map[string]interface{}{"url": s.URL, "apiKey": s.APIKey}
	return st.SaveConfig(ctx, rec)
}

// ClearSettings removes the saved configuration (env vars apply again).
func ClearSettings(ctx context.Context, st store.Store) error {
	rec, err := st.GetConfigByName(ctx, store.KindSetting, "", "", SettingsNamespace)
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
