package admin

// The panel's pages (issue #104 stage 3; ADR-0008): server-rendered
// html/template pages with htmx, served only behind the access layer of
// admin.go. Texts are Turkish and rendered here: the panel is not a client
// of the app API, so CLAUDE.md's "no Turkish interface text from the API"
// rule (which is about the app API) does not apply to it.
//
// Browser-side rules:
//   - No external resource: htmx and the stylesheet are embedded in the
//     binary and served from the panel's own origin; the CSP allows scripts,
//     styles, images and connections from 'self' only and no inline script
//     or style.
//   - Every state-changing form carries a CSRF token (csrf.go) on top of the
//     Origin check and the SameSite=Strict session cookie.
//   - Changes need a written reason and a Google sign-in in the last five
//     minutes; without one the page sends the administrator to sign in
//     again before the request reaches core or identity (which check both
//     again themselves).
//   - Pages never show an e-mail address, a birth date, a location, direct
//     message content (except the reporter's own evidence of a reported
//     message) or poll votes; the admin services do not return them and the
//     tests scan the rendered HTML.

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// HTMXPath is where the embedded htmx is served. The version is in the
// name, so a new version is a new file; TestStaticFiles pins its hash.
const HTMXPath = "/static/htmx-2.0.11.min.js"

// Reason limits, as core and identity check them.
const (
	minReason        = 10
	maxReason        = 1000
	maxSuspendReason = 500
	maxTayfaQuery    = 50
	minUserQuery     = 2
	maxUserQuery     = 100
)

