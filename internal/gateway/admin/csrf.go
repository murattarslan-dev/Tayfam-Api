package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net/http"
)

// CSRFCookie holds the panel's CSRF token. Every state-changing page
// request must send the same value in the form field "csrf" or the
// X-CSRF-Token header (double submit). The __Host- prefix keeps other
// hosts, subdomains included, from setting or overwriting it; it is
// HttpOnly because the token reaches the page through the server-rendered
// form, never through script. This is in addition to SameSite=Strict on
// the session cookie and the Origin check (sameOrigin).
const CSRFCookie = "__Host-tayfam_admin_csrf" // #nosec G101 -- a cookie name, not a credential

// CSRFField and CSRFHeader carry the token on a request.
const (
	CSRFField  = "csrf"
	CSRFHeader = "X-CSRF-Token"
)

const (
	csrfLen     = 43 // base64url of 32 random bytes
	maxFormSize = 16 << 10
)

// csrfToken returns the browser's CSRF token, setting a new one if it has
// none. It lives as long as the browser session.
func (h *handlers) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(CSRFCookie); err == nil && len(c.Value) == csrfLen {
		return c.Value
	}
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	v := base64.RawURLEncoding.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookie, Value: v, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
	// Later reads in this request see the new value.
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: v})
	return v
}

// requireCSRF parses the form (urlencoded only, size limited) and refuses
// the request unless it carries the browser's CSRF token.
func (h *handlers) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
			h.renderError(w, r, http.StatusUnsupportedMediaType, errorText("gecersiz"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormSize)
		if err := r.ParseForm(); err != nil {
			h.renderError(w, r, http.StatusBadRequest, errorText("gecersiz"))
			return
		}
		if !validCSRF(r) {
			h.renderError(w, r, http.StatusForbidden, errorText("csrf"))
			return
		}
		next(w, r)
	}
}

func validCSRF(r *http.Request) bool {
	c, err := r.Cookie(CSRFCookie)
	if err != nil || len(c.Value) != csrfLen {
		return false
	}
	sent := r.PostForm.Get(CSRFField)
	if sent == "" {
		sent = r.Header.Get(CSRFHeader)
	}
	return subtle.ConstantTimeCompare([]byte(sent), []byte(c.Value)) == 1
}
