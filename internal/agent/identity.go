package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Identity stores the agent's persistent identity.
type Identity struct {
	AgentID  string `json:"agent_id"`
	SecretHex string `json:"secret_hex"`
}

// IdentityManager manages the agent identity file.
type IdentityManager struct {
	stateDir string
	mu       sync.Mutex
	cached   *Identity
}

// NewIdentityManager creates a new identity manager.
func NewIdentityManager(stateDir string) *IdentityManager {
	return &IdentityManager{stateDir: stateDir}
}

// LoadOrCreate loads existing identity or creates a new one if enrollToken is provided.
// Returns the identity and a boolean indicating if it was newly created.
func (m *IdentityManager) LoadOrCreate(enrollToken string) (*Identity, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Try to load existing identity
	ident, err := m.load()
	if err == nil {
		m.cached = ident
		// Agent ID empty => enrollment never completed; redo it.
		return ident, ident.AgentID == "", nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}

	// No identity exists - need enroll token
	if enrollToken == "" {
		return nil, false, errors.New("no identity found and BMC_ENROLLMENT_TOKEN not provided")
	}

	// Generate new secret
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, false, err
	}

	// Create new identity (agent_id will be set after enroll)
	ident = &Identity{
		SecretHex: hex.EncodeToString(secret),
	}
	m.cached = ident
	if err := m.save(ident); err != nil {
		return nil, false, err
	}
	return ident, true, nil
}

// SetAgentID updates the identity with the assigned agent ID from server.
func (m *IdentityManager) SetAgentID(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	ident := m.cached
	if ident == nil {
		var err error
		ident, err = m.load()
		if err != nil {
			return err
		}
	}
	ident.AgentID = agentID
	m.cached = ident
	return m.save(ident)
}

// Get returns the cached identity or loads it. The identity must be complete
// (agent id assigned via enrollment).
func (m *IdentityManager) Get() (*Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil {
		if err := m.cached.validate(); err != nil {
			return nil, err
		}
		return m.cached, nil
	}
	ident, err := m.load()
	if err != nil {
		return nil, err
	}
	m.cached = ident
	if err := ident.validate(); err != nil {
		return nil, err
	}
	return ident, nil
}

// load reads identity.json from stateDir.
func (m *IdentityManager) load() (*Identity, error) {
	path := filepath.Join(m.stateDir, "identity.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ident Identity
	if err := json.Unmarshal(data, &ident); err != nil {
		return nil, err
	}
	return &ident, nil
}

func (i *Identity) validate() error {
	if i.AgentID == "" || i.SecretHex == "" {
		return errors.New("identity.json missing required fields")
	}
	return nil
}

// save writes identity.json to stateDir with 0600 permissions.
func (m *IdentityManager) save(ident *Identity) error {
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(m.stateDir, "identity.json")
	data, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return err
	}
	// Write with 0600 permissions
	return os.WriteFile(path, data, 0o600)
}

// SecretBytes returns the secret as raw bytes.
func (i *Identity) SecretBytes() ([]byte, error) {
	return hex.DecodeString(i.SecretHex)
}