func parseTemplates() (map[string]*template.Template, error) {
	// The page templates; each is parsed with layout.html.
	pageNames := []string{"login", "continue", "error", "dashboard", "tayfas", "tayfa", "users", "user", "reports", "report"}
	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("şablon %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}

// page is what every template gets.
type page struct {
	Title string
	Nav   string
	// Admin is the signed-in administrator's display name; empty on the
	// sign-in page.
	Admin string
	CSRF  string
	// ReauthUntil: until when changes are allowed without signing in again.
	ReauthUntil time.Time
	Notice      string
	Error       string
	Data        any
}

// ReauthOK reports whether changes are allowed right now.
func (p page) ReauthOK() bool { return time.Now().Before(p.ReauthUntil) }

type pageKey struct{}

// session is the verified administrator of a page request.
type session struct {
	claims auth.AdminClaims
	token  string
	name   string
}

func sessionFrom(ctx context.Context) session {
	s, _ := ctx.Value(pageKey{}).(session)
	return s
}

// upstream returns a context that carries the admin token to core and
// identity, which verify it themselves (CLAUDE.md rule 8).
func (s session) upstream(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(auth.WithToken(ctx, s.token), upstreamWait)
}

// requirePage lets a page run only with a valid admin token in the panel
// cookie whose user is still a platform administrator (identity checks the
// row on every call). Anything else goes to the sign-in page: the app's
// tayfam_at cookie and Authorization headers are never read here.
func (h *handlers) requirePage(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(TokenCookie)
		if err != nil || c.Value == "" {
			h.toSignIn(w, r, false, here(r))
			return
		}
		claims, err := h.cfg.Admin.Verify(r.Context(), c.Value)
		switch {
		case errors.Is(err, auth.ErrInvalidToken):
			h.clearCookie(w, TokenCookie)
			h.toSignIn(w, r, false, here(r))
			return
		case err != nil:
			h.renderError(w, r, http.StatusServiceUnavailable, msgUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(auth.WithToken(r.Context(), c.Value), upstreamWait)
		defer cancel()
		me, err := h.cfg.Identity.GetAdmin(ctx, &identityv1.GetAdminRequest{})
		if err != nil {
			if status.Code(err) == codes.Unauthenticated {
				h.clearCookie(w, TokenCookie)
				h.toSignIn(w, r, false, here(r))
				return
			}
			h.renderError(w, r, http.StatusServiceUnavailable, msgUnavailable)
			return
		}
		s := session{claims: claims, token: c.Value, name: me.GetDisplayName()}
		next(w, r.WithContext(context.WithValue(r.Context(), pageKey{}, s)))
	})
}

// toSignIn sends the browser to the sign-in page, which returns to back
// afterwards (if it is a panel path). reauth asks for a new Google sign-in
// before a change (ADR-0008, layer 6).
func (h *handlers) toSignIn(w http.ResponseWriter, r *http.Request, reauth bool, back string) {
	q := url.Values{}
	if reauth {
		q.Set("yeniden", "1")
	}
	if back != "/" && safePath(back) {
		q.Set("devam", back)
	}
	target := "/giris"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// here is the page to return to after signing in: the current page for a
// GET, the dashboard for a form (its target is not a page).
func here(r *http.Request) string {
	if r.Method != http.MethodGet {
		return "/"
	}
	if r.URL.RawQuery != "" {
		return r.URL.Path + "?" + r.URL.RawQuery
	}
	return r.URL.Path
}

// safePath accepts only a path on this host: it must start with one slash,
// so "//evil.example" and "/\evil.example" (which browsers read as another
// host) are refused, and must not carry control characters.
func safePath(p string) bool {
	if len(p) == 0 || len(p) > 512 || p[0] != '/' {
		return false
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == '\\') {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	return true
}

func (h *handlers) newPage(w http.ResponseWriter, r *http.Request, nav, title string) page {
	s := sessionFrom(r.Context())
	p := page{Title: title, Nav: nav, Admin: s.name, CSRF: h.csrfToken(w, r)}
	if !s.claims.AuthTime.IsZero() {
		p.ReauthUntil = s.claims.AuthTime.Add(auth.AdminReauthWindow)
	}
	p.Notice = noticeText(r.URL.Query().Get("sonuc"))
	p.Error = errorText(r.URL.Query().Get("hata"))
	return p
}

func (h *handlers) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	t := h.pages[name]
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, "layout.html", p); err != nil {
		h.cfg.Log.ErrorContext(r.Context(), "panel şablonu çalıştırılamadı", slog.String("page", name), slog.Any("err", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b.Bytes())
}

func (h *handlers) renderError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	p := h.newPage(w, r, "", "Hata")
	p.Error = msg
	h.render(w, r, code, "error", p)
}

// done finishes a change with a redirect (post/redirect/get) back to the
// page, with a fixed notice or error code; the query never carries text.
func done(w http.ResponseWriter, r *http.Request, back, key, code string) {
	http.Redirect(w, r, back+"?"+url.Values{key: {code}}.Encode(), http.StatusSeeOther)
}

// failed maps an admin service error to the page's answer. Reasons are
// read from the google.rpc.ErrorInfo detail; core and identity use the
// same ERROR_REASON_* names for these.
func (h *handlers) failed(w http.ResponseWriter, r *http.Request, back string, err error) {
	st := status.Convert(err)
	reason := errorReason(st)
	switch {
	case st.Code() == codes.Unauthenticated:
		h.clearCookie(w, TokenCookie)
		h.toSignIn(w, r, false, here(r))
	case reason == "ERROR_REASON_REAUTHENTICATION_REQUIRED":
		h.toSignIn(w, r, true, back)
	case reason == "ERROR_REASON_REASON_REQUIRED":
		done(w, r, back, "hata", "gerekce")
	case reason == "ERROR_REASON_LAST_ADMIN":
		done(w, r, back, "hata", "son-yonetici")
	case reason == "ERROR_REASON_SANCTION_CONFLICT":
		done(w, r, back, "hata", "yaptirim-var")
	case st.Code() == codes.NotFound:
		done(w, r, back, "hata", "bulunamadi")
	case st.Code() == codes.InvalidArgument:
		done(w, r, back, "hata", "gecersiz")
	case st.Code() == codes.FailedPrecondition || st.Code() == codes.AlreadyExists:
		done(w, r, back, "hata", "durum")
	case st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded:
		done(w, r, back, "hata", "erisilemiyor")
	default:
		h.cfg.Log.ErrorContext(r.Context(), "panel işlemi başarısız", slog.String("code", st.Code().String()), slog.String("reason", reason))
		done(w, r, back, "hata", "beklenmeyen")
	}
}

// failedPage answers a failed read of a page.
func (h *handlers) failedPage(w http.ResponseWriter, r *http.Request, err error) {
	switch st := status.Convert(err); st.Code() {
	case codes.Unauthenticated:
		h.clearCookie(w, TokenCookie)
		h.toSignIn(w, r, false, here(r))
	case codes.NotFound:
		h.renderError(w, r, http.StatusNotFound, errorText("bulunamadi"))
	case codes.InvalidArgument:
		h.renderError(w, r, http.StatusBadRequest, errorText("gecersiz"))
	case codes.Unavailable, codes.DeadlineExceeded:
		h.renderError(w, r, http.StatusServiceUnavailable, msgUnavailable)
	default:
		h.cfg.Log.ErrorContext(r.Context(), "panel sayfası okunamadı", slog.String("code", st.Code().String()))
		h.renderError(w, r, http.StatusInternalServerError, errorText("beklenmeyen"))
	}
}

func errorReason(st *status.Status) string {
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// change runs the checks every change shares, before anything is sent to
// core or identity: a recent Google sign-in and a written reason. It
// returns the trimmed reason, or false after answering.
func (h *handlers) change(w http.ResponseWriter, r *http.Request, back string, maxLen int) (string, bool) {
	s := sessionFrom(r.Context())
	if err := s.claims.RequireRecentAuth(h.cfg.Now()); err != nil {
		h.toSignIn(w, r, true, back)
		return "", false
	}
	reason := strings.TrimSpace(r.PostFormValue("gerekce"))
	if n := utf8.RuneCountInString(reason); !utf8.ValidString(reason) || n < minReason || n > maxLen {
		done(w, r, back, "hata", "gerekce")
		return "", false
	}
	return reason, true
}

// pathID returns the {id} path value if it is a UUID; otherwise it answers
// 404 itself.
func (h *handlers) pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	v := r.PathValue("id")
	if !isUUID(v) {
		h.renderError(w, r, http.StatusNotFound, errorText("bulunamadi"))
		return "", false
	}
	return strings.ToLower(v), true
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}

// --- Pano ---

type dashboardData struct {
	OpenReports   int
	UrgentReports int
	Queue         []*corev1.PlatformReport
	SignedInAt    time.Time
	ExpiresAt     time.Time
}

func (h *handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	s := sessionFrom(r.Context())
	ctx, cancel := s.upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Core.ListPlatformReports(ctx, &corev1.ListPlatformReportsRequest{})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	d := dashboardData{SignedInAt: s.claims.AuthTime, ExpiresAt: s.claims.ExpiresAt}
	for _, rep := range resp.GetReports() {
		if rep.GetResolvedOnPlatform() {
			continue
		}
		d.OpenReports++
		if urgent(rep) {
			d.UrgentReports++
		}
		if len(d.Queue) < 5 {
			d.Queue = append(d.Queue, rep)
		}
	}
	p := h.newPage(w, r, "pano", "Pano")
	p.Data = d
	h.render(w, r, http.StatusOK, "dashboard", p)
}

func urgent(r *corev1.PlatformReport) bool {
	return r.GetPriority() == corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED ||
		r.GetReason() == corev1.ReportReason_REPORT_REASON_CHILD_SAFETY
}

// --- Tayfalar ---

type tayfasData struct {
	Query  string
	Tayfas []*corev1.AdminTayfaSummary
}

func (h *handlers) tayfas(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > maxTayfaQuery || !utf8.ValidString(q) {
		h.renderError(w, r, http.StatusBadRequest, errorText("gecersiz"))
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Core.SearchTayfas(ctx, &corev1.SearchTayfasRequest{Query: q})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "tayfalar", "Tayfalar")
	p.Data = tayfasData{Query: q, Tayfas: resp.GetTayfas()}
	h.render(w, r, http.StatusOK, "tayfas", p)
}

type tayfaData struct {
	Tayfa   *corev1.AdminServiceGetTayfaResponse
	Members []*corev1.AdminMember
	Roles   []corev1.Role
}

func (h *handlers) tayfa(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	t, err := h.cfg.Core.GetTayfa(ctx, &corev1.AdminServiceGetTayfaRequest{TayfaId: id})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	m, err := h.cfg.Core.ListTayfaMembers(ctx, &corev1.ListTayfaMembersRequest{TayfaId: id})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "tayfalar", t.GetTayfa().GetName())
	p.Data = tayfaData{Tayfa: t, Members: m.GetMembers(),
		Roles: []corev1.Role{corev1.Role_ROLE_MEMBER, corev1.Role_ROLE_MODERATOR, corev1.Role_ROLE_ADMIN}}
	h.render(w, r, http.StatusOK, "tayfa", p)
}

func (h *handlers) suspendTayfa(w http.ResponseWriter, r *http.Request) {
	h.tayfaChange(w, r, maxSuspendReason, "askiya-alindi", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Core.SuspendTayfa(ctx, &corev1.SuspendTayfaRequest{TayfaId: id, Reason: reason})
		return err
	})
}

func (h *handlers) unsuspendTayfa(w http.ResponseWriter, r *http.Request) {
	h.tayfaChange(w, r, maxReason, "aski-kaldirildi", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Core.UnsuspendTayfa(ctx, &corev1.UnsuspendTayfaRequest{TayfaId: id, Reason: reason})
		return err
	})
}

