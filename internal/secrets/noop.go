package secrets

import (
	"encoding/base64"
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
	out := make([]byte, base64.StdEncoding.EncodedLen(len(plaintext)))
	base64.StdEncoding.Encode(out, []byte("noop:"+plaintext))
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
