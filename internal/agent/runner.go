package agent

import (
	"context"

	"log"
	"os"
	"sync"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/agent/backup"
	"backupmanagementcenter/internal/agent/pipeline"
	"backupmanagementcenter/internal/model"
)

// Runner executes commands received from the server.
type Runner struct {
	deps       pipeline.Deps
	dataDir    string
	identity   *Identity
	executeFn  func(ctx context.Context, d pipeline.Deps, tempDir string, op bmcv1.ExecuteCommand_Operation, params []byte, secrets backup.SecretBundle) (*pipeline.Result, error)

	// In-flight runs: run_id -> cancel func
	mu       sync.Mutex
	running  map[string]context.CancelFunc
	finished *lruCache // run_id -> RunResult (cached for idempotency)
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
		executeFn: pipeline.Execute,
	}
	return r
}

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

		// Extract secrets from SecretSet
		secrets := r.extractSecrets(cmd.Secrets)

		// Execute pipeline
		result, err := r.executeFn(runCtx, r.deps, tempDir, cmd.Operation, cmd.ParamsJson, secrets)

		var runResult *bmcv1.RunResult
		if err != nil {
			log.Printf("[ERROR] pipeline execute run %s: %v", runID, err)
			if ctx.Err() != nil {
				// Context was cancelled — treat as CANCELLED
				runResult = &bmcv1.RunResult{
					RunId:       runID,
					Status:      bmcv1.RunResult_CANCELLED,
					ErrorCode:   "cancelled",
					ErrorMessage: "run cancelled by server",
				}
			} else {
				runResult = &bmcv1.RunResult{
					RunId:        runID,
					Status:       bmcv1.RunResult_FAILED,
					ErrorCode:    "pipeline_error",
					ErrorMessage: err.Error(),
				}
			}
		} else {
			runResult = &bmcv1.RunResult{
				RunId:        runID,
				Status:       bmcv1.RunResult_SUCCEEDED,
				SnapshotIds:  result.SnapshotIDs,
				ResultJson:   string(result.ResultJSON),
			}
		}

		// Cache for idempotency
		r.finished.put(runID, runResult)

		// Send RunResult
		r.sendRunResult(stream, runResult)
	}()
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
	mu      sync.Mutex
	cap     int
	items   map[string]*bmcv1.RunResult
	order   []string
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