func (h *handlers) deleteTayfa(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	// Deleting needs an explicit confirmation besides the reason.
	if r.PostFormValue("onay") != "sil" {
		done(w, r, "/tayfalar/"+id, "hata", "onay")
		return
	}
	h.tayfaChange(w, r, maxReason, "silindi", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Core.DeleteTayfa(ctx, &corev1.AdminServiceDeleteTayfaRequest{TayfaId: id, Reason: reason})
		return err
	})
}

func (h *handlers) setMemberRole(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	user := r.PostFormValue("uye")
	role := parseRole(r.PostFormValue("rol"))
	if !isUUID(user) || role == corev1.Role_ROLE_UNSPECIFIED {
		done(w, r, "/tayfalar/"+id, "hata", "gecersiz")
		return
	}
	h.tayfaChange(w, r, maxReason, "rol-degisti", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Core.SetMemberRole(ctx, &corev1.SetMemberRoleRequest{
			TayfaId: id, UserId: strings.ToLower(user), Role: role, Reason: reason,
		})
		return err
	})
}

func (h *handlers) tayfaChange(w http.ResponseWriter, r *http.Request, maxLen int, notice string, do func(ctx context.Context, id, reason string) error) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	back := "/tayfalar/" + id
	reason, ok := h.change(w, r, back, maxLen)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	if err := do(ctx, id, reason); err != nil {
		h.failed(w, r, back, err)
		return
	}
	done(w, r, back, "sonuc", notice)
}

