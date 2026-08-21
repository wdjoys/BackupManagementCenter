// Package backup implements plan-kind adapters.
package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// FilesystemAdapter implements Adapter for filesystem plans.
type FilesystemAdapter struct{}

// Validate checks that paths are non-empty, absolute, and readable.
// Excludes are optional.
func (a *FilesystemAdapter) Validate(ctx context.Context, spec PlanSpec) error {
	if spec.Kind != KindFilesystem {
		return fmt.Errorf("invalid kind: %s", spec.Kind)
	}
	paths := spec.Source.Paths
	if len(paths) == 0 {
		return errors.New("paths must not be empty")
	}
	for _, p := range paths {
		if !isAbs(p) {
			return fmt.Errorf("path %q must be absolute", p)
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("path %q not accessible: %w", p, err)
		}
	}
	return nil
}

// Backup returns a BackupArtifact pointing to the live paths.
// Does not execute any command; restic will be invoked by the pipeline.
func (a *FilesystemAdapter) Backup(ctx context.Context, rc *RunContext) (*BackupArtifact, error) {
	var excludeFile string
	if len(rc.Task.Source.Excludes) > 0 {
		content := strings.Join(rc.Task.Source.Excludes, "\n") + "\n"
		var err error
		excludeFile, err = writeSecretFile(rc.TempDir, "excludes.txt", content)
		if err != nil {
			return nil, fmt.Errorf("write exclude file: %w", err)
		}
	}
	return &BackupArtifact{
		LivePaths:     rc.Task.Source.Paths,
		ExcludeFile:   excludeFile,
		OneFileSystem: rc.Task.Source.OneFileSystem,
	}, nil
}

// Restore is not used for filesystem; pipeline handles restic restore directly.
func (a *FilesystemAdapter) Restore(ctx context.Context, spec *RestoreSpec) error {
	return errors.New("filesystem restore is handled by pipeline via restic; adapter.Restore not used")
}

// isAbs checks if a path is absolute for Linux targets.
func isAbs(p string) bool {
	return strings.HasPrefix(p, "/")
}