// Package secretbox encrypts small secrets (provider API keys) at rest with
// AES-256-GCM. The data key either comes from a 0600 file beside the
// database (Open; tests and servers without a passphrase), or is unwrapped
// with the owner's passphrase (package auth) and held only in memory: until
// then the box is locked and every Seal or Unseal fails with ErrLocked.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrLocked: the passphrase has not been entered since the server started.
var ErrLocked = errors.New("secrets are locked: sign in with the passphrase first")

type Box struct {
	mu   sync.RWMutex
	aead cipher.AEAD
}

// Locked returns a box without a key; Unlock gives it one.
func Locked() *Box { return &Box{} }

// Unlock sets the data key (32 bytes).
func (b *Box) Unlock(key []byte) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.aead = aead
	b.mu.Unlock()
	return nil
}

// IsLocked reports whether the box has no key yet.
func (b *Box) IsLocked() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.aead == nil
}

func (b *Box) cipher() (cipher.AEAD, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.aead == nil {
		return nil, ErrLocked
	}
	return b.aead, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key: want 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open loads the key at path, creating a new random key if the file is missing.
func Open(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create secret key: %w", err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	b := Locked()
	if err := b.Unlock(key); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// Seal encrypts plain and returns base64(nonce || ciphertext).
func (b *Box) Seal(plain string) (string, error) {
	aead, err := b.cipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Unseal reverses Seal.
func (b *Box) Unseal(sealed string) (string, error) {
	aead, err := b.cipher()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	n := aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("sealed value too short")
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("cannot decrypt secret (wrong key file?)")
	}
	return string(plain), nil
}
