package agent

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/logging"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ErrRevoked is returned by Run when the server permanently rejected the
// agent's credentials because it was revoked. Retrying can never succeed:
// the process exits and re-enrollment with a fresh token is required.
var ErrRevoked = errors.New("agent revoked on server")

// isRevokedErr reports whether err is the server's PermissionDenied
// rejection of a revoked agent. Other PermissionDenied cases (e.g. version
// mismatch) must keep retrying. The error may be wrapped.
func isRevokedErr(err error) bool {
	if err == nil {
		return false
	}
	var gs interface{ GRPCStatus() *status.Status }
	if errors.As(err, &gs) {
		if st := gs.GRPCStatus(); st != nil &&
			st.Code() == codes.PermissionDenied &&
			strings.Contains(st.Message(), "revoked") {
			return true
		}
	}
	return strings.Contains(err.Error(), "agent is revoked")
}

// ConnectClient manages the bidirectional gRPC stream to the server.
type ConnectClient struct {
	cfg      ConfigProvider
	identity *Identity
	im       *IdentityManager
	prober   *Prober
	runner   *Runner
	logSink  *logging.Sink

	// Reconnection state
	reconnectCount atomic.Uint64
	cancelStream   context.CancelFunc
	streamCtx      context.Context

	// Heartbeat interval from server Welcome
	heartbeatInterval time.Duration
	mu                sync.Mutex

	// Chained components
	helloOnce sync.Once
}

// ConfigProvider abstracts the agent config for testing.
type ConfigProvider interface {
	GetServerGRPCURL() string
	GetServerTLS() bool
	GetDevInsecure() bool
	GetProbeInterval() time.Duration
	GetSourcePathMappings() []model.PathMapping
	GetRestorePathMappings() []model.PathMapping
}

// NewConnectClient assembles the control-stream client. The identity must
// already exist (LoadOrCreate/Enroll handled by main).
func NewConnectClient(cfg ConfigProvider, im *IdentityManager, prober *Prober, runner *Runner) *ConnectClient {
	ident, err := im.Get()
	if err != nil {
		log.Fatalf("[FATAL] identity not loaded before connect client creation: %v", err)
	}
	return &ConnectClient{
		cfg:               cfg,
		identity:          ident,
		im:                im,
		prober:            prober,
		runner:            runner,
		heartbeatInterval: 30 * time.Second,
	}
}

// SetLogSink绑定Agent进程日志转发器；连接重建时会自动切换发送目标。
func (c *ConnectClient) SetLogSink(sink *logging.Sink) {
	c.mu.Lock()
	c.logSink = sink
	c.mu.Unlock()
}

// Run starts the connect loop with reconnection.
func (c *ConnectClient) Run(ctx context.Context) error {
	for {
		connected, err := c.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[ERROR] connect failed: %v", err)
			c.backoff(ctx)
			continue
		}
		if connected {
			c.reconnectCount.Store(0)
			log.Printf("[INFO] connected to server")
		}

		// Start the stream loop
		streamCtx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		if c.cancelStream != nil {
			c.cancelStream()
		}
		c.cancelStream = cancel
		c.streamCtx = streamCtx
		c.mu.Unlock()

		// Run stream — blocks until disconnect
		if err := c.streamLoop(streamCtx); err != nil && ctx.Err() == nil {
			log.Printf("[WARN] stream disconnected: %v", err)
			if isRevokedErr(err) {
				log.Printf("[ERROR] credentials permanently rejected; stop reconnecting — re-enroll with a fresh token to use this agent again")
				return fmt.Errorf("%w (%v)", ErrRevoked, err)
			}
		}
		c.mu.Lock()
		c.cancelStream = nil
		c.mu.Unlock()

		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.backoff(ctx)
	}
}

// ReconnectCount returns the number of reconnections (atomic).
func (c *ConnectClient) ReconnectCount() uint64 {
	return c.reconnectCount.Load()
}

func (c *ConnectClient) connect(ctx context.Context) (bool, error) {
	c.reconnectCount.Add(1)

	conn, err := c.dial(ctx)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	_ = conn // used in streamLoop
	// Store connection in context for use in streamLoop
	c.mu.Lock()
	// We'll use the dial in streamLoop directly
	c.mu.Unlock()
	conn.Close()
	return true, nil
}

