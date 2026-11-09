package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
)

const (
	googleClient = "panel-test.apps.googleusercontent.com"
	tayfaID      = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a60"
	memberID     = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a61"
	reportID     = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a62"
	dmReportID   = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a63"
	xssName      = `<script>alert("x")</script><img src=x onerror=alert(1)>`
	evidence     = "Şikâyet edenin kendi kopyası: kanıt metni"
	goodReason   = "Topluluk kurallarına aykırı davranış"
	// hidden is a value that must never appear on a page: the e-mail
	// address an administrator searched for.
	hidden = "gizli.kisi@ornek.test"
)

// calls records the admin service methods a test reached.
type calls struct {
	mu   sync.Mutex
	list []string
	// tokens are the bearer tokens the calls carried.
	tokens []string
}

func (c *calls) add(ctx context.Context, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, name)
	c.tokens = append(c.tokens, bearer(ctx))
}

func (c *calls) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.list...)
}

func (c *calls) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list, c.tokens = nil, nil
}

func bearer(ctx context.Context) string {
	md, _ := metadata.FromOutgoingContext(ctx)
	if v := md.Get("authorization"); len(v) == 1 {
		return strings.TrimPrefix(v[0], "Bearer ")
	}
	return ""
}

type fakeCore struct {
	corev1.AdminServiceClient // unimplemented methods panic
	*calls
	tayfaName string
	err       map[string]error
}

func newFakeCore(c *calls) *fakeCore {
	return &fakeCore{calls: c, tayfaName: "Klasikçiler", err: map[string]error{}}
}

func (f *fakeCore) summary() *corev1.AdminTayfaSummary {
	return &corev1.AdminTayfaSummary{TayfaId: tayfaID, Name: f.tayfaName, Status: corev1.TayfaStatus_TAYFA_STATUS_ACTIVE,
		Listed: true, MemberCount: 3, CreateTime: timestamppb.Now()}
}

func (f *fakeCore) SearchTayfas(ctx context.Context, _ *corev1.SearchTayfasRequest, _ ...grpc.CallOption) (*corev1.SearchTayfasResponse, error) {
	f.add(ctx, "SearchTayfas")
	return &corev1.SearchTayfasResponse{Tayfas: []*corev1.AdminTayfaSummary{f.summary()}}, f.err["SearchTayfas"]
}

func (f *fakeCore) GetTayfa(ctx context.Context, _ *corev1.AdminServiceGetTayfaRequest, _ ...grpc.CallOption) (*corev1.AdminServiceGetTayfaResponse, error) {
	f.add(ctx, "GetTayfa")
	return &corev1.AdminServiceGetTayfaResponse{Tayfa: f.summary(), Description: f.tayfaName + " açıklaması", City: "İzmir",
		Staff: []*corev1.AdminMember{{UserId: memberID, DisplayName: "Ayşe", Role: corev1.Role_ROLE_ADMIN}}}, f.err["GetTayfa"]
}

func (f *fakeCore) ListTayfaMembers(ctx context.Context, _ *corev1.ListTayfaMembersRequest, _ ...grpc.CallOption) (*corev1.ListTayfaMembersResponse, error) {
	f.add(ctx, "ListTayfaMembers")
	return &corev1.ListTayfaMembersResponse{Members: []*corev1.AdminMember{
		{UserId: memberID, DisplayName: "Ayşe", Role: corev1.Role_ROLE_ADMIN, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, JoinTime: timestamppb.Now()},
	}}, nil
}

func (f *fakeCore) ListUserTayfas(ctx context.Context, _ *corev1.ListUserTayfasRequest, _ ...grpc.CallOption) (*corev1.ListUserTayfasResponse, error) {
	f.add(ctx, "ListUserTayfas")
	return &corev1.ListUserTayfasResponse{Tayfas: []*corev1.ListUserTayfasResponse_Entry{{Tayfa: f.summary(), Role: corev1.Role_ROLE_MEMBER}}}, nil
}

func (f *fakeCore) SuspendTayfa(ctx context.Context, _ *corev1.SuspendTayfaRequest, _ ...grpc.CallOption) (*corev1.SuspendTayfaResponse, error) {
	f.add(ctx, "SuspendTayfa")
	return &corev1.SuspendTayfaResponse{}, f.err["SuspendTayfa"]
}

func (f *fakeCore) UnsuspendTayfa(ctx context.Context, _ *corev1.UnsuspendTayfaRequest, _ ...grpc.CallOption) (*corev1.UnsuspendTayfaResponse, error) {
	f.add(ctx, "UnsuspendTayfa")
	return &corev1.UnsuspendTayfaResponse{}, nil
}

