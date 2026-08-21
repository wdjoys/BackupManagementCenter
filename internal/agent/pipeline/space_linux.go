// Package pipeline implements space checking for temp directory.
//go:build linux
// +build linux

package pipeline

import (
	"syscall"
)

// checkTempSpace checks available space in tempDir using statfs.
// Returns error if available < requiredBytes.
func checkTempSpace(tempDir string, requiredBytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(tempDir, &stat); err != nil {
		// If statfs fails, we skip the check rather than fail
		return nil
	}
	// Available bytes = free blocks * block size
	avail := int64(stat.Bavail) * int64(stat.Bsize)
	if avail < requiredBytes {
		return &PipelineError{Code: "insufficient_temp_space", Message: "insufficient temp space for dump"}
	}
	return nil
}