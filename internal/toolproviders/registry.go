// Package toolproviders is the plug-in layer for tools that talk to external
// services (web search, image generation, TTS, ...). Each category exposes
// ONE tool to the LLM (e.g. "web_search") backed by a primary provider and
// an ordered fallback chain. The LLM never sees individual providers.
//
// Providers are stateless: per-call config (API key, endpoint, ...) is passed
// in via Request.Config, so the same provider instance handles many tenants
// safely.
package toolproviders

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Provider is a single backend for a category. Implementations are pure Go;
// a subprocess/plugin Provider can be added later with the same interface.
type Provider interface {
	Category() string
	Name() string
	// Execute runs the provider. The category decides what Args/Response
	// shapes mean — all providers under a category share the same contract.
	Execute(ctx context.Context, req Request) (Response, error)
}

// CredentialFree is an optional Provider opt-in for backends that work
// without any per-tenant config (the built-in web_fetch direct fetcher
// is the canonical example: it just hits http.DefaultClient). Chain
// availability/skip rules treat these providers as always usable so the
// admin can pick them in the UI without typing a fake API key.
type CredentialFree interface {
	CredentialFree() bool
}

// ExplicitOnly is an optional Provider opt-in for backends that must never be
// selected by an auto chain or "all" mode. The provider is only usable when an
// admin explicitly adds it to a chain's Order. Chain.FilterExplicitOnly removes
// any provider that reports ExplicitOnly() == true.
type ExplicitOnly interface {
	ExplicitOnly() bool
}

func providerCredentialFree(p Provider) bool {
	cf, ok := p.(CredentialFree)
	return ok && cf.CredentialFree()
}

func providerExplicitOnly(p Provider) bool {
	eo, ok := p.(ExplicitOnly)
	return ok && eo.ExplicitOnly()
}

// Request carries the LLM-provided args plus the resolved per-tenant config.
type Request struct {
	Args   map[string]any
	Config ProviderConfig
}

// ProviderConfig holds the credentials/endpoint resolved for a single call.
// It comes from fastclaw.json toolProviders.<name>, optionally overridden by
// the agent's own config.
type ProviderConfig struct {
	APIKey   string
	Endpoint string
	Options  map[string]string
	// Model is the part after the slash in a "<provider>/<model>" reference.
	// Empty when the user wrote just "<provider>".
	Model string
}

// Response is the tool-visible text + optional structured payload.
type Response struct {
	Text string
	Raw  any
}

// Registry holds all registered Provider implementations, keyed by
// "<category>/<name>". It is concurrency-safe for reads once warm, so
// registration at init() is fine.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register installs p. Subsequent registrations with the same key win so
// tests can swap implementations.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[key(p.Category(), p.Name())] = p
}

// Get returns the Provider registered under category/name, or nil.
func (r *Registry) Get(category, name string) Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.providers[key(category, name)]
}

