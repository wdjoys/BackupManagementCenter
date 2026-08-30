package agentreg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/logging"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/notification"
	"backupmanagementcenter/internal/server/store"
	"backupmanagementcenter/internal/version"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	heartbeatInterval = 30 // seconds
	lastSeenThrottle  = 10 * time.Second
	offlineThreshold  = 90 * time.Second
	offlineCheckTick  = 30 * time.Second
	heartbeatThrottle = 10 * time.Second // min interval between last_seen_at writes
)

// instanceID is a unique identifier for this server instance, generated at startup.
var instanceID string

func init() {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	instanceID = hex.EncodeToString(b)
}

// Service implements bmcv1.AgentControlServer for the gRPC channel layer.
type Service struct {
	bmcv1.UnimplementedAgentControlServer

	store    store.Store
	reg      *Registry
	bus      events.Bus
	cfg      Config
	notifier notification.FailureNotifier

	// mu protects lastSeenWrite for the heartbeat throttle
	lastSeenMu     sync.Mutex
	lastSeenWrites map[string]time.Time

	// Once started goroutines
	startOnce sync.Once
	stopCh    chan struct{}
}

// Config holds optional configuration for the gRPC service.
type Config struct {
	// HeartbeatIntervalSeconds is sent to agents in Welcome messages.
	HeartbeatIntervalSeconds int32
	// OfflineCheckInterval controls how often the offline-detection goroutine runs.
	OfflineCheckInterval time.Duration
	// OfflineThreshold controls how long since last_seen before marking offline.
	OfflineThreshold time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		HeartbeatIntervalSeconds: heartbeatInterval,
		OfflineCheckInterval:     offlineCheckTick,
		OfflineThreshold:         offlineThreshold,
	}
}

// NewService creates a new AgentControl gRPC service. notifier receives one
// call per persisted plan-bound failed run; nil falls back to a no-op.
func NewService(s store.Store, reg *Registry, bus events.Bus, cfg Config, notifier notification.FailureNotifier) *Service {
	if notifier == nil {
		notifier = notification.NopNotifier{}
	}
	return &Service{
		store:          s,
		reg:            reg,
		bus:            bus,
		cfg:            cfg,
		notifier:       notifier,
		lastSeenWrites: make(map[string]time.Time),
		stopCh:         make(chan struct{}),
	}
}

// Start launches the background goroutine for offline detection.
func (s *Service) Start() {
	s.startOnce.Do(func() {
		go s.offlineLoop()
	})
}

// Stop signals the background goroutine to shut down.
func (s *Service) Stop() {
	close(s.stopCh)
}

// ---------------------------------------------------------------------------
// Enroll (unary)
// ---------------------------------------------------------------------------

func (s *Service) Enroll(ctx context.Context, req *bmcv1.EnrollRequest) (*bmcv1.EnrollResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "empty request")
	}

	// Validate enrollment token
	tokenHash := secrets.HashToken(req.GetEnrollmentToken())
	et, err := s.store.ConsumeEnrollmentToken(ctx, tokenHash, time.Now().UTC())
	if err != nil {
		if err == store.ErrTokenInvalid {
			return nil, status.Error(codes.PermissionDenied, "invalid or expired enrollment token")
		}
		return nil, status.Error(codes.Internal, "failed to consume enrollment token")
	}

	// Validate agent secret
	if len(req.GetSecret()) != 32 {
		return nil, status.Error(codes.InvalidArgument, "secret must be exactly 32 bytes")
	}

	// Generate agent ID (UUIDv7)
	agentID := model.NewUUIDv7()

	// Compute secret hash: sha256(hex(secret))
	secretHex := hex.EncodeToString(req.GetSecret())
	secretHash := secrets.HashToken(secretHex)

	// Build agent name from hostname or use first 8 chars of ID
	name := req.GetHostname()
	if name == "" {
		name = agentID[:8]
	}

	now := time.Now().UTC()
	agent := &model.Agent{
		ID:         agentID,
		Name:       name,
		Hostname:   req.GetHostname(),
		OS:         req.GetOs(),
		Arch:       req.GetArch(),
		Version:    req.GetVersion(),
		Status:     model.AgentOffline, // not online until Connect
		EnrolledAt: now,
		TokenHash:  secretHash,
		Revoked:    false,
	}

	// Use UpsertAgentOnConnect to create the agent row
	// (it's an INSERT OR REPLACE by ID, which works for first insert)
	if err := s.store.UpsertAgentOnConnect(ctx, agent); err != nil {
		return nil, status.Error(codes.Internal, "failed to create agent")
	}

	// Mark token as used
	_ = et // already consumed by ConsumeEnrollmentToken

	log.Printf("agent enrolled: id=%s name=%s hostname=%s os=%s arch=%s",
		agentID, name, req.GetHostname(), req.GetOs(), req.GetArch())

	return &bmcv1.EnrollResponse{AgentId: agentID}, nil
}

