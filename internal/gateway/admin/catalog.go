package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
)

// The catalog pages (issue #262; ADR-0025 §7, ADR-0039 §1, ADR-0040 §3 and
// §5): interests, event types, badges, study tags, score presets and the
// blocked domain list, managed without changing code.
//
// An entry is edited on its own form page (/katalog/{kind}?anahtar=key, or
// without anahtar for a new one). The form posts back to the same page:
// "Önizle" asks core to check the entry and shows the result (the
// components, fields and the lowest client version, with a warning when a
// supported client version does not know a component); nothing is written
// and no reason is needed. "Kaydet" needs a reason and a recent sign-in,
// like every change; a failed check shows the form again with the
// problem, the values kept. core checks everything again: the page only
// turns form fields into the request.
//
// The query carries only a kind and a catalog key; the form's text never
// goes into a URL.

// catalogKinds are the form pages, by path segment.
var catalogKinds = map[string]string{
	"ilgi-alani": "İlgi alanı",
	"tur":        "Etkinlik türü",
	"rozet":      "Rozet",
	"ders-konu":  "Ders/konu etiketi",
	"on-ayar":    "Yazboz ön ayarı",
}

// catalogKeyPattern is the form of a catalog key (definition.ValidKey).
var catalogKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,39}$`)

// maxDomainInput bounds the blocked domain field.
const maxDomainInput = 253

type catalogData struct {
	Resp *corev1.GetCatalogAdminResponse
	// Children maps a study tag key to its children, levels under "".
	Children map[string][]*corev1.CatalogStudyTag
}

// catalogPage lists the whole catalog.
func (h *handlers) catalogPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	resp, err := h.cfg.Core.GetCatalogAdmin(ctx, &corev1.GetCatalogAdminRequest{})
	if err != nil {
		h.failedPage(w, r, err)
		return
	}
	d := catalogData{Resp: resp, Children: map[string][]*corev1.CatalogStudyTag{}}
	for _, t := range resp.GetStudyTags() {
		d.Children[t.GetParentKey()] = append(d.Children[t.GetParentKey()], t)
	}
	p := h.newPage(w, r, "katalog", "Katalog")
	p.Data = d
	h.render(w, r, http.StatusOK, "catalog", p)
}

// catalogForm is an entry's form as typed: the values the page shows again.
type catalogForm struct {
	Kind      string
	KindLabel string
	Create    bool
	Key       string
	Position  string
	Labels    string
	Status    string
	Icon      string
	// Interest.
	Widgets string
	// Event type.
	Interests  string
	InPerson   bool
	Online     bool
	Definition string
	Version    int32
	// Badge.
	Scope     string
	RuleKind  string
	Metric    string
	Value     string
	Period    string
	EventType string
	// Study tag.
	Parent string
	// Score preset.
	Params string

	// Check is the result of a preview or of a save that failed a check.
	Check *corev1.CatalogCheck
	// Problem is a check's problem, or the page's own (a field that does
	// not parse).
	Problem string
	// Warnings name the served platforms whose lowest supported version
	// does not know every component of the type.
	Warnings []string
}

// catalogKind reads the {kind} path value; an unknown one is 404.
func (h *handlers) catalogKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	k := r.PathValue("kind")
	if _, ok := catalogKinds[k]; !ok {
		h.renderError(w, r, http.StatusNotFound, errorText("bulunamadi"))
		return "", false
	}
	return k, true
}

// catalogFormPage shows an entry's form: an existing entry's values, or an
// empty form.
func (h *handlers) catalogFormPage(w http.ResponseWriter, r *http.Request) {
	kind, ok := h.catalogKind(w, r)
	if !ok {
		return
	}
	key := r.URL.Query().Get("anahtar")
	f := &catalogForm{Kind: kind, KindLabel: catalogKinds[kind], Create: key == "", Status: "active", Labels: `{"tr": ""}`}
	switch kind {
	case "tur":
		f.Definition, f.InPerson = "{}", true
	case "on-ayar":
		f.Params = "{}"
	case "rozet":
		f.Scope, f.RuleKind, f.Period = "user", "threshold", "all_time"
	}
	if key != "" {
		if !catalogKeyPattern.MatchString(key) {
			h.renderError(w, r, http.StatusNotFound, errorText("bulunamadi"))
			return
		}
		ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
		defer cancel()
		resp, err := h.cfg.Core.GetCatalogAdmin(ctx, &corev1.GetCatalogAdminRequest{})
		if err != nil {
			h.failedPage(w, r, err)
			return
		}
		if !fillCatalogForm(f, resp, key) {
			h.renderError(w, r, http.StatusNotFound, errorText("bulunamadi"))
			return
		}
	}
	h.renderCatalogForm(w, r, http.StatusOK, f)
}

func (h *handlers) renderCatalogForm(w http.ResponseWriter, r *http.Request, code int, f *catalogForm) {
	title := f.KindLabel
	if f.Create {
		title = "Yeni: " + title
	}
	p := h.newPage(w, r, "katalog", title)
	p.Data = f
	h.render(w, r, code, "catalog_form", p)
}

func labelsText(m map[string]string) string {
	b, _ := json.Marshal(m) // map of strings; cannot fail
	return string(b)
}

func statusText(s corev1.CatalogStatus) string {
	if s == corev1.CatalogStatus_CATALOG_STATUS_RETIRED {
		return "retired"
	}
	return "active"
}

// fillCatalogForm puts the entry with key into f; false when there is
// none.
func fillCatalogForm(f *catalogForm, resp *corev1.GetCatalogAdminResponse, key string) bool {
	f.Key = key
	switch f.Kind {
	case "ilgi-alani":
		for _, in := range resp.GetInterests() {
			if in.GetKey() == key {
				f.Position, f.Labels, f.Icon = strconv.Itoa(int(in.GetPosition())), labelsText(in.GetLabels()), in.GetIcon()
				f.Status, f.Widgets = statusText(in.GetStatus()), strings.Join(in.GetDefaultWidgets(), ", ")
				return true
			}
		}
	case "tur":
		for _, et := range resp.GetEventTypes() {
			t := et.GetType()
			if t.GetKey() == key {
				f.Position, f.Labels, f.Icon = strconv.Itoa(int(et.GetPosition())), labelsText(t.GetLabels()), t.GetIcon()
				f.Status, f.Interests, f.Version = statusText(t.GetStatus()), strings.Join(t.GetInterestKeys(), ", "), t.GetVersion()
				f.InPerson = slices.Contains(t.GetModes(), corev1.EventMode_EVENT_MODE_IN_PERSON)
				f.Online = slices.Contains(t.GetModes(), corev1.EventMode_EVENT_MODE_ONLINE)
				f.Definition = indentJSON(t.GetDefinition())
				return true
			}
		}
	case "rozet":
		for _, b := range resp.GetBadges() {
			if b.GetKey() == key {
				f.Position, f.Labels, f.Icon = strconv.Itoa(int(b.GetPosition())), labelsText(b.GetLabels()), b.GetIcon()
				f.Status = statusText(b.GetStatus())
				f.Scope = map[corev1.BadgeScope]string{corev1.BadgeScope_BADGE_SCOPE_TAYFA: "tayfa"}[b.GetScope()]
				if f.Scope == "" {
					f.Scope = "user"
				}
				rule := b.GetRule()
				f.RuleKind = map[corev1.BadgeRuleKind]string{
					corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD: "threshold", corev1.BadgeRuleKind_BADGE_RULE_KIND_FIRST: "first",
					corev1.BadgeRuleKind_BADGE_RULE_KIND_CHAMPION: "champion",
				}[rule.GetKind()]
				f.Period = map[corev1.BadgePeriod]string{corev1.BadgePeriod_BADGE_PERIOD_YEAR: "year"}[rule.GetPeriod()]
				if f.Period == "" {
					f.Period = "all_time"
				}
				f.Metric, f.EventType = rule.GetMetric(), rule.GetEventType()
				if f.RuleKind == "threshold" {
					f.Value = strconv.FormatFloat(rule.GetValue(), 'f', -1, 64)
				}
				return true
			}
		}
	case "ders-konu":
		for _, t := range resp.GetStudyTags() {
			if t.GetKey() == key {
				f.Position, f.Labels, f.Parent = strconv.Itoa(int(t.GetPosition())), labelsText(t.GetLabels()), t.GetParentKey()
				f.Status = statusText(t.GetStatus())
				return true
			}
		}
	case "on-ayar":
		for _, p := range resp.GetScorePresets() {
			if p.GetKey() == key {
				f.Position, f.Labels, f.Params = strconv.Itoa(int(p.GetPosition())), labelsText(p.GetLabels()), indentJSON(p.GetParams())
				f.Status = statusText(p.GetStatus())
				return true
			}
		}
	}
	return false
}

// indentJSON shows stored JSON readably; text that does not parse is
// shown as it is.
func indentJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(b)
}

// readCatalogForm reads the posted form.
func readCatalogForm(r *http.Request, kind string) *catalogForm {
	v := func(name string) string { return strings.TrimSpace(r.PostFormValue(name)) }
	f := &catalogForm{
		Kind: kind, KindLabel: catalogKinds[kind], Create: r.PostFormValue("yeni") == "1",
		Key: v("anahtar"), Position: v("sira"), Labels: v("adlar"), Status: v("durum"), Icon: v("ikon"),
		Widgets: v("bilesenler"), Interests: v("ilgi_alanlari"), InPerson: r.PostFormValue("yuz_yuze") == "1",
		Online: r.PostFormValue("online") == "1", Definition: v("tanim"),
		Scope: v("kapsam"), RuleKind: v("kural"), Metric: v("metrik"), Value: v("deger"), Period: v("donem"),
		EventType: v("etkinlik_turu"), Parent: v("ust"), Params: v("kurallar"),
	}
	if n, err := strconv.ParseInt(v("surum"), 10, 32); err == nil && n > 0 {
		f.Version = int32(n) // #nosec G115 -- ParseInt bounds it to 32 bits
	}
	return f
}

// formProblem is a field the page could not read; core is not called.
type formProblem string

func (p formProblem) Error() string { return string(p) }

func parseLabels(s string) (map[string]string, error) {
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, formProblem(`Adlar bir JSON nesnesi olmalı, örneğin {"tr": "Doğa", "en": "Nature"}.`)
	}
	return m, nil
}

func parsePosition(s string) (int32, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, formProblem("Sıra bir tam sayı olmalı (0–9999).")
	}
	return int32(n), nil // #nosec G115 -- ParseInt bounds it to 32 bits
}

func parseStatus(s string) corev1.CatalogStatus {
	switch s {
	case "active":
		return corev1.CatalogStatus_CATALOG_STATUS_ACTIVE
	case "retired":
		return corev1.CatalogStatus_CATALOG_STATUS_RETIRED
	}
	return corev1.CatalogStatus_CATALOG_STATUS_UNSPECIFIED
}

// splitList reads a comma separated list; empty items are dropped.
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// save sends the form to core: with validateOnly as a preview, otherwise
// as a save with reason.
func (f *catalogForm) save(ctx context.Context, core CoreAdminClient, validateOnly bool, reason string) (*corev1.CatalogCheck, error) {
	labels, err := parseLabels(f.Labels)
	if err != nil {
		return nil, err
	}
	pos, err := parsePosition(f.Position)
	if err != nil {
		return nil, err
	}
	st := parseStatus(f.Status)
	switch f.Kind {
	case "ilgi-alani":
		resp, err := core.SaveCatalogInterest(ctx, &corev1.SaveCatalogInterestRequest{
			Interest: &corev1.CatalogInterest{Key: f.Key, Position: pos, Labels: labels, Icon: f.Icon, Status: st, DefaultWidgets: splitList(f.Widgets)},
			Create:   f.Create, ValidateOnly: validateOnly, Reason: reason,
		})
		return resp.GetCheck(), err
	case "tur":
		var modes []corev1.EventMode
		if f.InPerson {
			modes = append(modes, corev1.EventMode_EVENT_MODE_IN_PERSON)
		}
		if f.Online {
			modes = append(modes, corev1.EventMode_EVENT_MODE_ONLINE)
		}
		version := f.Version
		if f.Create {
			version = 0
		}
		resp, err := core.SaveCatalogEventType(ctx, &corev1.SaveCatalogEventTypeRequest{
			Type: &corev1.CatalogEventType{
				Key: f.Key, InterestKeys: splitList(f.Interests), Labels: labels, Icon: f.Icon, Modes: modes,
				Status: st, Version: version, Definition: f.Definition,
			},
			Position: pos, ValidateOnly: validateOnly, Reason: reason,
		})
		return resp.GetCheck(), err
	case "rozet":
		rule := &corev1.CatalogBadgeRule{EventType: f.EventType}
		switch f.RuleKind {
		case "threshold":
			rule.Kind, rule.Metric = corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD, f.Metric
			v, err := strconv.ParseFloat(f.Value, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, formProblem("Eşik değeri bir sayı olmalı.")
			}
			rule.Value = v
			rule.Period = corev1.BadgePeriod_BADGE_PERIOD_ALL_TIME
			if f.Period == "year" {
				rule.Period = corev1.BadgePeriod_BADGE_PERIOD_YEAR
			}
			rule.EventType = ""
		case "first":
			rule.Kind = corev1.BadgeRuleKind_BADGE_RULE_KIND_FIRST
		case "champion":
			rule.Kind = corev1.BadgeRuleKind_BADGE_RULE_KIND_CHAMPION
		}
		scope := corev1.BadgeScope_BADGE_SCOPE_USER
		if f.Scope == "tayfa" {
			scope = corev1.BadgeScope_BADGE_SCOPE_TAYFA
		}
		resp, err := core.SaveCatalogBadge(ctx, &corev1.SaveCatalogBadgeRequest{
			Badge:  &corev1.CatalogBadge{Key: f.Key, Scope: scope, Position: pos, Labels: labels, Icon: f.Icon, Status: st, Rule: rule},
			Create: f.Create, ValidateOnly: validateOnly, Reason: reason,
		})
		return resp.GetCheck(), err
	case "ders-konu":
		resp, err := core.SaveCatalogStudyTag(ctx, &corev1.SaveCatalogStudyTagRequest{
			Tag:    &corev1.CatalogStudyTag{Key: f.Key, ParentKey: f.Parent, Position: pos, Labels: labels, Status: st},
			Create: f.Create, ValidateOnly: validateOnly, Reason: reason,
		})
		return resp.GetCheck(), err
	case "on-ayar":
		resp, err := core.SaveCatalogScorePreset(ctx, &corev1.SaveCatalogScorePresetRequest{
			Preset: &corev1.AdminCatalogScorePreset{Key: f.Key, Position: pos, Labels: labels, Params: f.Params, Status: st},
			Create: f.Create, ValidateOnly: validateOnly, Reason: reason,
		})
		return resp.GetCheck(), err
	}
	return nil, formProblem("Bilinmeyen kayıt türü.")
}

// catalogSubmit handles the form's two buttons.
func (h *handlers) catalogSubmit(w http.ResponseWriter, r *http.Request) {
	kind, ok := h.catalogKind(w, r)
	if !ok {
		return
	}
	f := readCatalogForm(r, kind)
	// back is the form again; a failed request goes to the catalog page.
	back := "/katalog/" + kind
	if !f.Create && catalogKeyPattern.MatchString(f.Key) {
		back += "?" + url.Values{"anahtar": {f.Key}}.Encode()
	}
	preview := r.PostFormValue("islem") != "kaydet"
	reason := ""
	if !preview {
		s := sessionFrom(r.Context())
		if err := s.claims.RequireRecentAuth(h.cfg.Now()); err != nil {
			h.toSignIn(w, r, true, back)
			return
		}
		reason = strings.TrimSpace(r.PostFormValue("gerekce"))
		if n := utf8.RuneCountInString(reason); !utf8.ValidString(reason) || n < minReason || n > maxReason {
			f.Problem = errorText("gerekce")
			h.renderCatalogForm(w, r, http.StatusUnprocessableEntity, f)
			return
		}
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	check, err := f.save(ctx, h.cfg.Core, preview, reason)
	var fp formProblem
	switch {
	case err == nil:
	case asFormProblem(err, &fp):
		f.Problem = string(fp)
		h.renderCatalogForm(w, r, http.StatusUnprocessableEntity, f)
		return
	default:
		if pr, field, ok := checkProblem(err); ok {
			f.Problem = pr
			f.Check = &corev1.CatalogCheck{Problem: pr, Field: field}
			h.renderCatalogForm(w, r, http.StatusUnprocessableEntity, f)
			return
		}
		h.failed(w, r, "/katalog", err)
		return
	}
	if !preview {
		if !catalogKeyPattern.MatchString(f.Key) {
			done(w, r, "/katalog", "sonuc", "katalog-kaydedildi")
			return
		}
		http.Redirect(w, r, "/katalog/"+kind+"?"+url.Values{"anahtar": {f.Key}, "sonuc": {"katalog-kaydedildi"}}.Encode(), http.StatusSeeOther)
		return
	}
	f.Check = check
	f.Problem = check.GetProblem()
	f.Warnings = h.versionWarnings(check.GetMinClientVersion())
	code := http.StatusOK
	if f.Problem != "" {
		code = http.StatusUnprocessableEntity
	}
	h.renderCatalogForm(w, r, code, f)
}

func asFormProblem(err error, fp *formProblem) bool {
	return errors.As(err, fp)
}

// checkProblem reads a failed check from core's ERROR_REASON_VALIDATION:
// the problem and the field in the ErrorInfo metadata.
func checkProblem(err error) (problem, field string, ok bool) {
	st := status.Convert(err)
	if st.Code() != codes.InvalidArgument {
		return "", "", false
	}
	for _, d := range st.Details() {
		if info, isInfo := d.(*errdetails.ErrorInfo); isInfo && info.GetReason() == "ERROR_REASON_VALIDATION" {
			if p := info.GetMetadata()["problem"]; p != "" {
				return p, info.GetMetadata()["field"], true
			}
		}
	}
	return "", "", false
}

// versionWarnings names the served platforms whose lowest supported version
// is below min: their clients show "update the app" in place of a
// component (ADR-0025 §7, §9). A type may still be published.
func (h *handlers) versionWarnings(least string) []string {
	if least == "" {
		return nil
	}
	need, err := clientversion.Parse(least)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range h.cfg.ClientVersions.Served() {
		lo := h.cfg.ClientVersions.Range(p).Min
		if lo.IsZero() || lo.Compare(need) < 0 {
			name := string(p)
			if !lo.IsZero() {
				name += " (en düşük desteklenen " + lo.String() + ")"
			} else {
				name += " (en düşük sürüm ayarlı değil)"
			}
			out = append(out, name)
		}
	}
	return out
}

// blockDomain and unblockDomain change the blocked domain list.
func (h *handlers) blockDomain(w http.ResponseWriter, r *http.Request) {
	h.domainChange(w, r, "alan-adi-eklendi", func(ctx context.Context, d, reason string) error {
		_, err := h.cfg.Core.BlockDomain(ctx, &corev1.BlockDomainRequest{Domain: d, Reason: reason})
		return err
	})
}

func (h *handlers) unblockDomain(w http.ResponseWriter, r *http.Request) {
	h.domainChange(w, r, "alan-adi-kaldirildi", func(ctx context.Context, d, reason string) error {
		_, err := h.cfg.Core.UnblockDomain(ctx, &corev1.UnblockDomainRequest{Domain: d, Reason: reason})
		return err
	})
}

func (h *handlers) domainChange(w http.ResponseWriter, r *http.Request, notice string, do func(ctx context.Context, domain, reason string) error) {
	const back = "/katalog"
	reason, ok := h.change(w, r, back, maxReason)
	if !ok {
		return
	}
	d := strings.TrimSpace(r.PostFormValue("alan_adi"))
	if d == "" || len(d) > maxDomainInput {
		done(w, r, back, "hata", "alan-adi")
		return
	}
	ctx, cancel := sessionFrom(r.Context()).upstream(r.Context())
	defer cancel()
	if err := do(ctx, d, reason); err != nil {
		if _, _, isCheck := checkProblem(err); isCheck {
			done(w, r, back, "hata", "alan-adi")
			return
		}
		h.failed(w, r, back, err)
		return
	}
	done(w, r, back, "sonuc", notice)
}
