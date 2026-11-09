package rest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
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

// Cookie names for the web client (ADR-0004).
const (
	accessCookie  = "tayfam_at"
	refreshCookie = "tayfam_rt"
	refreshPath   = "/v1/auth/refresh"
	clientHeader  = "X-Tayfam-Client"
	maxBodyBytes  = 16 << 10
	upstreamWait  = 10 * time.Second
)

// AuthClient is the part of identity's AuthService the gateway uses.
type AuthClient interface {
	SignIn(ctx context.Context, in *identityv1.SignInRequest, opts ...grpc.CallOption) (*identityv1.SignInResponse, error)
	Refresh(ctx context.Context, in *identityv1.RefreshRequest, opts ...grpc.CallOption) (*identityv1.RefreshResponse, error)
	SignOut(ctx context.Context, in *identityv1.SignOutRequest, opts ...grpc.CallOption) (*identityv1.SignOutResponse, error)
}

// RegistrationClient is identity's RegistrationService.
type RegistrationClient interface {
	GetRequirements(ctx context.Context, in *identityv1.GetRequirementsRequest, opts ...grpc.CallOption) (*identityv1.GetRequirementsResponse, error)
	CompleteRegistration(ctx context.Context, in *identityv1.CompleteRegistrationRequest, opts ...grpc.CallOption) (*identityv1.CompleteRegistrationResponse, error)
}

// AuthDeps configures the auth and registration routes.
type AuthDeps struct {
	Auth         AuthClient
	Registration RegistrationClient
	Log          *slog.Logger
	// TrustCFConnectingIP uses Cloudflare's client IP header for rate
	// limits. Only gateway behind cloudflared may set it; NetworkPolicy lets
	// nothing else reach gateway (ADR-0004, ADR-0006).
	TrustCFConnectingIP bool
	SignInLimit         *ratelimit.Limiter
	RefreshLimit        *ratelimit.Limiter
	Now                 func() time.Time
}

type authHandlers struct{ d AuthDeps }

func registerAuth(mux *http.ServeMux, d AuthDeps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	h := authHandlers{d}
	mux.HandleFunc("POST /v1/auth/sign-in", h.signIn)
	mux.HandleFunc("POST /v1/auth/refresh", h.refresh)
	mux.HandleFunc("POST /v1/auth/sign-out", h.signOut)
	mux.HandleFunc("GET /v1/registration/requirements", h.requirements)
	mux.HandleFunc("POST /v1/registration", h.completeRegistration)
}

type signInBody struct {
	Provider string `json:"provider"`
	IDToken  string `json:"id_token"`
	Nonce    string `json:"nonce"`
	Platform string `json:"platform"`
}

// sessionBody is the response of sign-in and refresh. Tokens are omitted
// for the web client, which receives them as cookies.
type sessionBody struct {
	UserID              string    `json:"user_id"`
	SessionID           string    `json:"session_id"`
	AccessToken         string    `json:"access_token,omitempty"`
	AccessExpiresAt     time.Time `json:"access_expires_at"`
	RefreshToken        string    `json:"refresh_token,omitempty"`
	RefreshExpiresAt    time.Time `json:"refresh_expires_at"`
	RegistrationPending bool      `json:"registration_pending"`
}

var providers = map[string]identityv1.Provider{"google": identityv1.Provider_PROVIDER_GOOGLE}

var platforms = map[string]identityv1.Platform{
	"android": identityv1.Platform_PLATFORM_ANDROID,
	"ios":     identityv1.Platform_PLATFORM_IOS,
	"web":     identityv1.Platform_PLATFORM_WEB,
}

