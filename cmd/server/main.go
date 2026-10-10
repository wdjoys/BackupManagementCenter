// Command server is the BMC control plane binary: HTTP API + embedded Web
// UI, gRPC agent channel, scheduler and metrics endpoint.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/logging"
	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/agentreg"
	"backupmanagementcenter/internal/server/api"
	servercfg "backupmanagementcenter/internal/server/config"
	"backupmanagementcenter/internal/server/dispatchgrpc"
	"backupmanagementcenter/internal/server/events"
	"backupmanagementcenter/internal/server/jobs"
	"backupmanagementcenter/internal/server/metrics"
	"backupmanagementcenter/internal/server/notification"
	"backupmanagementcenter/internal/server/scheduler"
	"backupmanagementcenter/internal/server/store"
	"backupmanagementcenter/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-admin":
			runResetAdmin()
			return
		case "version", "-v", "--version":
			fmt.Printf("backup-center-server %s\n", version.Version)
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	log.Printf("[INFO] backup-center-server starting version=%s", version.Version)

	cfg, err := servercfg.LoadServer()
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("[FATAL] data dir: %v", err)
	}

	// Server instance ID: stable random UUID persisted under DataDir.
	instanceID, err := loadOrCreateInstanceID(cfg.DataDir)
	if err != nil {
		log.Fatalf("[FATAL] instance id: %v", err)
	}

	ctx := context.Background()
	// Load the master key before opening SQLite so every encrypted column,
	// including Telegram settings, uses the production sealer. Opening the
	// store first silently selected the development NoopSealer.
	var seal secrets.Sealer
	if !cfg.DevInsecure || cfg.MasterKeyExplicit {
		key, created, err := secrets.LoadOrCreateKey(cfg.MasterKeyFile)
		if err != nil {
			log.Fatalf("[FATAL] %v", err)
		}
		if created {
			log.Printf("[WARN] master key generated at %s; back up this file before storing credentials", cfg.MasterKeyFile)
		}
		seal, err = secrets.NewSealer(key)
		if err != nil {
			log.Fatalf("[FATAL] sealer: %v", err)
		}
	} else {
		log.Printf("[WARN] no master key configured (dev mode): secret sealing disabled")
		seal = secrets.NewNoopSealer()
	}

	dbPath := filepath.Join(cfg.DataDir, "bmc.db")
	// Preserve a consistent copy before applying schema/secret migrations. A
	// marker prevents creating a new copy on every restart; operators can
	// remove it to force another pre-migration backup.
	marker := filepath.Join(cfg.DataDir, ".pre-migration-backup.done")
	if _, statErr := os.Stat(dbPath); statErr == nil {
		if _, markerErr := os.Stat(marker); os.IsNotExist(markerErr) {
			backupPath := dbPath + ".pre-migration-" + time.Now().UTC().Format("20060102T150405Z") + ".bak"
			if backupErr := store.BackupSQLite(ctx, dbPath, backupPath); backupErr != nil {
				log.Printf("[WARN] sqlite pre-migration backup failed: %v", backupErr)
			} else if writeErr := os.WriteFile(marker, []byte(backupPath+"\n"), 0o600); writeErr != nil {
				log.Printf("[WARN] write migration backup marker: %v", writeErr)
			}
		}
	}

	st, err := store.NewWithSealer(dbPath, seal)
	if err != nil {
		log.Fatalf("[FATAL] store: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("[FATAL] migrate: %v", err)
	}
	logStore, ok := st.(store.LogStore)
	if !ok {
		log.Fatalf("[FATAL] process log storage is unavailable")
	}
	serverLogSink := logging.NewSink(os.Stderr, 4096)
	serverLogSink.SetHandler(func(entry logging.Entry) error {
		if err := logStore.AppendServerLogs(ctx, []model.SystemLog{{
			SourceSeq: entry.Seq,
			Timestamp: entry.Timestamp,
			Type:      entry.Type,
			Level:     entry.Level,
			Message:   entry.Message,
		}}); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] persist server log: %v\n", err)
		}
		return nil
	})
	log.SetFlags(0)
	log.SetOutput(serverLogSink)
	slog.SetDefault(slog.New(slog.NewTextHandler(serverLogSink, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.Printf("[INFO] server logging initialized data_dir=%s http=%s grpc=%s metrics=%s", cfg.DataDir, cfg.ListenAddr, cfg.GRPCAddr, cfg.MetricsAddr)

	go periodicSQLiteBackup(dbPath, cfg.DataDir)

	bus := events.New()
	met := metrics.New()
	reg := agentreg.NewRegistry()

	// Failure notifications: Telegram target is configured from the web UI
	// and read per call; unconfigured settings disable sending.
	notifier := notification.NewTelegramNotifier(st, cfg.PublicURL)

	// 在线 Agent 数在抓取时由 registry 计算：此前该 Gauge 从未被赋值，导出恒为 0。
	met.SetAgentsOnlineFunc(func() float64 { return float64(len(reg.List())) })

	// 终态运行计数：此前 bmc_runs_total/bmc_run_duration_seconds 只被写入一个合成的
	// restore_requested 标签，从不统计任何真实终态运行（运维无法据此监控备份成败）。
	// 通过 store 的可选观察者接口在 TransactionRun 提交后统一观测，覆盖全部转换点。
	if os, ok := st.(interface {
		SetRunObserver(store.RunObserver)
	}); ok {
		os.SetRunObserver(terminalRunMetrics{met: met})
	}

	ready := &atomic.Bool{}

	// Orchestrator + dispatcher (Src wired after construction to break the
	// constructor cycle).
	orch := jobs.New(st, seal, nil, bus, instanceID)
	disp := dispatchgrpc.NewDispatcher(st, reg, dispatchgrpc.DefaultConfig(), notifier)
	disp.Src = orch
	disp.Bus = bus
	orch.Disp = disp // break constructor cycle: dispatcher needs orchestrator as CommandSource
	// 恢复的前置授权读取 Agent 当前连接的能力（不是持久化的展示快照）。
	orch.AgentCaps = reg
	disp.StartWatchdog()
	svc := agentreg.NewService(st, reg, bus, agentreg.Config{
		HeartbeatIntervalSeconds: 30,
		OfflineCheckInterval:     30 * time.Second,
		OfflineThreshold:         90 * time.Second,
	}, notifier, orch.WarmSnapshotCache)
	svc.SetMetrics(met)
	// 离线检测循环：把超过 OfflineThreshold 未上报心跳的 Agent 标记为 offline。
	// 此前只接了 Stop 而漏了 Start，导致控制流异常中断（未走干净关闭）的 Agent
	// 永久显示 online —— 指标 bmc_agents_online 只统计活跃流，会与之矛盾，运维
	// 也可能误以为该主机的备份仍在正常工作。
	svc.Start()

	// Restart recovery: retry idempotent work left in-flight, but fail
	// destructive operations because their external side effects are unknown.
	recoverStaleRuns(ctx, st, notifier)

	// Rebuild the durable queue after a restart. Runs that were queued before
	// the process exited must not depend on an in-memory enqueue call.
	if queued, qerr := st.ListRunsByStatus(ctx, []string{model.RunQueued}); qerr != nil {
		log.Printf("[WARN] restart queue recovery: %v", qerr)
	} else {
		for _, run := range queued {
			disp.Enqueue(ctx, run.ID, run.AgentID, run.RepositoryID)
		}
		if len(queued) > 0 {
			log.Printf("[INFO] recovered %d queued runs", len(queued))
		}
	}

	sched := scheduler.New(st, schedAdapter{orch}, notifier, cfg.HistoryRetentionDays)
	sched.Start()
	defer sched.Stop()
	defer disp.StopWatchdog()

	// gRPC listener: TLS with dynamic certificate reload, or plaintext when
	// BMC_TLS_MODE=none (TLS terminated by a reverse proxy).
	tlsCfg, err := serverTLS(cfg)
	if err != nil {
		log.Fatalf("[FATAL] tls: %v", err)
	}
	var gs *grpc.Server
	if tlsCfg != nil {
		gs = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		log.Printf("[WARN] gRPC running WITHOUT TLS - deploy behind a TLS-terminating proxy")
		gs = grpc.NewServer()
	}
	bmcv1.RegisterAgentControlServer(gs, svc)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatalf("[FATAL] grpc listen: %v", err)
	}
	go func() {
		if err := gs.Serve(grpcLis); err != nil {
			log.Printf("[ERROR] grpc serve: %v", err)
		}
	}()
	handler := api.New(&api.Server{
		ST: st, Bus: bus, Met: met, Jobs: orch,
		Version:              version.Version,
		PublicURL:            cfg.PublicURL,
		Reg:                  reg,
		Ready:                ready.Load,
		DatabaseRestoreKinds: kindSet(cfg.DatabaseRestoreKinds),
	})

	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("[FATAL] http listen: %v", err)
	}
	go func() {
		var serr error
		if tlsCfg != nil {
			httpSrv.TLSConfig = tlsCfg
			serr = httpSrv.ServeTLS(ln, "", "")
		} else {
			serr = httpSrv.Serve(ln)
		}
		if serr != nil && serr != http.ErrServerClosed {
			log.Printf("[ERROR] http serve: %v", serr)
		}
	}()

	// Metrics: loopback only per plan.
	go func() {
		if err := http.ListenAndServe(cfg.MetricsAddr, met.Handler()); err != nil {
			log.Printf("[ERROR] metrics serve: %v", err)
		}
	}()

	ready.Store(true)
	log.Printf("[INFO] listening http=%s grpc=%s metrics=%s instance=%s", cfg.ListenAddr, cfg.GRPCAddr, cfg.MetricsAddr, instanceID)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	shCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shCtx)
	gs.GracefulStop()
	svc.Stop()
}