func parseRole(v string) corev1.Role {
	switch v {
	case "uye":
		return corev1.Role_ROLE_MEMBER
	case "moderator":
		return corev1.Role_ROLE_MODERATOR
	case "yonetici":
		return corev1.Role_ROLE_ADMIN
	}
	return corev1.Role_ROLE_UNSPECIFIED
}

// --- Kullanıcılar ---

type usersData struct {
	Searched bool
	Users    []*identityv1.AdminUserSummary
}

// users shows the search form. The search itself is a POST (searchUsers):
// a query may be an e-mail address, which must not end up in a URL (browser
// history, proxy logs) or be echoed back on the page.
func (h *handlers) users(w http.ResponseWriter, r *http.Request) {
	p := h.newPage(w, r, "kullanicilar", "Kullanıcılar")
	p.Data = usersData{}
	h.render(w, r, http.StatusOK, "users", p)
}

func (h *handlers) searchUsers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.PostFormValue("q"))
	if n := utf8.RuneCountInString(q); !utf8.ValidString(q) || n < minUserQuery || n > maxUserQuery {
		p := h.newPage(w, r, "kullanicilar", "Kullanıcılar")
		p.Error = errorText("arama")
		p.Data = usersData{}
		h.render(w, r, http.StatusBadRequest, "users", p)
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Users.SearchUsers(ctx, &identityv1.SearchUsersRequest{Query: q})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "kullanicilar", "Kullanıcılar")
	p.Data = usersData{Searched: true, Users: resp.GetUsers()}
	h.render(w, r, http.StatusOK, "users", p)
}

