package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/gateway/app/clientversion"
)

// The catalog pages (issue #262).

func (f *fakeCore) GetCatalogAdmin(ctx context.Context, _ *corev1.GetCatalogAdminRequest, _ ...grpc.CallOption) (*corev1.GetCatalogAdminResponse, error) {
	f.add(ctx, "GetCatalogAdmin")
	active, retired := corev1.CatalogStatus_CATALOG_STATUS_ACTIVE, corev1.CatalogStatus_CATALOG_STATUS_RETIRED
	return &corev1.GetCatalogAdminResponse{
		CatalogVersion: 7,
		Interests: []*corev1.CatalogInterest{
			{Key: "games", Position: 50, Labels: map[string]string{"tr": "Oyun"}, Icon: "casino", Status: active, DefaultWidgets: []string{"stats"}},
			{Key: "old_games", Position: 60, Labels: map[string]string{"tr": f.tayfaName}, Icon: "casino", Status: retired},
		},
		EventTypes: []*corev1.AdminCatalogEventType{{
			Type: &corev1.CatalogEventType{Key: "chess", InterestKeys: []string{"games"}, Labels: map[string]string{"tr": "Satranç"},
				Icon: "chess", Modes: []corev1.EventMode{corev1.EventMode_EVENT_MODE_IN_PERSON, corev1.EventMode_EVENT_MODE_ONLINE},
				Status: active, Version: 3, Definition: `{"components":[{"kind":"draw"},{"kind":"timer"}]}`},
			Position: 530, MinClientVersion: "0.3.0",
		}},
		Badges: []*corev1.CatalogBadge{{
			Key: "first_event", Scope: corev1.BadgeScope_BADGE_SCOPE_USER, Labels: map[string]string{"tr": "İlk etkinlik"}, Icon: "stars",
			Status: active, Rule: &corev1.CatalogBadgeRule{Kind: corev1.BadgeRuleKind_BADGE_RULE_KIND_FIRST},
		}},
		StudyTags: []*corev1.CatalogStudyTag{
			{Key: "lgs", Labels: map[string]string{"tr": "LGS"}, Status: active},
			{Key: "lgs_math", ParentKey: "lgs", Labels: map[string]string{"tr": "Matematik"}, Status: active},
			{Key: "lgs_other", ParentKey: "lgs", Position: 999, Labels: map[string]string{"tr": "Diğer"}, Status: active},
		},
		ScorePresets: []*corev1.AdminCatalogScorePreset{
			{Key: "batak", Position: 1, Labels: map[string]string{"tr": "Batak"}, Params: `{"side":"player"}`, Status: active},
		},
		BlockedDomains: []*corev1.AdminBlockedDomain{{Domain: "bit.ly", CreateTime: timestamppb.Now(), Initial: true}},
	}, f.err["GetCatalogAdmin"]
}

func (f *fakeCore) catalogSave(ctx context.Context, name string, req any) (*corev1.CatalogCheck, error) {
	f.add(ctx, name)
	f.catalogReq = req
	if err := f.err[name]; err != nil {
		return nil, err
	}
	if f.check != nil {
		return f.check, nil
	}
	return &corev1.CatalogCheck{}, nil
}

func (f *fakeCore) SaveCatalogInterest(ctx context.Context, in *corev1.SaveCatalogInterestRequest, _ ...grpc.CallOption) (*corev1.SaveCatalogInterestResponse, error) {
	c, err := f.catalogSave(ctx, "SaveCatalogInterest", in)
	if err != nil {
		return nil, err
	}
	return &corev1.SaveCatalogInterestResponse{Check: c, CatalogVersion: 8}, nil
}

func (f *fakeCore) SaveCatalogEventType(ctx context.Context, in *corev1.SaveCatalogEventTypeRequest, _ ...grpc.CallOption) (*corev1.SaveCatalogEventTypeResponse, error) {
	c, err := f.catalogSave(ctx, "SaveCatalogEventType", in)
	if err != nil {
		return nil, err
	}
	return &corev1.SaveCatalogEventTypeResponse{Check: c, CatalogVersion: 8}, nil
}

