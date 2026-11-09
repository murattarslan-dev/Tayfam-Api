package admin

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// The audit log page (issue #110; Notion "Platform Yönetim Paneli →
// Denetim kaydı"). Entries come from identity's AdminAuditService; the
// written reason is shown here and nowhere else (never logged). Filters
// are whitelisted values, UUIDs and dates only, so the URL never carries
// free text.

// auditPageSize is the number of entries per page.
const auditPageSize = 50

// maxCursor bounds the imlec parameter (identity refuses longer ones too).
const maxCursor = 512

// dateLayout is the value of <input type="date">.
const dateLayout = "2006-01-02"

type option struct {
	Value, Label string
}

// auditActions are the actions written to the platform audit log
// (identity platformadmin, core admin), in the filter's order.
func auditActions() []option {
	return []option{
		{"admin.sign_in", "Panel girişi"},
		{"tayfa.suspend", "Tayfa askısı"},
		{"tayfa.unsuspend", "Tayfa askısını kaldırma"},
		{"tayfa.delete", "Tayfa silme"},
		{"membership.set_role", "Rol düzeltme"},
		{"report.view_item", "Şikâyet edilen öğeyi görüntüleme"},
		{"report.decide", "Şikâyet kararı"},
		{"user.revoke_sessions", "Oturumları kapatma"},
		{"user.suspend", "Kullanıcı askısı"},
		{"user.ban", "Kullanıcı banı"},
		{"user.lift_sanction", "Yaptırımı kaldırma"},
	}
}

func auditTargetTypes() []option {
	return []option{
		{"tayfa", "Tayfa"},
		{"user", "Kullanıcı"},
		{"report", "Şikâyet"},
		{"membership", "Üyelik"},
		{"moderation_term", "Moderasyon terimi"},
		{"subscription", "Abonelik"},
	}
}

func optionLabel(opts []option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return ""
}

// actionLabel is the Turkish name of an audit action; an unknown one is
// shown as its code.
func actionLabel(v string) string {
	if l := optionLabel(auditActions(), v); l != "" {
		return l
	}
	return v
}

func targetTypeLabel(v string) string {
	if l := optionLabel(auditTargetTypes(), v); l != "" {
		return l
	}
	if v == "" {
		return "—"
	}
	return v
}

// auditFilter is the page's query, as typed back into the form.
type auditFilter struct {
	Action, TargetType, Related, From, To string
}

func (f auditFilter) values() url.Values {
	v := url.Values{}
	for k, s := range map[string]string{"islem": f.Action, "tur": f.TargetType, "ilgili": f.Related, "baslangic": f.From, "bitis": f.To} {
		if s != "" {
			v.Set(k, s)
		}
	}
	return v
}

type auditData struct {
	Filter  auditFilter
	Actions []option
	Types   []option
	Entries []*identityv1.PlatformAuditEntry
	// Next is the next page's URL; empty on the last page.
	Next     string
	Filtered bool
}

// parseAuditFilter reads and checks the query. Dates are days in the
// panel's zone; bitis includes its day.
func parseAuditFilter(q url.Values) (auditFilter, *identityv1.ListPlatformAuditLogsRequest, bool) {
	f := auditFilter{
		Action: q.Get("islem"), TargetType: q.Get("tur"), Related: strings.ToLower(q.Get("ilgili")),
		From: q.Get("baslangic"), To: q.Get("bitis"),
	}
	req := &identityv1.ListPlatformAuditLogsRequest{PageSize: auditPageSize, Action: f.Action, TargetType: f.TargetType, RelatedId: f.Related}
	if f.Action != "" && optionLabel(auditActions(), f.Action) == "" {
		return f, nil, false
	}
	if f.TargetType != "" && optionLabel(auditTargetTypes(), f.TargetType) == "" {
		return f, nil, false
	}
	if f.Related != "" && !isUUID(f.Related) {
		return f, nil, false
	}
	var from, to time.Time
	var err error
	if f.From != "" {
		if from, err = time.ParseInLocation(dateLayout, f.From, panelZone()); err != nil {
			return f, nil, false
		}
		req.StartTime = timestamppb.New(from)
	}
	if f.To != "" {
		if to, err = time.ParseInLocation(dateLayout, f.To, panelZone()); err != nil {
			return f, nil, false
		}
		to = to.AddDate(0, 0, 1)
		req.EndTime = timestamppb.New(to)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return f, nil, false
	}
	cur := q.Get("imlec")
	if len(cur) > maxCursor {
		return f, nil, false
	}
	req.PageCursor = cur
	return f, req, true
}

func (h *handlers) auditLog(w http.ResponseWriter, r *http.Request) {
	f, req, ok := parseAuditFilter(r.URL.Query())
	if !ok {
		h.renderError(w, r, http.StatusBadRequest, errorText("gecersiz"))
		return
	}
	s := sessionFrom(r.Context())
	ctx, cancel := s.upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Audit.ListPlatformAuditLogs(ctx, req)
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	d := auditData{Filter: f, Actions: auditActions(), Types: auditTargetTypes(), Entries: resp.GetEntries(),
		Filtered: len(f.values()) > 0}
	if next := resp.GetNextPageCursor(); next != "" {
		v := f.values()
		v.Set("imlec", next)
		d.Next = "/denetim?" + v.Encode()
	}
	p := h.newPage(w, r, "denetim", "Denetim kaydı")
	p.Data = d
	h.render(w, r, http.StatusOK, "audit", p)
}

// --- Pano sayaçları ---

// jobLabel is the Turkish name of a core background job.
func jobLabel(job string) string {
	switch job {
	case "tayfa-cleanup":
		return "Hareketsiz tayfa temizliği"
	case "tayfa-deletion":
		return "Tayfa silme (72 saat bekleme)"
	case "purge":
		return "Kalıcı silme (30 gün)"
	case "hike-alert":
		return "Yürüyüş dönüş alarmı"
	case "plates":
		return "Plaka temizliği"
	case "report-escalation":
		return "Şikâyetleri platforma yükseltme"
	case "retention":
		return "Saklama süresi temizliği"
	}
	return job
}
