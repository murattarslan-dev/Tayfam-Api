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

const (
	maxBodyBytes = 16 << 10
	upstreamWait = 10 * time.Second
	// maxIntegrityToken bounds a Play Integrity token (ADR-0022); sign-in
	// and refresh bodies that may carry one get maxAuthBodyBytes.
	maxIntegrityToken = 16 << 10
	maxAuthBodyBytes  = 32 << 10
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
}

type authHandlers struct {
	d AuthDeps
	// webClient accepts sign-in with platform web: the local web build,
	// never in prod (ADR-0017).
	webClient bool
}

func registerAuth(mux *http.ServeMux, d AuthDeps, webClient bool) {
	h := authHandlers{d: d, webClient: webClient}
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
	// IntegrityToken is the optional Play Integrity proof (ADR-0022).
	IntegrityToken string `json:"integrity_token"`
}

// sessionBody is the response of sign-in and refresh. The tokens are
// always in the body; no cookie is set (ADR-0017).
type sessionBody struct {
	UserID              string    `json:"user_id"`
	SessionID           string    `json:"session_id"`
	AccessToken         string    `json:"access_token"`
	AccessExpiresAt     time.Time `json:"access_expires_at"`
	RefreshToken        string    `json:"refresh_token"`
	RefreshExpiresAt    time.Time `json:"refresh_expires_at"`
	RegistrationPending bool      `json:"registration_pending"`
	ConsentRequired     bool      `json:"consent_required"`
	// IntegrityCheckAfter: send integrity_token with the first refresh
	// after it (ADR-0022); absent while checks are off.
	IntegrityCheckAfter *time.Time `json:"integrity_check_after,omitempty"`
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
	if !decodeLimit(w, r, &b, maxAuthBodyBytes) {
		return
	}
	provider, okP := providers[b.Provider]
	platform, okPl := platforms[b.Platform]
	if b.Platform == "web" && !h.webClient {
		okPl = false
	}
	if !okP || !okPl || b.IDToken == "" || len(b.IDToken) > 8192 || len(b.Nonce) < 16 || len(b.Nonce) > 256 ||
		len(b.IntegrityToken) > maxIntegrityToken {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.d.Auth.SignIn(ctx, &identityv1.SignInRequest{
		Provider: provider, IdToken: b.IDToken, Nonce: b.Nonce, Platform: platform, IntegrityToken: b.IntegrityToken,
	})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	h.writeSession(w, resp.GetTokens())
}

type refreshBody struct {
	RefreshToken   string `json:"refresh_token"`
	IntegrityToken string `json:"integrity_token"`
}

func (h authHandlers) refresh(w http.ResponseWriter, r *http.Request) {
	if !h.d.RefreshLimit.Allow(h.clientIP(r)) {
		httpx.WriteProblem(w, r, http.StatusTooManyRequests, "rate-limited", "Too Many Requests", "")
		return
	}
	// The refresh token comes in the body only; a cookie is ignored.
	var b refreshBody
	if !decodeLimit(w, r, &b, maxAuthBodyBytes) {
		return
	}
	if len(b.IntegrityToken) > maxIntegrityToken {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	token := b.RefreshToken
	if token == "" || len(token) > 64 {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "invalid-refresh", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.d.Auth.Refresh(ctx, &identityv1.RefreshRequest{RefreshToken: token, IntegrityToken: b.IntegrityToken})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	h.writeSession(w, resp.GetTokens())
}

func (h authHandlers) signOut(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		httpx.WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Unauthorized", "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	_, err := h.d.Auth.SignOut(auth.WithToken(ctx, token), &identityv1.SignOutRequest{})
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
	token := bearer(r)
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
	// Optional; identity ignores a code that finds nobody.
	ReferralCode string `json:"referral_code"`
}

func (h authHandlers) completeRegistration(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
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
		ReferralCode: b.ReferralCode,
	})
	if err != nil {
		h.upstreamError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h authHandlers) writeSession(w http.ResponseWriter, t *identityv1.Tokens) {
	writeJSON(w, http.StatusOK, sessionBody{
		UserID: t.GetUserId(), SessionID: t.GetSessionId(),
		AccessToken:         t.GetAccessToken(),
		AccessExpiresAt:     t.GetAccessExpireTime().AsTime(),
		RefreshToken:        t.GetRefreshToken(),
		RefreshExpiresAt:    t.GetRefreshExpireTime().AsTime(),
		RegistrationPending: t.GetRegistrationPending(),
		ConsentRequired:     t.GetConsentRequired(),
		IntegrityCheckAfter: optTime(t.GetIntegrityCheckAfter()),
	})
}

func (h authHandlers) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	writeUpstreamError(w, r, h.d.Log, "identity", err)
}

func (h authHandlers) clientIP(r *http.Request) string {
	return clientIP(r, h.d.TrustCFConnectingIP)
}

// bearer returns the access token from the Authorization header, the only
// place it is read from: a cookie, whatever its name, authenticates
// nothing (ADR-0017).
func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
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