func (f *fakeCore) DeleteTayfa(ctx context.Context, _ *corev1.AdminServiceDeleteTayfaRequest, _ ...grpc.CallOption) (*corev1.AdminServiceDeleteTayfaResponse, error) {
	f.add(ctx, "DeleteTayfa")
	return &corev1.AdminServiceDeleteTayfaResponse{}, nil
}

func (f *fakeCore) SetMemberRole(ctx context.Context, _ *corev1.SetMemberRoleRequest, _ ...grpc.CallOption) (*corev1.SetMemberRoleResponse, error) {
	f.add(ctx, "SetMemberRole")
	return &corev1.SetMemberRoleResponse{}, f.err["SetMemberRole"]
}

func (f *fakeCore) reportList() []*corev1.PlatformReport {
	return []*corev1.PlatformReport{
		{ReportId: dmReportID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE, TargetId: memberID,
			SubjectUserId: memberID, ReporterId: userID, Reason: corev1.ReportReason_REPORT_REASON_CHILD_SAFETY,
			Priority: corev1.ReportPriority_REPORT_PRIORITY_MINOR_INVOLVED, EscalateTime: timestamppb.Now()},
		{ReportId: reportID, TayfaId: tayfaID, TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT, TargetId: memberID,
			SubjectUserId: memberID, ReporterId: userID, Reason: corev1.ReportReason_REPORT_REASON_SPAM,
			Priority: corev1.ReportPriority_REPORT_PRIORITY_NORMAL, Description: f.tayfaName, EscalateTime: timestamppb.Now()},
	}
}

func (f *fakeCore) ListPlatformReports(ctx context.Context, _ *corev1.ListPlatformReportsRequest, _ ...grpc.CallOption) (*corev1.ListPlatformReportsResponse, error) {
	f.add(ctx, "ListPlatformReports")
	return &corev1.ListPlatformReportsResponse{Reports: f.reportList()}, nil
}

func (f *fakeCore) GetReportedItem(ctx context.Context, in *corev1.GetReportedItemRequest, _ ...grpc.CallOption) (*corev1.GetReportedItemResponse, error) {
	f.add(ctx, "GetReportedItem")
	reps := f.reportList()
	if in.GetReportId() == dmReportID {
		return &corev1.GetReportedItemResponse{Report: reps[0], Item: &corev1.ReportedItem{
			TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE, AuthorId: memberID, Body: evidence}}, nil
	}
	return &corev1.GetReportedItemResponse{Report: reps[1], Item: &corev1.ReportedItem{
		TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_ANNOUNCEMENT, AuthorId: memberID, Title: f.tayfaName, Body: f.tayfaName}}, nil
}

func (f *fakeCore) DecideReport(ctx context.Context, _ *corev1.DecideReportRequest, _ ...grpc.CallOption) (*corev1.DecideReportResponse, error) {
	f.add(ctx, "DecideReport")
	return &corev1.DecideReportResponse{}, nil
}

type fakeUsers struct {
	identityv1.AdminUserServiceClient
	*calls
	sanctionErr error
	sanction    *identityv1.Sanction
	lastQuery   string
}

func newFakeUsers(c *calls) *fakeUsers { return &fakeUsers{calls: c} }

func (f *fakeUsers) SearchUsers(ctx context.Context, in *identityv1.SearchUsersRequest, _ ...grpc.CallOption) (*identityv1.SearchUsersResponse, error) {
	f.add(ctx, "SearchUsers")
	f.lastQuery = in.GetQuery()
	return &identityv1.SearchUsersResponse{Users: []*identityv1.AdminUserSummary{
		{UserId: memberID, DisplayName: "Ayşe", ReferralCode: "AYSE1234", Registered: true},
	}}, nil
}

func (f *fakeUsers) GetUser(ctx context.Context, _ *identityv1.GetUserRequest, _ ...grpc.CallOption) (*identityv1.GetUserResponse, error) {
	f.add(ctx, "GetUser")
	return &identityv1.GetUserResponse{User: &identityv1.AdminUserSummary{UserId: memberID, DisplayName: "Ayşe", ReferralCode: "AYSE1234", Registered: true},
		CreateTime: timestamppb.Now(), ActiveSessions: 2, Sanction: f.sanction}, nil
}

func (f *fakeUsers) RevokeUserSessions(ctx context.Context, _ *identityv1.RevokeUserSessionsRequest, _ ...grpc.CallOption) (*identityv1.RevokeUserSessionsResponse, error) {
	f.add(ctx, "RevokeUserSessions")
	return &identityv1.RevokeUserSessionsResponse{Revoked: 2}, nil
}

