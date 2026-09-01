// Package secrets implements AES-256-GCM sealing of sensitive columns.
//
// Every record is sealed with a random nonce; the additional authenticated
// data is fixed to "<table>:<row-id>:<column>" so ciphertexts cannot be moved
// between rows or columns. The 32-byte master key is loaded from
// BMC_MASTER_KEY_FILE exactly once at startup.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const KeyLen = 32

var ErrTooShort = errors.New("secrets: ciphertext too short")

// Sealer is implemented by AESGCMSealer (production) and NoopSealer (dev).
type Sealer interface {
	Seal(table, rowID, column, plaintext string) ([]byte, error)
	Open(table, rowID, column string, data []byte) (string, error)
}

// AESGCMSealer seals with AES-256-GCM and AAD "<table>:<row-id>:<column>".
type AESGCMSealer struct {
	aead cipher.AEAD
}

// LoadKey reads exactly 32 bytes from path.
func LoadKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read master key: %w", err)
	}
	key := make([]byte, KeyLen)
	n := copy(key, b)
	if n != KeyLen {
		return nil, fmt.Errorf("secrets: master key must be %d bytes, got %d", KeyLen, n)
	}
	return key, nil
}

// LoadOrCreateKey loads an existing 32-byte key or atomically creates one.
func LoadOrCreateKey(path string) ([]byte, bool, error) {
	key, err := LoadKey(path)
	if err == nil {
		return key, false, nil
	}
	if !os.IsNotExist(errors.Unwrap(err)) {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, fmt.Errorf("secrets: create master key directory: %w", err)
	}
	key = make([]byte, KeyLen)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, false, fmt.Errorf("secrets: generate master key: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if err := f.Chmod(0o600); err != nil {
			_ = f.Close()
			return nil, false, fmt.Errorf("secrets: set master key permissions: %w", err)
		}
		_, writeErr := f.Write(key)
		closeErr := f.Close()
		if writeErr != nil {
			return nil, false, fmt.Errorf("secrets: write master key: %w", writeErr)
		}
		if closeErr != nil {
			return nil, false, fmt.Errorf("secrets: close master key: %w", closeErr)
		}
		return key, true, nil
	}
	if !os.IsExist(err) {
		return nil, false, fmt.Errorf("secrets: create master key: %w", err)
	}
	key, err = LoadKey(path)
	if err != nil {
		return nil, false, err
	}
	return key, false, nil
}

func NewSealer(key []byte) (Sealer, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("secrets: key must be %d bytes", KeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMSealer{aead: aead}, nil
}

func aad(table, rowID, column string) []byte {
	return []byte(strings.Join([]string{table, rowID, column}, ":"))
}

// Seal encrypts plaintext; output = nonce || ciphertext, base64 std encoding.
func (s *AESGCMSealer) Seal(table, rowID, column, plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(plaintext), aad(table, rowID, column))
	out := make([]byte, base64.StdEncoding.EncodedLen(len(ct)))
	base64.StdEncoding.Encode(out, ct)
	return out, nil
}

// Open decrypts data produced by Seal with identical table/rowID/column.
func (s *AESGCMSealer) Open(table, rowID, column string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", fmt.Errorf("secrets: decode: %w", err)
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns+1 {
		return "", ErrTooShort
	}
	pt, err := s.aead.Open(nil, raw[:ns], raw[ns:], aad(table, rowID, column))
	if err != nil {
		return "", fmt.Errorf("secrets: open: %w", err)
	}
	return string(pt), nil
}

// HashToken returns lowercase hex SHA-256; used for session tokens, agent
// secrets and enrollment tokens (only hashes are persisted).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