type userData struct {
	User      *identityv1.GetUserResponse
	Tayfas    []*corev1.ListUserTayfasResponse_Entry
	Durations []durationOption
}

type durationOption struct {
	Value string
	Label string
	d     time.Duration
}

// suspensionDurations are the lengths a platform suspension can take;
// identity accepts 1 hour to 1 year ahead.
func suspensionDurations() []durationOption {
	day := 24 * time.Hour
	return []durationOption{
		{"1", "1 gün", day}, {"3", "3 gün", 3 * day}, {"7", "7 gün", 7 * day},
		{"30", "30 gün", 30 * day}, {"90", "90 gün", 90 * day}, {"180", "180 gün", 180 * day},
	}
}

func (h *handlers) user(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	u, err := h.cfg.Users.GetUser(ctx, &identityv1.GetUserRequest{UserId: id})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	t, err := h.cfg.Core.ListUserTayfas(ctx, &corev1.ListUserTayfasRequest{UserId: id})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "kullanicilar", u.GetUser().GetDisplayName())
	p.Data = userData{User: u, Tayfas: t.GetTayfas(), Durations: suspensionDurations()}
	h.render(w, r, http.StatusOK, "user", p)
}

func (h *handlers) revokeSessions(w http.ResponseWriter, r *http.Request) {
	h.userChange(w, r, "oturumlar-kapandi", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Users.RevokeUserSessions(ctx, &identityv1.RevokeUserSessionsRequest{UserId: id, Reason: reason})
		return err
	})
}

func (h *handlers) sanctionUser(w http.ResponseWriter, r *http.Request) {
	req := &identityv1.SanctionUserRequest{}
	switch r.PostFormValue("tur") {
	case "aski":
		req.Type = identityv1.SanctionType_SANCTION_TYPE_SUSPEND
		v := r.PostFormValue("sure")
		for _, o := range suspensionDurations() {
			if o.Value == v {
				req.EndTime = timestamppb.New(h.cfg.Now().Add(o.d))
			}
		}
		if req.EndTime == nil {
			h.badUserForm(w, r)
			return
		}
	case "ban":
		req.Type = identityv1.SanctionType_SANCTION_TYPE_BAN
	default:
		h.badUserForm(w, r)
		return
	}
	notice := "askiya-alindi"
	if req.Type == identityv1.SanctionType_SANCTION_TYPE_BAN {
		notice = "banlandi"
	}
	h.userChange(w, r, notice, func(ctx context.Context, id, reason string) error {
		req.UserId, req.Reason = id, reason
		_, err := h.cfg.Users.SanctionUser(ctx, req)
		return err
	})
}

func (h *handlers) badUserForm(w http.ResponseWriter, r *http.Request) {
	if id, ok := h.pathID(w, r); ok {
		done(w, r, "/kullanicilar/"+id, "hata", "gecersiz")
	}
}

func (h *handlers) liftSanction(w http.ResponseWriter, r *http.Request) {
	h.userChange(w, r, "yaptirim-kaldirildi", func(ctx context.Context, id, reason string) error {
		_, err := h.cfg.Users.LiftSanction(ctx, &identityv1.LiftSanctionRequest{UserId: id, Reason: reason})
		return err
	})
}