// ---------------------------------------------------------------------------
// Connect (bidirectional streaming)
// ---------------------------------------------------------------------------

func (s *Service) Connect(stream bmcv1.AgentControl_ConnectServer) error {
	// Extract agent identity from metadata
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	agentIDs := md.Get("bmc-agent-id")
	agentSecrets := md.Get("bmc-agent-secret")
	if len(agentIDs) == 0 || len(agentSecrets) == 0 {
		return status.Error(codes.Unauthenticated, "missing bmc-agent-id or bmc-agent-secret")
	}

	agentID := agentIDs[0]
	agentSecret := agentSecrets[0]

	// Authenticate: compute secret hash (sha256(hex(secret))) and look up agent
	secretHash := secrets.HashToken(agentSecret)
	agent, err := s.store.GetAgentBySecretHash(stream.Context(), secretHash)
	if err != nil {
		if err == store.ErrNotFound {
			return status.Error(codes.PermissionDenied, "invalid agent credentials")
		}
		return status.Error(codes.Internal, "failed to authenticate agent")
	}

	if agent.Revoked {
		return status.Error(codes.PermissionDenied, "agent is revoked")
	}

	// Register the stream (kicks old connection if any)
	sendCh, streamCtx := s.reg.Register(stream.Context(), agentID)

	// Ensure cleanup on exit
	defer func() {
		if s.reg.UnregisterIf(agentID, streamCtx) {
			s.handleDisconnect(agentID)
		}
	}()

	// Wait for the Hello message
	firstMsg, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.Unavailable, "failed to receive hello: %v", err)
	}

	hello := firstMsg.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}

	// Version handshake
	serverMajor, serverMinor, _, serverOk := version.Parts()
	clientMajor, clientMinor, _, clientOk := version.PartsFromString(hello.GetVersion())
	versionMinorMismatch := false

	if !serverOk || !clientOk {
		// If we can't parse versions, allow connection but warn
		versionMinorMismatch = true
	} else if serverMajor != clientMajor {
		// Major version mismatch: reject
		return status.Error(codes.PermissionDenied,
			fmt.Sprintf("major version mismatch: server=%d, agent=%d", serverMajor, clientMajor))
	} else if serverMinor != clientMinor {
		// Minor version mismatch: allow with warning
		versionMinorMismatch = true
	}

	// Send Welcome message
	welcome := &bmcv1.Welcome{
		ServerVersion:            version.Version,
		ServerInstanceId:         instanceID,
		HeartbeatIntervalSeconds: s.cfg.HeartbeatIntervalSeconds,
		VersionMinorMismatch:     versionMinorMismatch,
	}

	welcomeMsg := &bmcv1.ServerMessage{
		Payload: &bmcv1.ServerMessage_Welcome{Welcome: welcome},
	}

	if err := stream.Send(welcomeMsg); err != nil {
		return status.Errorf(codes.Unavailable, "failed to send welcome: %v", err)
	}

	// Update agent status to online
	now := time.Now().UTC()
	agent.Status = model.AgentOnline
	agent.Hostname = hello.GetHostname()
	agent.OS = hello.GetOs()
	agent.Arch = hello.GetArch()
	agent.Version = hello.GetVersion()
	agent.LastSeenAt = &now
	if err := s.store.UpsertAgentOnConnect(stream.Context(), agent); err != nil {
		log.Printf("failed to upsert agent on connect: %v", err)
	}
	log.Printf("[INFO] agent connected id=%s hostname=%s os=%s arch=%s version=%s minor_mismatch=%t",
		agentID,
		agent.Hostname,
		agent.OS,
		agent.Arch,
		agent.Version,
		versionMinorMismatch,
	)

	// Start a goroutine to forward messages from sendCh to the stream
	errCh := make(chan error, 1)
	go s.sendLoop(streamCtx, stream, sendCh, errCh)

	// Main receive loop: Recv runs in its own goroutine so the loop can also
	// wake on streamCtx cancellation (Registry.Unregister — e.g. agent revoked
	// or replaced by a newer connection). Cancelling the context alone never
	// unblocks Recv.
	type recvResult struct {
		msg *bmcv1.AgentMessage
		err error
	}
	recvCh := make(chan recvResult, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			recvCh <- recvResult{msg, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-streamCtx.Done():
			return status.Error(codes.Canceled, "stream cancelled")
		case r := <-recvCh:
			if r.err != nil {
				// Stream ended
				select {
				case sendErr := <-errCh:
					return sendErr
				default:
				}
				return nil
			}

			if err := s.handleAgentMessage(stream, agentID, r.msg); err != nil {
				log.Printf("error handling agent message from %s: %v", agentID, err)
			}
		}
	}
}

