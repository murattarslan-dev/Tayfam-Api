package rest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// --- fakeCore: EventComponentService (issue #238) ---

const (
	eventComponentService = "tayfam.core.v1.EventComponentService"
	refereeID             = "01965a3c-0000-7000-8000-0000000000c1"
	scopeID               = "01965a3c-0000-7000-8000-0000000000c2"
)

func testEventComponents() []*corev1.EventComponent {
	return []*corev1.EventComponent{
		{Key: "draw", Kind: "draw", Enabled: true},
		{Key: "bracket", Kind: "bracket", Optional: true, Enabled: false, Choices: []*corev1.ComponentChoice{
			{Param: "best_of", Value: "3", Options: []string{"1", "3", "5"}},
			{Param: "format", Value: "swiss", Options: []string{"elimination", "league", "swiss"}},
		}},
	}
}

func testReferee() *corev1.EventReferee {
	return &corev1.EventReferee{
		Id: refereeID, UserId: userID, ComponentKey: "bracket", ScopeId: scopeID,
		CreateTime: timestamppb.New(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)),
	}
}

func (f *fakeCore) ListEventComponents(ctx context.Context, in *corev1.ListEventComponentsRequest, _ ...grpc.CallOption) (*corev1.ListEventComponentsResponse, error) {
	return &corev1.ListEventComponentsResponse{Components: testEventComponents(), CallerCanManage: true}, f.record(ctx, in)
}

func (f *fakeCore) SetEventComponent(ctx context.Context, in *corev1.SetEventComponentRequest, _ ...grpc.CallOption) (*corev1.SetEventComponentResponse, error) {
	return &corev1.SetEventComponentResponse{Components: testEventComponents(), Locked: true}, f.record(ctx, in)
}

func (f *fakeCore) ListEventReferees(ctx context.Context, in *corev1.ListEventRefereesRequest, _ ...grpc.CallOption) (*corev1.ListEventRefereesResponse, error) {
	return &corev1.ListEventRefereesResponse{Referees: []*corev1.EventReferee{testReferee(), {Id: scopeID, UserId: userID}}}, f.record(ctx, in)
}

func (f *fakeCore) AssignEventReferee(ctx context.Context, in *corev1.AssignEventRefereeRequest, _ ...grpc.CallOption) (*corev1.AssignEventRefereeResponse, error) {
	return &corev1.AssignEventRefereeResponse{Referee: testReferee()}, f.record(ctx, in)
}

func (f *fakeCore) RemoveEventReferee(ctx context.Context, in *corev1.RemoveEventRefereeRequest, _ ...grpc.CallOption) (*corev1.RemoveEventRefereeResponse, error) {
	return &corev1.RemoveEventRefereeResponse{}, f.record(ctx, in)
}

const componentsPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/components"
const refereesPath = "/v1/tayfas/" + tayfaID + "/events/" + eventID + "/referees"

func TestEventComponentRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "GET", componentsPath, "")
	if w.Code != http.StatusOK {
		t.Fatalf("liste: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	if r := got.(*corev1.ListEventComponentsRequest); r.GetTayfaId() != tayfaID || r.GetEventId() != eventID || tok != "USERTOKEN" {
		t.Fatalf("core'a giden: %v %q", got, tok)
	}
	list := decodeBody[eventComponentsJSON](t, w)
	if len(list.Components) != 2 || list.Locked || !list.CallerCanManage {
		t.Fatalf("liste: %+v", list)
	}
	if d := list.Components[0]; d.Key != "draw" || d.Kind != "draw" || d.Optional || !d.Enabled || d.Choices == nil || len(d.Choices) != 0 {
		t.Fatalf("kura: %+v", d)
	}
	b := list.Components[1]
	if b.Key != "bracket" || !b.Optional || b.Enabled || len(b.Choices) != 2 || b.Choices[0].Param != "best_of" || b.Choices[0].Value != "3" ||
		strings.Join(b.Choices[1].Options, ",") != "elimination,league,swiss" {
		t.Fatalf("eşleşme ağacı: %+v", b)
	}
	// Empty lists are arrays, not null.
	if !strings.Contains(w.Body.String(), `"choices":[]`) {
		t.Fatalf("boş seçim listesi: %s", w.Body)
	}

	w = as(h, "PUT", componentsPath+"/bracket", `{"enabled":true,"choices":[{"param":"format","value":"league"},{"param":"best_of","value":"5"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("seçim: %d %s", w.Code, w.Body)
	}
	got, _, _ = f.last()
	sel := got.(*corev1.SetEventComponentRequest).GetSelection()
	if sel.GetComponentKey() != "bracket" || sel.Enabled == nil || !sel.GetEnabled() || len(sel.GetChoices()) != 2 ||
		sel.GetChoices()["format"] != "league" || sel.GetChoices()["best_of"] != "5" {
		t.Fatalf("core'a giden seçim: %v", sel)
	}
	if out := decodeBody[eventComponentsJSON](t, w); !out.Locked || len(out.Components) != 2 {
		t.Fatalf("seçim yanıtı: %+v", out)
	}
	// Only a switch: enabled false is sent as false, not as unset.
	w = as(h, "PUT", componentsPath+"/bracket", `{"enabled":false}`)
	got, _, _ = f.last()
	if sel := got.(*corev1.SetEventComponentRequest).GetSelection(); w.Code != http.StatusOK || sel.Enabled == nil || sel.GetEnabled() || len(sel.GetChoices()) != 0 {
		t.Fatalf("kapatma: %d %v", w.Code, sel)
	}
	// Only choices: enabled stays unset.
	as(h, "PUT", componentsPath+"/bracket", `{"choices":[{"param":"format","value":"swiss"}]}`)
	got, _, _ = f.last()
	if sel := got.(*corev1.SetEventComponentRequest).GetSelection(); sel.Enabled != nil {
		t.Fatalf("gönderilmeyen enabled: %v", sel)
	}

	_, _, before := f.last()
	for name, tc := range map[string]struct{ path, body string }{
		"boş gövde":           {componentsPath + "/bracket", `{}`},
		"bozuk anahtar":       {componentsPath + "/Bracket", `{"enabled":true}`},
		"gövdede anahtar":     {componentsPath + "/bracket", `{"key":"draw","enabled":true}`},
		"bozuk parametre":     {componentsPath + "/bracket", `{"choices":[{"param":"Format","value":"swiss"}]}`},
		"boş değer":           {componentsPath + "/bracket", `{"choices":[{"param":"format","value":""}]}`},
		"uzun değer":          {componentsPath + "/bracket", `{"choices":[{"param":"format","value":"` + strings.Repeat("a", 41) + `"}]}`},
		"tekrar eden":         {componentsPath + "/bracket", `{"choices":[{"param":"format","value":"swiss"},{"param":"format","value":"league"}]}`},
		"seçenek listesi":     {componentsPath + "/bracket", `{"choices":[{"param":"format","value":"swiss","options":["x"]}]}`},
		"bilinmeyen alan":     {componentsPath + "/bracket", `{"enabled":true,"params":{}}`},
		"çok seçim":           {componentsPath + "/bracket", `{"choices":[` + strings.TrimSuffix(strings.Repeat(`{"param":"format","value":"swiss"},`, 21), ",") + `]}`},
		"bozuk etkinlik":      {"/v1/tayfas/" + tayfaID + "/events/x/components/bracket", `{"enabled":true}`},
		"sayı olarak enabled": {componentsPath + "/bracket", `{"enabled":1}`},
	} {
		if w := as(h, "PUT", tc.path, tc.body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, _, after := f.last(); after != before {
		t.Fatalf("bozuk istek core'a gitti: %d → %d", before, after)
	}

	f.err = coreErr(codes.FailedPrecondition, "ERROR_REASON_COMPONENT_LOCKED")
	if w := as(h, "PUT", componentsPath+"/bracket", `{"enabled":true}`); w.Code != http.StatusConflict || problemType(t, w) != "component-locked" {
		t.Fatalf("kilitli: %d %s", w.Code, w.Body)
	}
	f.err = coreErr(codes.NotFound, "ERROR_REASON_COMPONENT_NOT_FOUND")
	if w := as(h, "PUT", componentsPath+"/draw", `{"enabled":true}`); w.Code != http.StatusNotFound || problemType(t, w) != "component-not-found" {
		t.Fatalf("olmayan bileşen: %d %s", w.Code, w.Body)
	}
}

func TestEventRefereeRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "GET", refereesPath, "")
	list := decodeBody[eventRefereesJSON](t, w)
	if w.Code != http.StatusOK || len(list.Referees) != 2 {
		t.Fatalf("liste: %d %s", w.Code, w.Body)
	}
	if r := list.Referees[0]; r.ID != refereeID || r.UserID != userID || r.ComponentKey != "bracket" || r.ScopeID != scopeID ||
		r.CreatedAt == nil || !r.CreatedAt.Equal(time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("hakem: %+v", r)
	}
	// An assignment to the whole event has neither a component nor a scope.
	if strings.Count(w.Body.String(), "component_key") != 1 || strings.Count(w.Body.String(), "scope_id") != 1 {
		t.Fatalf("boş alanlar yazıldı: %s", w.Body)
	}

	w = as(h, "POST", refereesPath, `{"user_id":"`+userID+`","component_key":"bracket","scope_id":"`+scopeID+`"}`)
	if w.Code != http.StatusCreated || decodeBody[eventRefereeJSON](t, w).ID != refereeID {
		t.Fatalf("atama: %d %s", w.Code, w.Body)
	}
	got, tok, _ := f.last()
	if r := got.(*corev1.AssignEventRefereeRequest); r.GetTayfaId() != tayfaID || r.GetEventId() != eventID || r.GetUserId() != userID ||
		r.GetComponentKey() != "bracket" || r.GetScopeId() != scopeID || tok != "USERTOKEN" {
		t.Fatalf("core'a giden: %v", got)
	}
	if w := as(h, "POST", refereesPath, `{"user_id":"`+userID+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("etkinlik hakemi: %d %s", w.Code, w.Body)
	}

	_, _, before := f.last()
	for name, body := range map[string]string{
		"kullanıcı yok":     `{}`,
		"bozuk kullanıcı":   `{"user_id":"x"}`,
		"bozuk bileşen":     `{"user_id":"` + userID + `","component_key":"Bracket"}`,
		"bozuk kapsam":      `{"user_id":"` + userID + `","component_key":"bracket","scope_id":"x"}`,
		"bileşensiz kapsam": `{"user_id":"` + userID + `","scope_id":"` + scopeID + `"}`,
		"bilinmeyen alan":   `{"user_id":"` + userID + `","role":"admin"}`,
	} {
		if w := as(h, "POST", refereesPath, body); w.Code != http.StatusBadRequest || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := as(h, "DELETE", refereesPath+"/x", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bozuk hakem kimliği: %d", w.Code)
	}
	if _, _, after := f.last(); after != before {
		t.Fatalf("bozuk istek core'a gitti")
	}

	w = as(h, "DELETE", refereesPath+"/"+refereeID, "")
	got, _, _ = f.last()
	if r := got.(*corev1.RemoveEventRefereeRequest); w.Code != http.StatusNoContent || r.GetRefereeId() != refereeID || r.GetEventId() != eventID {
		t.Fatalf("kaldırma: %d %v", w.Code, got)
	}
	f.err = coreErr(codes.NotFound, "ERROR_REASON_MEMBER_NOT_FOUND")
	if w := as(h, "POST", refereesPath, `{"user_id":"`+userID+`"}`); w.Code != http.StatusNotFound || problemType(t, w) != "member-not-found" {
		t.Fatalf("üye değil: %d %s", w.Code, w.Body)
	}
}

// A new event carries the organizer's selections to core.
func TestCreateEventWithComponents(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	body := func(components string) string {
		return `{"type":"general","mode":"online","title":"Turnuva","starts_at":"2026-12-01T10:00:00Z","ends_at":"2026-12-01T12:00:00Z",` +
			`"online_url":"https://example.com/oda","client_request_id":"` + userID + `","components":` + components + `}`
	}
	w := as(h, "POST", "/v1/tayfas/"+tayfaID+"/events", body(`[{"key":"teams","enabled":true},{"key":"bracket","choices":[{"param":"format","value":"league"}]}]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("oluşturma: %d %s", w.Code, w.Body)
	}
	got, _, _ := f.last()
	sels := got.(*corev1.CreateEventRequest).GetComponents()
	if len(sels) != 2 || sels[0].GetComponentKey() != "teams" || sels[0].Enabled == nil || !sels[0].GetEnabled() ||
		sels[1].GetComponentKey() != "bracket" || sels[1].Enabled != nil || sels[1].GetChoices()["format"] != "league" {
		t.Fatalf("core'a giden seçimler: %v", sels)
	}
	_, _, before := f.last()
	for name, c := range map[string]string{
		"anahtarsız":    `[{"enabled":true}]`,
		"bozuk anahtar": `[{"key":"Teams","enabled":true}]`,
		"bozuk seçim":   `[{"key":"bracket","choices":[{"param":"format","value":""}]}]`,
		"çok bileşen":   `[` + strings.TrimSuffix(strings.Repeat(`{"key":"teams"},`, 21), ",") + `]`,
	} {
		if w := as(h, "POST", "/v1/tayfas/"+tayfaID+"/events", body(c)); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, _, after := f.last(); after != before {
		t.Fatal("bozuk seçim core'a gitti")
	}
}
