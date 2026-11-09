// Package admin is gateway's router for the platform administration panel
// at admin.tayfam.app (ADR-0008, issue #104). Stage 1 is the access layer
// only: the panel's pages (stage 3) and the admin services behind them
// (stage 2) are added on top of it.
//
// The panel is protected by seven independent layers (Notion "Platform
// Yönetim Paneli"); gateway enforces these on every request:
//
//  1. Separate address: the router answers only on the admin host (Mux), and
//     the app API never does. Without Cloudflare Access configuration the
//     router is not built at all and the admin host answers 404.
//  2. and 3. Cloudflare Access: the signed JWT in Cf-Access-Jwt-Assertion is
//     verified against the team's keys and the application's AUD tag. A
//     request without it, or with an invalid one, is refused before any
//     route runs.
//  5. A separate session: the admin token lives in a __Host- cookie
//     (HttpOnly, Secure, SameSite=Strict, Path=/, host-only), is checked
//     with auth.AdminVerifier and is sent to identity, which checks it and
//     platform_admins again. App tokens and cookies are ignored here.
//
// Panel sign-in (layer 4 and the Google side of 5) is identity's
// AdminSignIn: gateway passes the Google ID token, the nonce from its own
// cookie and the e-mail address of the verified Access JWT.
//
// Requests that change state must come from the panel's own origin
// (Origin and Sec-Fetch-Site), in addition to SameSite=Strict.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

// Cookie names. The __Host- prefix makes browsers refuse the cookie unless
// it is Secure, has Path=/ and no Domain, so it is bound to the admin host
// and never sent to tayfam.app or api.tayfam.app.
const (
	TokenCookie = "__Host-tayfam_admin"       // #nosec G101 -- a cookie name, not a credential
	NonceCookie = "__Host-tayfam_admin_nonce" // #nosec G101 -- a cookie name, not a credential
)

const (
	// nonceTTL is how long a sign-in attempt may take.
	nonceTTL     = 5 * time.Minute
	maxBodyBytes = 16 << 10
	maxIDToken   = 8 << 10
	upstreamWait = 10 * time.Second
)

// IdentityClient is identity's AdminAuthService.
type IdentityClient interface {
	AdminSignIn(ctx context.Context, in *identityv1.AdminSignInRequest, opts ...grpc.CallOption) (*identityv1.AdminSignInResponse, error)
	GetAdmin(ctx context.Context, in *identityv1.GetAdminRequest, opts ...grpc.CallOption) (*identityv1.GetAdminResponse, error)
}

// AccessVerifier checks Cloudflare Access JWTs (auth.AccessVerifier).
type AccessVerifier interface {
	Verify(ctx context.Context, token string) (auth.AccessClaims, error)
}

// AdminVerifier checks admin tokens (auth.AdminVerifier).
type AdminVerifier interface {
	Verify(ctx context.Context, token string) (auth.AdminClaims, error)
}

// Config configures the admin router.
type Config struct {
	// Host is the panel's host name, e.g. admin.tayfam.app.
	Host     string
	Access   AccessVerifier
	Admin    AdminVerifier
	Identity IdentityClient
	Log      *slog.Logger
	// SignInLimit limits sign-in attempts per client IP.
	SignInLimit *ratelimit.Limiter
	// TrustCFConnectingIP: see rest.AuthDeps.
	TrustCFConnectingIP bool
	Now                 func() time.Time
}

// NewHandler returns the admin router. Every field of cfg is required.
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Host == "" || cfg.Access == nil || cfg.Admin == nil || cfg.Identity == nil || cfg.Log == nil || cfg.SignInLimit == nil {
		return nil, errors.New("admin: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &handlers{cfg: cfg, origin: "https://" + cfg.Host}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/nonce", h.nonce)
	mux.HandleFunc("POST /auth/sign-in", h.signIn)
	mux.HandleFunc("POST /auth/sign-out", h.signOut)
	mux.Handle("GET /auth/me", h.requireAdmin(http.HandlerFunc(h.me)))
	mux.HandleFunc("/", httpx.NotFound)
	return httpx.Chain(mux, securityHeaders, h.requireAccess, h.sameOrigin), nil
}

// Mux sends requests for the admin host to admin and every other request
// to api. A nil admin (panel not configured) answers 404 on the admin host,
// so the panel's address leads nowhere until Cloudflare Access is set up.
// The app API is never served on the admin host.
func Mux(api http.Handler, adminHost string, admin http.Handler) http.Handler {
	adminHost = strings.ToLower(adminHost)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hostOf(r) != adminHost {
			api.ServeHTTP(w, r)
			return
		}
		if admin == nil {
			httpx.NotFound(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
}

func hostOf(r *http.Request) string {
	h := r.Host
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

type handlers struct {
	cfg    Config
	origin string
}

type accessKey struct{}

type adminKey struct{}

type adminCaller struct {
	claims auth.AdminClaims
	token  string
}

// securityHeaders: nothing on the panel may be framed, sniffed, cached or
// loaded from elsewhere. Stage 3 widens the CSP to the panel's own scripts
// and styles ('self'), never to other origins.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// requireAccess refuses every request without a valid Cloudflare Access
// JWT (ADR-0008, layer 3). Only the header is read, never the
// CF_Authorization cookie, as Cloudflare advises.
func (h *handlers) requireAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vals := r.Header.Values(auth.AccessHeader)
		if len(vals) != 1 {
			httpx.WriteProblem(w, r, http.StatusForbidden, "access-denied", "Forbidden", "")
			return
		}
		claims, err := h.cfg.Access.Verify(r.Context(), vals[0])
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			h.cfg.Log.WarnContext(r.Context(), "Cloudflare Access JWT reddedildi")
			httpx.WriteProblem(w, r, http.StatusForbidden, "access-denied", "Forbidden", "")
			return
		case err != nil:
			h.cfg.Log.WarnContext(r.Context(), "Cloudflare Access anahtarları alınamadı", slog.Any("err", err))
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, claims)))
	})
}