func (h authHandlers) signIn(w http.ResponseWriter, r *http.Request) {
	if !h.d.SignInLimit.Allow(h.clientIP(r)) {
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	}
	var b signInBody
	if !decode(w, r, &b) {
		return
	}
	provider, okP := providers[b.Provider]
	platform, okPl := platforms[b.Platform]
	if !okP || !okPl || b.IDToken == "" || len(b.IDToken) > 8192 || len(b.Nonce) < 16 || len(b.Nonce) > 256 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.d.Auth.SignIn(ctx, &identityv1.SignInRequest{Provider: provider, IdToken: b.IDToken, Nonce: b.Nonce, Platform: platform})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	h.writeSession(w, b.Platform == "web", resp.GetTokens())
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

func (h authHandlers) refresh(w http.ResponseWriter, r *http.Request) {
	if !h.d.RefreshLimit.Allow(h.clientIP(r)) {
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	}
	// The web client sends the cookie; mobile clients send the body.
	var token string
	web := false
	if c, err := r.Cookie(refreshCookie); err == nil && c.Value != "" {
		token, web = c.Value, true
	} else {
		var b refreshBody
		if !decode(w, r, &b) {
			return
		}
		token = b.RefreshToken
	}
	if token == "" || len(token) > 64 {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-refresh", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.d.Auth.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: token})
	if err != nil {
		if web && status.Code(err) == codes.Unauthenticated {
			h.clearCookies(w)
		}
		h.upstreamError(w, r, err)
		return
	}
	h.writeSession(w, web, resp.GetTokens())
}

func (h authHandlers) signOut(w http.ResponseWriter, r *http.Request) {
	token, web := bearer(r)
	if token == "" {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	_, err := h.d.Auth.SignOut(auth.WithToken(ctx, token), &identityv1.SignOutRequest{})
	if web {
		h.clearCookies(w)
	}
	if err != nil && status.Code(err) != codes.Unauthenticated {
		h.upstreamError(w, r, err)
		return
	}
	// An expired or unknown token still ends with the client signed out.
	w.WriteHeader(http.StatusNoContent)
}

type requirementsBody struct {
	PrivacyNoticeVersion string `json:"privacy_notice_version"`
	TermsVersion         string `json:"terms_version"`
	MinimumAge           int32  `json:"minimum_age"`
}

func (h authHandlers) requirements(w http.ResponseWriter, r *http.Request) {
	token, _ := bearer(r)
	if token == "" {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.d.Registration.GetRequirements(auth.WithToken(ctx, token), &identityv1.GetRequirementsRequest{})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, requirementsBody{resp.GetPrivacyNoticeVersion(), resp.GetTermsVersion(), resp.GetMinimumAge()})
}

type registrationBody struct {
	BirthDate                    string `json:"birth_date"`
	DisplayName                  string `json:"display_name"`
	AcceptedPrivacyNoticeVersion string `json:"accepted_privacy_notice_version"`
	AcceptedTermsVersion         string `json:"accepted_terms_version"`
}

func (h authHandlers) completeRegistration(w http.ResponseWriter, r *http.Request) {
	token, _ := bearer(r)
	if token == "" {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	var b registrationBody
	if !decode(w, r, &b) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	_, err := h.d.Registration.CompleteRegistration(auth.WithToken(ctx, token), &identityv1.CompleteRegistrationRequest{
		BirthDate: b.BirthDate, DisplayName: b.DisplayName,
		AcceptedPrivacyNoticeVersion: b.AcceptedPrivacyNoticeVersion, AcceptedTermsVersion: b.AcceptedTermsVersion,
	})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h authHandlers) writeSession(w http.ResponseWriter, web bool, t *identityv1.Tokens) {
	body := sessionBody{
		UserID: t.GetUserId(), SessionID: t.GetSessionId(),
		AccessExpiresAt:     t.GetAccessExpireTime().AsTime(),
		RefreshExpiresAt:    t.GetRefreshExpireTime().AsTime(),
		RegistrationPending: t.GetRegistrationPending(),
	}
	if web {
		now := h.d.Now()
		http.SetCookie(w, h.cookie(accessCookie, t.GetAccessToken(), "/", body.AccessExpiresAt.Sub(now)))
		http.SetCookie(w, h.cookie(refreshCookie, t.GetRefreshToken(), refreshPath, body.RefreshExpiresAt.Sub(now)))
	} else {
		body.AccessToken, body.RefreshToken = t.GetAccessToken(), t.GetRefreshToken()
	}
	writeJSON(w, http.StatusOK, body)
}

func (h authHandlers) cookie(name, value, path string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: path,
		MaxAge: max(int(ttl.Seconds()), 1),
		// Secure is always on: browsers accept Secure cookies on
		// http://localhost too, so local development needs no exception.
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	}
}

func (h authHandlers) clearCookies(w http.ResponseWriter) {
	for _, c := range []struct{ name, path string }{{accessCookie, "/"}, {refreshCookie, refreshPath}} {
		http.SetCookie(w, &http.Cookie{Name: c.name, Value: "", Path: c.path, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	}
}

func (h authHandlers) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	writeUpstreamError(w, r, h.d.Log, "identity", err)
}

func (h authHandlers) clientIP(r *http.Request) string {
	return clientIP(r, h.d.TrustCFConnectingIP)
}

// bearer returns the access token from the Authorization header or, for
// the web client, the access cookie.
func bearer(r *http.Request) (token string, web bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):]), false
	}
	if c, err := r.Cookie(accessCookie); err == nil {
		return c.Value, true
	}
	return "", false
}

// decode reads a small JSON body strictly: right content type, size limit,
// no unknown fields, nothing after the object.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, maxBodyBytes)
}

// decodeLimit is decode with a body limit of limit bytes, for the few
// requests that carry long text.
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		httpx.WriteProblem(w, r, http.StatusUnsupportedMediaType, "invalid-request", "Unsupported Media Type", "")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
