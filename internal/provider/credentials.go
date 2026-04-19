package provider

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// CredentialEntry represents a stored credential.
type CredentialEntry struct {
	Name   string            `json:"name"`
	Type   string            `json:"type"`   // "api_key", "oauth", "token"
	Source string            `json:"source"` // "config", "env", "store"
	Keys   map[string]string `json:"keys"`
}

// CredentialManager handles secure credential storage and retrieval.
type CredentialManager struct {
	masterKey     []byte
	entries       map[string]*CredentialEntry
	storePath     string
	needsReencrypt bool // true after legacy-key fallback decrypt
	mu            sync.RWMutex
}

// NewCredentialManager creates a new credential manager for the default user.
// If passphrase is empty, uses a machine-derived key.
func NewCredentialManager(passphrase string) (*CredentialManager, error) {
	return NewCredentialManagerForUser(config.DefaultUserID, passphrase)
}

// NewCredentialManagerForUser creates a credential manager scoped to a specific
// user. Credentials live at ~/.fastclaw/users/{userID}/credentials.json and
// are encrypted with a key derived from the user ID, so one user's file
// cannot be decrypted with another user's key even if moved on disk.
func NewCredentialManagerForUser(userID, passphrase string) (*CredentialManager, error) {
	storeDir, err := config.EnsureUserDir(userID)
	if err != nil {
		return nil, fmt.Errorf("ensure user dir: %w", err)
	}

	key := deriveKeyForUser(userID, passphrase)

	cm := &CredentialManager{
		masterKey: key,
		entries:   make(map[string]*CredentialEntry),
		storePath: filepath.Join(storeDir, "credentials.json"),
	}

	// Load existing credentials
	if err := cm.load(); err != nil && !os.IsNotExist(err) {
		// If decryption fails, start fresh
		cm.entries = make(map[string]*CredentialEntry)
	}

	// If we decrypted with the legacy key, immediately re-save with the
	// new per-user key so the file is migrated on disk.
	if cm.needsReencrypt {
		if err := cm.save(); err == nil {
			cm.needsReencrypt = false
		}
	}

	return cm, nil
}

// Set stores a credential key-value pair.
func (cm *CredentialManager) Set(name, key, value string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	entry, ok := cm.entries[name]
	if !ok {
		entry = &CredentialEntry{
			Name:   name,
			Type:   "api_key",
			Source: "store",
			Keys:   make(map[string]string),
		}
		cm.entries[name] = entry
	}

	entry.Keys[key] = value
	return cm.save()
}

// Get retrieves a credential value.
func (cm *CredentialManager) Get(name, key string) (string, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	entry, ok := cm.entries[name]
	if !ok {
		return "", fmt.Errorf("credential %q not found", name)
	}

	val, ok := entry.Keys[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in credential %q", key, name)
	}

	return val, nil
}

// List returns all credential entries.
func (cm *CredentialManager) List() []CredentialEntry {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	result := make([]CredentialEntry, 0, len(cm.entries))
	for _, e := range cm.entries {
		// Copy without exposing key values
		masked := CredentialEntry{
			Name:   e.Name,
			Type:   e.Type,
			Source: e.Source,
			Keys:   make(map[string]string),
		}
		for k, v := range e.Keys {
			if len(v) > 8 {
				masked.Keys[k] = v[:4] + "..." + v[len(v)-4:]
			} else {
				masked.Keys[k] = "****"
			}
		}
		result = append(result, masked)
	}
	return result
}

// Delete removes a credential entry.
func (cm *CredentialManager) Delete(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.entries[name]; !ok {
		return fmt.Errorf("credential %q not found", name)
	}

	delete(cm.entries, name)
	return cm.save()
}