func (f *fakeCore) SaveCatalogBadge(ctx context.Context, in *corev1.SaveCatalogBadgeRequest, _ ...grpc.CallOption) (*corev1.SaveCatalogBadgeResponse, error) {
	c, err := f.catalogSave(ctx, "SaveCatalogBadge", in)
	if err != nil {
		return nil, err
	}
	return &corev1.SaveCatalogBadgeResponse{Check: c, CatalogVersion: 8}, nil
}

func (f *fakeCore) SaveCatalogStudyTag(ctx context.Context, in *corev1.SaveCatalogStudyTagRequest, _ ...grpc.CallOption) (*corev1.SaveCatalogStudyTagResponse, error) {
	c, err := f.catalogSave(ctx, "SaveCatalogStudyTag", in)
	if err != nil {
		return nil, err
	}
	return &corev1.SaveCatalogStudyTagResponse{Check: c, CatalogVersion: 8}, nil
}

func (f *fakeCore) SaveCatalogScorePreset(ctx context.Context, in *corev1.SaveCatalogScorePresetRequest, _ ...grpc.CallOption) (*corev1.SaveCatalogScorePresetResponse, error) {
	c, err := f.catalogSave(ctx, "SaveCatalogScorePreset", in)
	if err != nil {
		return nil, err
	}
	return &corev1.SaveCatalogScorePresetResponse{Check: c, CatalogVersion: 7}, nil
}

func (f *fakeCore) BlockDomain(ctx context.Context, in *corev1.BlockDomainRequest, _ ...grpc.CallOption) (*corev1.BlockDomainResponse, error) {
	if _, err := f.catalogSave(ctx, "BlockDomain", in); err != nil {
		return nil, err
	}
	return &corev1.BlockDomainResponse{}, nil
}

func (f *fakeCore) UnblockDomain(ctx context.Context, in *corev1.UnblockDomainRequest, _ ...grpc.CallOption) (*corev1.UnblockDomainResponse, error) {
	if _, err := f.catalogSave(ctx, "UnblockDomain", in); err != nil {
		return nil, err
	}
	return &corev1.UnblockDomainResponse{}, nil
}

func problemErr(field, problem string) error {
	st, _ := status.New(codes.InvalidArgument, "x").WithDetails(&errdetails.ErrorInfo{
		Reason: "ERROR_REASON_VALIDATION", Domain: "test", Metadata: map[string]string{"field": field, "problem": problem},
	})
	return st.Err()
}

// typeForm is a valid event type form for chess.
func typeForm(action string) url.Values {
	return url.Values{
		"islem": {action}, "anahtar": {"chess"}, "surum": {"3"}, "adlar": {`{"tr": "Satranç", "en": "Chess"}`}, "sira": {"530"},
		"ikon": {"chess"}, "ilgi_alanlari": {"games, learning"}, "yuz_yuze": {"1"}, "online": {"1"},
		"tanim": {`{"components": [{"kind": "draw"}]}`}, "durum": {"active"},
	}
}