func (f *fakeUsers) SanctionUser(ctx context.Context, in *identityv1.SanctionUserRequest, _ ...grpc.CallOption) (*identityv1.SanctionUserResponse, error) {
	f.add(ctx, "SanctionUser:"+in.GetType().String())
	if f.sanctionErr != nil {
		return nil, f.sanctionErr
	}
	return &identityv1.SanctionUserResponse{}, nil
}

func (f *fakeUsers) LiftSanction(ctx context.Context, _ *identityv1.LiftSanctionRequest, _ ...grpc.CallOption) (*identityv1.LiftSanctionResponse, error) {
	f.add(ctx, "LiftSanction")
	return &identityv1.LiftSanctionResponse{}, nil
}

func reasonErr(code codes.Code, reason string) error {
	st, _ := status.New(code, "x").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "test"})
	return st.Err()
}

const csrfValue = "cccccccccccccccccccccccccccccccccccccccccc" + "c" // 43 characters, like a real token

// page returns a GET request for a panel page with the admin session.
func (e *env) page(path string) *http.Request {
	r := e.req(http.MethodGet, path, "")
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
	return r
}

// form returns a same-origin form POST with the admin session and the
// CSRF token in cookie and field.
func (e *env) form(path string, v url.Values) *http.Request {
	if v == nil {
		v = url.Values{}
	}
	if !v.Has(CSRFField) {
		v.Set(CSRFField, csrfValue)
	}
	r := e.req(http.MethodPost, path, v.Encode())
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: csrfValue})
	return r
}

// upstreamCalls are the admin service calls in order (core and identity
// share one record).
func (e *env) upstreamCalls() []string { return e.core.names() }

func (e *env) resetCalls() { e.core.reset() }

// getPages are every page, with IDs that the fakes know.
var getPages = []string{
	"/", "/tayfalar", "/tayfalar?q=klasik", "/tayfalar/" + tayfaID, "/kullanicilar", "/kullanicilar/" + memberID,
	"/sikayetler", "/sikayetler?tumu=1", "/sikayetler/" + reportID, "/sikayetler/" + dmReportID,
}

// changes are every state-changing page request with a valid form.
func changes() map[string]url.Values {
	r := func(extra ...string) url.Values {
		v := url.Values{"gerekce": {goodReason}}
		for i := 0; i+1 < len(extra); i += 2 {
			v.Set(extra[i], extra[i+1])
		}
		return v
	}
	return map[string]url.Values{
		"/tayfalar/" + tayfaID + "/aski":                 r(),
		"/tayfalar/" + tayfaID + "/aski-kaldir":          r(),
		"/tayfalar/" + tayfaID + "/sil":                  r("onay", "sil"),
		"/tayfalar/" + tayfaID + "/rol":                  r("uye", memberID, "rol", "yonetici"),
		"/kullanicilar":                                  {"q": {"Ayşe"}},
		"/kullanicilar/" + memberID + "/oturumlar":       r(),
		"/kullanicilar/" + memberID + "/yaptirim":        r("tur", "aski", "sure", "7"),
		"/kullanicilar/" + memberID + "/yaptirim-kaldir": r(),
		"/sikayetler/" + reportID + "/karar":             r("karar", "kaldir"),
	}
}

// TestPagesNeedAdminToken: no page and no change runs without the panel's
// admin token. An app token, in the panel cookie, the app cookie or an
// Authorization header, opens nothing. Nothing reaches core or identity's
// admin services.
func TestPagesNeedAdminToken(t *testing.T) {
	e := setup(t)
	for name, set := range map[string]func(*http.Request){
		"çerez yok": func(*http.Request) {},
		"panel çerezinde uygulama token'ı": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.appTok})
		},
		"uygulama çerezi ve başlığı": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "tayfam_at", Value: e.adminTok})
			r.Header.Set("Authorization", "Bearer "+e.adminTok)
		},
		"bozuk token": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: TokenCookie, Value: "a.b.c"}) },
	} {
		t.Run(name, func(t *testing.T) {
			e.resetCalls()
			var reqs []*http.Request
			for _, p := range getPages {
				reqs = append(reqs, e.req(http.MethodGet, p, ""))
			}
			for p, v := range changes() {
				v.Set(CSRFField, csrfValue)
				r := e.req(http.MethodPost, p, v.Encode())
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: csrfValue})
				reqs = append(reqs, r)
			}
			for _, r := range reqs {
				set(r)
				w := e.do(r)
				loc := w.Header().Get("Location")
				if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/giris") {
					t.Errorf("%s %s: %d %q", r.Method, r.URL, w.Code, loc)
				}
				if strings.Contains(w.Body.String(), "Klasikçiler") || strings.Contains(w.Body.String(), "Ayşe") {
					t.Errorf("%s %s: yanıtta veri var", r.Method, r.URL)
				}
			}
			if c := e.upstreamCalls(); len(c) != 0 {
				t.Fatalf("yönetim servisleri çağrıldı: %v", c)
			}
		})
	}
}

