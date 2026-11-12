package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// integrityVerifier passes "PASSED" with the integrity claim, any other
// token but "BAD" without it.
type integrityVerifier struct{}

func (integrityVerifier) Verify(_ context.Context, token string) (auth.Claims, error) {
	switch token {
	case "BAD":
		return auth.Claims{}, auth.ErrInvalidToken
	case "PASSED":
		return auth.Claims{Subject: userID, Integrity: true}, nil
	}
	return auth.Claims{Subject: userID}, nil
}

func integrityRouter(enforce bool) http.Handler {
	d := authTestDeps(&fakeIdentity{}, 100)
	d.Integrity = IntegrityDeps{Enforce: enforce, Verifier: integrityVerifier{}}
	return NewRouter(d)
}

func withToken(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok, clientHeader: "android"}
}

// TestIntegrityEnforced: in enforce mode a token without the claim gets
// integrity-required on every route but the exempt ones; the WebSocket
// handshake too (ADR-0022 §6).
func TestIntegrityEnforced(t *testing.T) {
	h := integrityRouter(true)
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/v1/me/account", "USERTOKEN", http.StatusForbidden},
		{"GET", "/v1/tayfas/" + tayfaID, "USERTOKEN", http.StatusForbidden},
		{"GET", "/v1/realtime", "USERTOKEN", http.StatusForbidden},
		{"GET", "/v1/me/account", "PASSED", http.StatusOK},
		// Exempt: own sessions, consents, account deletion, data export.
		{"GET", "/v1/me/sessions", "USERTOKEN", http.StatusOK},
		{"GET", "/v1/me/consents", "USERTOKEN", http.StatusOK},
		{"DELETE", "/v1/me", "USERTOKEN", http.StatusNoContent},
		{"GET", "/v1/me/data-exports", "USERTOKEN", http.StatusOK},
		// An invalid token is the route's to answer, not integrity's.
		{"GET", "/v1/me/account", "BAD", http.StatusUnauthorized},
	} {
		w := do(h, tc.method, tc.path, "", withToken(tc.token))
		if tc.want == http.StatusForbidden {
			if w.Code != http.StatusForbidden || problemType(t, w) != "integrity-required" {
				t.Errorf("%s %s (%s): %d %s", tc.method, tc.path, tc.token, w.Code, problemType(t, w))
			}
			continue
		}
		if w.Code == http.StatusForbidden && problemType(t, w) == "integrity-required" {
			t.Errorf("%s %s (%s): bütünlük reddi", tc.method, tc.path, tc.token)
		}
	}
	// No token: open routes stay open.
	if w := do(h, "GET", "/v1/status", "", nil); w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
}

// TestIntegrityReportRefusesNothing: off and report modes leave every route
// as it was.
func TestIntegrityReportRefusesNothing(t *testing.T) {
	h := integrityRouter(false)
	for _, path := range []string{"/v1/me/account", "/v1/tayfas/" + tayfaID} {
		if w := do(h, "GET", path, "", withToken("USERTOKEN")); w.Code == http.StatusForbidden {
			t.Errorf("%s: %d %s", path, w.Code, problemType(t, w))
		}
	}
}

// TestIntegrityExemptRoutesExist keeps the exempt list in step with the
// contract: a renamed route would silently lose its exemption.
func TestIntegrityExemptRoutesExist(t *testing.T) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/v1/\S+):$`)
	opRe := regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
	ops := map[string]bool{}
	var path string
	for line := range strings.SplitSeq(string(spec), "\n") {
		if m := pathRe.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		if m := opRe.FindStringSubmatch(line); m != nil && path != "" {
			ops[strings.ToUpper(m[1])+" "+path] = true
		}
	}
	for route := range integrityExempt {
		if !ops[route] {
			t.Errorf("muaf rota sözleşmede yok: %s", route)
		}
	}
	// The KVKK and account routes ADR-0022 names must stay exempt.
	for _, must := range []string{"DELETE /v1/me", "POST /v1/me/data-exports", "POST /v1/auth/refresh"} {
		if !integrityExempt[must] {
			t.Errorf("%s muaf değil", must)
		}
	}
}

// TestIntegrityFieldsPassThrough: the proof reaches identity and the next
// check time comes back; an oversized proof is refused.
func TestIntegrityFieldsPassThrough(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	f := &fakeIdentity{checkAfter: timestamppb.New(at)}
	h := newTestRouter(f)
	body := strings.Replace(signInBodyFor("android"), `{`, `{"integrity_token":"play-1",`, 1)
	w := do(h, "POST", "/v1/auth/sign-in", body, map[string]string{clientHeader: "android"})
	var b sessionBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || f.gotSignIn.GetIntegrityToken() != "play-1" || b.IntegrityCheckAfter == nil || !b.IntegrityCheckAfter.Equal(at) {
		t.Fatalf("giriş: %d %s", w.Code, w.Body)
	}
	w = do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"R","integrity_token":"play-2"}`, map[string]string{clientHeader: "android"})
	if w.Code != 200 || f.gotRefreshReq.GetIntegrityToken() != "play-2" {
		t.Fatalf("yenileme: %d %s", w.Code, w.Body)
	}
	long := strings.Repeat("x", maxIntegrityToken+1)
	w = do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"R","integrity_token":"`+long+`"}`, map[string]string{clientHeader: "android"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("uzun token: %d", w.Code)
	}
	// Checks off: the field is absent.
	f.checkAfter = nil
	w = do(h, "POST", "/v1/auth/refresh", `{"refresh_token":"R"}`, map[string]string{clientHeader: "android"})
	if strings.Contains(w.Body.String(), "integrity_check_after") {
		t.Fatalf("kapalıyken alan var: %s", w.Body)
	}
}
