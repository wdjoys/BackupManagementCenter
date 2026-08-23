package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/pipeline"
	"backupmanagementcenter/internal/agent/restic"
	"backupmanagementcenter/internal/model"
)

// Runner executes commands received from the server.
type Runner struct {
	deps      pipeline.Deps
	dataDir   string
	identity  *Identity
	executeFn func(ctx context.Context, d pipeline.Deps, tempDir string, op bmcv1.ExecuteCommand_Operation, params []byte, secrets backup.SecretBundle) (*pipeline.Result, error)

	// In-flight runs: run_id -> cancel func
	mu       sync.Mutex
	running  map[string]context.CancelFunc
	finished *lruCache // run_id -> RunResult (cached for idempotency)
	repoMu   sync.Mutex
	repoLocks map[string]*sync.Mutex
	slots chan struct{}

	prober *Prober // optional; refreshed tool paths before each execution
}

// NewRunner creates a new runner.
func NewRunner(deps pipeline.Deps, dataDir string, identity *Identity) *Runner {
	if deps.Tools == nil {
		deps.Tools = make(map[string]model.ToolInfo)
	}
	r := &Runner{
		deps:      deps,
		dataDir:   dataDir,
		identity:  identity,
		running:   make(map[string]context.CancelFunc),
		finished:  newLRUCache(512),
		repoLocks: make(map[string]*sync.Mutex),
		executeFn: pipeline.Execute,
	}
	if deps.MaxConcurrency > 0 { r.slots = make(chan struct{}, deps.MaxConcurrency) }
	return r
}

// SetProber wires the capability prober so tool paths stay fresh.
func (r *Runner) SetProber(p *Prober) { r.prober = p }

