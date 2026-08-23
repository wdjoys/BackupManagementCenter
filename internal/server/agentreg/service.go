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
		s.reg.Unregister(agentID)
		s.handleDisconnect(agentID)
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
		tools = append(tools, model.ToolInfo{
			Name:    t.GetName(),
			Path:    t.GetPath(),
			Version: t.GetVersion(),
		})
	}
	return s.store.SaveAgentCapabilities(ctx, agentID, tools, time.Now().UTC())
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

	if run.Status == model.RunDispatched {
		return s.store.TransitionRun(ctx, runID, model.RunDispatched, model.RunRunning, func(r *model.Run) {
			now := time.Now().UTC()
			r.StartedAt = &now
		})
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

	run.Progress = progress
	progressJSON, _ := json.Marshal(progress)
	run.ProgressJSON = string(progressJSON)

	// We publish progress event without needing to persist progress_json separately
	// since it's typically updated through TransitionRun or direct DB update.
	// For simplicity, we just publish the event.
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

	err = s.store.TransitionRun(ctx, runID, model.RunRunning, toStatus, func(r *model.Run) {
		r.FinishedAt = &now
		r.ErrorCode = result.GetErrorCode()
		r.ErrorMessage = result.GetErrorMessage()
		r.SnapshotID = snapshotID
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

	// Mark repository checked for backup/check operations on success
	if toStatus == model.RunSucceeded && run.Operation != "" {
		if run.Operation == model.OpBackup || run.Operation == model.OpCheck {
			// Find the plan to get the repository (plan_id may be empty for system operations)
			if run.PlanID != "" {
				plan, planErr := s.store.GetPlan(ctx, run.PlanID)
				if planErr == nil && plan.RepositoryID != "" {
					_ = s.store.MarkRepositoryChecked(ctx, plan.RepositoryID, time.Now().UTC())
				}
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

// ---------------------------------------------------------------------------
// Disconnect handling
// ---------------------------------------------------------------------------

func (s *Service) handleDisconnect(agentID string) {
	ctx := context.Background()

	// Set agent offline
	_ = s.store.SetAgentStatus(ctx, agentID, model.AgentOffline, time.Now().UTC())

	// Reset dispatched/running runs back to queued
	runs, err := s.store.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning})
	if err == nil {
		for _, run := range runs {
			if run.AgentID == agentID {
				_ = s.store.TransitionRun(ctx, run.ID, run.Status, model.RunQueued, func(r *model.Run) {
					r.StartedAt = nil
				})
			}
		}
	}

	s.reg.notifyDisconnect(agentID)
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
