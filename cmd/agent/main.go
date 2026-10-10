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

// waitForIdleOrSignal 等到在途任务数归零、或到达 deadline、或收到第二次信号。
// 返回 false 表示收到了第二次信号（调用方应立即退出）。
func waitForIdleOrSignal(inFlight func() int, sigCh <-chan os.Signal, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for inFlight() > 0 && time.Now().Before(deadline) {
		select {
		case <-sigCh:
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return true
}

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

	if n := agent.SweepStaleRunDirs(cfg.DataDir, cfg.RestoreRoots); n > 0 {
		log.Printf("[INFO] cleaned %d stale run temp dir(s) left by a previous process under %s", n, cfg.DataDir)
	}

	runner := agent.NewRunner(pipeline.Deps{
		Exec: agent.OSExecutor{}, SourceRoots: cfg.SourceRoots, RestoreRoots: cfg.RestoreRoots,
		SourcePathMappings: cfg.SourcePathMappings, RestorePathMappings: cfg.RestorePathMappings,
		ScratchMinFreeBytes: cfg.ScratchMinFreeBytes, MaxConcurrency: cfg.MaxConcurrency, ResticCacheDir: cfg.ResticCacheDir,
		ResticCheckReadDataSubset: cfg.ResticCheckReadDataSubset,
		Logf: func(level, format string, args ...any) {
			log.Printf("[%s] "+format, append([]any{level}, args...)...)
		},
	}, cfg.DataDir, ident)

	prober := agent.NewProber()
	runner.SetProber(prober)
	client := agent.NewConnectClient(cfgAdapter{cfg}, im, prober, runner)
	client.SetLogSink(agentLogSink)

	// 优雅关闭：收到 SIGTERM 后先让在途 run 收尾（回滚与结果上报都靠这段时间），
	// 再取消 ctx 退出。不能把 signal.NotifyContext 的 ctx 直接当工作 ctx——它在收到
	// 信号的瞬间就取消，client.Run 立即返回，宽限形同死代码（实测 docker restart
	// 0.5s 返回、在途恢复被硬中断、既不回滚也不上报结果，目标库留在半导入状态）。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-sigCh
		log.Printf("[INFO] shutdown signal received")
		// 阶段一：让在途 run 自然收尾（导出/校验/结果上报都在这段时间）。空闲则
		// 立即进入下一步，不必等满固定宽限（实测空闲重启会白等 20s）。
		if !waitForIdleOrSignal(runner.InFlight, sigCh, 20*time.Second) {
			log.Printf("[INFO] second shutdown signal received; exiting now")
			cancel()
			return
		}
		cancel()
		// 阶段二：cancel 会触发取消路径的回滚（独立预算）。必须等它结束再退出，
		// 否则回滚被打断，目标留在半导入状态、只能靠人工恢复（实测 >20s 的恢复）。
		if !waitForIdleOrSignal(runner.InFlight, sigCh, 90*time.Second) {
			log.Printf("[INFO] second shutdown signal received; exiting now")
		}
	}()

	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("[FATAL] run loop: %v", err)
	}
	log.Printf("[INFO] agent stopped")
}