// TestPagesWithAdminToken: every page renders with the admin token, and
// core and identity receive exactly that token (they verify it again).
func TestPagesWithAdminToken(t *testing.T) {
	e := setup(t)
	for _, p := range getPages {
		w := e.do(e.page(p))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %s", p, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Yönetici") { // the admin's name from GetAdmin
			t.Errorf("%s: yönetici adı yok", p)
		}
	}
	for _, tok := range e.core.tokens {
		if tok != e.adminTok {
			t.Fatalf("servise giden token admin token'ı değil")
		}
	}
	if e.identity.gotToken != e.adminTok {
		t.Fatal("GetAdmin admin token'ını almadı")
	}
}

// TestRemovedAdminSignedOut: once identity says the user is no longer a
// platform administrator, pages send to sign-in and drop the cookie.
func TestRemovedAdminSignedOut(t *testing.T) {
	e := setup(t)
	e.identity.getErr = status.Error(codes.Unauthenticated, "x")
	w := e.do(e.page("/tayfalar"))
	if w.Code != http.StatusSeeOther || cookie(w, TokenCookie) == nil || cookie(w, TokenCookie).MaxAge >= 0 {
		t.Fatalf("%d %v", w.Code, w.Result().Cookies())
	}
	if len(e.upstreamCalls()) != 0 {
		t.Fatal("yönetim servisi çağrıldı")
	}
}

// TestCSRF: a change without the CSRF token, with a different one, or from
// another origin is refused before anything reaches core or identity.
func TestCSRF(t *testing.T) {
	e := setup(t)
	for path, v := range changes() {
		for name, mod := range map[string]func(*http.Request){
			"token yok": func(r *http.Request) {
				_ = r.ParseForm()
				r.PostForm.Del(CSRFField)
				r.Body = io.NopCloser(strings.NewReader(r.PostForm.Encode()))
				r.PostForm, r.Form = nil, nil
			},
			"çerez yok": func(r *http.Request) {
				r.Header.Del("Cookie")
				r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
			},
			"başka token": func(r *http.Request) {
				r.Header.Del("Cookie")
				r.AddCookie(&http.Cookie{Name: TokenCookie, Value: e.adminTok})
				r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: strings.Repeat("b", 43)})
			},
			"JSON gövde": func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		} {
			e.resetCalls()
			r := e.form(path, cloneValues(v))
			mod(r)
			w := e.do(r)
			if w.Code != http.StatusForbidden && w.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s: %d", name, path, w.Code)
			}
			if c := e.upstreamCalls(); len(c) != 0 {
				t.Errorf("%s %s: servis çağrıldı %v", name, path, c)
			}
		}
		// Another origin: refused by the Origin check.
		e.resetCalls()
		r := e.form(path, cloneValues(v))
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if w := e.do(r); w.Code != http.StatusForbidden || len(e.upstreamCalls()) != 0 {
			t.Errorf("başka origin %s: %d", path, w.Code)
		}
	}
	// With the token every change goes through.
	for path, v := range changes() {
		e.resetCalls()
		w := e.do(e.form(path, cloneValues(v)))
		if w.Code != http.StatusSeeOther && w.Code != http.StatusOK {
			t.Errorf("geçerli %s: %d", path, w.Code)
		}
		if len(e.upstreamCalls()) == 0 {
			t.Errorf("geçerli %s: servis çağrılmadı", path)
		}
		if loc := w.Header().Get("Location"); strings.Contains(loc, "hata=") {
			t.Errorf("geçerli %s: %s", path, loc)
		}
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// TestCSRFTokenOnForms: every page's forms carry the token of the cookie.
func TestCSRFTokenOnForms(t *testing.T) {
	e := setup(t)
	w := e.do(e.page("/tayfalar/" + tayfaID))
	c := cookie(w, CSRFCookie)
	if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("CSRF çerezi: %+v", c)
	}
	forms := regexp.MustCompile(`(?s)<form[^>]*method="post".*?</form>`).FindAllString(w.Body.String(), -1)
	if len(forms) < 4 {
		t.Fatalf("%d form", len(forms))
	}
	for _, f := range forms {
		if !strings.Contains(f, `name="csrf" value="`+c.Value+`"`) {
			t.Errorf("CSRF alanı yok: %s", f)
		}
	}
}