// knownEnvVars maps provider names to their environment variable patterns.
var knownEnvVars = map[string][]string{
	"openai":     {"OPENAI_API_KEY"},
	"anthropic":  {"ANTHROPIC_API_KEY"},
	"openrouter": {"OPENROUTER_API_KEY"},
	"google":     {"GOOGLE_API_KEY", "GEMINI_API_KEY"},
	"mistral":    {"MISTRAL_API_KEY"},
	"cohere":     {"COHERE_API_KEY"},
	"groq":       {"GROQ_API_KEY"},
	"together":   {"TOGETHER_API_KEY"},
	"deepseek":   {"DEEPSEEK_API_KEY"},
}

// Discover scans environment variables for known API key patterns.
func (cm *CredentialManager) Discover() []CredentialEntry {
	var discovered []CredentialEntry

	for providerName, envVars := range knownEnvVars {
		for _, envVar := range envVars {
			val := os.Getenv(envVar)
			if val == "" {
				continue
			}
			entry := CredentialEntry{
				Name:   providerName,
				Type:   "api_key",
				Source: "env",
				Keys:   map[string]string{"apiKey": val},
			}
			discovered = append(discovered, entry)
		}
	}

	return discovered
}

// InjectEnv returns environment variables suitable for injecting into a sandbox.
func (cm *CredentialManager) InjectEnv() map[string]string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	env := make(map[string]string)

	for name, entry := range cm.entries {
		if apiKey, ok := entry.Keys["apiKey"]; ok {
			// Map back to env var name
			envVars, known := knownEnvVars[name]
			if known && len(envVars) > 0 {
				env[envVars[0]] = apiKey
			} else {
				env[strings.ToUpper(name)+"_API_KEY"] = apiKey
			}
		}
	}

	// Also include any env-discovered credentials
	for _, envVars := range knownEnvVars {
		for _, envVar := range envVars {
			if val := os.Getenv(envVar); val != "" {
				env[envVar] = val
			}
		}
	}

	return env
}

func (cm *CredentialManager) save() error {
	data, err := json.Marshal(cm.entries)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	encrypted, err := encrypt(data, cm.masterKey)
	if err != nil {
		return fmt.Errorf("encrypt credentials: %w", err)
	}

	if err := os.WriteFile(cm.storePath, encrypted, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	return nil
}

func (cm *CredentialManager) load() error {
	data, err := os.ReadFile(cm.storePath)
	if err != nil {
		return err
	}

	decrypted, err := decrypt(data, cm.masterKey)
	if err != nil {
		// Fallback: try legacy key format (pre-multiuser) so existing
		// installs don't lose their stored credentials after upgrading.
		legacyKey := legacyDeriveKey()
		decrypted, err = decrypt(data, legacyKey)
		if err != nil {
			return fmt.Errorf("decrypt credentials: %w", err)
		}
		cm.needsReencrypt = true
	}

	return json.Unmarshal(decrypted, &cm.entries)
}

// legacyDeriveKey returns the old (pre-multiuser) machine-derived key so
// we can decrypt credentials files created before per-user KEK was added.
func legacyDeriveKey() []byte {
	hostname, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	hash := sha256.Sum256([]byte("fastclaw:" + hostname + ":" + home))
	return hash[:]
}

func deriveKey(passphrase string) []byte {
	return deriveKeyForUser(config.DefaultUserID, passphrase)
}

// deriveKeyForUser mixes the user ID into the encryption key so that each
// user's credentials file is encrypted with a distinct KEK. In the absence
// of an explicit passphrase, a machine-derived seed (hostname + home) is
// still included so the same user ID yields different keys on different
// hosts — preventing wholesale copy of the credentials file across hosts
// from decrypting.
func deriveKeyForUser(userID, passphrase string) []byte {
	if userID == "" {
		userID = config.DefaultUserID
	}
	var seed string
	if passphrase != "" {
		seed = "fastclaw:user:" + userID + ":pp:" + passphrase
	} else {
		hostname, _ := os.Hostname()
		home, _ := os.UserHomeDir()
		seed = "fastclaw:user:" + userID + ":host:" + hostname + ":" + home
	}
	hash := sha256.Sum256([]byte(seed))
	return hash[:]
}

func encrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(ciphertext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
