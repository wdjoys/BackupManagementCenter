package backup

import (
	"os"
	"path/filepath"
)

// writeSecretFile creates <temp>/<name> with 0600 permissions.
func writeSecretFile(tempDir, name, content string) (string, error) {
	p := filepath.Join(tempDir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return "", err
	}
	return p, f.Close()
}
