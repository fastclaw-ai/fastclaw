// Package users manages API keys for accessing FastClaw agents via the HTTP API.
//
// API keys are stored in ~/.fastclaw/apikeys.json. Each key grants access to
// the agent API (chat, sessions, config). The gateway auth token (in fastclaw.json)
// is the admin key — it can manage other API keys.
package users

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// APIKey is one entry in the registry.
type APIKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"createdAt"`
}

// User is kept as an alias for backward compatibility with API responses.
type User = APIKey

// Registry is an in-memory, file-backed API key store.
type Registry struct {
	path  string
	mu    sync.RWMutex
	keys  map[string]*APIKey // id -> APIKey
	byKey map[string]string  // key -> id
}

// DefaultPath returns ~/.fastclaw/apikeys.json.
func DefaultPath() (string, error) {
	home, err := config.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "apikeys.json"), nil
}

func Load() (*Registry, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

func LoadFrom(path string) (*Registry, error) {
	r := &Registry{
		path:  path,
		keys:  make(map[string]*APIKey),
		byKey: make(map[string]string),
	}

	// Try new format first (apikeys.json)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Try legacy users.json
		legacyPath := filepath.Join(filepath.Dir(path), "users.json")
		data, err = os.ReadFile(legacyPath)
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read apikeys: %w", err)
	}

	var list []*APIKey
	if err := json.Unmarshal(data, &list); err != nil {
		// Try legacy format (tokens field)
		var legacy []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Tokens []string `json:"tokens"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			for _, u := range legacy {
				for _, t := range u.Tokens {
					ak := &APIKey{ID: u.ID, Name: u.Name, Key: t, CreatedAt: time.Now()}
					r.keys[ak.ID+"-"+t[:8]] = ak
					r.byKey[t] = ak.ID
				}
			}
			return r, nil
		}
		return nil, fmt.Errorf("parse apikeys: %w", err)
	}

	for _, ak := range list {
		r.keys[ak.ID] = ak
		r.byKey[ak.Key] = ak.ID
	}
	return r, nil
}

func (r *Registry) Save() error {
	r.mu.RLock()
	list := make([]*APIKey, 0, len(r.keys))
	for _, ak := range r.keys {
		list = append(list, ak)
	}
	r.mu.RUnlock()

	data, _ := json.MarshalIndent(list, "", "  ")
	os.MkdirAll(filepath.Dir(r.path), 0o700)
	return os.WriteFile(r.path, data, 0o600)
}

// Add creates a new API key and returns it.
func (r *Registry) Add(id, name string) (*APIKey, string, error) {
	if id == "" {
		return nil, "", errors.New("id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.keys[id]; exists {
		return nil, "", fmt.Errorf("API key %q already exists", id)
	}
	key, _ := newToken()
	ak := &APIKey{
		ID:        id,
		Name:      name,
		Key:       key,
		CreatedAt: time.Now().UTC(),
	}
	r.keys[id] = ak
	r.byKey[key] = id
	return ak, key, nil
}

// IssueToken creates a new key for an existing entry (replaces old key).
func (r *Registry) IssueToken(id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ak, ok := r.keys[id]
	if !ok {
		return "", fmt.Errorf("API key %q not found", id)
	}
	// Remove old key mapping
	delete(r.byKey, ak.Key)
	// Generate new key
	key, _ := newToken()
	ak.Key = key
	r.byKey[key] = id
	return key, nil
}

func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ak, ok := r.keys[id]
	if !ok {
		return fmt.Errorf("API key %q not found", id)
	}
	delete(r.byKey, ak.Key)
	delete(r.keys, id)
	return nil
}

func (r *Registry) List() []*APIKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*APIKey, 0, len(r.keys))
	for _, ak := range r.keys {
		cp := *ak
		out = append(out, &cp)
	}
	return out
}

func (r *Registry) Get(id string) (*APIKey, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ak, ok := r.keys[id]
	if !ok {
		return nil, false
	}
	cp := *ak
	return &cp, true
}

// LookupByToken returns the key ID associated with a bearer token.
func (r *Registry) LookupByToken(token string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byKey[token]
	return id, ok
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.keys)
}

func newToken() (string, error) {
	var buf [32]byte
	rand.Read(buf[:])
	return "fc_" + hex.EncodeToString(buf[:]), nil
}
