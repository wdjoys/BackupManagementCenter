package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	mu        sync.Mutex
	running   map[string]context.CancelFunc
	finished  *lruCache // run_id -> RunResult (cached for idempotency)
	repoMu    sync.Mutex
	repoLocks map[string]*sync.Mutex
	slots     chan struct{}

	prober *Prober // optional; refreshed tool paths before each execution

	// stream 是当前活跃的上行流。run 的生命周期可能跨越多次重连（服务端重启/网络
	// 抖动），日志、进度与结果必须发到**当前**连接：闭包捕获派发时的流会让重连后
	// 的发送全部 EOF（实测进度与日志丢失，run 只能等下一次重派 + 结果重放才收敛）。
	streamMu sync.Mutex
	stream   bmcv1.AgentControl_ConnectClient
}

// SweepStaleRunDirs 回收上一次进程遗留的 run 临时目录（bmc-run-*）。
//
// runner 用 defer os.RemoveAll(tempDir) 清理自己的临时目录，但进程被 SIGTERM/
// SIGKILL 终止时 defer 不会执行：备份/恢复被中断后，半个 dump 会永久留在 DataDir
// 下（实测每次中断泄漏 160MB–1.8GB，反复重启可写满容器磁盘）。启动时没有任何 run
// 在跑，这些目录必定属于已死进程，可安全回收。返回清理的目录数。
func SweepStaleRunDirs(dataDir string, restoreRoots []string) int {
	removed := 0
	if entries, err := os.ReadDir(dataDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "bmc-run-") {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dataDir, e.Name())); err == nil {
				removed++
			}
		}
	}
	// 恢复过程中被硬杀（SIGKILL）时 Import 的 defer 不会执行，恢复根目录会残留
	// 半成品临时文件 .bmc-restore-*（实测一次 973MB）。启动时没有恢复在跑，这些
	// 文件必定属于已死进程，可安全回收。
	for _, root := range restoreRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), ".bmc-restore-") {
				continue
			}
			if err := os.Remove(filepath.Join(root, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed
}

// NewRunner creates a new runner.
// SetStream 记录当前活跃的上行流（客户端每次成功连接后调用）。
func (r *Runner) SetStream(stream bmcv1.AgentControl_ConnectClient) {
	r.streamMu.Lock()
	r.stream = stream
	r.streamMu.Unlock()
}

// currentStream 返回当前活跃的上行流。
func (r *Runner) currentStream() bmcv1.AgentControl_ConnectClient {
	r.streamMu.Lock()
	defer r.streamMu.Unlock()
	return r.stream
}

func NewRunner(deps pipeline.Deps, dataDir string, identity *Identity) *Runner {
	if deps.Tools == nil {
		deps.Tools = make(map[string]model.ToolInfo)
	}
	if deps.Exec == nil {
		deps.Exec = OSExecutor{}
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
	if deps.MaxConcurrency > 0 {
		r.slots = make(chan struct{}, deps.MaxConcurrency)
	}
	return r
}

// SetProber wires the capability prober so tool paths stay fresh.
func (r *Runner) SetProber(p *Prober) { r.prober = p }

// Execute handles an ExecuteCommand from the server.
func (r *Runner) Execute(ctx context.Context, stream bmcv1.AgentControl_ConnectClient, cmd *bmcv1.ExecuteCommand) {
	if r.currentStream() == nil {
		r.SetStream(stream)
	}
	runID := cmd.RunId

	// Check idempotency cache first
	if cached := r.finished.get(runID); cached != nil {
		log.Printf("[INFO] run %s already finished, replaying result", runID)
		r.sendRunResult(cached)
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
		allMappings := append(append([]model.PathMapping(nil), deps.SourcePathMappings...), deps.RestorePathMappings...)
		var logSeq atomic.Uint64

		deps.Logf = func(level, format string, args ...any) {
			msg := fmt.Sprintf(format, args...)
			msg = pipeline.HostDisplayText(msg, allMappings)
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
							Source:             model.RunLogSourceAgent,
						}},
					},
				},
			}
			if err := r.currentStream().Send(batch); err != nil {
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
						DetailJson: p.DetailJSON,
					},
				},
			}
			if err := r.currentStream().Send(msg); err != nil {
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
			// 安全恢复结果（保护快照 ID / phase）必须在映射取消状态之前提取：
			// context 被取消不能吞掉 rollback/new_target_cleaned 等结论。
			failureJSON := failureResultJSON(err)
			if runCtx.Err() != nil {
				log.Printf("[WARN] pipeline cancelled run_id=%s operation=%s error=%v", runID, operationName(cmd.Operation), err)
				// Context was cancelled — treat as CANCELLED unless the pipeline
				// produced a trustworthy restore outcome.
				code := "cancelled"
				message := "run cancelled by server"
				if len(failureJSON) > 0 {
					code, message = failureCodeAndMessage(err, "cancelled", message)
				}
				runResult = &bmcv1.RunResult{
					RunId:        runID,
					Status:       bmcv1.RunResult_CANCELLED,
					ErrorCode:    code,
					ErrorMessage: message,
					ResultJson:   string(failureJSON),
				}
			} else {
				code, msg := failureCodeAndMessage(err, "pipeline_error", err.Error())
				msg = pipeline.HostDisplayText(msg, allMappings)
				log.Printf("[ERROR] pipeline execute run_id=%s operation=%s error_code=%s error=%s", runID, operationName(cmd.Operation), code, msg)
				runResult = &bmcv1.RunResult{
					RunId:        runID,
					Status:       bmcv1.RunResult_FAILED,
					ErrorCode:    code,
					ErrorMessage: msg,
					ResultJson:   string(failureJSON),
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
		r.sendRunResult(runResult)
	}()
}

// failureResultJSON extracts the non-secret failure payload of a pipeline error.
func failureResultJSON(err error) []byte {
	var pe *pipeline.PipelineError
	if errors.As(err, &pe) {
		return pe.ResultJSON
	}
	return nil
}

// failureCodeAndMessage maps a pipeline error to its stable code and message,
// falling back to the supplied defaults.
func failureCodeAndMessage(err error, code, msg string) (string, string) {
	var pe *pipeline.PipelineError
	if errors.As(err, &pe) {
		if pe.Code != "" {
			code = pe.Code
		}
		// restic 的"具体分类"（如 repository_missing）比 pipeline 的通用码更有价值；
		// 但 restic 的兜底码 restic_failed 没有信息量，不能覆盖 pipeline 给出的明确
		// 分类——否则保护备份失败会被报成 restic_failed，pre_restore_backup_failed
		// 这类稳定码永远不会出现在 run.error_code 上（实测该码全仓无消费方）。
		var re *restic.ResticError
		if errors.As(err, &re) && re.Code != "" && (re.Code != resticFailedCode || isGenericFailureCode(code)) {
			code = re.Code
		}
		// Message 里往往已经写明了调用方的判定（例如"导入失败;回滚也失败"），
		// 直接换成 Cause 会把它吞掉——rollback_failed 这类需要人工判断的场景
		// 恰好最需要那句话。保留 Message，必要时再补 Cause。
		switch {
		case pe.Message != "":
			msg = pe.Message
			if pe.Cause != nil && !strings.Contains(msg, pe.Cause.Error()) {
				msg += ": " + pe.Cause.Error()
			}
		case pe.Cause != nil:
			msg = pe.Cause.Error()
		}
	}
	return code, msg
}

// resticFailedCode 是 restic 的兜底错误码：只说明"restic 退出非零"，不携带任何
// 分类信息（具体分类如 repository_missing 才值得优先于通用码）。
const resticFailedCode = "restic_failed"

// isGenericFailureCode 判断错误码是否只是"操作失败"级别的兜底码。这类码不携带
// 分类信息，可以被更有信息量的码覆盖；反之明确分类（如 pre_restore_backup_failed、
// rollback_failed）必须保留，否则监控与 UI 依据 error_code 的判定会失效。
func isGenericFailureCode(code string) bool {
	switch code {
	case "", "pipeline_error", "backup_failed", resticFailedCode:
		return true
	}
	return false
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
func (r *Runner) sendRunResult(result *bmcv1.RunResult) {
	msg := &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_RunResult{
			RunResult: result,
		},
	}
	if err := r.currentStream().Send(msg); err != nil {
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
	r.sendRunResult(result)
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