// Execute handles an ExecuteCommand from the server.
func (r *Runner) Execute(ctx context.Context, stream bmcv1.AgentControl_ConnectClient, cmd *bmcv1.ExecuteCommand) {
	runID := cmd.RunId

	// Check idempotency cache first
	if cached := r.finished.get(runID); cached != nil {
		log.Printf("[INFO] run %s already finished, replaying result", runID)
		r.sendRunResult(stream, cached)
		return
	}

	// Check if already running (duplicate command_id)
	r.mu.Lock()
	if _, exists := r.running[runID]; exists {
		r.mu.Unlock()
		log.Printf("[WARN] run %s already in progress, ignoring duplicate", runID)
		return
	}
	r.mu.Unlock()

	// Send CommandAccepted immediately
	accepted := &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_CommandAccepted{
			CommandAccepted: &bmcv1.CommandAccepted{
				CommandId: cmd.CommandId,
				RunId:     cmd.RunId,
			},
		},
	}
	if err := stream.Send(accepted); err != nil {
		log.Printf("[ERROR] send CommandAccepted: %v", err)
		return
	}

	// Create private temp directory
	if err := os.MkdirAll(r.dataDir, 0o700); err != nil {
		log.Printf("[ERROR] create data dir: %v", err)
		r.sendErrorResult(stream, runID, bmcv1.RunResult_FAILED, "temp_dir_failed", err.Error())
		return
	}
	tempDir, err := os.MkdirTemp(r.dataDir, "bmc-run-*")
	if err != nil {
		log.Printf("[ERROR] create temp dir: %v", err)
		r.sendErrorResult(stream, runID, bmcv1.RunResult_FAILED, "temp_dir_failed", err.Error())
		return
	}

	// Create cancellable context for this run
	runCtx, cancel := context.WithCancel(ctx)

	// Register as running
	r.mu.Lock()
	r.running[runID] = cancel
	r.mu.Unlock()

	// Execute in goroutine
	go func() {
		defer func() {
			// Cleanup: unregister and remove temp dir
			r.mu.Lock()
			delete(r.running, runID)
			r.mu.Unlock()
			_ = os.RemoveAll(tempDir)
		}()

		// Clone deps with per-run log/progress sinks that stream upstream.
		deps := r.deps
		var logSeq atomic.Uint64
		deps.Logf = func(level, format string, args ...any) {
			msg := fmt.Sprintf(format, args...)
			log.Printf("[run %s] [%s] %s", runID, level, msg)
			seq := logSeq.Add(1)
			batch := &bmcv1.AgentMessage{
				MessageId: newMessageID(),
				Payload: &bmcv1.AgentMessage_RunLogBatch{
					RunLogBatch: &bmcv1.RunLogBatch{
						RunId: runID,
						Entries: []*bmcv1.LogEntry{{
							Seq:                uint64(seq),
							TimestampUnixNanos: time.Now().UnixNano(),
							Level:              protoLevel(level),
							Message:            msg,
						}},
					},
				},
			}
			if err := stream.Send(batch); err != nil {
				log.Printf("[WARN] send log batch run %s: %v", runID, err)
			}
		}
		deps.Progress = func(p model.Progress) {
			msg := &bmcv1.AgentMessage{
				MessageId: newMessageID(),
				Payload: &bmcv1.AgentMessage_RunProgress{
					RunProgress: &bmcv1.RunProgress{
						RunId:      runID,
						Phase:      p.Phase,
						Percent:    p.Percent,
						BytesDone:  p.BytesDone,
						BytesTotal: p.BytesTotal,
						FilesDone:  p.FilesDone,
						FilesTotal: p.FilesTotal,
					},
				},
			}
			if err := stream.Send(msg); err != nil {
				log.Printf("[WARN] send progress run %s: %v", runID, err)
			}
		}

		if r.prober != nil {
			for k, v := range r.prober.GetCached() {
				deps.Tools[k] = v
			}
		}
		// Extract secrets from SecretSet
		secrets := r.extractSecrets(cmd.Secrets)
		if repoKey := commandRepositoryKey(cmd.ParamsJson); repoKey != "" {
			repoLock := r.repositoryLock(repoKey)
			repoLock.Lock()
			defer repoLock.Unlock()
		}
		// Execute pipeline, respecting the global concurrency cap without
		// leaving cancelled commands blocked behind a full semaphore.
		var result *pipeline.Result
		var err error
		if r.slots != nil {
			select {
			case r.slots <- struct{}{}:
				defer func() { <-r.slots }()
			case <-runCtx.Done():
				err = runCtx.Err()
			}
		}
		if err == nil {
			result, err = r.executeFn(runCtx, deps, tempDir, cmd.Operation, cmd.ParamsJson, secrets)
		}

		var runResult *bmcv1.RunResult
		if err != nil {
			log.Printf("[ERROR] pipeline execute run %s: %v", runID, err)
			if runCtx.Err() != nil {
				// Context was cancelled — treat as CANCELLED
				runResult = &bmcv1.RunResult{
					RunId:        runID,
					Status:       bmcv1.RunResult_CANCELLED,
					ErrorCode:    "cancelled",
					ErrorMessage: "run cancelled by server",
				}
			} else {
				code, msg := "pipeline_error", err.Error()
				var pe *pipeline.PipelineError
				if errors.As(err, &pe) {
					if pe.Code != "" {
						code = pe.Code
					}
					// Prefer the stable restic-mapped code (e.g.
					// repository_missing) over the generic op failure code.
					var re *restic.ResticError
					if errors.As(err, &re) && re.Code != "" {
						code = re.Code
					}
					if pe.Cause != nil {
						msg = pe.Cause.Error()
					}
				}
				runResult = &bmcv1.RunResult{
					RunId:        runID,
					Status:       bmcv1.RunResult_FAILED,
					ErrorCode:    code,
					ErrorMessage: msg,
				}
			}
		} else {
			runResult = &bmcv1.RunResult{
				RunId:       runID,
				Status:      bmcv1.RunResult_SUCCEEDED,
				SnapshotIds: result.SnapshotIDs,
				ResultJson:  string(result.ResultJSON),
			}
		}

		// Cache for idempotency
		r.finished.put(runID, runResult)

		// Send RunResult
		r.sendRunResult(stream, runResult)
	}()
}

