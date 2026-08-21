// Package pipeline implements space checking for temp directory.
//go:build !linux
// +build !linux

package pipeline

// checkTempSpace on non-Linux: skip check.
func checkTempSpace(tempDir string, requiredBytes int64) error {
	return nil
}