// sendLoop forwards messages from the registry's send channel to the gRPC stream.
func (s *Service) sendLoop(ctx context.Context, stream bmcv1.AgentControl_ConnectServer, sendCh <-chan *bmcv1.ServerMessage, errCh chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sendCh:
			if !ok {
				return
			}
			if err := stream.Send(msg); err != nil {
				errCh <- err
				return
			}
		}
	}
}

// handleAgentMessage dispatches an incoming AgentMessage to the appropriate handler.
func (s *Service) handleAgentMessage(stream bmcv1.AgentControl_ConnectServer, agentID string, msg *bmcv1.AgentMessage) error {
	switch p := msg.GetPayload().(type) {
	case *bmcv1.AgentMessage_Heartbeat:
		return s.handleHeartbeat(stream.Context(), agentID, p.Heartbeat)
	case *bmcv1.AgentMessage_CapabilitiesReport:
		return s.handleCapabilities(stream.Context(), agentID, p.CapabilitiesReport)
	case *bmcv1.AgentMessage_CommandAccepted:
		return s.handleCommandAccepted(stream.Context(), agentID, p.CommandAccepted)
	case *bmcv1.AgentMessage_RunProgress:
		return s.handleRunProgress(stream.Context(), agentID, p.RunProgress)
	case *bmcv1.AgentMessage_RunLogBatch:
		return s.handleRunLogBatch(stream.Context(), agentID, p.RunLogBatch)
	case *bmcv1.AgentMessage_RunResult:
		return s.handleRunResult(stream.Context(), agentID, p.RunResult)
	default:
		// Unknown message type — ignore
		return nil
	}
}

// ---------------------------------------------------------------------------
// Message handlers
// ---------------------------------------------------------------------------

func (s *Service) handleHeartbeat(ctx context.Context, agentID string, hb *bmcv1.Heartbeat) error {
	// Throttle last_seen_at writes to at most once per 10 seconds
	s.lastSeenMu.Lock()
	lastWrite, ok := s.lastSeenWrites[agentID]
	now := time.Now().UTC()
	if ok && now.Sub(lastWrite) < heartbeatThrottle {
		s.lastSeenMu.Unlock()
		return nil
	}
	s.lastSeenWrites[agentID] = now
	s.lastSeenMu.Unlock()

	return s.store.SetAgentStatus(ctx, agentID, model.AgentOnline, now)
}

func (s *Service) handleCapabilities(ctx context.Context, agentID string, report *bmcv1.CapabilitiesReport) error {
	tools := make([]model.ToolInfo, 0, len(report.GetTools()))
	for _, t := range report.GetTools() {
		tools = append(tools, model.ToolInfo{Name: t.GetName(), Path: t.GetPath(), Version: t.GetVersion()})
	}
	toMappings := func(input []*bmcv1.PathMapping) []model.PathMapping {
		out := make([]model.PathMapping, 0, len(input))
		for _, m := range input { out = append(out, model.PathMapping{HostPath: m.GetHostPath(), RuntimePath: m.GetRuntimePath(), ReadOnly: m.GetReadOnly()}) }
		return out
	}
	return s.store.SaveAgentCapabilities(ctx, agentID, tools, toMappings(report.GetSourcePathMappings()), toMappings(report.GetRestorePathMappings()), time.Now().UTC())
}

