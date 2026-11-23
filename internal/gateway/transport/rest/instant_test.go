package rest

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/ratelimit"
)

const friendID = "01965a3c-0000-7000-8000-0000000000c1"

func TestCreateInstantEvent(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	start, end := t0.Add(48*time.Hour), t0.Add(50*time.Hour)
	ev := `{"type":"general","mode":"online","title":"Film gecesi","starts_at":"` + start.Format(time.RFC3339) +
		`","ends_at":"` + end.Format(time.RFC3339) + `","online_url":"https://example.org/film","client_request_id":"` + reqID + `"}`
	w := as(h, "POST", "/v1/instant-events", `{"event":`+ev+`,"invitee_user_ids":["`+friendID+`"]}`)
	if w.Code != 201 || w.Header().Get("Location") != "/v1/tayfas/"+tayfaID+"/events/"+eventID {
		t.Fatalf("açma: %d %v %s", w.Code, w.Header(), w.Body)
	}
	want := &corev1.CreateInstantEventRequest{
		Event: &corev1.CreateEventRequest{
			TypeKey: "general", Mode: corev1.EventMode_EVENT_MODE_ONLINE, Title: "Film gecesi",
			StartTime: timestamppb.New(start), EndTime: timestamppb.New(end), OnlineUrl: "https://example.org/film",
			ClientRequestId: reqID,
		},
		InviteeUserIds: []string{friendID},
	}
	if got, _, _ := f.last(); !proto.Equal(got, want) {
		t.Fatalf("core'a giden = %v", got)
	}
	b := decodeBody[instantEventJSON](t, w)
	if b.Tayfa.Kind != "instant" || b.Tayfa.InstantEventID != eventID || b.Event.ID != eventID {
		t.Fatalf("gövde = %+v", b)
	}

	// A now event with the friends to tell (issue #265).
	nowEv := `{"type":"general","mode":"online","title":"Film gecesi","starts_now":true,"online_url":"https://example.org/film","client_request_id":"` + reqID + `"}`
	if w := as(h, "POST", "/v1/instant-events", `{"event":`+nowEv+`,"now_recipient_ids":["`+friendID+`"]}`); w.Code != 201 {
		t.Fatalf("şimdi açma: %d %s", w.Code, w.Body)
	}
	if got, _, _ := f.last(); !got.(*corev1.CreateInstantEventRequest).GetEvent().GetStartsNow() ||
		!slices.Equal(got.(*corev1.CreateInstantEventRequest).GetNowRecipientIds(), []string{friendID}) {
		t.Fatalf("core'a giden = %v", got)
	}

	// Bad bodies never reach core.
	f2 := &fakeCore{}
	h2 := coreRouter(f2)
	for name, body := range map[string]string{
		"etkinlik yok":    `{"invitee_user_ids":[]}`,
		"tekrar":          `{"event":` + strings.TrimSuffix(ev, "}") + `,"recurrence":{"rrule":"FREQ=WEEKLY","time_zone":"Europe/Istanbul"}}}`,
		"kapak":           `{"event":` + strings.TrimSuffix(ev, "}") + `,"cover_upload_id":"` + uploadID2 + `"}}`,
		"bozuk davetli":   `{"event":` + ev + `,"invitee_user_ids":["1"]}`,
		"fazla davetli":   `{"event":` + ev + `,"invitee_user_ids":[` + strings.Repeat(`"`+friendID+`",`, 10) + `"` + friendID + `"]}`,
		"bilinmeyen alan": `{"event":` + ev + `,"tayfa_id":"` + tayfaID + `"}`,
		"başlıksız":       `{"event":` + strings.Replace(ev, `"Film gecesi"`, `""`, 1) + `}`,
		// now_recipient_ids only with starts_now, at most 30 (issue #265).
		"planlıya şimdi alıcısı": `{"event":` + ev + `,"now_recipient_ids":["` + friendID + `"]}`,
		"bozuk şimdi alıcısı":    `{"event":` + nowEv + `,"now_recipient_ids":["1"]}`,
		"fazla şimdi alıcısı":    `{"event":` + nowEv + `,"now_recipient_ids":[` + strings.Repeat(`"`+friendID+`",`, 30) + `"` + friendID + `"]}`,
	} {
		if w := as(h2, "POST", "/v1/instant-events", body); w.Code != 400 || problemType(t, w) != "invalid-request" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if _, _, n := f2.last(); n != 0 {
		t.Fatal("geçersiz istek core'a gitti")
	}
}

func TestConvertInstantTayfa(t *testing.T) {
	f := &fakeCore{}
	d := testCoreDeps(f, 100)
	d.CreateTayfaLimit = ratelimit.Every(time.Hour, 1)
	h := coreRouterWith(d)
	path := "/v1/tayfas/" + tayfaID + "/conversion"

	// A bad body costs no hourly slot.
	if w := as(h, "POST", path, `{"name":"a"}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 400 {
		t.Fatalf("bozuk istek: %d", w.Code)
	}
	w := as(h, "POST", path, `{"name":"Film Tayfası","interests":["general"],"join_policy":"open"}`, "CF-Connecting-IP", "203.0.113.7")
	if w.Code != 200 {
		t.Fatalf("dönüştürme: %d %s", w.Code, w.Body)
	}
	want := &corev1.ConvertInstantTayfaRequest{
		TayfaId: tayfaID, Name: "Film Tayfası", InterestKeys: []string{"general"},
		JoinPolicy: corev1.JoinPolicy_JOIN_POLICY_OPEN,
	}
	if got, _, _ := f.last(); !proto.Equal(got, want) {
		t.Fatalf("core'a giden = %v", got)
	}
	// Converting is creating a tayfa: one per hour per IP.
	if w := as(h, "POST", path, `{"name":"Film Tayfası"}`, "CF-Connecting-IP", "203.0.113.7"); w.Code != 429 {
		t.Fatalf("ikinci dönüşüm: %d", w.Code)
	}
	if _, _, n := f.last(); n != 1 {
		t.Fatalf("core çağrısı %d", n)
	}
}

func TestMyTayfasKind(t *testing.T) {
	f := &fakeCore{tayfa: testTayfa()}
	f.tayfa.Kind = corev1.TayfaKind_TAYFA_KIND_STANDARD
	w := as(coreRouter(f), "GET", "/v1/tayfas", "")
	b := decodeBody[myTayfaListJSON](t, w)
	if w.Code != 200 || len(b.Tayfas) != 2 || b.Tayfas[0].Tayfa == nil || b.Tayfas[0].Tayfa.Kind != "standard" ||
		strings.Contains(w.Body.String(), "instant_event_id") || strings.Contains(w.Body.String(), "converted_at") {
		t.Fatalf("liste = %d %s", w.Code, w.Body)
	}
}

// TestTayfaConvertedAt: converted_at is passed on only when core sets it
// (#219).
func TestTayfaConvertedAt(t *testing.T) {
	f := &fakeCore{tayfa: testTayfa()}
	f.tayfa.Kind = corev1.TayfaKind_TAYFA_KIND_STANDARD
	at := time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)
	f.tayfa.ConvertTime = timestamppb.New(at)
	w := as(coreRouter(f), "GET", "/v1/tayfas", "")
	b := decodeBody[myTayfaListJSON](t, w)
	if w.Code != 200 || len(b.Tayfas) == 0 || b.Tayfas[0].Tayfa == nil || b.Tayfas[0].Tayfa.ConvertedAt == nil ||
		!b.Tayfas[0].Tayfa.ConvertedAt.Equal(at) {
		t.Fatalf("liste = %d %s", w.Code, w.Body)
	}
	w = as(coreRouter(f), "GET", "/v1/tayfas/"+tayfaID, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"converted_at":"2026-11-16T09:30:00Z"`) {
		t.Fatalf("tayfa = %d %s", w.Code, w.Body)
	}
}