func (r *Runner) repositoryLock(key string) *sync.Mutex {
	r.repoMu.Lock()
	defer r.repoMu.Unlock()
	if lock := r.repoLocks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	r.repoLocks[key] = lock
	return lock
}

func commandRepositoryKey(params []byte) string {
	var envelope struct {
		Repository struct {
			RepositoryPath string `json:"repository_path"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(params, &envelope); err == nil && envelope.Repository.RepositoryPath != "" {
		return envelope.Repository.RepositoryPath
	}
	return ""
}

// Cancel cancels a running command by run_id.
func (r *Runner) Cancel(runID string) {
	r.mu.Lock()
	cancel, exists := r.running[runID]
	r.mu.Unlock()
	if exists {
		log.Printf("[INFO] cancelling run %s", runID)
		cancel()
	} else {
		// If already finished, check if we have cached result
		if cached := r.finished.get(runID); cached != nil {
			// Already finished, nothing to cancel
			return
		}
		log.Printf("[WARN] cancel requested for unknown run %s", runID)
	}
}

// sendRunResult sends a RunResult to the server.
func (r *Runner) sendRunResult(stream bmcv1.AgentControl_ConnectClient, result *bmcv1.RunResult) {
	msg := &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_RunResult{
			RunResult: result,
		},
	}
	if err := stream.Send(msg); err != nil {
		log.Printf("[ERROR] send RunResult: %v", err)
	}
}

// sendErrorResult sends a failed RunResult.
func (r *Runner) sendErrorResult(stream bmcv1.AgentControl_ConnectClient, runID string, status bmcv1.RunResult_Status, code, msg string) {
	result := &bmcv1.RunResult{
		RunId:        runID,
		Status:       status,
		ErrorCode:    code,
		ErrorMessage: msg,
	}
	r.sendRunResult(stream, result)
}

// extractSecrets populates SecretBundle from the proto SecretSet.
func (r *Runner) extractSecrets(secrets *bmcv1.SecretSet) backup.SecretBundle {
	bundle := backup.SecretBundle{}
	if secrets == nil {
		return bundle
	}
	bundle.RcloneConf = secrets.RcloneConf
	bundle.ResticPassword = secrets.ResticPassword
	bundle.DBPassword = secrets.DbPassword
	return bundle
}

// lruCache is a simple LRU cache for run results.
type lruCache struct {
	mu    sync.Mutex
	cap   int
	items map[string]*bmcv1.RunResult
	order []string
}

func newLRUCache(cap int) *lruCache {
	return &lruCache{
		cap:   cap,
		items: make(map[string]*bmcv1.RunResult),
		order: make([]string, 0, cap),
	}
}

func (c *lruCache) get(key string) *bmcv1.RunResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if val, ok := c.items[key]; ok {
		// Move to front (most recent)
		c.moveToFront(key)
		return val
	}
	return nil
}

func (c *lruCache) put(key string, val *bmcv1.RunResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.items[key]; exists {
		c.items[key] = val
		c.moveToFront(key)
		return
	}

	if len(c.items) >= c.cap {
		// Evict least recently used
		lru := c.order[len(c.order)-1]
		delete(c.items, lru)
		c.order = c.order[:len(c.order)-1]
	}

	c.items[key] = val
	c.order = append([]string{key}, c.order...)
}

func (c *lruCache) moveToFront(key string) {
	for i, k := range c.order {
		if k == key {
			if i > 0 {
				copy(c.order[1:i+1], c.order[0:i])
				c.order[0] = key
			}
			break
		}
	}
}

// protoLevel maps log level strings to proto enum values.
func protoLevel(level string) bmcv1.LogLevel_Level {
	switch level {
	case "debug":
		return bmcv1.LogLevel_DEBUG
	case "warn":
		return bmcv1.LogLevel_WARN
	case "error":
		return bmcv1.LogLevel_ERROR
	default:
		return bmcv1.LogLevel_INFO
	}
}
