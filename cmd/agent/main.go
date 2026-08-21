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
	"backupmanagementcenter/internal/version"
)

// cfgAdapter adapts config.Agent to agent.ConfigProvider.
type cfgAdapter struct{ c config.Agent }

func (a cfgAdapter) GetServerGRPCURL() string        { return a.c.ServerGRPCURL }
func (a cfgAdapter) GetServerTLS() bool              { return a.c.ServerTLS }
func (a cfgAdapter) GetDevInsecure() bool            { return a.c.DevInsecure }
func (a cfgAdapter) GetProbeInterval() time.Duration { return time.Duration(a.c.ProbeInterval) * time.Second }

func main() {
	log.Printf("[INFO] bmc-agent starting version=%s", version.Version)

	cfg, err := config.LoadAgent()
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

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
		agentID, err := e.Enroll(ectx, cfg.EnrollToken, secret)
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

	runner := agent.NewRunner(pipeline.Deps{}, cfg.DataDir, ident)

	prober := agent.NewProber()
	client := agent.NewConnectClient(cfgAdapter{cfg}, im, prober, runner)

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