// Names returns all registered provider names in a category, sorted.
func (r *Registry) Names(category string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	prefix := category + "/"
	for k := range r.providers {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out
}

func key(category, name string) string { return category + "/" + name }

// ---------- Chain execution ----------

// Chain is the per-call wiring for a tool category: the ordered list of
// "<provider>/<model>" references to try, whether to fall back, and a lookup
// function that resolves each provider's credentials (which may be tenant-
// scoped). A Chain is cheap to build and throw away per agent.
type Chain struct {
	Category     string
	Order        []string // e.g. ["exa/auto", "brave/web", "searxng/default"]
	AutoFallback bool
	Registry     *Registry
	// GetConfig returns the config for a provider name. Allows per-agent /
	// per-tenant overrides without the Chain owning any state.
	GetConfig func(providerName string) ProviderConfig
}

// FilterExplicitOnly removes explicit-only providers from the chain's Order.
// Call this after building the chain from config so that auto-selected
// providers that are marked explicit-only are excluded.
func (c *Chain) FilterExplicitOnly() {
	if c.Registry == nil {
		return
	}
	var filtered []string
	for _, ref := range c.Order {
		name, _ := parseRef(ref)
		p := c.Registry.Get(c.Category, name)
		if p != nil && providerExplicitOnly(p) {
			continue
		}
		filtered = append(filtered, ref)
	}
	c.Order = filtered
}

// Available reports whether at least one provider in Order is registered AND
// has a usable config (non-empty APIKey or Endpoint). Used at tool-registration
// time to decide whether to advertise the category's tool to the LLM at all —
// mirrors OpenClaw's rule that absent credentials hide the tool.
func (c *Chain) Available() bool {
	for _, ref := range c.Order {
		name, _ := parseRef(ref)
		if c.Registry == nil {
			continue
		}
		p := c.Registry.Get(c.Category, name)
		if p == nil {
			continue
		}
		cfg := c.GetConfig(name)
		if cfg.APIKey != "" || cfg.Endpoint != "" || providerCredentialFree(p) {
			return true
		}
	}
	return false
}

// Execute runs the chain: tries each provider in Order until one succeeds.
// A provider is skipped (treated as a retriable miss) when:
//   - It isn't registered
//   - Its config has no APIKey or Endpoint
//   - It returns a retriable error (network, timeout, 429, 5xx, ErrNoResults)
// Any other error terminates the chain (so config bugs surface fast).
// When AutoFallback is false, only the first configured provider is tried.
func (c *Chain) Execute(ctx context.Context, args map[string]any) (Response, error) {
	if c.Registry == nil {
		return Response{}, fmt.Errorf("tool chain for %q has no registry", c.Category)
	}
	if len(c.Order) == 0 {
		return Response{}, fmt.Errorf("no providers configured for %q", c.Category)
	}
	var errs []error
	for i, ref := range c.Order {
		name, model := parseRef(ref)
		p := c.Registry.Get(c.Category, name)
		if p == nil {
			errs = append(errs, fmt.Errorf("%s: provider not registered", ref))
			if !c.AutoFallback {
				break
			}
			continue
		}
		cfg := c.GetConfig(name)
		cfg.Model = model
		if cfg.APIKey == "" && cfg.Endpoint == "" && !providerCredentialFree(p) {
			errs = append(errs, fmt.Errorf("%s: no API key configured", ref))
			if !c.AutoFallback {
				break
			}
			continue
		}
		resp, err := p.Execute(ctx, Request{Args: args, Config: cfg})
		if err == nil && !isEmpty(resp) {
			return resp, nil
		}
		if err == nil {
			err = ErrNoResults
		}
		errs = append(errs, fmt.Errorf("%s: %w", ref, err))
		// Stop on first non-retriable error; otherwise keep falling back.
		if !isRetriable(err) || !c.AutoFallback || i == len(c.Order)-1 {
			if !isRetriable(err) {
				return Response{}, errors.Join(errs...)
			}
		}
	}
	return Response{}, errors.Join(errs...)
}

// parseRef splits "exa/auto" into ("exa", "auto"). "exa" alone returns
// ("exa", "").
func parseRef(ref string) (name, model string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func isEmpty(r Response) bool { return strings.TrimSpace(r.Text) == "" && r.Raw == nil }

// ---------- Error classification ----------

// ErrNoResults is returned by a provider when its request succeeded but the
// result set is empty. Chain execution treats it as retriable so the next
// provider gets a shot.
var ErrNoResults = errors.New("no results")

// RetriableError marks an error class that should trigger fallback to the
// next provider. Providers return these for network failures, upstream 5xx
// and 429, and timeouts. Other errors (e.g. malformed args) are fatal.
type RetriableError struct{ Err error }

func (r *RetriableError) Error() string { return r.Err.Error() }
func (r *RetriableError) Unwrap() error { return r.Err }

// Retry wraps err so the chain treats it as retriable.
func Retry(err error) error {
	if err == nil {
		return nil
	}
	return &RetriableError{Err: err}
}

func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoResults) {
		return true
	}
	var re *RetriableError
	return errors.As(err, &re)
}
