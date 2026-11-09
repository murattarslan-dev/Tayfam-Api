package rest

import (
	"context"
	"net/http"
	"time"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/httpx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// registerAccountManagement adds the session, consent and account
// deletion routes (issue #75). Like the other account routes, gateway
// verifies the token first and identity verifies it again, together with
// the token's session (ADR-0011).
func registerAccountManagement(mux *http.ServeMux, h coreHandlers) {
	a := h.authed
	mux.HandleFunc("DELETE /v1/me", a(h.deleteAccount))
	mux.HandleFunc("GET /v1/me/sessions", a(h.listSessions))
	mux.HandleFunc("DELETE /v1/me/sessions/{session_id}", a(h.revokeSession))
	mux.HandleFunc("POST /v1/me/sessions/sign-out-others", a(h.revokeOtherSessions))
	mux.HandleFunc("GET /v1/me/consents", a(h.getConsents))
	mux.HandleFunc("POST /v1/me/consents", a(h.acceptConsents))
}

var platformNames = map[identityv1.Platform]string{
	identityv1.Platform_PLATFORM_ANDROID: "android",
	identityv1.Platform_PLATFORM_IOS:     "ios",
	identityv1.Platform_PLATFORM_WEB:     "web",
}

type deviceSessionBody struct {
	SessionID  string    `json:"session_id"`
	Platform   string    `json:"platform"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Current    bool      `json:"current"`
}

type deviceSessionListBody struct {
	Sessions []deviceSessionBody `json:"sessions"`
}

type revokedSessionsBody struct {
	RevokedCount int32 `json:"revoked_count"`
}

type consentStatusBody struct {
	PrivacyNoticeVersion string `json:"privacy_notice_version"`
	TermsVersion         string `json:"terms_version"`
	ConsentRequired      bool   `json:"consent_required"`
}

type consentBody struct {
	PrivacyNoticeVersion string `json:"privacy_notice_version"`
	TermsVersion         string `json:"terms_version"`
}

func (h coreHandlers) deleteAccount(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.Accounts.DeleteAccount(ctx, &identityv1.DeleteAccountRequest{}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	// The account and its sessions are gone; the web client's cookies go
	// with them.
	if _, web := bearer(r); web {
		authHandlers{}.clearCookies(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) listSessions(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Accounts.ListSessions(ctx, &identityv1.ListSessionsRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	out := deviceSessionListBody{Sessions: make([]deviceSessionBody, 0, len(resp.GetSessions()))}
	for _, s := range resp.GetSessions() {
		platform, known := platformNames[s.GetPlatform()]
		if !known {
			platform = "unknown"
		}
		out.Sessions = append(out.Sessions, deviceSessionBody{
			SessionID: s.GetSessionId(), Platform: platform,
			CreatedAt: s.GetCreateTime().AsTime(), LastSeenAt: s.GetLastSeenTime().AsTime(),
			Current: s.GetCurrent(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h coreHandlers) revokeSession(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	sid, ok := id.Parse(r.PathValue("session_id"))
	if !ok {
		httpx.WriteProblem(w, r, http.StatusNotFound, "session-not-found", "Not Found", "")
		return
	}
	if _, err := h.d.Accounts.RevokeSession(ctx, &identityv1.RevokeSessionRequest{SessionId: sid.String()}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h coreHandlers) revokeOtherSessions(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Accounts.RevokeOtherSessions(ctx, &identityv1.RevokeOtherSessionsRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, revokedSessionsBody{RevokedCount: resp.GetRevokedCount()})
}

func (h coreHandlers) getConsents(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	resp, err := h.d.Accounts.GetConsents(ctx, &identityv1.GetConsentsRequest{})
	if err != nil {
		h.identityFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, consentStatusBody{
		PrivacyNoticeVersion: resp.GetPrivacyNoticeVersion(),
		TermsVersion:         resp.GetTermsVersion(),
		ConsentRequired:      resp.GetConsentRequired(),
	})
}

func (h coreHandlers) acceptConsents(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var b consentBody
	if !decode(w, r, &b) {
		return
	}
	if b.PrivacyNoticeVersion == "" || b.TermsVersion == "" || len(b.PrivacyNoticeVersion) > 32 || len(b.TermsVersion) > 32 {
		httpx.WriteProblem(w, r, http.StatusBadRequest, "invalid-request", "Bad Request", "")
		return
	}
	if _, err := h.d.Accounts.AcceptConsents(ctx, &identityv1.AcceptConsentsRequest{
		PrivacyNoticeVersion: b.PrivacyNoticeVersion, TermsVersion: b.TermsVersion,
	}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