func (c *ConnectClient) dial(ctx context.Context) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithUserAgent("bmc-agent/" + version.Version),
	}
	if c.cfg.GetServerTLS() {
		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
		if c.cfg.GetDevInsecure() {
			tlsCfg.InsecureSkipVerify = true
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	return grpc.NewClient(c.cfg.GetServerGRPCURL(), opts...)
}

func (c *ConnectClient) streamLoop(ctx context.Context) error {
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := bmcv1.NewAgentControlClient(conn)

	// Create metadata with auth
	secretBytes, err := hex.DecodeString(c.identity.SecretHex)
	if err != nil {
		return fmt.Errorf("decode secret: %w", err)
	}
	md := metadata.Pairs(
		"bmc-agent-id", c.identity.AgentID,
		"bmc-agent-secret", hex.EncodeToString(secretBytes),
		"bmc-agent-version", version.Version,
	)
	callCtx := metadata.NewOutgoingContext(ctx, md)

	stream, err := client.Connect(callCtx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	ss := newSafeStream(stream)

	// Reset heartbeat interval
	c.mu.Lock()
	c.heartbeatInterval = 30 * time.Second
	c.mu.Unlock()

	// Send Hello (first message)
	hello := c.buildHello()
	if err := ss.Send(hello); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	c.mu.Lock()
	logSink := c.logSink
	c.mu.Unlock()
	if logSink != nil {
		logSink.SetHandler(func(entry logging.Entry) error {
			return ss.Send(agentProcessLogMessage(entry))
		})
		defer logSink.ClearHandler()
	}

	// Start heartbeat goroutine
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go c.heartbeatLoop(heartbeatCtx, ss)

	// Start capability probe goroutine
	probeCtx, probeCancel := context.WithCancel(ctx)
	defer probeCancel()
	go c.capabilityLoop(probeCtx, ss)

	// Receive messages from server
	for {
		msg, err := ss.Recv()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}
		if err := c.handleServerMessage(ctx, ss, msg); err != nil {
			log.Printf("[ERROR] handling message: %v", err)
		}
	}
}

func (c *ConnectClient) buildHello() *bmcv1.AgentMessage {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_Hello{
			Hello: &bmcv1.Hello{
				Hostname: hostname,
				Os:       runtime.GOOS,
				Arch:     runtime.GOARCH,
				Version:  version.Version,
			},
		},
	}
}

func agentProcessLogMessage(entry logging.Entry) *bmcv1.AgentMessage {
	return &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_RunLogBatch{
			RunLogBatch: &bmcv1.RunLogBatch{
				Entries: []*bmcv1.LogEntry{{
					Seq:                entry.Seq,
					TimestampUnixNanos: entry.Timestamp.UnixNano(),
					Level:              protoLevel(entry.Level),
					Message:            entry.Message,
				}},
			},
		},
	}
}

func (c *ConnectClient) heartbeatLoop(ctx context.Context, stream bmcv1.AgentControl_ConnectClient) {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()

	startTime := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			interval := c.heartbeatInterval
			c.mu.Unlock()
			ticker.Reset(interval)

			uptime := time.Since(startTime).Seconds()
			msg := &bmcv1.AgentMessage{
				MessageId: newMessageID(),
				Payload: &bmcv1.AgentMessage_Heartbeat{
					Heartbeat: &bmcv1.Heartbeat{
						UnixNanos:     time.Now().UnixNano(),
						UptimeSeconds: uptime,
					},
				},
			}
			if err := stream.Send(msg); err != nil {
				log.Printf("[ERROR] heartbeat send: %v", err)
				return
			}
		}
	}
}

func (c *ConnectClient) capabilityLoop(ctx context.Context, stream bmcv1.AgentControl_ConnectClient) {
	// Send capabilities immediately on connect
	c.sendCapabilities(ctx, stream)

	probeInterval := c.cfg.GetProbeInterval()
	if probeInterval <= 0 {
		probeInterval = 10 * time.Minute
	}
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sendCapabilities(ctx, stream)
		}
	}
}