func (h *handlers) userChange(w http.ResponseWriter, r *http.Request, notice string, do func(ctx context.Context, id, reason string) error) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	back := "/kullanicilar/" + id
	reason, ok := h.change(w, r, back, maxReason)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	if err := do(ctx, id, reason); err != nil {
		h.failed(w, r, back, err)
		return
	}
	done(w, r, back, "sonuc", notice)
}

// --- Şikâyetler ---

type reportsData struct {
	All     bool
	Reports []*corev1.PlatformReport
}

func (h *handlers) reports(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("tumu") == "1"
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Core.ListPlatformReports(ctx, &corev1.ListPlatformReportsRequest{IncludeResolved: all})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "sikayetler", "Şikâyetler")
	p.Data = reportsData{All: all, Reports: resp.GetReports()}
	h.render(w, r, http.StatusOK, "reports", p)
}

type reportData struct {
	Report    *corev1.PlatformReport
	Item      *corev1.ReportedItem
	Decisions []decisionOption
}

type decisionOption struct {
	Value string
	Label string
	d     corev1.PlatformDecision
}

// decisionsFor lists the decisions that fit the report: content removal
// only for content, suspending only with a tayfa, banning only with a
// known reported person.
func decisionsFor(rep *corev1.PlatformReport) []decisionOption {
	var out []decisionOption
	switch rep.GetTargetType() {
	case corev1.ReportTargetType_REPORT_TARGET_TYPE_USER, corev1.ReportTargetType_REPORT_TARGET_TYPE_TAYFA,
		corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE, corev1.ReportTargetType_REPORT_TARGET_TYPE_UNSPECIFIED:
	default:
		out = append(out, decisionOption{"kaldir", "İçeriği kaldır", corev1.PlatformDecision_PLATFORM_DECISION_CONTENT_REMOVED})
	}
	out = append(out, decisionOption{"uyar", "Uyar", corev1.PlatformDecision_PLATFORM_DECISION_WARNED})
	if rep.GetTayfaId() != "" {
		out = append(out, decisionOption{"tayfa-aski", "Tayfayı askıya al", corev1.PlatformDecision_PLATFORM_DECISION_TAYFA_SUSPENDED})
	}
	if rep.GetSubjectUserId() != "" {
		out = append(out, decisionOption{"ban", "Kullanıcıyı banla", corev1.PlatformDecision_PLATFORM_DECISION_USER_BANNED})
	}
	return append(out, decisionOption{"reddet", "Reddet", corev1.PlatformDecision_PLATFORM_DECISION_DISMISSED})
}

// report shows one report and the reported item. core writes the viewing
// to the platform audit log.
func (h *handlers) report(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Core.GetReportedItem(ctx, &corev1.GetReportedItemRequest{ReportId: id})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	p := h.newPage(w, r, "sikayetler", "Şikâyet")
	d := reportData{Report: resp.GetReport(), Item: resp.GetItem()}
	if !d.Report.GetResolvedOnPlatform() {
		d.Decisions = decisionsFor(d.Report)
	}
	p.Data = d
	h.render(w, r, http.StatusOK, "report", p)
}

