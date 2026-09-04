// Command agent is the BMC agent binary: it enrolls once, then maintains an
// outbound gRPC control stream to the server and executes dispatched jobs.
package main

import (
	"context"
	"encoding/hex"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"backupmanagementcenter/internal/agent"
	"backupmanagementcenter/internal/agent/config"
	"backupmanagementcenter/internal/agent/pipeline"
	"backupmanagementcenter/internal/logging"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/version"
)

// cfgAdapter adapts config.Agent to agent.ConfigProvider.
type cfgAdapter struct{ c config.Agent }

func (a cfgAdapter) GetServerGRPCURL() string { return a.c.ServerGRPCURL }
func (a cfgAdapter) GetServerTLS() bool       { return a.c.ServerTLS }
func (a cfgAdapter) GetDevInsecure() bool     { return a.c.DevInsecure }
func (a cfgAdapter) GetProbeInterval() time.Duration {
	return time.Duration(a.c.ProbeInterval) * time.Second
}
func (a cfgAdapter) GetSourcePathMappings() []model.PathMapping  { return a.c.SourcePathMappings }
func (a cfgAdapter) GetRestorePathMappings() []model.PathMapping { return a.c.RestorePathMappings }
func main() {

	agentLogSink := logging.NewSink(os.Stderr, 4096)
	log.SetFlags(0)
	log.SetOutput(agentLogSink)
	log.Printf("[INFO] bmc-agent starting version=%s", version.Version)

	cfg, err := config.LoadAgent()
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	log.Printf("[INFO] agent configuration server=%s tls=%t dev_insecure=%t state_dir=%s data_dir=%s restic_cache_dir=%s source_roots=%v restore_roots=%v source_path_mappings=%v restore_path_mappings=%v probe_interval=%ds max_concurrency=%d", cfg.ServerGRPCURL, cfg.ServerTLS, cfg.DevInsecure, cfg.StateDir, cfg.DataDir, cfg.ResticCacheDir, cfg.SourceRoots, cfg.RestoreRoots, cfg.SourcePathMappings, cfg.RestorePathMappings, cfg.ProbeInterval, cfg.MaxConcurrency)
	im := agent.NewIdentityManager(cfg.StateDir)
	ident, created, err := im.LoadOrCreate(cfg.EnrollToken)
	if err != nil {
		log.Fatalf("[FATAL] identity: %v", err)
	}
	if created {
		e := agent.Enroller{ServerGRPCURL: cfg.ServerGRPCURL, ServerTLS: cfg.ServerTLS, DevInsecure: cfg.DevInsecure}
		secret, err := hex.DecodeString(ident.SecretHex)
		if err != nil {
			log.Fatalf("[FATAL] secret decode: %v", err)
		}
		ectx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		agentID, err := e.Enroll(ectx, cfg.EnrollToken, secret, cfg.TargetAgentID)
		cancel()
		if err != nil {
			log.Fatalf("[FATAL] enroll: %v", err)
		}
		if err := im.SetAgentID(agentID); err != nil {
			log.Fatalf("[FATAL] save agent id: %v", err)
		}
		ident.AgentID = agentID
		log.Printf("[INFO] enrolled as agent %s", agentID)
	}

	runner := agent.NewRunner(pipeline.Deps{
		Exec: agent.OSExecutor{}, SourceRoots: cfg.SourceRoots, RestoreRoots: cfg.RestoreRoots,
		SourcePathMappings: cfg.SourcePathMappings, RestorePathMappings: cfg.RestorePathMappings,
		ScratchMinFreeBytes: cfg.ScratchMinFreeBytes, MaxConcurrency: cfg.MaxConcurrency, ResticCacheDir: cfg.ResticCacheDir,
		Logf: func(level, format string, args ...any) {
			log.Printf("[%s] "+format, append([]any{level}, args...)...)
		},
	}, cfg.DataDir, ident)

	prober := agent.NewProber()
	runner.SetProber(prober)
	client := agent.NewConnectClient(cfgAdapter{cfg}, im, prober, runner)
	client.SetLogSink(agentLogSink)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Printf("[INFO] shutdown signal received")
		// Give in-flight runs up to 20s to finish.
		time.Sleep(20 * time.Second)
		stop()
	}()

	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("[FATAL] run loop: %v", err)
	}
	log.Printf("[INFO] agent stopped")
}
