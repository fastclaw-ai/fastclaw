package usermodels

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

// Mirrors settings-secret encryption: the optional deployment key must be
// stable across replicas and backups. Plain legacy values pass through.
func secretCipher() (cipher.AEAD, error) {
	secret := os.Getenv("CONFIG_ENCRYPTION_KEY")
	if secret == "" {
		return nil, nil
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func sealSecret(value, owner string) (string, error) {
	c, err := secretCipher()
	if err != nil {
		return "", err
	}
	if c == nil {
		return value, nil
	}
	nonce := make([]byte, c.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return "byok:v1:" + base64.StdEncoding.EncodeToString(c.Seal(nonce, nonce, []byte(value), []byte(owner))), nil
}
func openSecret(value, owner string) (string, error) {
	if !strings.HasPrefix(value, "byok:v1:") {
		return value, nil
	}
	c, err := secretCipher()
	if err != nil {
		return "", err
	}
	if c == nil {
		return "", errors.New("BYOK encryption key is unavailable")
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "byok:v1:"))
	if err != nil || len(b) < c.NonceSize() {
		return "", errors.New("invalid encrypted key")
	}
	plain, err := c.Open(nil, b[:c.NonceSize()], b[c.NonceSize():], []byte(owner))
	if err != nil {
		return "", errors.New("could not decrypt BYOK key")
	}
	return string(plain), nil
}
