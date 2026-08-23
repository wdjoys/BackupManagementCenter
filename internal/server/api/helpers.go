package api

import (
	"strings"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/jobs"
)

// stableCodes is the list of error codes that may appear inside orchestrator
// error strings; the first match becomes the HTTP error code.
var stableCodes = []string{
	model.ErrServerRestarted,
	model.ErrAgentUnavailable,
	model.ErrPartialSourceRead,
	model.ErrRepositoryLocked,
	model.ErrWrongRepositoryPassword,
	model.ErrCancelled,
	model.ErrInsufficientTempSpace,
	model.ErrMissingTools,
	model.ErrInvalidPlan,
	model.ErrPathValidation,
	model.ErrRestoreTargetNotEmpty,
	model.ErrRestoreVerification,
	model.ErrStorageRemoteUnreachable,
	model.ErrDatabaseRestoreDisabled,
	model.ErrTimeout,
	model.ErrAgentDisconnected,
	model.ErrPreRestoreBackupFailed,
	model.ErrRollbackFailed,
	model.ErrPhysicalBackupRequired,
}

func errorCode(err error) (string, bool) {
	msg := err.Error()
	for _, c := range stableCodes {
		if strings.Contains(msg, c) {
			return c, true
		}
	}
	return "", false
}

func redactMsg(s string) string { return jobs.Redact(s) }