func TestCatalogPage(t *testing.T) {
	e := setup(t)
	body := e.do(e.page("/katalog")).Body.String()
	for _, want := range []string{"Katalog sürümü 7", `href="/katalog/tur?anahtar=chess"`, "Satranç", "Emekli", "LGS", "Matematik",
		`href="/katalog/on-ayar?anahtar=batak"`, "bit.ly", "Başlangıç listesi", `action="/katalog/alan-adi-ekle"`} {
		if !strings.Contains(body, want) {
			t.Errorf("katalog sayfasında %q yok", want)
		}
	}
	// A study tag is shown under its parent.
	if i, j := strings.Index(body, "LGS"), strings.Index(body, "Matematik"); i < 0 || j < i {
		t.Error("ders/konu ağacı")
	}
	// The edit form shows the stored values.
	form := e.do(e.page("/katalog/tur?anahtar=chess")).Body.String()
	for _, want := range []string{`name="surum" value="3"`, `value="games"`, "&#34;kind&#34;: &#34;draw&#34;", `name="anahtar" value="chess" readonly`} {
		if !strings.Contains(form, want) {
			t.Errorf("tür formunda %q yok", want)
		}
	}
	// Unknown kinds and keys are 404; a key of another form is never echoed.
	for _, p := range []string{"/katalog/silah", "/katalog/tur?anahtar=yok_boyle", "/katalog/tur?anahtar=%3Cscript%3E"} {
		if w := e.do(e.page(p)); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<script>") {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// TestCatalogPreview: "Önizle" asks core with validate_only, needs no
// reason and shows the check; a component unknown to a supported build is
// warned about.
func TestCatalogPreview(t *testing.T) {
	e := setup(t)
	e.core.check = &corev1.CatalogCheck{
		MinClientVersion: "0.4.0", Version: 4, NewVersion: true,
		Components: []*corev1.CatalogPreviewComponent{{Kind: "draw", Key: "draw", DefaultOn: true, MinClientVersion: "0.4.0"}},
		Fields:     []*corev1.CatalogPreviewField{{Key: "tempo", Kind: "choice", Labels: map[string]string{"tr": "Tempo"}}},
	}
	versions, err := clientversion.NewPolicy(map[clientversion.Platform]clientversion.RawRange{clientversion.Android: {Min: "0.3.0"}})
	if err != nil {
		t.Fatal(err)
	}
	e.h = e.handler(t, func(c *Config) { c.ClientVersions = versions.Without(clientversion.Web) })
	w := e.do(e.form("/katalog/tur", typeForm("onizle")))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	req, ok := e.core.catalogReq.(*corev1.SaveCatalogEventTypeRequest)
	if !ok || !req.GetValidateOnly() || req.GetReason() != "" || req.GetType().GetVersion() != 3 || req.GetPosition() != 530 ||
		strings.Join(req.GetType().GetInterestKeys(), ",") != "games,learning" || len(req.GetType().GetModes()) != 2 ||
		req.GetType().GetLabels()["en"] != "Chess" || req.GetType().GetDefinition() != `{"components": [{"kind": "draw"}]}` {
		t.Fatalf("istek = %v", e.core.catalogReq)
	}
	body := w.Body.String()
	for _, want := range []string{"Önizleme", "yeni sürüm: 4", "Tempo", "draw", "android (en düşük desteklenen 0.3.0)", "ios (en düşük sürüm ayarlı değil)"} {
		if !strings.Contains(body, want) {
			t.Errorf("önizlemede %q yok", want)
		}
	}
	// The form keeps what was typed.
	if !strings.Contains(body, `value="games, learning"`) {
		t.Error("form değerleri kayboldu")
	}
	// A problem core finds is shown with the form, nothing else.
	e.core.check = &corev1.CatalogCheck{Problem: "tanım: components[0].kind: bilinmeyen değer", Field: "definition"}
	w = e.do(e.form("/katalog/tur", typeForm("onizle")))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "components[0].kind: bilinmeyen değer") ||
		strings.Contains(w.Body.String(), "Denetimden geçti") {
		t.Fatalf("sorunlu önizleme: %d", w.Code)
	}
}

// TestCatalogSave: "Kaydet" needs a reason (checked here first), sends
// the reason, and redirects to the saved entry; a failed check comes back
// as the form with the problem.
func TestCatalogSave(t *testing.T) {
	e := setup(t)
	v := typeForm("kaydet")
	w := e.do(e.form("/katalog/tur", cloneValues(v)))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Gerekçe 10 ile 1000") || len(e.upstreamCalls()) != 0 {
		t.Fatalf("gerekçesiz: %d %v", w.Code, e.upstreamCalls())
	}
	v.Set("gerekce", goodReason)
	w = e.do(e.form("/katalog/tur", cloneValues(v)))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Path != "/katalog/tur" || loc.Query().Get("anahtar") != "chess" || loc.Query().Get("sonuc") != "katalog-kaydedildi" {
		t.Fatalf("kayıt: %d %s", w.Code, loc)
	}
	if req := e.core.catalogReq.(*corev1.SaveCatalogEventTypeRequest); req.GetValidateOnly() || req.GetReason() != goodReason {
		t.Fatalf("istek = %v", req)
	}
	if body := e.do(e.page(loc.String())).Body.String(); !strings.Contains(body, "Katalog kaydı kaydedildi.") {
		t.Fatal("kayıt bildirimi yok")
	}

	e.core.err["SaveCatalogEventType"] = problemErr("version", "tür bu arada değişti; sayfayı yenileyip yeniden düzenle")
	w = e.do(e.form("/katalog/tur", cloneValues(v)))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "tür bu arada değişti") ||
		!strings.Contains(w.Body.String(), `value="games, learning"`) {
		t.Fatalf("sorunlu kayıt: %d", w.Code)
	}
	// core's own recent sign-in check leads to sign-in, back to the form.
	e.core.err["SaveCatalogEventType"] = reasonErr(codes.PermissionDenied, "ERROR_REASON_REAUTHENTICATION_REQUIRED")
	w = e.do(e.form("/katalog/tur", cloneValues(v)))
	if loc, _ := url.Parse(w.Header().Get("Location")); w.Code != http.StatusSeeOther || loc.Path != "/giris" || loc.Query().Get("yeniden") != "1" {
		t.Fatalf("yeniden giriş: %d %s", w.Code, loc)
	}

	// A field the page cannot read never reaches core.
	for name, mod := range map[string]func(url.Values){
		"bozuk adlar": func(v url.Values) { v.Set("adlar", "Satranç") },
		"bozuk sıra":  func(v url.Values) { v.Set("sira", "on") },
	} {
		e.resetCalls()
		x := cloneValues(v)
		mod(x)
		if w := e.do(e.form("/katalog/tur", x)); w.Code != http.StatusUnprocessableEntity || len(e.upstreamCalls()) != 0 {
			t.Errorf("%s: %d %v", name, w.Code, e.upstreamCalls())
		}
	}
}

