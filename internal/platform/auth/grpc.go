package auth

import (
	"context"
	"errors"
	"fmt"
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

type serviceCallerKey struct{}

type adminCallerKey struct{}

type caller struct {
	claims Claims
	token  string
}

// CallerFrom returns the verified user of the current request. It is never
// set on service methods.
func CallerFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	return c.claims, ok
}

// ServiceCallerFrom returns the verified calling service of a service method
// (ADR-0013). It is never set on user methods.
func ServiceCallerFrom(ctx context.Context) (ServiceClaims, bool) {
	c, ok := ctx.Value(serviceCallerKey{}).(ServiceClaims)
	return c, ok
}

// AdminCallerFrom returns the verified platform administrator of an admin
// method (ADR-0008). It is never set on user or service methods. Being a
// platform administrator is checked by identity on every call; the token
// alone only says who signed in to the panel.
func AdminCallerFrom(ctx context.Context) (AdminClaims, bool) {
	c, ok := ctx.Value(adminCallerKey{}).(adminCaller)
	return c.claims, ok
}

type adminCaller struct {
	claims AdminClaims
	token  string
}

// Guard enforces authentication on a gRPC server. Every method requires a
// valid access token unless it is listed as public: a new method is
// protected by default (ADR-0004, ADR-0011).
//
// Service methods (ADR-0013) are a separate path: the methods of the gRPC
// services named in ServicePolicy.Services accept only service tokens, and
// only from the ServiceAccounts listed for that exact method. A user token
// never reaches a service method and a service token never reaches a user
// method. A service method without a caller list is refused.
type Guard struct {
	v      *Verifier
	public map[string]bool

	sv              *ServiceVerifier
	serviceServices map[string]bool
	callers         map[string]map[string]bool // full method → allowed subjects

	av            *AdminVerifier
	adminServices map[string]bool
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

// ServicePolicy lists the service methods of a server (ADR-0013).
type ServicePolicy struct {
	// Verifier checks service tokens. It may be nil only where there is no
	// cluster (local development); every service method is then refused.
	Verifier *ServiceVerifier
	// Services are gRPC service names, e.g. "tayfam.core.v1.UserEventService",
	// whose every method is a service method.
	Services []string
	// Callers maps a full method name to the ServiceAccounts allowed to call
	// it.
	Callers map[string][]ServiceAccount
}

// WithServices returns g with the service methods of p. It refuses a policy
// that would mix the two paths: a public service method, a caller list for a
// method outside p.Services, an empty or invalid caller.
func (g *Guard) WithServices(p ServicePolicy) (*Guard, error) {
	services := make(map[string]bool, len(p.Services))
	for _, s := range p.Services {
		if s == "" || strings.ContainsAny(s, "/ ") {
			return nil, fmt.Errorf("geçersiz servis adı %q", s)
		}
		services[s] = true
	}
	for m := range g.public {
		if services[serviceOf(m)] {
			return nil, fmt.Errorf("%s hem açık hem servis metodu olamaz", m)
		}
	}
	callers := make(map[string]map[string]bool, len(p.Callers))
	for m, list := range p.Callers {
		if !services[serviceOf(m)] {
			return nil, fmt.Errorf("%s servis metodu olarak tanımlı bir serviste değil", m)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("%s için izinli çağıran yok", m)
		}
		allowed := make(map[string]bool, len(list))
		for _, sa := range list {
			if !sa.Valid() {
				return nil, fmt.Errorf("%s için geçersiz ServiceAccount", m)
			}
			allowed[sa.Subject()] = true
		}
		callers[m] = allowed
	}
	for svc := range services {
		if g.adminServices[svc] {
			return nil, fmt.Errorf("%s hem admin hem servis servisi olamaz", svc)
		}
	}
	out := *g
	out.sv = p.Verifier
	out.serviceServices = services
	out.callers = callers
	return &out, nil
}

// AdminPolicy lists the admin services of a server (ADR-0008). Every
// method of these services accepts only admin tokens; admin tokens are
// accepted nowhere else.
type AdminPolicy struct {
	// Verifier checks admin tokens. It may be nil; every admin method is
	// then refused.
	Verifier *AdminVerifier
	// Services are gRPC service names, e.g. "tayfam.core.v1.AdminService".
	Services []string
}

// WithAdmin returns g with the admin services of p. A public method may sit
// in an admin service (the panel's sign-in); a service may not be both an
// admin service and a service-token service.
func (g *Guard) WithAdmin(p AdminPolicy) (*Guard, error) {
	services := make(map[string]bool, len(p.Services))
	for _, s := range p.Services {
		if s == "" || strings.ContainsAny(s, "/ ") {
			return nil, fmt.Errorf("geçersiz servis adı %q", s)
		}
		if g.serviceServices[s] {
			return nil, fmt.Errorf("%s hem admin hem servis servisi olamaz", s)
		}
		services[s] = true
	}
	out := *g
	out.av = p.Verifier
	out.adminServices = services
	return &out, nil
}

// serviceOf returns "pkg.Service" from "/pkg.Service/Method", or "".
func serviceOf(fullMethod string) string {
	rest, ok := strings.CutPrefix(fullMethod, "/")
	if !ok {
		return ""
	}
	svc, _, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return svc
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
	token, ok := bearerToken(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	if g.serviceServices[serviceOf(method)] {
		return g.authenticateService(ctx, method, token)
	}
	if g.adminServices[serviceOf(method)] {
		return g.authenticateAdmin(ctx, token)
	}
	claims, err := g.v.Verify(ctx, token)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return nil, errUnauthenticated
	case err != nil:
		return nil, status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	}
	return context.WithValue(ctx, callerKey{}, caller{claims: claims, token: token}), nil
}

// authenticateService checks a service method call. A token that does not
// verify as a service token (a user token included) is Unauthenticated; a
// valid service token from a ServiceAccount not listed for the method is
// PermissionDenied. Neither says which check failed.
func (g *Guard) authenticateService(ctx context.Context, method, token string) (context.Context, error) {
	allowed := g.callers[method]
	if g.sv == nil || len(allowed) == 0 {
		return nil, errUnauthenticated
	}
	claims, err := g.sv.Verify(ctx, token)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return nil, errUnauthenticated
	case err != nil:
		return nil, status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	}
	if !allowed[claims.Account.Subject()] {
		return nil, errNotAllowed
	}
	return context.WithValue(ctx, serviceCallerKey{}, claims), nil
}

