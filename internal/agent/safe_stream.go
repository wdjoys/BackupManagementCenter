package agent

import (
	"context"
	"sync"

	"google.golang.org/grpc/metadata"

	bmcv1 "backupmanagementcenter/api/proto/v1"
)

// safeStream wraps the gRPC client stream so concurrent senders (heartbeat,
// capability loop, run runner) never interleave frames. grpc-go streams are
// not safe for concurrent SendMsg calls.
type safeStream struct {
	inner bmcv1.AgentControl_ConnectClient
	mu    sync.Mutex
}

func newSafeStream(inner bmcv1.AgentControl_ConnectClient) *safeStream {
	return &safeStream{inner: inner}
}

func (s *safeStream) Send(msg *bmcv1.AgentMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Send(msg)
}

func (s *safeStream) Recv() (*bmcv1.ServerMessage, error) { return s.inner.Recv() }

func (s *safeStream) CloseSend() error { return nil }

func (s *safeStream) Context() context.Context { return s.inner.Context() }

func (s *safeStream) Header() (metadata.MD, error) { return s.inner.Header() }

func (s *safeStream) Trailer() metadata.MD { return s.inner.Trailer() }

func (s *safeStream) SendMsg(m any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.SendMsg(m)
}

func (s *safeStream) RecvMsg(m any) error { return s.inner.RecvMsg(m) }