// decideReport records the platform's decision. It reads the report again
// (core records that as a viewing) so the decision is checked against the
// report itself, not against form fields. A ban is applied in
// identity first (SanctionUser), then recorded on the report in core; if
// the second call fails the ban stands and the decision can be sent again
// (an existing ban is then accepted).
func (h *handlers) decideReport(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	back := "/sikayetler/" + id
	reason, ok := h.change(w, r, back, maxReason)
	if !ok {
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	cur, err := h.cfg.Core.GetReportedItem(ctx, &corev1.GetReportedItemRequest{ReportId: id})
	if err != nil {
		h.failed(w, r, back, err)
		return
	}
	rep := cur.GetReport()
	var decision corev1.PlatformDecision
	for _, o := range decisionsFor(rep) {
		if o.Value == r.PostFormValue("karar") {
			decision = o.d
		}
	}
	if decision == corev1.PlatformDecision_PLATFORM_DECISION_UNSPECIFIED {
		done(w, r, back, "hata", "gecersiz")
		return
	}
	if decision == corev1.PlatformDecision_PLATFORM_DECISION_USER_BANNED {
		if err := h.ban(ctx, rep, reason); err != nil {
			h.failed(w, r, back, err)
			return
		}
	}
	if _, err := h.cfg.Core.DecideReport(ctx, &corev1.DecideReportRequest{ReportId: id, Decision: decision, Reason: reason}); err != nil {
		h.failed(w, r, back, err)
		return
	}
	// Back to the queue, not to the report: opening the report again would
	// write one more viewing to the audit log.
	done(w, r, "/sikayetler", "sonuc", "karar-verildi")
}

func (h *handlers) ban(ctx context.Context, rep *corev1.PlatformReport, reason string) error {
	_, err := h.cfg.Users.SanctionUser(ctx, &identityv1.SanctionUserRequest{
		UserId: rep.GetSubjectUserId(), Type: identityv1.SanctionType_SANCTION_TYPE_BAN,
		Reason: reason, ReportId: rep.GetReportId(),
	})
	if err == nil || errorReason(status.Convert(err)) != "ERROR_REASON_SANCTION_CONFLICT" {
		return err
	}
	// Already sanctioned: fine if it is a ban (an earlier attempt whose
	// decision was not recorded), otherwise the suspension must be lifted
	// first.
	u, gerr := h.cfg.Users.GetUser(ctx, &identityv1.GetUserRequest{UserId: rep.GetSubjectUserId()})
	if gerr == nil && u.GetSanction().GetType() == identityv1.SanctionType_SANCTION_TYPE_BAN {
		return nil
	}
	return err
}

// --- Texts ---

const msgUnavailable = "Servise şu anda ulaşılamıyor. Biraz sonra tekrar deneyin."

// noticeText and errorText give the only texts a redirect can ask for: the
// query carries a code, never text.
func noticeText(code string) string {
	switch code {
	case "askiya-alindi":
		return "Askıya alındı."
	case "aski-kaldirildi":
		return "Askı kaldırıldı."
	case "silindi":
		return "Tayfa silindi."
	case "rol-degisti":
		return "Rol değiştirildi."
	case "oturumlar-kapandi":
		return "Kullanıcının tüm oturumları kapatıldı."
	case "banlandi":
		return "Kullanıcı banlandı."
	case "yaptirim-kaldirildi":
		return "Yaptırım kaldırıldı."
	case "karar-verildi":
		return "Karar kaydedildi."
	}
	return ""
}

func errorText(code string) string {
	switch code {
	case "gerekce":
		return "Gerekçe 10 ile 1000 karakter arasında olmalı (tayfa askısında en fazla 500)."
	case "onay":
		return "Silmek için onay kutusuna \"sil\" yazın."
	case "son-yonetici":
		return "Tayfanın son yöneticisinin rolü düşürülemez."
	case "yaptirim-var":
		return "Kullanıcının etkin bir yaptırımı var; önce onu kaldırın."
	case "bulunamadi":
		return "Kayıt bulunamadı."
	case "gecersiz":
		return "İstek geçersiz."
	case "durum":
		return "İşlem kaydın şu anki durumunda yapılamıyor."
	case "erisilemiyor":
		return msgUnavailable
	case "beklenmeyen":
		return "Beklenmeyen bir hata oluştu."
	case "arama":
		return "Arama 2 ile 100 karakter arasında olmalı."
	case "giris":
		return "Giriş tamamlanamadı. Tekrar deneyin."
	case "csrf":
		return "Form doğrulanamadı. Sayfayı yenileyip tekrar deneyin."
	}
	return ""
}