func (s *Service) handleCommandAccepted(ctx context.Context, agentID string, ca *bmcv1.CommandAccepted) error {
	runID := ca.GetRunId()
	// Transition run from dispatched -> running (or keep dispatched and record ack)
	// We attempt dispatched -> running; if the run is already running, that's fine.
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil // run already gone, idempotent
		}
		return err
	}
	if run.AgentID != agentID {
		return status.Error(codes.PermissionDenied, "run belongs to another agent")
	}

	if run.Status == model.RunDispatched {
		err := s.store.TransitionRun(ctx, runID, model.RunDispatched, model.RunRunning, func(r *model.Run) {
			now := time.Now().UTC()
			r.StartedAt = &now
			r.LeaseExpiresAt = nil
		})
		if err == nil && run.Operation == model.OpRestore {
			if rs, ok := s.store.(interface {
				UpdateRestorePhase(context.Context, string, string) error
			}); ok {
				_ = rs.UpdateRestorePhase(ctx, runID, "running")
			}
		}
		return err
	}
	return nil
}

func (s *Service) handleRunProgress(ctx context.Context, agentID string, rp *bmcv1.RunProgress) error {
	runID := rp.GetRunId()

	progress := model.Progress{
		Phase:      rp.GetPhase(),
		Percent:    rp.GetPercent(),
		BytesDone:  rp.GetBytesDone(),
		BytesTotal: rp.GetBytesTotal(),
		FilesDone:  rp.GetFilesDone(),
		FilesTotal: rp.GetFilesTotal(),
	}

	// Update progress_json in the run
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil // idempotent
		}
		return err
	}
	if run.AgentID != agentID {
		return status.Error(codes.PermissionDenied, "run belongs to another agent")
	}

	run.Progress = progress
	progressJSON, _ := json.Marshal(progress)
	run.ProgressJSON = string(progressJSON)

	if ps, ok := s.store.(interface {
		UpdateRunProgress(context.Context, string, string) error
	}); ok {
		if err := ps.UpdateRunProgress(ctx, runID, run.ProgressJSON); err != nil {
			return err
		}
	}

	s.bus.Publish(runID, events.Event{
		Type:     events.Progress,
		Progress: &progress,
	})

	return nil
}

func (s *Service) handleRunLogBatch(ctx context.Context, agentID string, batch *bmcv1.RunLogBatch) error {
	runID := batch.GetRunId()
	entries := batch.GetEntries()
	if len(entries) == 0 {
		return nil
	}
	// run_id为空表示Agent进程日志；非空仍按运行日志处理。
	if runID == "" {
		return s.handleAgentLogBatch(ctx, agentID, entries)
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil
		}
		return err
	}
	if run.AgentID != agentID {
		return status.Error(codes.PermissionDenied, "run belongs to another agent")
	}

	logs := make([]model.RunLog, 0, len(entries))
	for _, entry := range entries {
		logLevel := "info"
		switch entry.GetLevel() {
		case bmcv1.LogLevel_DEBUG:
			logLevel = "debug"
		case bmcv1.LogLevel_WARN:
			logLevel = "warn"
		case bmcv1.LogLevel_ERROR:
			logLevel = "error"
		}

		logEntry := model.RunLog{
			RunID:     runID,
			Seq:       entry.GetSeq(),
			Timestamp: time.Unix(0, entry.GetTimestampUnixNanos()).UTC(),
			Level:     logLevel,
			Message:   entry.GetMessage(),
		}
		logs = append(logs, logEntry)

		// Publish log event
		s.bus.Publish(runID, events.Event{
			Type:  events.Log,
			Entry: &logEntry,
		})
	}

	return s.store.AppendRunLogs(ctx, logs)
}
func (s *Service) handleAgentLogBatch(ctx context.Context, agentID string, entries []*bmcv1.LogEntry) error {
	logStore, ok := s.store.(store.LogStore)
	if !ok {
		return fmt.Errorf("agent log storage is unavailable")
	}
	logs := make([]model.SystemLog, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		level := "info"
		switch entry.GetLevel() {
		case bmcv1.LogLevel_DEBUG:
			level = "debug"
		case bmcv1.LogLevel_WARN:
			level = "warn"
		case bmcv1.LogLevel_ERROR:
			level = "error"
		}
		timestamp := time.Now().UTC()
		if nanos := entry.GetTimestampUnixNanos(); nanos != 0 {
			timestamp = time.Unix(0, nanos).UTC()
		}
		logs = append(logs, model.SystemLog{
			SourceSeq: entry.GetSeq(),
			Timestamp: timestamp,
			Type:      logging.ClassifyType(entry.GetMessage()),
			Level:     level,
			Message:   entry.GetMessage(),
		})
	}
	return logStore.AppendAgentLogs(ctx, agentID, logs)
}

