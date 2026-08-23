package agent

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsRevokedErr(t *testing.T) {
	rpc := status.Error(codes.PermissionDenied, "agent is revoked")
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain rpc", rpc, true},
		{"wrapped connect", fmt.Errorf("connect: %w", rpc), true},
		{"wrapped recv", fmt.Errorf("recv: %w", rpc), true},
		// PermissionDenied is also used for version mismatch; must keep retrying.
		{"version mismatch", status.Error(codes.PermissionDenied, "major version mismatch: server=1, agent=0"), false},
		{"unavailable", status.Error(codes.Unavailable, "connection refused"), false},
		{"plain error", errors.New("boom"), false},
		// String fallback when gRPC status cannot be unwrapped.
		{"string fallback", errors.New("rpc error: code = PermissionDenied desc = agent is revoked"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRevokedErr(tc.err); got != tc.want {
				t.Fatalf("isRevokedErr() = %v, want %v", got, tc.want)
			}
		})
	}
}
