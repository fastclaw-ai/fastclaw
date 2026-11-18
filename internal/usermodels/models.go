// Package usermodels stores a user's BYOK configuration as one atomic settings row.
package usermodels

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

const Namespace = "byok.models"

type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	APIBase string   `json:"apiBase"`
	APIType string   `json:"apiType"`
	APIKey  string   `json:"apiKey,omitempty"`
	HasKey  bool     `json:"hasKey"`
	Models  []string `json:"models"`
}
type Config struct {
	Providers    []Provider `json:"providers"`
	DefaultModel string     `json:"defaultModel"`
}

func Load(ctx context.Context, st store.Store, uid string) (Config, error) {
	c := Config{Providers: []Provider{}}
	row, err := st.GetConfigByName(ctx, store.KindSetting, uid, "", Namespace)
	if errors.Is(err, store.ErrNotFound) {
		return c, nil
	}
	if err != nil || row == nil {
		return c, err
	}
	b, err := json.Marshal(row.Data)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	if err != nil {
		return c, err
	}
	for i := range c.Providers {
		c.Providers[i].APIKey, err = openSecret(c.Providers[i].APIKey, uid+":"+c.Providers[i].ID)
		if err != nil {
			return Config{}, err
		}
	}
	return c, nil
}
func View(c Config) Config {
	out := c
	out.Providers = append([]Provider{}, c.Providers...)
	for i := range out.Providers {
		out.Providers[i].HasKey = out.Providers[i].APIKey != ""
		out.Providers[i].APIKey = ""
	}
	return out
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,60}$`)

func Save(ctx context.Context, st store.Store, uid string, c Config) error {
	return save(ctx, st, uid, c, false)
}

func SaveVerified(ctx context.Context, st store.Store, uid string, c Config) error {
	return save(ctx, st, uid, c, true)
}

func save(ctx context.Context, st store.Store, uid string, c Config, verify bool) error {
	c.Providers = append([]Provider{}, c.Providers...)
	old, err := Load(ctx, st, uid)
	if err != nil {
		return err
	}
	if len(c.Providers) > 20 {
		return errors.New("too many providers")
	}
	seen := map[string]bool{}
	selected := c.DefaultModel == ""
	for i := range c.Providers {
		p := &c.Providers[i]
		if !idPattern.MatchString(p.ID) || seen[p.ID] || len(p.Name) == 0 || len(p.Name) > 100 {
			return errors.New("invalid provider")
		}
		seen[p.ID] = true
		if err := prepareProvider(p, old); err != nil {
			return err
		}
		for _, model := range p.Models {
			if c.DefaultModel == p.ID+"/"+model {
				selected = true
			}
		}
		if verify && !providerUnchanged(*p, old) {
			if err := requireVerified(ctx, st, uid, *p); err != nil {
				return err
			}
		}
		p.HasKey = false
	}
	if !selected {
		return errors.New("select one of your saved models")
	}
	for i := range c.Providers {
		c.Providers[i].APIKey, err = sealSecret(c.Providers[i].APIKey, uid+":"+c.Providers[i].ID)
		if err != nil {
			return err
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var data map[string]interface{}
	if err = json.Unmarshal(b, &data); err != nil {
		return err
	}
	return scope.SaveSetting(ctx, st, uid, "", Namespace, data)
}

// Apply runs after owner and agent overlays. No system credential can be
// selected through this namespace, and it never silently falls back.
func Apply(ctx context.Context, st store.Store, uid string, rc *config.ResolvedAgent) error {
	c, err := Load(ctx, st, uid)
	if err != nil {
		return err
	}
	if c.DefaultModel == "" {
		return nil
	}
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if c.DefaultModel == p.ID+"/"+m {
				key := providerKey(p.ID)
				rc.Providers = map[string]config.ProviderConfig{}
				rc.Providers[key] = config.ProviderConfig{APIKey: p.APIKey, APIBase: p.APIBase, APIType: p.APIType, BYOK: true}
				rc.Model = key + "/" + m
				return nil
			}
		}
	}
	return errors.New("saved BYOK model is unavailable")
}

func providerKey(id string) string {
	return "byok-" + id
}