// sameOrigin refuses state-changing requests that do not come from the
// panel's own pages. Browsers always send Origin on such requests.
func (h *handlers) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
		default:
			site := r.Header.Get("Sec-Fetch-Site")
			if r.Header.Get("Origin") != h.origin || (site != "" && site != "same-origin") {
				httpx.WriteProblem(w, r, http.StatusForbidden, "cross-origin", "Forbidden", "")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdmin verifies the admin token cookie. The app's tayfam_at cookie
// and Authorization headers are ignored: on the admin host only the admin
// cookie counts.
func (h *handlers) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(TokenCookie)
		if err != nil || c.Value == "" {
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
			return
		}
		claims, err := h.cfg.Admin.Verify(r.Context(), c.Value)
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			h.clearCookie(w, TokenCookie)
			httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
			return
		case err != nil:
			httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
			return
		}
		ctx := context.WithValue(r.Context(), adminKey{}, adminCaller{claims: claims, token: c.Value})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// nonce starts a sign-in attempt: a random nonce for the Google sign-in,
// kept in an HttpOnly cookie so the ID token can only be used from this
// browser.
func (h *handlers) nonce(w http.ResponseWriter, r *http.Request) {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	n := base64.RawURLEncoding.EncodeToString(b[:])
	h.setCookie(w, NonceCookie, n, h.cfg.Now().Add(nonceTTL))
	writeJSON(w, http.StatusOK, map[string]string{"nonce": n})
}

type signInBody struct {
	IDToken string `json:"id_token"`
}

func (h *handlers) signIn(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.SignInLimit.Allow(h.clientIP(r)) {
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	}
	nc, err := r.Cookie(NonceCookie)
	if err != nil || len(nc.Value) < 16 || len(nc.Value) > 256 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	var b signInBody
	if !decode(w, r, &b) {
		return
	}
	if b.IDToken == "" || len(b.IDToken) > maxIDToken {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	access, _ := r.Context().Value(accessKey{}).(auth.AccessClaims)
	// The nonce is single use: whatever the outcome, the attempt is over.
	h.clearCookie(w, NonceCookie)
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.cfg.Identity.AdminSignIn(ctx, &identityv1.AdminSignInRequest{
		IdToken: b.IDToken, Nonce: nc.Value, AccessEmail: access.Email,
	})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	exp := resp.GetExpireTime().AsTime()
	h.setCookie(w, TokenCookie, resp.GetAdminToken(), exp)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":    resp.GetUserId(),
		"expires_at": exp.UTC(),
		"auth_time":  resp.GetAuthTime().AsTime().UTC(),
	})
}

// signOut removes the panel cookie. The token itself stays valid until it
// expires; removing the platform_admins row ends it at once (identity
// checks the row on every call).
func (h *handlers) signOut(w http.ResponseWriter, _ *http.Request) {
	h.clearCookie(w, TokenCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Context().Value(adminKey{}).(adminCaller)
	ctx, cancel := context.WithTimeout(auth.WithToken(r.Context(), c.token), upstreamWait)
	defer cancel()
	resp, err := h.cfg.Identity.GetAdmin(ctx, &identityv1.GetAdminRequest{})
	if err != nil {
		if status.Code(err) == codes.Unauthenticated {
			h.clearCookie(w, TokenCookie)
		}
		h.upstreamError(w, r, err)
		return
	}
	authTime := resp.GetAuthTime().AsTime().UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":      resp.GetUserId(),
		"display_name": resp.GetDisplayName(),
		"auth_time":    authTime,
		"expires_at":   resp.GetExpireTime().AsTime().UTC(),
		// Until when destructive actions are allowed without signing in
		// again (ADR-0008, layer 6).
		"reauth_until": authTime.Add(auth.AdminReauthWindow),
	})
}

func (h *handlers) setCookie(w http.ResponseWriter, name, value string, expires time.Time) {
	maxAge := int(expires.Sub(h.cfg.Now()).Seconds())
	if maxAge <= 0 {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func (h *handlers) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// upstreamError answers without passing on any internal detail. Every
// refused sign-in looks the same.
func (h *handlers) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	switch st := status.Convert(err); st.Code() {
	case codes.Unauthenticated:
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
	case codes.PermissionDenied:
		httpx.WriteProblem(w, r, http.StatusForbidden, "reauthentication-required", "Forbidden", "")
	case codes.Unavailable, codes.DeadlineExceeded, codes.Unimplemented:
		httpx.WriteProblem(w, r, http.StatusServiceUnavailable, "unavailable", "Service Unavailable", "")
	default:
		h.cfg.Log.ErrorContext(r.Context(), "identity çağrısı başarısız", slog.String("code", st.Code().String()))
		httpx.WriteProblem(w, r, http.StatusInternalServerError, "internal", "Internal Server Error", "")
	}
}

// clientIP: Cloudflare's client IP when trusted, else the peer address.
// Used only as a rate limit key, never logged.
func (h *handlers) clientIP(r *http.Request) string {
	if h.cfg.TrustCFConnectingIP {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		httpx.WriteProblem(w, r, http.StatusUnsupportedMediaType, "unsupported-media-type", "Unsupported Media Type", "")
		return false
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
