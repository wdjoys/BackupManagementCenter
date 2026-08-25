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
	if cfg.MasterKeyFile != "" {
		key, err := secrets.LoadKey(cfg.MasterKeyFile)
		if err != nil {
			log.Fatalf("[FATAL] %v", err)
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

	ready := &atomic.Bool{}

	// Orchestrator + dispatcher (Src wired after construction to break the
	// constructor cycle).
	orch := jobs.New(st, seal, nil, bus, instanceID)
	disp := dispatchgrpc.NewDispatcher(st, reg, dispatchgrpc.DefaultConfig(), notifier)
	disp.Src = orch
	orch.Disp = disp // break constructor cycle: dispatcher needs orchestrator as CommandSource
	disp.StartWatchdog()
	svc := agentreg.NewService(st, reg, bus, agentreg.Config{
		HeartbeatIntervalSeconds: 30,
		OfflineCheckInterval:     30 * time.Second,
		OfflineThreshold:         90 * time.Second,
	}, notifier)

	// Restart recovery: retry idempotent work left in-flight, but fail
	// destructive operations because their external side effects are unknown.
	if stale, listErr := st.ListRunsByStatus(ctx, []string{model.RunDispatched, model.RunRunning}); listErr != nil {
		log.Printf("[WARN] stale run recovery: %v", listErr)
	} else {
		for _, run := range stale {
			if startupRetryable(run.Operation) {
				_ = st.TransitionRun(ctx, run.ID, run.Status, model.RunQueued, func(r *model.Run) { r.StartedAt = nil; r.LeaseExpiresAt = nil; r.ErrorCode = ""; r.ErrorMessage = "" })
				continue
			}
			finished := time.Now().UTC()
			if err := st.TransitionRun(ctx, run.ID, run.Status, model.RunFailed, func(r *model.Run) { r.FinishedAt = &finished; r.ErrorCode = model.ErrAgentDisconnected; r.ErrorMessage = "server restarted during non-retryable operation"; r.LeaseExpiresAt = nil }); err == nil {
				if rs, ok := st.(interface{ DeleteRunSecrets(context.Context, string) error }); ok { _ = rs.DeleteRunSecrets(ctx, run.ID) }
				if nerr := notifier.NotifyPlanFailure(ctx, run.ID); nerr != nil { notification.LogFailure(run.ID, nerr) }
			}
		}
	}

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

	sched := scheduler.New(st, schedAdapter{orch}, notifier)
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
		Version: version.Version,
		PublicURL: cfg.PublicURL,
		Reg:     reg,
		Ready:   ready.Load,
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
	run, err := a.o.SystemRun(ctx, repo.AgentID, repositoryID, model.OpCheck, model.CheckTask{}, 30*time.Minute)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

func (a schedAdapter) StartRetentionRun(ctx context.Context, repositoryID string) error {
	return a.o.StartRetentionRun(ctx, repositoryID)
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
