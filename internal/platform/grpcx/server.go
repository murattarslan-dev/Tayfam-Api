// Package grpcx holds the gRPC building blocks shared by services: a server
// with recovery and logging interceptors, the standard health service, and a
// client constructor for internal calls (ADR-0002).
package grpcx

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// MaxMessageBytes bounds request and response sizes. Files never travel over
// gRPC (ADR-0007), so internal messages stay small.
const MaxMessageBytes = 4 << 20

// NewServer returns a gRPC server with panic recovery and access logging.
// Interceptors log the method, status code and duration only; request and
// response bodies are never logged (ADR-0008).
func NewServer(log *slog.Logger, opts ...grpc.ServerOption) *grpc.Server {
	base := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unaryRecover(log), unaryLog(log)),
		grpc.ChainStreamInterceptor(streamRecover(log), streamLog(log)),
		grpc.MaxRecvMsgSize(MaxMessageBytes),
		grpc.MaxSendMsgSize(MaxMessageBytes),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.ConnectionTimeout(5 * time.Second),
	}
	return grpc.NewServer(append(base, opts...)...)
}

// Health is the standard grpc.health.v1 service. Its overall status follows
// the service lifecycle: SERVING after start, NOT_SERVING once draining.
type Health struct {
	srv *health.Server
}

// RegisterHealth registers the health service on s and returns it.
func RegisterHealth(s *grpc.Server) *Health {
	h := &Health{srv: health.NewServer()}
	healthpb.RegisterHealthServer(s, h.srv)
	return h
}

// SetServing marks the whole server as serving.
func (h *Health) SetServing() { h.srv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING) }

// SetDraining marks the whole server as not serving and stops watch streams.
func (h *Health) SetDraining() { h.srv.Shutdown() }

func unaryRecover(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if v := recover(); v != nil {
				logPanic(ctx, log, info.FullMethod, v)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func streamRecover(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if v := recover(); v != nil {
				logPanic(ss.Context(), log, info.FullMethod, v)
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

func logPanic(ctx context.Context, log *slog.Logger, method string, v any) {
	log.LogAttrs(ctx, slog.LevelError, "grpc panic",
		slog.String("method", method),
		slog.Any("panic", v),
		slog.String("stack", string(debug.Stack())),
	)
}

func unaryLog(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isHealth(info.FullMethod) {
			return handler(ctx, req)
		}
		start := time.Now()
		resp, err := handler(ctx, req)
		logCall(ctx, log, info.FullMethod, err, start)
		return resp, err
	}
}

func streamLog(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isHealth(info.FullMethod) {
			return handler(srv, ss)
		}
		start := time.Now()
		err := handler(srv, ss)
		logCall(ss.Context(), log, info.FullMethod, err, start)
		return err
	}
}

func logCall(ctx context.Context, log *slog.Logger, method string, err error, start time.Time) {
	code := status.Code(err)
	level := slog.LevelInfo
	if code == codes.Internal || code == codes.Unknown || code == codes.DataLoss {
		level = slog.LevelError
	}
	log.LogAttrs(ctx, level, "grpc",
		slog.String("method", method),
		slog.String("code", code.String()),
		slog.Duration("duration", time.Since(start)),
	)
}

// isHealth reports whether method belongs to the health service; health
// checks run every few seconds and are not logged.
func isHealth(method string) bool {
	return len(method) > len("/grpc.health.v1.Health/") &&
		method[:len("/grpc.health.v1.Health/")] == "/grpc.health.v1.Health/"
}