func periodicSQLiteBackup(dbPath, dataDir string) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		backupPath := filepath.Join(dataDir, "bmc.db.daily-"+time.Now().UTC().Format("20060102T150405Z")+".bak")
		if err := store.BackupSQLite(context.Background(), dbPath, backupPath); err != nil {
			log.Printf("[WARN] daily sqlite backup failed: %v", err)
		} else {
			log.Printf("[INFO] daily sqlite backup written: %s", backupPath)
		}
	}
}

func startupRetryable(op string) bool {
	switch op {
	case model.OpBackup, model.OpCheck, model.OpSnapshots, model.OpSnapshotLs,
		model.OpValidatePaths, model.OpProbeCaps, model.OpVerifyRemote:
		return true
	default:
		return false
	}
}

// schedAdapter adapts the orchestrator to the scheduler's narrow interface.
type schedAdapter struct{ o *jobs.Orchestrator }

func (a schedAdapter) StartPlanRun(ctx context.Context, planID string, scheduledAt *time.Time) error {
	_, err := a.o.StartPlanRun(ctx, planID, scheduledAt)
	return err
}

func (a schedAdapter) SystemRunCheck(ctx context.Context, repositoryID string) (string, error) {
	repo, err := a.o.Store.GetRepository(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	if repo.RepositoryPath == "" {
		return "", fmt.Errorf("repository %s has empty repository path", repositoryID)
	}
	params := model.CheckTask{
		Repository: model.RepoAccess{RepositoryPath: repo.RepositoryPath},
	}
	run, err := a.o.SystemRun(ctx, repo.AgentID, repositoryID, model.OpCheck, params, 30*time.Minute)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

func (a schedAdapter) StartRetentionRun(ctx context.Context, repositoryID string) error {
	return a.o.StartRetentionRun(ctx, repositoryID)
}

func (a schedAdapter) TickSnapshotCleanup(ctx context.Context, now time.Time) error {
	return a.o.TickSnapshotCleanup(ctx, now)
}

func serverTLS(cfg servercfg.Server) (*tls.Config, error) {
	if cfg.TLSMode == "none" {
		return nil, nil
	}
	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		if cfg.DevInsecure {
			return nil, nil
		}
		return nil, fmt.Errorf("tls cert/key required outside dev mode")
	}
	if _, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
			if err != nil {
				return nil, fmt.Errorf("reload TLS certificate: %w", err)
			}
			return &cert, nil
		},
	}, nil
}