// TestTemplatesEscape: a tayfa name with markup is shown as text.
func TestTemplatesEscape(t *testing.T) {
	e := setup(t)
	e.core.tayfaName = xssName
	for _, p := range []string{"/tayfalar", "/tayfalar/" + tayfaID, "/kullanicilar/" + memberID, "/sikayetler/" + reportID} {
		body := e.do(e.page(p)).Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
			t.Errorf("%s: kaçışsız çıktı", p)
		}
		if !strings.Contains(body, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;") {
			t.Errorf("%s: kaçışlı ad yok", p)
		}
	}
}

var (
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// Words that would mean a page shows what the panel must not.
	forbiddenWords = regexp.MustCompile(`(?i)doğum|birth|konum|enlem|boylam|latitude|longitude|e-posta adresi:|oy veren|kim oy`)
)

// TestPagesHidePersonalData scans every rendered page: no e-mail address,
// birth date, location or poll voter; the e-mail an administrator searched
// for is not shown again; a direct message report shows only the
// reporter's evidence.
func TestPagesHidePersonalData(t *testing.T) {
	e := setup(t)
	pages := map[string]string{}
	for _, p := range getPages {
		pages[p] = e.do(e.page(p)).Body.String()
	}
	w := e.do(e.form("/kullanicilar", url.Values{"q": {hidden}}))
	if w.Code != http.StatusOK || e.users.lastQuery != hidden {
		t.Fatalf("arama: %d", w.Code)
	}
	pages["POST /kullanicilar"] = w.Body.String()
	for p, body := range pages {
		if m := emailRe.FindString(body); m != "" {
			t.Errorf("%s: e-posta adresi: %s", p, m)
		}
		if strings.Contains(body, hidden) {
			t.Errorf("%s: aranan e-posta sayfada", p)
		}
		if m := forbiddenWords.FindString(body); m != "" {
			t.Errorf("%s: %q", p, m)
		}
	}
	if !strings.Contains(pages["/sikayetler/"+dmReportID], evidence) {
		t.Fatal("DM şikâyetinde kanıt metni yok")
	}
	// The search went in the body, never in a URL.
	if strings.Contains(w.Header().Get("Location"), "@") {
		t.Fatal("e-posta URL'de")
	}
}

// TestChangeNeedsRecentSignIn: with a Google sign-in older than 5 minutes
// every change goes to the sign-in page (and back to the page afterwards)
// without reaching core or identity.
func TestChangeNeedsRecentSignIn(t *testing.T) {
	e := setup(t)
	past := time.Now().Add(-10 * time.Minute)
	old, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: e.signingKey, Issuer: issuer, Now: func() time.Time { return past }})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := old.Issue(userID, sessID, past)
	if err != nil {
		t.Fatal(err)
	}
	e.adminTok = tok
	for path, v := range changes() {
		if path == "/kullanicilar" { // a search, not a change
			continue
		}
		e.resetCalls()
		w := e.do(e.form(path, cloneValues(v)))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusSeeOther || loc.Path != "/giris" || loc.Query().Get("yeniden") != "1" {
			t.Errorf("%s: %d %s", path, w.Code, loc)
		}
		page := path[:strings.LastIndex(path, "/")]
		if got := loc.Query().Get("devam"); got != page {
			t.Errorf("%s: devam %q", path, got)
		}
		if c := e.upstreamCalls(); len(c) != 0 {
			t.Errorf("%s: servis çağrıldı %v", path, c)
		}
	}
	// Reading still works.
	if w := e.do(e.page("/tayfalar/" + tayfaID)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "yeniden giriş gerekir") {
		t.Fatalf("okuma: %d", w.Code)
	}
}

// TestUpstreamReauthRequired: core's own 5-minute check also leads to
// sign-in.
func TestUpstreamReauthRequired(t *testing.T) {
	e := setup(t)
	e.core.err["SuspendTayfa"] = reasonErr(codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED")
	w := e.do(e.form("/tayfalar/"+tayfaID+"/aski", url.Values{"gerekce": {goodReason}}))
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/giris?") || !strings.Contains(loc, "yeniden=1") {
		t.Fatalf("%d %s", w.Code, loc)
	}
}