func (c *ConnectClient) sendCapabilities(ctx context.Context, stream bmcv1.AgentControl_ConnectClient) {
	if ctx.Err() != nil {
		return
	}
	tools := c.prober.Probe(ctx)
	protoTools := make([]*bmcv1.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		protoTools = append(protoTools, &bmcv1.ToolInfo{
			Name:    tool.Name,
			Path:    tool.Path,
			Version: tool.Version,
		})
	}
	sourceMappings := c.cfg.GetSourcePathMappings()
	protoSourceMappings := make([]*bmcv1.PathMapping, 0, len(sourceMappings))
	for _, mapping := range sourceMappings {
		protoSourceMappings = append(protoSourceMappings, &bmcv1.PathMapping{HostPath: mapping.HostPath, RuntimePath: mapping.RuntimePath, ReadOnly: mapping.ReadOnly})
	}
	restoreMappings := c.cfg.GetRestorePathMappings()
	protoRestoreMappings := make([]*bmcv1.PathMapping, 0, len(restoreMappings))
	for _, mapping := range restoreMappings {
		protoRestoreMappings = append(protoRestoreMappings, &bmcv1.PathMapping{HostPath: mapping.HostPath, RuntimePath: mapping.RuntimePath, ReadOnly: mapping.ReadOnly})
	}
	msg := &bmcv1.AgentMessage{
		MessageId: newMessageID(),
		Payload: &bmcv1.AgentMessage_CapabilitiesReport{CapabilitiesReport: &bmcv1.CapabilitiesReport{
			Tools: protoTools, SourcePathMappings: protoSourceMappings, RestorePathMappings: protoRestoreMappings,
		}},
	}
	if err := stream.Send(msg); err != nil {
		log.Printf("[ERROR] capabilities send: %v", err)
	}
}

func (c *ConnectClient) handleServerMessage(ctx context.Context, stream bmcv1.AgentControl_ConnectClient, msg *bmcv1.ServerMessage) error {
	switch payload := msg.Payload.(type) {
	case *bmcv1.ServerMessage_Welcome:
		w := payload.Welcome
		log.Printf("[INFO] server welcome: version=%s instance=%s", w.ServerVersion, w.ServerInstanceId)
		if w.HeartbeatIntervalSeconds > 0 {
			c.mu.Lock()
			c.heartbeatInterval = time.Duration(w.HeartbeatIntervalSeconds) * time.Second
			c.mu.Unlock()
		}
		if w.VersionMinorMismatch {
			log.Printf("[WARN] server version minor mismatch (agent=%s, server=%s)", version.Version, w.ServerVersion)
		}

	case *bmcv1.ServerMessage_ExecuteCommand:
		cmd := payload.ExecuteCommand
		log.Printf("[INFO] execute command: command_id=%s run_id=%s operation=%s", cmd.CommandId, cmd.RunId, operationName(cmd.Operation))
		c.runner.Execute(ctx, stream, cmd)

	case *bmcv1.ServerMessage_CancelCommand:
		cc := payload.CancelCommand
		log.Printf("[INFO] cancel command: run_id=%s", cc.RunId)
		c.runner.Cancel(cc.RunId)

	default:
		log.Printf("[WARN] unknown server message type: %T", payload)
	}
	return nil
}
func operationName(op bmcv1.ExecuteCommand_Operation) string {
	switch op {
	case bmcv1.ExecuteCommand_BACKUP:
		return "backup"
	case bmcv1.ExecuteCommand_RESTORE:
		return "restore"
	case bmcv1.ExecuteCommand_RESTORE_DRY_RUN:
		return "restore_dry_run"
	case bmcv1.ExecuteCommand_CHECK:
		return "check"
	case bmcv1.ExecuteCommand_FORGET:
		return "forget"
	case bmcv1.ExecuteCommand_SNAPSHOTS:
		return "snapshots"
	case bmcv1.ExecuteCommand_SNAPSHOT_LS:
		return "snapshot_ls"
	case bmcv1.ExecuteCommand_VERIFY_STORAGE_REMOTE:
		return "verify_storage_remote"
	case bmcv1.ExecuteCommand_VALIDATE_PATHS:
		return "validate_paths"
	case bmcv1.ExecuteCommand_PROBE_CAPABILITIES:
		return "probe_capabilities"
	default:
		return fmt.Sprintf("unknown(%d)", op)
	}
}

func (c *ConnectClient) backoff(ctx context.Context) {
	// Exponential backoff with jitter: 1s base, 60s cap
	count := c.ReconnectCount()
	base := time.Second
	max := 60 * time.Second
	delay := base * time.Duration(1<<min(count, 6))
	if delay > max {
		delay = max
	}
	// Add jitter: ±25%
	jitter := time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
	log.Printf("[INFO] reconnecting in %v (attempt %d)", jitter, count)

	select {
	case <-ctx.Done():
	case <-time.After(jitter):
	}
}

// newMessageID generates a unique message ID for agent messages.
func newMessageID() string {
	b := make([]byte, 16)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// Ensure proto is used (import cycle avoidance in proto.Message usage)
var _ proto.Message = (*bmcv1.AgentMessage)(nil)
