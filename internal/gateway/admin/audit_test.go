package admin

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAuditPage: the page passes its filters to identity (dates as days in
// Turkish time, bitis inclusive), shows each entry with its reason and
// links, and offers the next page with the same filters.
func TestAuditPage(t *testing.T) {
	e := setup(t)
	e.audit.next = "imlec-2"
	w := e.do(e.page("/denetim?islem=tayfa.suspend&tur=tayfa&ilgili=" + strings.ToUpper(tayfaID) + "&baslangic=2026-11-01&bitis=2026-11-09&imlec=imlec-1"))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	in := e.audit.last
	if in.GetAction() != "tayfa.suspend" || in.GetTargetType() != "tayfa" || in.GetRelatedId() != tayfaID ||
		in.GetPageCursor() != "imlec-1" || in.GetPageSize() != auditPageSize {
		t.Fatalf("istek = %v", in)
	}
	if got, want := in.GetStartTime().AsTime(), time.Date(2026, 10, 31, 21, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("başlangıç %v", got)
	}
	if got, want := in.GetEndTime().AsTime(), time.Date(2026, 11, 9, 21, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("bitiş %v", got)
	}
	body := w.Body.String()
	for _, want := range []string{
		auditReason, "Tayfa askısı", "Rol düzeltme", `href="/tayfalar/` + tayfaID + `"`, `href="/kullanicilar/` + memberID + `"`,
		`value="tayfa.suspend" selected`, `value="2026-11-01"`, `aria-current="page">Denetim kaydı`,
		`href="/denetim?baslangic=2026-11-01&amp;bitis=2026-11-09&amp;ilgili=` + tayfaID + `&amp;imlec=imlec-2&amp;islem=tayfa.suspend&amp;tur=tayfa"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sayfada yok: %s", want)
		}
	}
	// The log is read-only: the only form that posts is sign-out.
	if n := len(regexp.MustCompile(`method="post"`).FindAllString(body, -1)); n != 1 {
		t.Errorf("%d POST formu", n)
	}

	// No filter: no cursor, no dates, no next link.
	e.audit.next = ""
	w = e.do(e.page("/denetim"))
	if in := e.audit.last; w.Code != http.StatusOK || in.GetStartTime() != nil || in.GetEndTime() != nil || in.GetAction() != "" || in.GetPageCursor() != "" {
		t.Fatalf("%d %v", w.Code, in)
	}
	if strings.Contains(w.Body.String(), "Daha eski kayıtlar") {
		t.Error("son sayfada sonraki bağlantısı")
	}
}

// TestAuditPageRefusesBadFilters: a filter outside the whitelist, a bad
// ID or date, or an over-long cursor is answered in gateway with 400.
func TestAuditPageRefusesBadFilters(t *testing.T) {
	e := setup(t)
	for _, q := range []string{
		"islem=user.delete_everything", "islem=" + strings.Repeat("a", 40), "tur=message", "ilgili=12345",
		"ilgili=" + tayfaID + "x", "baslangic=01.11.2026", "bitis=2026-13-01",
		"baslangic=2026-11-09&bitis=2026-11-01", "imlec=" + strings.Repeat("a", maxCursor+1),
	} {
		e.resetCalls()
		w := e.do(e.page("/denetim?" + q))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, w.Code)
		}
		for _, c := range e.upstreamCalls() {
			if c == "ListPlatformAuditLogs" {
				t.Errorf("%s: identity çağrıldı", q)
			}
		}
	}
	// A cursor identity refuses (another filter's) is 400 as well.
	e.audit.err = status.Error(codes.InvalidArgument, "x")
	if w := e.do(e.page("/denetim?imlec=baska")); w.Code != http.StatusBadRequest {
		t.Errorf("geçersiz imleç: %d", w.Code)
	}
	// identity no longer accepts the administrator: back to sign-in.
	e.audit.err = status.Error(codes.Unauthenticated, "x")
	if w := e.do(e.page("/denetim")); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/giris") {
		t.Errorf("oturum: %d", w.Code)
	}
}

// TestAuditReasonEscaped: a reason with markup is shown as text.
func TestAuditReasonEscaped(t *testing.T) {
	e := setup(t)
	e.audit.reason = xssName
	body := e.do(e.page("/denetim")).Body.String()
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Fatal("kaçışsız gerekçe")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;") {
		t.Fatal("kaçışlı gerekçe yok")
	}
}

// TestDashboardCounts: the dashboard shows the counts of core and identity
// and the job runs; nothing else from them.
func TestDashboardCounts(t *testing.T) {
	e := setup(t)
	w := e.do(e.page("/"))
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		">523<", ">311<", ">7<", "31 günü geçti", ">41<", ">37<", "Hareketsiz tayfa temizliği", "Kalıcı silme (30 gün)",
		"Başarısız", "Veri dışa aktarma talepleri henüz yok",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panoda yok: %s", want)
		}
	}
	if strings.Contains(body, auditReason) {
		t.Error("panoda denetim gerekçesi")
	}
	calls := strings.Join(e.upstreamCalls(), ",")
	if !strings.Contains(calls, "GetStats") || !strings.Contains(calls, "GetUserStats") {
		t.Fatalf("çağrılar: %s", calls)
	}
	// A failed count is an error page, not a dashboard with made-up zeros.
	e.core.err["GetStats"] = status.Error(codes.Unavailable, "x")
	if w := e.do(e.page("/")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("sayaç hatası: %d", w.Code)
	}
}