// TestChangeNeedsReason: a missing or short reason, a too long suspension
// reason and a deletion without confirmation are refused in gateway.
func TestChangeNeedsReason(t *testing.T) {
	e := setup(t)
	for name, tc := range map[string]struct {
		path string
		v    url.Values
		want string
	}{
		"gerekçe yok":    {"/tayfalar/" + tayfaID + "/aski", url.Values{}, "gerekce"},
		"kısa gerekçe":   {"/kullanicilar/" + memberID + "/oturumlar", url.Values{"gerekce": {"   kısa   "}}, "gerekce"},
		"uzun askı":      {"/tayfalar/" + tayfaID + "/aski", url.Values{"gerekce": {strings.Repeat("a", 501)}}, "gerekce"},
		"çok uzun":       {"/sikayetler/" + reportID + "/karar", url.Values{"gerekce": {strings.Repeat("a", 1001)}, "karar": {"uyar"}}, "gerekce"},
		"silme onaysız":  {"/tayfalar/" + tayfaID + "/sil", url.Values{"gerekce": {goodReason}}, "onay"},
		"geçersiz rol":   {"/tayfalar/" + tayfaID + "/rol", url.Values{"gerekce": {goodReason}, "uye": {memberID}, "rol": {"sahip"}}, "gecersiz"},
		"geçersiz üye":   {"/tayfalar/" + tayfaID + "/rol", url.Values{"gerekce": {goodReason}, "uye": {"x"}, "rol": {"uye"}}, "gecersiz"},
		"geçersiz süre":  {"/kullanicilar/" + memberID + "/yaptirim", url.Values{"gerekce": {goodReason}, "tur": {"aski"}, "sure": {"3650"}}, "gecersiz"},
		"geçersiz karar": {"/sikayetler/" + dmReportID + "/karar", url.Values{"gerekce": {goodReason}, "karar": {"kaldir"}}, "gecersiz"},
	} {
		e.resetCalls()
		w := e.do(e.form(tc.path, tc.v))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusSeeOther || loc.Query().Get("hata") != tc.want {
			t.Errorf("%s: %d %s", name, w.Code, loc)
		}
		for _, c := range e.upstreamCalls() {
			if c != "GetReportedItem" { // the decision reads the report first
				t.Errorf("%s: servis çağrıldı %v", name, c)
			}
		}
	}
	// The error code is shown as fixed text; arbitrary text never is.
	body := e.do(e.page("/tayfalar/" + tayfaID + "?hata=gerekce&sonuc=%3Cb%3Ex")).Body.String()
	if !strings.Contains(body, "Gerekçe 10 ile 1000") || strings.Contains(body, "<b>x") {
		t.Fatal("hata metni")
	}
}

// TestLastAdminError: core's errors become fixed messages on the page.
func TestLastAdminError(t *testing.T) {
	e := setup(t)
	e.core.err["SetMemberRole"] = reasonErr(codes.FailedPrecondition, "ERROR_REASON_LAST_ADMIN")
	w := e.do(e.form("/tayfalar/"+tayfaID+"/rol", url.Values{"gerekce": {goodReason}, "uye": {memberID}, "rol": {"uye"}}))
	if loc := w.Header().Get("Location"); loc != "/tayfalar/"+tayfaID+"?hata=son-yonetici" {
		t.Fatalf("%d %s", w.Code, loc)
	}
}

// TestBanDecision: a ban decision bans in identity first, then records the
// decision in core; an existing ban (an earlier attempt) is accepted, an
// existing suspension is not.
func TestBanDecision(t *testing.T) {
	e := setup(t)
	path := "/sikayetler/" + reportID + "/karar"
	v := url.Values{"gerekce": {goodReason}, "karar": {"ban"}}
	if w := e.do(e.form(path, cloneValues(v))); w.Header().Get("Location") != "/sikayetler?sonuc=karar-verildi" {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Location"))
	}
	if got := strings.Join(e.upstreamCalls(), ","); got != "GetReportedItem,SanctionUser:SANCTION_TYPE_BAN,DecideReport" {
		t.Fatalf("çağrılar: %s", got)
	}

	e.resetCalls()
	e.users.sanctionErr = reasonErr(codes.FailedPrecondition, "ERROR_REASON_SANCTION_CONFLICT")
	e.users.sanction = &identityv1.Sanction{Type: identityv1.SanctionType_SANCTION_TYPE_BAN}
	if w := e.do(e.form(path, cloneValues(v))); !strings.Contains(w.Header().Get("Location"), "sonuc=karar-verildi") {
		t.Fatalf("mevcut ban: %s", w.Header().Get("Location"))
	}

	e.resetCalls()
	e.users.sanction = &identityv1.Sanction{Type: identityv1.SanctionType_SANCTION_TYPE_SUSPEND}
	if w := e.do(e.form(path, cloneValues(v))); !strings.Contains(w.Header().Get("Location"), "hata=yaptirim-var") {
		t.Fatalf("mevcut askı: %s", w.Header().Get("Location"))
	}
	for _, c := range e.upstreamCalls() {
		if c == "DecideReport" {
			t.Fatal("askı varken karar kaydedildi")
		}
	}
}

