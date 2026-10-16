package secrets

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
)

// NoopSealer is a development-mode sealer that only base64-encodes. It keeps
// the wire format non-obvious but provides NO confidentiality. Production
// deployments must configure BMC_MASTER_KEY_FILE.
type NoopSealer struct{ mu sync.Mutex }

func NewNoopSealer() *NoopSealer { return &NoopSealer{} }

func (n *NoopSealer) Seal(table, rowID, column, plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	payload := []byte("noop:" + plaintext)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(payload)))
	base64.StdEncoding.Encode(out, payload)
	return out, nil
}

func (n *NoopSealer) Open(table, rowID, column string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", fmt.Errorf("secrets: noop decode: %w", err)
	}
	const prefix = "noop:"
	if len(raw) < len(prefix) || string(raw[:len(prefix)]) != prefix {
		return "", fmt.Errorf("secrets: noop: invalid payload")
	}
	return string(raw[len(prefix):]), nil
}

// Fingerprint 在无密钥的开发实现里只能退化为裸哈希（与 Seal 一样不提供任何机密性保证）。
// 生产部署必须使用 AESGCMSealer，其指纹是以 master key 为密钥的 HMAC。
func (n *NoopSealer) Fingerprint(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + value))
	return hex.EncodeToString(sum[:])
}