// TestCatalogForms: every kind's form becomes its request.
func TestCatalogForms(t *testing.T) {
	e := setup(t)
	base := url.Values{"islem": {"onizle"}, "yeni": {"1"}, "adlar": {`{"tr": "Ad"}`}, "sira": {"5"}, "durum": {"retired"}}
	with := func(kv ...string) url.Values {
		v := cloneValues(base)
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	cases := map[string]struct {
		form  url.Values
		check func(any) bool
	}{
		"ilgi-alani": {with("anahtar", "dance", "ikon", "music_note", "bilesenler", "stats, upcoming"), func(r any) bool {
			x := r.(*corev1.SaveCatalogInterestRequest)
			return x.GetCreate() && x.GetInterest().GetKey() == "dance" && len(x.GetInterest().GetDefaultWidgets()) == 2 &&
				x.GetInterest().GetStatus() == corev1.CatalogStatus_CATALOG_STATUS_RETIRED
		}},
		"rozet": {with("anahtar", "ten", "ikon", "stars", "kapsam", "tayfa", "kural", "threshold", "metrik", "events", "deger", "10", "donem", "year"), func(r any) bool {
			b := r.(*corev1.SaveCatalogBadgeRequest).GetBadge()
			return b.GetScope() == corev1.BadgeScope_BADGE_SCOPE_TAYFA && b.GetRule().GetKind() == corev1.BadgeRuleKind_BADGE_RULE_KIND_THRESHOLD &&
				b.GetRule().GetValue() == 10 && b.GetRule().GetPeriod() == corev1.BadgePeriod_BADGE_PERIOD_YEAR && b.GetRule().GetMetric() == "events"
		}},
		"ders-konu": {with("anahtar", "kpss", "ust", ""), func(r any) bool {
			x := r.(*corev1.SaveCatalogStudyTagRequest)
			return x.GetCreate() && x.GetTag().GetKey() == "kpss" && x.GetTag().GetParentKey() == ""
		}},
		"on-ayar": {with("anahtar", "king", "kurallar", `{"side": "player"}`), func(r any) bool {
			p := r.(*corev1.SaveCatalogScorePresetRequest).GetPreset()
			return p.GetKey() == "king" && p.GetParams() == `{"side": "player"}` && p.GetPosition() == 5
		}},
		"tur": {with("anahtar", "new_type", "ikon", "event", "ilgi_alanlari", "general", "tanim", "{}", "surum", "9"), func(r any) bool {
			x := r.(*corev1.SaveCatalogEventTypeRequest)
			// A new type is always version 0, whatever the form says.
			return x.GetType().GetVersion() == 0 && len(x.GetType().GetModes()) == 0
		}},
	}
	for kind, c := range cases {
		w := e.do(e.form("/katalog/"+kind, c.form))
		if w.Code != http.StatusOK || !c.check(e.core.catalogReq) {
			t.Errorf("%s: %d %v", kind, w.Code, e.core.catalogReq)
		}
	}
	// A threshold that is not a number is refused here.
	e.resetCalls()
	if w := e.do(e.form("/katalog/rozet", with("anahtar", "ten", "kural", "threshold", "deger", "on"))); w.Code != http.StatusUnprocessableEntity || len(e.upstreamCalls()) != 0 {
		t.Fatalf("bozuk eşik: %d", w.Code)
	}
}

func TestBlockedDomains(t *testing.T) {
	e := setup(t)
	w := e.do(e.form("/katalog/alan-adi-ekle", url.Values{"alan_adi": {" example.com "}, "gerekce": {goodReason}}))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Path != "/katalog" || loc.Query().Get("sonuc") != "alan-adi-eklendi" {
		t.Fatalf("ekleme: %d %s", w.Code, loc)
	}
	if req := e.core.catalogReq.(*corev1.BlockDomainRequest); req.GetDomain() != "example.com" || req.GetReason() != goodReason {
		t.Fatalf("istek = %v", req)
	}
	// core's check becomes a fixed message, not its text.
	e.core.err["UnblockDomain"] = problemErr("domain", "alan adı listede yok")
	w = e.do(e.form("/katalog/alan-adi-kaldir", url.Values{"alan_adi": {"example.org"}, "gerekce": {goodReason}}))
	if loc, _ := url.Parse(w.Header().Get("Location")); w.Code != http.StatusSeeOther || loc.Query().Get("hata") != "alan-adi" {
		t.Fatalf("çıkarma: %d %s", w.Code, loc)
	}
	e.resetCalls()
	for _, v := range []url.Values{{"alan_adi": {""}, "gerekce": {goodReason}}, {"alan_adi": {"example.com"}}} {
		if w := e.do(e.form("/katalog/alan-adi-ekle", v)); w.Code != http.StatusSeeOther || len(e.upstreamCalls()) != 0 {
			t.Fatalf("eksik alan: %d %v", w.Code, e.upstreamCalls())
		}
	}
}

// TestCatalogAuditEntries: catalog actions are named and their key shown.
func TestCatalogAuditEntries(t *testing.T) {
	if actionLabel("catalog.type_update") == "catalog.type_update" || actionLabel("catalog.domain_block") == "catalog.domain_block" {
		t.Fatal("katalog işlemlerinin adı yok")
	}
}