func loadOrCreateInstanceID(dataDir string) (string, error) {
	p := filepath.Join(dataDir, "instance_id")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 16 {
		return string(b), nil
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	return id, os.WriteFile(p, []byte(id), 0o600)
}

func newUUID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func printUsage() {
	fmt.Println("Usage: backup-center-server [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (no args)    Start the backup management center server")
	fmt.Println("  reset-admin  Reset admin user and active sessions to re-trigger setup initialization")
	fmt.Println("  version      Print version information")
	fmt.Println("  help         Print this help message")
	fmt.Println()
	fmt.Println("Environment Variables:")
	fmt.Println("  BMC_DATA_DIR        Data directory containing bmc.db (default: ./data)")
	fmt.Println("  BMC_MASTER_KEY_FILE Master key file path (default: $BMC_DATA_DIR/master.key)")
}

func runResetAdmin() {
	dataDir := os.Getenv("BMC_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	dbPath := filepath.Join(dataDir, "bmc.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Fatalf("[FATAL] database file not found at %s", dbPath)
	}

	ctx := context.Background()
	seal := secrets.NewNoopSealer()
	st, err := store.NewWithSealer(dbPath, seal)
	if err != nil {
		log.Fatalf("[FATAL] failed to open database: %v", err)
	}
	defer st.Close()

	has, err := st.HasAdmin(ctx)
	if err != nil {
		log.Fatalf("[FATAL] check admin: %v", err)
	}
	if !has {
		fmt.Println("[INFO] No admin user found. Web setup is already active.")
		return
	}

	if err := st.ResetAdmin(ctx); err != nil {
		log.Fatalf("[FATAL] failed to reset admin: %v", err)
	}

	fmt.Println("[SUCCESS] Admin user and sessions have been cleared successfully.")
	fmt.Println("Please restart the server (if running) and visit the Web UI to complete initial setup.")
}

// kindSet 把配置中的 kind 列表转换为查表结构。
func kindSet(kinds []string) map[string]bool {
	out := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		out[k] = true
	}
	return out
}

