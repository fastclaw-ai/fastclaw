// Package usermodels stores a user's BYOK configuration as one atomic settings row.
package usermodels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

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
		u, e := url.Parse(p.APIBase)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(p.APIBase) > 2048 {
			return errors.New("invalid HTTPS API URL")
		}
		if p.APIType != "openai-completions" && p.APIType != "anthropic-messages" {
			return errors.New("unsupported API type")
		}
		if p.APIKey == "" {
			for _, o := range old.Providers {
				if o.ID == p.ID && o.APIBase == p.APIBase && o.APIType == p.APIType {
					p.APIKey = o.APIKey
				}
			}
		}
		if strings.TrimSpace(p.APIKey) == "" || len(p.APIKey) > 8192 || strings.ContainsAny(p.APIKey, "\r\n") {
			return errors.New("API key required")
		}
		if len(p.Models) == 0 || len(p.Models) > 100 {
			return errors.New("models required")
		}
		models := map[string]bool{}
		for _, m := range p.Models {
			if strings.TrimSpace(m) != m || m == "" || len(m) > 200 || strings.ContainsAny(m, "\r\n") || models[m] {
				return errors.New("invalid model ID")
			}
			models[m] = true
			if c.DefaultModel == p.ID+"/"+m {
				selected = true
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
				key := "byok-" + p.ID
				rc.Providers = map[string]config.ProviderConfig{}
				rc.Providers[key] = config.ProviderConfig{APIKey: p.APIKey, APIBase: p.APIBase, APIType: p.APIType, BYOK: true}
				rc.Model = key + "/" + m
				return nil
			}
		}
	}
	return errors.New("saved BYOK model is unavailable")
}
