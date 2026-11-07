package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	authHeader   = "authorization"
	bearerPrefix = "Bearer "
	healthPrefix = "/grpc.health.v1.Health/"
)

type callerKey struct{}

type caller struct {
	claims Claims
	token  string
}

// CallerFrom returns the verified caller of the current request.
func CallerFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	return c.claims, ok
}

// Guard enforces authentication on a gRPC server. Every method requires a
// valid access token unless it is listed as public: a new method is
// protected by default (ADR-0004, ADR-0011).
type Guard struct {
	v      *Verifier
	public map[string]bool
}

// NewGuard returns a Guard. publicMethods are full method names, e.g.
// "/tayfam.identity.v1.KeyService/ListPublicKeys". The health service is
// always public.
func NewGuard(v *Verifier, publicMethods ...string) *Guard {
	p := make(map[string]bool, len(publicMethods))
	for _, m := range publicMethods {
		p[m] = true
	}
	return &Guard{v: v, public: p}
}

// ServerOptions returns the interceptors to pass to grpcx.NewServer.
func (g *Guard) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(g.unary),
		grpc.ChainStreamInterceptor(g.stream),
	}
}

func (g *Guard) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, err := g.authenticate(ctx, info.FullMethod)
	if err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (g *Guard) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, err := g.authenticate(ss.Context(), info.FullMethod)
	if err != nil {
		return err
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}

func (g *Guard) authenticate(ctx context.Context, method string) (context.Context, error) {
	// Decide on the route the request actually arrived on, not the name the
	// handler reports: a handler registered under another service name still
	// reports its original method name in the interceptor info.
	if routed, ok := grpc.Method(ctx); ok {
		method = routed
	}
	if g.public[method] || strings.HasPrefix(method, healthPrefix) {
		return ctx, nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(authHeader)
	if len(vals) != 1 || !strings.HasPrefix(vals[0], bearerPrefix) {
		return nil, errUnauthenticated
	}
	token := vals[0][len(bearerPrefix):]
	claims, err := g.v.Verify(ctx, token)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return nil, errUnauthenticated
	case err != nil:
		return nil, status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	}
	return context.WithValue(ctx, callerKey{}, caller{claims: claims, token: token}), nil
}

// errUnauthenticated carries no detail on which check failed.
var errUnauthenticated = status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// ForwardToken returns client options that pass the current caller's access
// token on to the next service unchanged, so it can verify the token itself
// (ADR-0004). Calls made outside a verified request carry no token.
func ForwardToken() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(withToken(ctx), method, req, reply, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(withToken(ctx), desc, cc, method, opts...)
		}),
	}
}

// WithToken attaches a raw access token to an outgoing call. gateway uses it
// for the token it received from the client.
func WithToken(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, authHeader, bearerPrefix+token)
}

func withToken(ctx context.Context) context.Context {
	c, ok := ctx.Value(callerKey{}).(caller)
	if !ok {
		return ctx
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(authHeader)) > 0 {
		return ctx
	}
	return WithToken(ctx, c.token)
}