// TestDecisionsFitReport: content removal is not offered for a direct
// message, banning needs a reported person.
func TestDecisionsFitReport(t *testing.T) {
	values := func(r *corev1.PlatformReport) string {
		var out []string
		for _, d := range decisionsFor(r) {
			out = append(out, d.Value)
		}
		return strings.Join(out, ",")
	}
	if got := values(&corev1.PlatformReport{TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_DM_MESSAGE, SubjectUserId: memberID}); got != "uyar,ban,reddet" {
		t.Errorf("DM: %s", got)
	}
	if got := values(&corev1.PlatformReport{TargetType: corev1.ReportTargetType_REPORT_TARGET_TYPE_POLL, TayfaId: tayfaID}); got != "kaldir,uyar,tayfa-aski,reddet" {
		t.Errorf("anket: %s", got)
	}
}

// TestGoogleSignIn walks the redirect flow: the sign-in form sends to
// Google with a nonce and a state kept in cookies; Google's form post is
// accepted only with the matching state, and identity gets the nonce of
// the cookie and the Access e-mail address.
func TestGoogleSignIn(t *testing.T) {
	e := setup(t)
	w := e.do(e.req(http.MethodGet, "/giris?devam=%2Ftayfalar%2F"+tayfaID, ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/giris/google"`) {
		t.Fatalf("giriş sayfası: %d", w.Code)
	}
	csrf := cookie(w, CSRFCookie)

	start := func(next string) *httptest.ResponseRecorder {
		r := e.req(http.MethodPost, "/giris/google", url.Values{CSRFField: {csrf.Value}, "devam": {next}}.Encode())
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(csrf)
		return e.do(r)
	}
	w = start("/tayfalar/" + tayfaID)
	loc, _ := url.Parse(w.Header().Get("Location"))
	q := loc.Query()
	if w.Code != http.StatusSeeOther || loc.Scheme+"://"+loc.Host+loc.Path != GoogleAuthURL {
		t.Fatalf("yönlendirme: %d %s", w.Code, loc)
	}
	nc, sc := cookie(w, NonceCookie), cookie(w, StateCookie)
	for _, c := range []*http.Cookie{nc, sc} {
		if c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteNoneMode || c.Path != "/" || c.Domain != "" || c.MaxAge <= 0 || c.MaxAge > 300 {
			t.Fatalf("giriş çerezi: %+v", c)
		}
	}
	if q.Get("client_id") != googleClient || q.Get("redirect_uri") != origin+CallbackPath || q.Get("response_type") != "id_token" ||
		q.Get("response_mode") != "form_post" || q.Get("scope") != "openid email" || q.Get("nonce") != nc.Value ||
		q.Get("state") == "" || strings.Contains(sc.Value, q.Get("nonce")) {
		t.Fatalf("parametreler: %v", q)
	}

	callback := func(state string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://"+host+CallbackPath,
			strings.NewReader(url.Values{"state": {state}, "id_token": {"google-id-token"}}.Encode()))
		r.Header.Set(auth.AccessHeader, e.access)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://accounts.google.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		return e.do(r)
	}
	// Refusals: identity is never asked.
	for name, w := range map[string]*httptest.ResponseRecorder{
		"başka state":    callback("baska", nc, sc),
		"state çerezsiz": callback(q.Get("state"), nc),
		"nonce çerezsiz": callback(q.Get("state"), sc),
	} {
		if w.Code == http.StatusOK || cookie(w, TokenCookie) != nil || e.identity.signIn != nil {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}

	w = callback(q.Get("state"), nc, sc)
	if w.Code != http.StatusOK {
		t.Fatalf("geri dönüş: %d %s", w.Code, w.Body.String())
	}
	got := e.identity.signIn
	if got == nil || got.GetNonce() != nc.Value || got.GetIdToken() != "google-id-token" || !strings.EqualFold(got.GetAccessEmail(), accessTo) {
		t.Fatalf("identity'ye giden: %+v", got)
	}
	checkCookie(t, cookie(w, TokenCookie), TokenCookie)
	if c := cookie(w, NonceCookie); c == nil || c.MaxAge >= 0 {
		t.Fatal("nonce çerezi silinmedi")
	}
	// Continues to the page it came from, with a navigation the browser
	// sends the SameSite=Strict cookie on.
	if !strings.Contains(w.Body.String(), `http-equiv="refresh" content="0;url=/tayfalar/`+tayfaID+`"`) {
		t.Fatalf("devam: %s", w.Body.String())
	}

	// Google's error answer, and identity's refusal: the sign-in page.
	e.identity.signInErr = status.Error(codes.Unauthenticated, "x")
	w = start("/")
	w = callback(cookieValueState(t, w), cookie(w, NonceCookie), cookie(w, StateCookie))
	if w.Code != http.StatusUnauthorized || cookie(w, TokenCookie) != nil || !strings.Contains(w.Body.String(), "Giriş tamamlanamadı") {
		t.Fatalf("ret: %d", w.Code)
	}
}

func cookieValueState(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("state")
}

// TestSignInRedirectStaysOnPanel: the page to return to after sign-in is
// always a path on the panel.
func TestSignInRedirectStaysOnPanel(t *testing.T) {
	for _, p := range []string{"//evil.example", "/\\evil.example", "https://evil.example", "evil", "/a\nb", ""} {
		if safePath(p) {
			t.Errorf("%q kabul edildi", p)
		}
	}
	for _, p := range []string{"/", "/tayfalar/" + tayfaID, "/tayfalar?q=a"} {
		if !safePath(p) {
			t.Errorf("%q reddedildi", p)
		}
	}
	e := setup(t)
	w := e.do(e.req(http.MethodGet, "/giris?devam=%2F%2Fevil.example", ""))
	if strings.Contains(w.Body.String(), "evil.example") {
		t.Fatal("başka adres formda")
	}
}

// TestNoInlineScriptOrStyle: the CSP allows only the panel's own files;
// pages have no inline script, style or event handler, and load htmx from
// the panel.
func TestNoInlineScriptOrStyle(t *testing.T) {
	e := setup(t)
	w := e.do(e.page("/"))
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP'de %q yok: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe") || strings.Contains(csp, "http:") {
		t.Fatalf("CSP: %s", csp)
	}
	scripts := regexp.MustCompile(`<script[^>]*>`)
	inline := regexp.MustCompile(`(?i)\sstyle=|\son[a-z]+=|<style|hx-on|javascript:`)
	for _, p := range append(append([]string(nil), getPages...), "/giris") {
		body := e.do(e.page(p)).Body.String()
		tags := scripts.FindAllString(body, -1)
		if len(tags) != 1 || tags[0] != `<script src="`+HTMXPath+`" defer>` {
			t.Errorf("%s: script etiketleri %v", p, tags)
		}
		if m := inline.FindString(body); m != "" {
			t.Errorf("%s: satır içi %q", p, m)
		}
		if strings.Contains(body, "https://") {
			t.Errorf("%s: harici adres", p)
		}
	}
}