func (s *Service) handleRunResult(ctx context.Context, agentID string, result *bmcv1.RunResult) error {
	runID := result.GetRunId()

	// Map RunResult status to model status
	var toStatus string
	switch result.GetStatus() {
	case bmcv1.RunResult_SUCCEEDED:
		toStatus = model.RunSucceeded
	case bmcv1.RunResult_FAILED:
		toStatus = model.RunFailed
	case bmcv1.RunResult_CANCELLED:
		toStatus = model.RunCancelled
	default:
		return fmt.Errorf("unknown run result status: %v", result.GetStatus())
	}

	// Find the current run to determine the "from" status
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		if err == store.ErrNotFound {
			return nil // idempotent
		}
		return err
	}
	// Build the snapshot ID from the first entry
	snapshotID := ""
	if len(result.GetSnapshotIds()) > 0 {
		snapshotID = result.GetSnapshotIds()[0]
	}

	// Terminal transition accepts both running and dispatched source states.
	resultJSON := result.GetResultJson()
	now := time.Now().UTC()

	if run.AgentID != agentID {
		return status.Error(codes.PermissionDenied, "run belongs to another agent")
	}
	initRepositoryID := ""
	if isRepositoryInitRun(run) {
		initRepositoryID = run.RepositoryID
	}

	err = s.store.TransitionRun(ctx, runID, model.RunRunning, toStatus, func(r *model.Run) {
		r.FinishedAt = &now
		r.ErrorCode = result.GetErrorCode()
		r.ErrorMessage = result.GetErrorMessage()
		r.SnapshotID = snapshotID
		r.LeaseExpiresAt = nil
		if len(resultJSON) > 0 {
			r.ProgressJSON = string(resultJSON)
		}
	})

	if errors.Is(err, store.ErrInvalidTransition) {
		// Fast-finished runs may still be 'dispatched'.
		err = s.store.TransitionRun(ctx, runID, model.RunDispatched, toStatus, func(r *model.Run) {
			now := time.Now().UTC()
			r.StartedAt = &now
			r.FinishedAt = &now
			r.ErrorCode = result.GetErrorCode()
			r.ErrorMessage = result.GetErrorMessage()
			r.SnapshotID = snapshotID
			r.LeaseExpiresAt = nil
			if len(resultJSON) > 0 {
				r.ProgressJSON = string(resultJSON)
			}
		})
	}

	if err == store.ErrInvalidTransition {
		// Already terminal — idempotent.
		return nil
	}
	if err != nil {
		return err
	}
	if isTerminal(toStatus) {
		// Binding waits synchronously for restic init, but the HTTP request can
		// time out while the agent is still finishing. Reconcile the repository
		// status from the eventual terminal init result so a late success heals
		// the pending/error row automatically.
		if initRepositoryID != "" {
			repositoryStatus := "error"
			if toStatus == model.RunSucceeded {
				repositoryStatus = "ready"
			}
			if err := s.store.UpdateRepositoryStatus(ctx, initRepositoryID, repositoryStatus); err != nil {
				log.Printf("failed to update repository %s status after init: %v", initRepositoryID, err)
			}
		}
		if run.Operation == model.OpRestore {
			phase := "failed"
			if toStatus == model.RunSucceeded {
				phase = "succeeded"
			}
			if rs, ok := s.store.(interface {
				UpdateRestorePhase(context.Context, string, string) error
			}); ok {
				_ = rs.UpdateRestorePhase(ctx, runID, phase)
			}
		}
		if rs, ok := s.store.(interface {
			DeleteRunSecrets(context.Context, string) error
		}); ok {
			if secretErr := rs.DeleteRunSecrets(ctx, runID); secretErr != nil {
				log.Printf("failed to delete run secrets for %s: %v", runID, secretErr)
			}
		}
	}

	// Mark repository checked for backup/check operations on success
	if toStatus == model.RunSucceeded && run.Operation != "" {
		if run.Operation == model.OpBackup || run.Operation == model.OpCheck {
			// Scheduled repository checks are system runs with no plan ID, so
			// prefer the repository carried directly on the run. Backups keep
			// the plan lookup as a compatibility fallback for older rows.
			repositoryID := run.RepositoryID
			if repositoryID == "" && run.PlanID != "" {
				plan, planErr := s.store.GetPlan(ctx, run.PlanID)
				if planErr == nil {
					repositoryID = plan.RepositoryID
				}
			}
			if repositoryID != "" {
				_ = s.store.MarkRepositoryChecked(ctx, repositoryID, time.Now().UTC())
			}
		}
	}

	// Publish state event
	updatedRun, _ := s.store.GetRun(ctx, runID)
	if updatedRun != nil {
		s.bus.Publish(runID, events.Event{
			Type: events.State,
			Run:  updatedRun,
		})
	}

	// Notify only after the failed terminal state is durably committed and
	// the state event published. Duplicate agent results return earlier via
	// ErrInvalidTransition and never reach this point.
	if toStatus == model.RunFailed {
		if err := s.notifier.NotifyPlanFailure(ctx, runID); err != nil {
			notification.LogFailure(runID, err)
		}
	}

	return nil
}

