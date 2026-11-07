package grpcx

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
)

// NewClient returns a connection to an internal service, e.g. "core:9090".
// The connection is established lazily on the first call.
//
// Traffic between services is plaintext inside the cluster; only allowed
// callers can reach a service (NetworkPolicy) and every service verifies the
// caller's token itself (ADR-0011). mTLS may be added later.
func NewClient(target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	base := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(MaxMessageBytes),
			grpc.MaxCallSendMsgSize(MaxMessageBytes),
		),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	conn, err := grpc.NewClient(target, append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("grpc istemcisi oluşturulamadı (%s): %w", target, err)
	}
	return conn, nil
}

// HealthCheck returns a function that reports whether the service behind
// conn answers SERVING to the standard health check.
func HealthCheck(conn *grpc.ClientConn) func(ctx context.Context) error {
	client := healthpb.NewHealthClient(conn)
	return func(ctx context.Context) error {
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			return fmt.Errorf("sağlık kontrolü başarısız: %w", err)
		}
		if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return fmt.Errorf("hazır değil: %s", resp.GetStatus())
		}
		return nil
	}
}
