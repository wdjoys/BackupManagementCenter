// Command server is the BMC control plane binary: HTTP API + embedded Web
// UI, gRPC agent channel, scheduler and metrics endpoint.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
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

	st, err := store.New(filepath.Join(cfg.DataDir, "bmc.db"))
	if err != nil {
		log.Fatalf("[FATAL] store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("[FATAL] migrate: %v", err)
	}

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

	bus := events.New()
	met := metrics.New()
	reg := agentreg.NewRegistry()

	// Failure notifications: Telegram when both env vars are set (enforced
	// by config), no-op otherwise. One shared instance for every producer.
	var notifier notification.FailureNotifier = notification.NopNotifier{}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		notifier = notification.NewTelegramNotifier(st, cfg.TelegramBotToken, cfg.TelegramChatID, cfg.PublicURL)
		log.Printf("[INFO] telegram plan-failure notifications enabled")
	}

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

	// Restart recovery: runs left dispatched/running by a previous process
	// can no longer be trusted.
	staleIDs, err := st.FailStaleRuns(ctx, []string{"dispatched", "running"}, "server_restarted", time.Now().UTC())
	if err != nil {
		log.Printf("[WARN] stale run recovery: %v", err)
	} else if len(staleIDs) > 0 {
		log.Printf("[INFO] marked %d stale runs failed (server_restarted)", len(staleIDs))
		for _, runID := range staleIDs {
			if nerr := notifier.NotifyPlanFailure(ctx, runID); nerr != nil {
				notification.LogFailure(runID, nerr)
			}
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