// authenticateAdmin checks an admin method call. Anything that does not
// verify as an admin token (an access or service token included) is
// Unauthenticated.
func (g *Guard) authenticateAdmin(ctx context.Context, token string) (context.Context, error) {
	if g.av == nil {
		return nil, errUnauthenticated
	}
	claims, err := g.av.Verify(ctx, token)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return nil, errUnauthenticated
	case err != nil:
		return nil, status.Error(codes.Unavailable, "kimlik doğrulama şu anda yapılamıyor")
	}
	return context.WithValue(ctx, adminCallerKey{}, adminCaller{claims: claims, token: token}), nil
}

func bearerToken(ctx context.Context) (string, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(authHeader)
	if len(vals) != 1 || !strings.HasPrefix(vals[0], bearerPrefix) {
		return "", false
	}
	return vals[0][len(bearerPrefix):], true
}

// errNotAllowed: a verified service that may not call this method.
var errNotAllowed = status.Error(codes.PermissionDenied, "bu çağrıya izin yok")

// errUnauthenticated carries no detail on which check failed.
var errUnauthenticated = status.Error(codes.Unauthenticated, "kimlik doğrulanamadı")

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// ForwardToken returns client options that pass the current caller's access
// token (or, on an admin method, admin token) on to the next service
// unchanged, so it can verify the token itself (ADR-0004). Calls made
// outside a verified request carry no token.
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
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get(authHeader)) > 0 {
		return ctx
	}
	if c, ok := ctx.Value(callerKey{}).(caller); ok {
		return WithToken(ctx, c.token)
	}
	// An admin method calling on (core asking identity whether the caller is
	// still a platform administrator) passes the admin token unchanged.
	if c, ok := ctx.Value(adminCallerKey{}).(adminCaller); ok && c.token != "" {
		return WithToken(ctx, c.token)
	}
	return ctx
}

// WithCallerForTest returns ctx carrying c as the verified caller, as Guard
// would set it. Only tests may use it; production code gets callers from
// Guard after the token is verified.
func WithCallerForTest(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, callerKey{}, caller{claims: c})
}

// WithAdminCallerForTest returns ctx carrying c as the verified platform
// administrator, as Guard would set it on an admin method. Only tests may
// use it.
func WithAdminCallerForTest(ctx context.Context, c AdminClaims) context.Context {
	return context.WithValue(ctx, adminCallerKey{}, adminCaller{claims: c})
}

// WithAdminTokenForTest is WithAdminCallerForTest that also keeps the raw
// admin token, so ForwardToken passes it on. Only tests may use it.
func WithAdminTokenForTest(ctx context.Context, c AdminClaims, token string) context.Context {
	return context.WithValue(ctx, adminCallerKey{}, adminCaller{claims: c, token: token})
}

// WithServiceCallerForTest returns ctx carrying c as the verified calling
// service, as Guard would set it on a service method. Only tests may use it.
func WithServiceCallerForTest(ctx context.Context, c ServiceClaims) context.Context {
	return context.WithValue(ctx, serviceCallerKey{}, c)
}