// htmxSHA256 pins the embedded htmx 2.0.11 (npm htmx.org, dist/htmx.min.js,
// package integrity sha512-Thx/WtpeOQqSrqBCw/A1cwGJGg4UrVa3+sW0GmrM3p4gJgO89ecH4qtbnyzDDWFvBTqjnIMCgELTNt636dtamA==).
const htmxSHA256 = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"

func TestStaticFiles(t *testing.T) {
	e := setup(t)
	w := e.do(e.req(http.MethodGet, HTMXPath, ""))
	sum := sha256.Sum256(w.Body.Bytes())
	if w.Code != http.StatusOK || hex.EncodeToString(sum[:]) != htmxSHA256 {
		t.Fatalf("htmx: %d %x", w.Code, sum)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("htmx türü: %s", ct)
	}
	if w := e.do(e.req(http.MethodGet, "/static/panel.css", "")); w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d", w.Code)
	}
	if w := e.do(e.req(http.MethodGet, "/static/yok.js", "")); w.Code != http.StatusNotFound {
		t.Fatalf("olmayan dosya: %d", w.Code)
	}
}

// TestUnknownIDs: a malformed ID answers 404 without calling anything.
func TestUnknownIDs(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/tayfalar/x", "/kullanicilar/1", "/sikayetler/" + strings.Repeat("g", 36)} {
		e.resetCalls()
		if w := e.do(e.page(p)); w.Code != http.StatusNotFound || len(e.upstreamCalls()) != 0 {
			t.Errorf("%s: %d %v", p, w.Code, e.upstreamCalls())
		}
	}
}

// TestUserSearchLimits: the query length is checked in gateway too.
func TestUserSearchLimits(t *testing.T) {
	e := setup(t)
	for _, q := range []string{"a", strings.Repeat("a", 101)} {
		e.resetCalls()
		if w := e.do(e.form("/kullanicilar", url.Values{"q": {q}})); w.Code != http.StatusBadRequest || len(e.upstreamCalls()) != 0 {
			t.Errorf("%d karakter: %d", len(q), w.Code)
		}
	}
}

// TestSignOutPage removes the session cookie.
func TestSignOutPage(t *testing.T) {
	e := setup(t)
	w := e.do(e.form("/cikis", nil))
	if c := cookie(w, TokenCookie); w.Code != http.StatusSeeOther || c == nil || c.MaxAge >= 0 {
		t.Fatalf("%d %v", w.Code, c)
	}
}
