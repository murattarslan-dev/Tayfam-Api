package admin

// Panel sign-in through Google's redirect flow, without any script from
// Google: OpenID Connect with response_type=id_token and
// response_mode=form_post. Google posts the ID token straight to the
// callback; no client secret exists, so none can leak from gateway.
//
//  1. POST /giris/google (CSRF-checked) creates a nonce and a state, keeps
//     them in two short-lived HttpOnly cookies and redirects to Google.
//  2. Google posts id_token and state to POST /auth/google/callback. That
//     request comes from accounts.google.com, so it is exempt from the
//     Origin check; instead the state must match the state cookie
//     (constant time) and identity checks that the ID token carries the
//     nonce of the nonce cookie, was issued in the last 5 minutes and
//     belongs to the Cloudflare Access user (AdminAuthService.AdminSignIn,
//     #107). Both cookies are single use.
//  3. The answer sets the session cookie and continues with a page that
//     navigates on (meta refresh) instead of a redirect: the browser would
//     not send the SameSite=Strict session cookie on a redirect that a
//     cross-site POST started.
//
// The nonce and state cookies must reach the cross-site POST from Google,
// so they are SameSite=None (still __Host-, Secure, HttpOnly, 5 minutes).
// They are useless to anyone else: the nonce only binds an ID token to this
// browser, and the session cookie stays SameSite=Strict.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

// StateCookie holds the state of a Google sign-in and where to go after it.
const StateCookie = "__Host-tayfam_admin_state" // #nosec G101 -- a cookie name, not a credential

// GoogleAuthURL is Google's OpenID Connect authorization endpoint.
const GoogleAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

// CallbackPath is the redirect URI's path; the Google OAuth client lists
// https://<admin host>/auth/google/callback.
const CallbackPath = "/auth/google/callback"

type loginData struct {
	Reauth bool
	Next   string
}

// loginPage shows the sign-in button. With yeniden=1 it asks for a new
// sign-in before a change (ADR-0008, layer 6).
func (h *handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	next := q.Get("devam")
	if !safePath(next) {
		next = "/"
	}
	p := h.newPage(w, r, "", "Giriş")
	p.Data = loginData{Reauth: q.Get("yeniden") == "1", Next: next}
	h.render(w, r, http.StatusOK, "login", p)
}

// startGoogle begins a sign-in: nonce and state in cookies, then Google.
func (h *handlers) startGoogle(w http.ResponseWriter, r *http.Request) {
	next := r.PostFormValue("devam")
	if !safePath(next) {
		next = "/"
	}
	nonce, state := random(), random()
	h.setLoginCookie(w, NonceCookie, nonce)
	h.setLoginCookie(w, StateCookie, state+"."+base64.RawURLEncoding.EncodeToString([]byte(next)))
	v := url.Values{
		"client_id":     {h.cfg.GoogleClientID},
		"redirect_uri":  {h.origin + CallbackPath},
		"response_type": {"id_token"},
		"response_mode": {"form_post"},
		"scope":         {"openid email"},
		"nonce":         {nonce},
		"state":         {state},
		// Choosing the account is an explicit step on every sign-in.
		"prompt": {"select_account"},
	}
	http.Redirect(w, r, GoogleAuthURL+"?"+v.Encode(), http.StatusSeeOther)
}

// googleCallback receives Google's form post.
func (h *handlers) googleCallback(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.SignInLimit.Allow(h.clientIP(r)) {
		h.loginFailed(w, r, http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormSize)
	if err := r.ParseForm(); err != nil {
		h.loginFailed(w, r, http.StatusBadRequest)
		return
	}
	nc, nerr := r.Cookie(NonceCookie)
	sc, serr := r.Cookie(StateCookie)
	// Single use, whatever the outcome.
	h.clearLoginCookie(w, NonceCookie)
	h.clearLoginCookie(w, StateCookie)
	if nerr != nil || serr != nil || len(nc.Value) < 16 || len(nc.Value) > 256 {
		h.loginFailed(w, r, http.StatusBadRequest)
		return
	}
	state, nextEnc, _ := strings.Cut(sc.Value, ".")
	sent := r.PostForm.Get("state")
	if state == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(state)) != 1 {
		h.loginFailed(w, r, http.StatusBadRequest)
		return
	}
	idToken := r.PostForm.Get("id_token")
	if idToken == "" || len(idToken) > maxIDToken {
		// Includes Google's error answers (error=access_denied, ...).
		h.loginFailed(w, r, http.StatusUnauthorized)
		return
	}
	access, _ := r.Context().Value(accessKey{}).(auth.AccessClaims)
	ctx, cancel := context.WithTimeout(r.Context(), upstreamWait)
	defer cancel()
	resp, err := h.cfg.Identity.AdminSignIn(ctx, &identityv1.AdminSignInRequest{
		IdToken: idToken, Nonce: nc.Value, AccessEmail: access.Email,
	})
	if err != nil {
		// Every refusal looks the same (identity says nothing either).
		h.loginFailed(w, r, http.StatusUnauthorized)
		return
	}
	h.setCookie(w, TokenCookie, resp.GetAdminToken(), resp.GetExpireTime().AsTime())
	next := "/"
	if b, err := base64.RawURLEncoding.DecodeString(nextEnc); err == nil && safePath(string(b)) {
		next = string(b)
	}
	p := h.newPage(w, r, "", "Giriş yapıldı")
	p.Data = loginData{Next: next}
	h.render(w, r, http.StatusOK, "continue", p)
}

func (h *handlers) loginFailed(w http.ResponseWriter, r *http.Request, code int) {
	p := h.newPage(w, r, "", "Giriş")
	p.Error = errorText("giris")
	p.Data = loginData{Next: "/"}
	h.render(w, r, code, "login", p)
}

// signOutPage removes the session cookie (see signOut).
func (h *handlers) signOutPage(w http.ResponseWriter, r *http.Request) {
	h.clearCookie(w, TokenCookie)
	http.Redirect(w, r, "/giris", http.StatusSeeOther)
}

func (h *handlers) setLoginCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(nonceTTL.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
}

func (h *handlers) clearLoginCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
	})
}

func random() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	return base64.RawURLEncoding.EncodeToString(b[:])
}
