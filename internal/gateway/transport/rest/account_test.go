package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

const sessionID = "01965a3c-0000-7000-8000-000000000030"

// Account management on fakeCore (AccountClient, issue #75).

func (f *fakeCore) ListSessions(ctx context.Context, in *identityv1.ListSessionsRequest, _ ...grpc.CallOption) (*identityv1.ListSessionsResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &identityv1.ListSessionsResponse{Sessions: []*identityv1.Session{
		{SessionId: sessionID, Platform: identityv1.Platform_PLATFORM_IOS, CreateTime: timestamppb.New(t0), LastSeenTime: timestamppb.New(t0.Add(time.Hour)), Current: true},
		{SessionId: "s2", Platform: identityv1.Platform(99), CreateTime: timestamppb.New(t0), LastSeenTime: timestamppb.New(t0)},
	}}, nil
}
func (f *fakeCore) RevokeSession(ctx context.Context, in *identityv1.RevokeSessionRequest, _ ...grpc.CallOption) (*identityv1.RevokeSessionResponse, error) {
	return &identityv1.RevokeSessionResponse{}, f.record(ctx, in)
}
func (f *fakeCore) RevokeOtherSessions(ctx context.Context, in *identityv1.RevokeOtherSessionsRequest, _ ...grpc.CallOption) (*identityv1.RevokeOtherSessionsResponse, error) {
	return &identityv1.RevokeOtherSessionsResponse{RevokedCount: 2}, f.record(ctx, in)
}
func (f *fakeCore) GetConsents(ctx context.Context, in *identityv1.GetConsentsRequest, _ ...grpc.CallOption) (*identityv1.GetConsentsResponse, error) {
	return &identityv1.GetConsentsResponse{PrivacyNoticeVersion: "2026-12-01", TermsVersion: "2026-11-08", ConsentRequired: true}, f.record(ctx, in)
}
func (f *fakeCore) AcceptConsents(ctx context.Context, in *identityv1.AcceptConsentsRequest, _ ...grpc.CallOption) (*identityv1.AcceptConsentsResponse, error) {
	return &identityv1.AcceptConsentsResponse{}, f.record(ctx, in)
}
func (f *fakeCore) DeleteAccount(ctx context.Context, in *identityv1.DeleteAccountRequest, _ ...grpc.CallOption) (*identityv1.DeleteAccountResponse, error) {
	return &identityv1.DeleteAccountResponse{}, f.record(ctx, in)
}

var accountRoutes = []struct{ method, path, body string }{
	{"DELETE", "/v1/me", ""},
	{"GET", "/v1/me/sessions", ""},
	{"DELETE", "/v1/me/sessions/" + sessionID, ""},
	{"POST", "/v1/me/sessions/sign-out-others", ""},
	{"GET", "/v1/me/consents", ""},
	{"POST", "/v1/me/consents", `{"privacy_notice_version":"a","terms_version":"b"}`},
}

func TestAccountRoutesNeedToken(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	for _, rt := range accountRoutes {
		for name, hdr := range map[string]map[string]string{
			"token yok":      {clientHeader: "android"},
			"geçersiz token": {"Authorization": "Bearer BAD", clientHeader: "android"},
		} {
			w := do(h, rt.method, rt.path, rt.body, hdr)
			if w.Code != http.StatusUnauthorized || problemType(t, w) != "unauthenticated" {
				t.Errorf("%s %s %s: %d %s", name, rt.method, rt.path, w.Code, w.Body)
			}
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatal("geçersiz token'la identity çağrıldı")
	}
	// State-changing calls need the CSRF header.
	w := do(h, "DELETE", "/v1/me", "", map[string]string{"Authorization": "Bearer USERTOKEN"})
	if _, _, n := f.last(); w.Code != http.StatusBadRequest || problemType(t, w) != "missing-client-header" || n != 0 {
		t.Fatalf("X-Tayfam-Client olmadan silme: %d", w.Code)
	}
}

func TestDeleteAccountRoute(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "DELETE", "/v1/me", "")
	got, token, n := f.last()
	if _, ok := got.(*identityv1.DeleteAccountRequest); w.Code != http.StatusNoContent || !ok || token != "USERTOKEN" || n != 1 {
		t.Fatalf("silme: %d %T %q", w.Code, got, token)
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("mobil silmede çerez yazıldı")
	}

	// Web: cookies are cleared after deletion.
	w = do(h, "DELETE", "/v1/me", "", map[string]string{"Cookie": accessCookie + "=WEBTOKEN", clientHeader: "web"})
	if _, token, _ := f.last(); w.Code != http.StatusNoContent || token != "WEBTOKEN" {
		t.Fatalf("web silme: %d", w.Code)
	}
	cleared := 0
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared++
		}
	}
	if cleared != 2 {
		t.Fatalf("silinen çerez = %d", cleared)
	}

	// Reauthentication required: 403, cookies kept (the user signs in again).
	f.err = identityErr(codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED")
	w = do(h, "DELETE", "/v1/me", "", map[string]string{"Cookie": accessCookie + "=WEBTOKEN", clientHeader: "web"})
	if w.Code != http.StatusForbidden || problemType(t, w) != "reauthentication-required" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("yeniden doğrulama: %d %s", w.Code, w.Body)
	}
	// A deleted account's leftover token: identity says unauthenticated.
	f.err = status.Error(codes.Unauthenticated, "x")
	if w := as(h, "DELETE", "/v1/me", ""); w.Code != http.StatusUnauthorized || problemType(t, w) != "unauthenticated" {
		t.Fatalf("silinmiş hesabın token'ı: %d", w.Code)
	}
}

func TestSessionRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "GET", "/v1/me/sessions", "")
	if w.Code != 200 {
		t.Fatalf("liste: %d %s", w.Code, w.Body)
	}
	list := decodeBody[deviceSessionListBody](t, w)
	if len(list.Sessions) != 2 {
		t.Fatalf("liste gövdesi: %s", w.Body)
	}
	if s := list.Sessions[0]; s.SessionID != sessionID || s.Platform != "ios" || !s.Current || !s.LastSeenAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("oturum = %+v", s)
	}
	if list.Sessions[1].Platform != "unknown" {
		t.Fatalf("bilinmeyen platform = %q", list.Sessions[1].Platform)
	}
	var raw map[string][]map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for k := range raw["sessions"][0] {
		switch k {
		case "session_id", "platform", "created_at", "last_seen_at", "current":
		default:
			t.Fatalf("beklenmeyen alan %q", k)
		}
	}

	w = as(h, "DELETE", "/v1/me/sessions/"+sessionID, "")
	if got, _, _ := f.last(); w.Code != http.StatusNoContent || got.(*identityv1.RevokeSessionRequest).GetSessionId() != sessionID {
		t.Fatalf("iptal: %d %v", w.Code, got)
	}
	_, _, before := f.last()
	if w := as(h, "DELETE", "/v1/me/sessions/not-a-uuid", ""); w.Code != http.StatusNotFound || problemType(t, w) != "session-not-found" {
		t.Fatalf("geçersiz kimlik: %d", w.Code)
	}
	if _, _, n := f.last(); n != before {
		t.Fatal("geçersiz oturum kimliği identity'ye ulaştı")
	}
	f.err = identityErr(codes.NotFound, "ERROR_REASON_SESSION_NOT_FOUND")
	if w := as(h, "DELETE", "/v1/me/sessions/"+sessionID, ""); w.Code != http.StatusNotFound || problemType(t, w) != "session-not-found" {
		t.Fatalf("başkasının oturumu: %d", w.Code)
	}
	f.err = nil

	w = as(h, "POST", "/v1/me/sessions/sign-out-others", "")
	if rv := decodeBody[revokedSessionsBody](t, w); w.Code != 200 || rv.RevokedCount != 2 {
		t.Fatalf("diğerleri: %d %s", w.Code, w.Body)
	}
}

func TestConsentRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/me/consents", "")
	if st := decodeBody[consentStatusBody](t, w); w.Code != 200 || !st.ConsentRequired || st.PrivacyNoticeVersion != "2026-12-01" {
		t.Fatalf("durum: %d %s", w.Code, w.Body)
	}
	w = as(h, "POST", "/v1/me/consents", `{"privacy_notice_version":"2026-12-01","terms_version":"2026-11-08"}`)
	got, _, n := f.last()
	if in, ok := got.(*identityv1.AcceptConsentsRequest); w.Code != http.StatusNoContent || !ok || in.GetPrivacyNoticeVersion() != "2026-12-01" || in.GetTermsVersion() != "2026-11-08" {
		t.Fatalf("onay: %d %v", w.Code, got)
	}
	for _, body := range []string{
		`{"privacy_notice_version":"","terms_version":"x"}`,
		`{"privacy_notice_version":"012345678901234567890123456789012","terms_version":"x"}`,
		`{"privacy_notice_version":"a","terms_version":"b","fazla":1}`,
	} {
		if w := as(h, "POST", "/v1/me/consents", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if _, _, m := f.last(); m != n {
		t.Fatal("geçersiz gövde identity'ye ulaştı")
	}
	f.err = identityErr(codes.FailedPrecondition, "ERROR_REASON_CONSENT_OUTDATED")
	if w := as(h, "POST", "/v1/me/consents", `{"privacy_notice_version":"a","terms_version":"b"}`); w.Code != http.StatusConflict || problemType(t, w) != "consent-outdated" {
		t.Fatalf("eski sürüm: %d", w.Code)
	}
}

func TestSessionBodyCarriesConsentRequired(t *testing.T) {
	f := &fakeIdentity{consentRequired: true}
	w := do(newTestRouter(f), "POST", "/v1/auth/sign-in", signInBodyFor("android"), map[string]string{clientHeader: "android"})
	var b map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || b["consent_required"] != true {
		t.Fatalf("consent_required: %d %s", w.Code, w.Body)
	}
}