// terminalRunMetrics 把终态运行折算成指标（时长取 queued→finished）。
type terminalRunMetrics struct{ met *metrics.Metrics }

func (t terminalRunMetrics) ObserveRunTerminal(run model.Run) {
	var d time.Duration
	if run.FinishedAt != nil {
		d = run.FinishedAt.Sub(run.QueuedAt)
		if d < 0 {
			d = 0
		}
	}
	t.met.ObserveRun(run.Operation, run.Status, d)
}

// staleRunStore 是重启对账所需的最小 store 面（便于单测注入真实 store）。
type staleRunStore interface {
	ListRunsByStatus(ctx context.Context, statuses []string) ([]model.Run, error)
	TransitionRun(ctx context.Context, id, from, to string, mutate func(*model.Run)) error
	FinishRestoreRun(ctx context.Context, in store.FinishRestoreRunInput) error
}

type runFailureNotifier interface {
	NotifyPlanFailure(ctx context.Context, runID string) error
}

// recoverStaleRuns 处理重启前遗留的在途 run：可重试的放回队列；破坏性操作
// （备份/恢复）直接失败，并把被中断的恢复请求一并推进到可人工解除的安全终态。
//
// 此前只把恢复 run 置 failed 而不同步 restore_requests.phase，该行会永久停在
// 中间态（如 restoring）：POST /restores/{id}/resolve 以 restore_conflict 拒绝，
// 而中间态不是安全相位，全局数据库恢复占用会被一直占住，重启也不自愈。
func recoverStaleRuns(ctx context.Context, st staleRunStore, notifier runFailureNotifier) {
	stale, err := st.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning})
	if err != nil {
		log.Printf("[WARN] stale run recovery: %v", err)
		return
	}
	for _, run := range stale {
		if startupRetryable(run.Operation) {
			_ = st.TransitionRun(ctx, run.ID, run.Status, model.RunQueued, func(r *model.Run) {
				r.StartedAt = nil
				r.LeaseExpiresAt = nil
				r.ErrorCode = ""
				r.ErrorMessage = ""
			})
			continue
		}
		finished := time.Now().UTC()
		if err := st.TransitionRun(ctx, run.ID, run.Status, model.RunFailed, func(r *model.Run) {
			r.FinishedAt = &finished
			r.ErrorCode = model.ErrAgentDisconnected
			r.ErrorMessage = "server restarted during non-retryable operation"
			r.LeaseExpiresAt = nil
		}); err != nil {
			continue
		}
		if run.Operation == model.OpRestore {
			_ = st.FinishRestoreRun(ctx, store.FinishRestoreRunInput{
				RunID:        run.ID,
				ToStatus:     model.RunFailed,
				FinishedAt:   finished,
				ErrorCode:    model.ErrAgentDisconnected,
				ErrorMessage: "server restarted during non-retryable operation",
				Phase:        model.RestorePhaseManualRecoveryNeeded,
			})
		}
		if rs, ok := st.(interface {
			DeleteRunSecrets(context.Context, string) error
		}); ok {
			_ = rs.DeleteRunSecrets(ctx, run.ID)
		}
		if nerr := notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil {
			notification.LogFailure(run.ID, nerr)
		}
	}
}
