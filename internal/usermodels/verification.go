package usermodels

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

const verificationNamespace = "byok.models.verification"

func prepareProvider(p *Provider, old Config) error {
	if !idPattern.MatchString(p.ID) || len(p.Name) == 0 || len(p.Name) > 100 {
		return errors.New("invalid provider")
	}
	u, err := url.Parse(p.APIBase)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(p.APIBase) > 2048 {
		return errors.New("invalid HTTPS API URL")
	}
	if p.APIType != "openai-completions" && p.APIType != "anthropic-messages" {
		return errors.New("unsupported API type")
	}
	if p.APIKey == "" {
		for _, saved := range old.Providers {
			if saved.ID == p.ID && saved.APIBase == p.APIBase && saved.APIType == p.APIType {
				p.APIKey = saved.APIKey
				break
			}
		}
	}
	if strings.TrimSpace(p.APIKey) == "" || len(p.APIKey) > 8192 || strings.ContainsAny(p.APIKey, "\r\n") {
		return errors.New("API key required")
	}
	if len(p.Models) == 0 || len(p.Models) > 100 {
		return errors.New("models required")
	}
	seen := make(map[string]bool)
	for _, model := range p.Models {
		if strings.TrimSpace(model) != model || model == "" || len(model) > 200 || strings.ContainsAny(model, "\r\n") || seen[model] {
			return errors.New("invalid model ID")
		}
		seen[model] = true
	}
	p.HasKey = false
	return nil
}

func fingerprint(p Provider) string {
	p.HasKey = false
	data, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func providerUnchanged(candidate Provider, old Config) bool {
	for _, saved := range old.Providers {
		if saved.ID == candidate.ID {
			return fingerprint(saved) == fingerprint(candidate)
		}
	}
	return false
}

func requireVerified(ctx context.Context, st store.Store, uid string, p Provider) error {
	row, err := st.GetConfigByName(ctx, store.KindSetting, uid, "", verificationNamespace)
	if err == nil && row != nil && row.Data["fingerprint"] == fingerprint(p) &&
		time.Since(row.UpdatedAt) < 15*time.Minute {
		return nil
	}
	return errors.New("test this provider before saving")
}

// TestConnection sends only a short probe using the same guarded transport as normal BYOK turns.
func TestConnection(ctx context.Context, st store.Store, uid string, p Provider) (string, error) {
	return testConnection(ctx, st, uid, p, func(p Provider) modelChat {
		return provider.FromConfig(config.ProviderConfig{
			APIKey: p.APIKey, APIBase: p.APIBase, APIType: p.APIType, BYOK: true,
		})
	})
}

type modelChat interface {
	Chat(context.Context, []provider.Message, []provider.Tool, string, int, float64) (*provider.Response, error)
}

func testConnection(ctx context.Context, st store.Store, uid string, p Provider, makeClient func(Provider) modelChat) (string, error) {
	old, err := Load(ctx, st, uid)
	if err != nil {
		return "", err
	}
	if err := prepareProvider(&p, old); err != nil {
		return "", err
	}
	client := makeClient(p)
	probeCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	for _, model := range p.Models {
		response, err := client.Chat(probeCtx, []provider.Message{{Role: "user", Content: "Reply OK."}}, nil, providerKey(p.ID)+"/"+model, 64, 1)
		if err != nil || response == nil || strings.TrimSpace(response.Content+response.Thinking) == "" {
			return model, nil
		}
	}
	return "", scope.SaveSetting(ctx, st, uid, "", verificationNamespace, map[string]interface{}{
		"fingerprint": fingerprint(p),
	})
}
