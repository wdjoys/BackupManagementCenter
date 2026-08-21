package agent

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"runtime"

	bmcv1 "backupmanagementcenter/api/proto/v1"
	"backupmanagementcenter/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Enroller performs the one-time Enroll RPC.
type Enroller struct {
	ServerGRPCURL string
	ServerTLS     bool
	DevInsecure   bool
}

// Enroll submits the enrollment token and returns the assigned agent ID.
func (e *Enroller) Enroll(ctx context.Context, enrollToken string, secret []byte) (string, error) {
	conn, err := e.dial()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	client := bmcv1.NewAgentControlClient(conn)
	req := &bmcv1.EnrollRequest{
		EnrollmentToken: enrollToken,
		Hostname:        hostname,
		Os:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		Version:         version.Version,
		Secret:          secret,
	}
	resp, err := client.Enroll(ctx, req)
	if err != nil {
		return "", fmt.Errorf("enroll: %w", err)
	}
	if resp.AgentId == "" {
		return "", fmt.Errorf("enroll: server returned empty agent id")
	}
	return resp.AgentId, nil
}

func (e *Enroller) dial() (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{}
	if e.ServerTLS {
		creds := credentials.NewTLS(e.tlsConfig())
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	return grpc.NewClient(e.ServerGRPCURL, opts...)
}

func (e *Enroller) tlsConfig() *tls.Config {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if e.DevInsecure {
		cfg.InsecureSkipVerify = true
	}
	return cfg
}