// isRepositoryInitRun distinguishes the repository bootstrap form of the
// legacy FORGET operation from normal retention/forget runs.
func isRepositoryInitRun(run *model.Run) bool {
	if run == nil || run.Operation != model.OpForget || run.RepositoryID == "" {
		return false
	}
	var task model.InitTask
	if err := json.Unmarshal([]byte(run.ProgressJSON), &task); err != nil {
		return false
	}
	return task.ResticInit
}

// isTerminal reports whether a run status is immutable.  Keep this helper
// local to the agent callback service so duplicate terminal callbacks can be
// treated idempotently without coupling the service to the orchestrator.
func isTerminal(status string) bool {
	return status == model.RunSucceeded || status == model.RunFailed || status == model.RunCancelled
}

// ---------------------------------------------------------------------------
// Disconnect handling
// ---------------------------------------------------------------------------

func (s *Service) handleDisconnect(agentID string) {
	log.Printf("[WARN] agent disconnected id=%s", agentID)
	ctx := context.Background()

	// Set agent offline
	_ = s.store.SetAgentStatus(ctx, agentID, model.AgentOffline, time.Now().UTC())

	// Reset only retry-safe work. A restore/init/forget operation may have
	// changed external state before the stream disappeared, so silently
	// replaying it would be destructive. Those runs become an explicit
	// operator-visible failure instead.
	runs, err := s.store.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning})
	if err == nil {
		for _, run := range runs {
			if run.AgentID == agentID {
				if retryableOperation(run.Operation) {
					_ = s.store.TransitionRun(ctx, run.ID, run.Status, model.RunQueued, func(r *model.Run) {
						r.StartedAt = nil
						r.LeaseExpiresAt = nil
						r.ErrorCode = ""
						r.ErrorMessage = ""
					})
					continue
				}
				finished := time.Now().UTC()
				if err := s.store.TransitionRun(ctx, run.ID, run.Status, model.RunFailed, func(r *model.Run) {
					r.FinishedAt = &finished
					r.ErrorCode = model.ErrAgentDisconnected
					r.ErrorMessage = "agent stream disconnected during a non-retryable operation"
					r.LeaseExpiresAt = nil
				}); err == nil {
					if rs, ok := s.store.(interface {
						DeleteRunSecrets(context.Context, string) error
					}); ok {
						_ = rs.DeleteRunSecrets(ctx, run.ID)
					}
					if nerr := s.notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
						notification.LogFailure(run.ID, nerr)
					}
				}
			}
		}
	}

	s.reg.notifyDisconnect(agentID)
}

func retryableOperation(op string) bool {
	switch op {
	case model.OpBackup, model.OpCheck, model.OpSnapshots, model.OpSnapshotLs,
		model.OpValidatePaths, model.OpProbeCaps, model.OpVerifyRemote:
		return true
	default:
		return false
	}
}

// offlineLoop periodically checks for agents that haven't sent a heartbeat
// and marks them offline.
func (s *Service) offlineLoop() {
	ticker := time.NewTicker(s.cfg.OfflineCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkOfflineAgents()
		}
	}
}

func (s *Service) checkOfflineAgents() {
	ctx := context.Background()
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		log.Printf("failed to list agents for offline check: %v", err)
		return
	}

	now := time.Now().UTC()
	for _, agent := range agents {
		if agent.Status != model.AgentOnline {
			continue
		}
		if agent.LastSeenAt == nil {
			continue
		}
		if now.Sub(*agent.LastSeenAt) > s.cfg.OfflineThreshold {
			// Mark as offline in DB only (stream already disconnected)
			_ = s.store.SetAgentStatus(ctx, agent.ID, model.AgentOffline, now)
		}
	}
}
