package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

const contactID = "0193b2a4-7c3e-7a10-8b2c-3d4e5f607182"

func testDiscoverItem() *corev1.DiscoverItem {
	return &corev1.DiscoverItem{
		Event: &corev1.DiscoverEvent{
			EventId: eventID, Type: corev1.EventType_EVENT_TYPE_HIKE, Title: "Belgrad yürüyüşü",
			StartTime: timestamppb.New(t0.Add(48 * time.Hour)), EndTime: timestamppb.New(t0.Add(52 * time.Hour)),
			City: "İstanbul", District: "Sarıyer",
		},
		Tayfa: &corev1.DiscoverTayfa{
			TayfaId: tayfaID, Name: "Yürüyüşçüler", CreateTime: timestamppb.New(t0.Add(-365 * 24 * time.Hour)),
			EventCount: 12, AttendanceCount: 80,
		},
		Friends:     []*corev1.DiscoverPerson{{UserId: userID, DisplayName: "Ayşe"}},
		FriendCount: 3,
	}
}

func (f *fakeCore) ListDiscoverFeed(ctx context.Context, in *corev1.ListDiscoverFeedRequest, _ ...grpc.CallOption) (*corev1.ListDiscoverFeedResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	if f.feed != nil {
		return &corev1.ListDiscoverFeedResponse{Items: f.feed}, nil
	}
	return &corev1.ListDiscoverFeedResponse{Items: []*corev1.DiscoverItem{testDiscoverItem()}}, nil
}
func (f *fakeCore) JoinFromDiscover(ctx context.Context, in *corev1.JoinFromDiscoverRequest, _ ...grpc.CallOption) (*corev1.JoinFromDiscoverResponse, error) {
	m := testMembership()
	m.Status = corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING
	return &corev1.JoinFromDiscoverResponse{Membership: m}, f.record(ctx, in)
}
func (f *fakeCore) GetTayfaDiscover(ctx context.Context, in *corev1.GetTayfaDiscoverRequest, _ ...grpc.CallOption) (*corev1.GetTayfaDiscoverResponse, error) {
	return &corev1.GetTayfaDiscoverResponse{State: corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_UNDER_REVIEW, Enabled: true}, f.record(ctx, in)
}
func (f *fakeCore) SetTayfaDiscover(ctx context.Context, in *corev1.SetTayfaDiscoverRequest, _ ...grpc.CallOption) (*corev1.SetTayfaDiscoverResponse, error) {
	return &corev1.SetTayfaDiscoverResponse{State: corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_PENDING, Enabled: in.GetEnabled()}, f.record(ctx, in)
}
func (f *fakeCore) GetEventFeedback(ctx context.Context, in *corev1.GetEventFeedbackRequest, _ ...grpc.CallOption) (*corev1.GetEventFeedbackResponse, error) {
	return &corev1.GetEventFeedbackResponse{}, f.record(ctx, in)
}
func (f *fakeCore) SetEventFeedback(ctx context.Context, in *corev1.SetEventFeedbackRequest, _ ...grpc.CallOption) (*corev1.SetEventFeedbackResponse, error) {
	return &corev1.SetEventFeedbackResponse{Answer: in.GetAnswer(), UpdateTime: timestamppb.New(t0)}, f.record(ctx, in)
}
func (f *fakeCore) GetSafetyContact(ctx context.Context, in *corev1.GetSafetyContactRequest, _ ...grpc.CallOption) (*corev1.GetSafetyContactResponse, error) {
	return &corev1.GetSafetyContactResponse{SafetyContact: &corev1.SafetyContact{}}, f.record(ctx, in)
}
func (f *fakeCore) SetSafetyContact(ctx context.Context, in *corev1.SetSafetyContactRequest, _ ...grpc.CallOption) (*corev1.SetSafetyContactResponse, error) {
	return &corev1.SetSafetyContactResponse{SafetyContact: &corev1.SafetyContact{
		Contact: &corev1.DiscoverPerson{UserId: in.GetContactUserId(), DisplayName: "Can"}, GraceMinutes: 60,
	}}, f.record(ctx, in)
}
func (f *fakeCore) ClearSafetyContact(ctx context.Context, in *corev1.ClearSafetyContactRequest, _ ...grpc.CallOption) (*corev1.ClearSafetyContactResponse, error) {
	return &corev1.ClearSafetyContactResponse{}, f.record(ctx, in)
}
func (f *fakeCore) MarkReturned(ctx context.Context, in *corev1.MarkReturnedRequest, _ ...grpc.CallOption) (*corev1.MarkReturnedResponse, error) {
	return &corev1.MarkReturnedResponse{SafetyContact: &corev1.SafetyContact{GraceMinutes: 60, ReturnTime: timestamppb.New(t0)}}, f.record(ctx, in)
}
func (f *fakeCore) ListSafetyShares(ctx context.Context, in *corev1.ListSafetySharesRequest, _ ...grpc.CallOption) (*corev1.ListSafetySharesResponse, error) {
	if err := f.record(ctx, in); err != nil {
		return nil, err
	}
	return &corev1.ListSafetySharesResponse{Shares: []*corev1.SafetyShare{{
		Participant: &corev1.DiscoverPerson{UserId: userID, DisplayName: "Ayşe"},
		StartTime:   timestamppb.New(t0), EndTime: timestamppb.New(t0.Add(time.Hour)),
		GraceMinutes: 60, AlertTime: timestamppb.New(t0.Add(2 * time.Hour)),
		State: corev1.SafetyShareState_SAFETY_SHARE_STATE_ALERT_SENT,
	}}}, nil
}

// TestDiscoverFeed: the feed carries facts and visible friends, never a
// score, a position, a meeting point or coordinates (ADR-0033 §4).
func TestDiscoverFeed(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	w := as(h, "GET", "/v1/discover/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("akış: %d %s", w.Code, w.Body)
	}
	if _, tok, _ := f.last(); tok != "USERTOKEN" {
		t.Fatalf("token iletilmedi: %q", tok)
	}
	v := decodeBody[discoverFeedJSON](t, w)
	if len(v.Items) != 1 || v.Items[0].FriendCount != 3 || v.Items[0].Tayfa.EventCount != 12 || v.Items[0].Event.Type != "hike" ||
		len(v.Items[0].Friends) != 1 || v.Items[0].Friends[0].DisplayName != "Ayşe" {
		t.Fatalf("akış: %+v", v)
	}
	body := w.Body.String()
	for _, banned := range []string{"score", "rank", "position", "meeting", "lat", "lng", "trusted", "verified", "approved", "next_page"} {
		if strings.Contains(body, `"`+banned) {
			t.Errorf("akışta %q alanı var: %s", banned, body)
		}
	}
}

// TestDiscoverFeedNoFriends: Keşfet is open to everyone (ADR-0035); an
// item with no visible friend still carries both fields, as an empty list
// and 0, so the client can tell "no friend" from a missing field.
func TestDiscoverFeedNoFriends(t *testing.T) {
	it := testDiscoverItem()
	it.Friends, it.FriendCount = nil, 0
	h := coreRouter(&fakeCore{feed: []*corev1.DiscoverItem{it}})
	w := as(h, "GET", "/v1/discover/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("akış: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"friends":[]`) || !strings.Contains(body, `"friend_count":0`) {
		t.Fatalf("arkadaşsız öğe: %s", body)
	}
}

// TestDiscoverFeatureDisabled: with Keşfet off core answers
// FEATURE_DISABLED, which is 404 feature-disabled with no body data.
func TestDiscoverFeatureDisabled(t *testing.T) {
	f := &fakeCore{err: coreErr(codes.NotFound, "ERROR_REASON_FEATURE_DISABLED")}
	h := coreRouter(f)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/discover/events", ""},
		{"GET", "/v1/me/safety-shares", ""},
		{"POST", "/v1/discover/tayfas/" + tayfaID + "/join", ""},
	} {
		w := as(h, c.method, c.path, c.body)
		if w.Code != http.StatusNotFound || problemType(t, w) != "feature-disabled" || strings.Contains(w.Body.String(), "items") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
}

func TestDiscoverRoutes(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)

	w := as(h, "POST", "/v1/discover/tayfas/"+tayfaID+"/join", "")
	got, _, _ := f.last()
	if w.Code != http.StatusCreated || got.(*corev1.JoinFromDiscoverRequest).GetTayfaId() != tayfaID ||
		decodeBody[membershipJSON](t, w).Status != "pending" {
		t.Fatalf("katılım: %d %s", w.Code, w.Body)
	}

	w = as(h, "PUT", "/v1/tayfas/"+tayfaID+"/discover", `{"enabled":true}`)
	got, _, _ = f.last()
	if w.Code != http.StatusOK || !got.(*corev1.SetTayfaDiscoverRequest).GetEnabled() ||
		decodeBody[tayfaDiscoverJSON](t, w) != (tayfaDiscoverJSON{State: "pending", Enabled: true}) {
		t.Fatalf("ayar: %d %s", w.Code, w.Body)
	}
	w = as(h, "GET", "/v1/tayfas/"+tayfaID+"/discover", "")
	if w.Code != http.StatusOK || decodeBody[tayfaDiscoverJSON](t, w).State != "under_review" {
		t.Fatalf("durum: %d %s", w.Code, w.Body)
	}

	ev := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	w = as(h, "PUT", ev+"/feedback", `{"answer":"serious"}`)
	got, _, _ = f.last()
	if w.Code != http.StatusOK || got.(*corev1.SetEventFeedbackRequest).GetAnswer() != corev1.FeedbackAnswer_FEEDBACK_ANSWER_SERIOUS ||
		decodeBody[eventFeedbackJSON](t, w).Answer != "serious" {
		t.Fatalf("geri bildirim: %d %s", w.Code, w.Body)
	}
	if w = as(h, "GET", ev+"/feedback", ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "{}" {
		t.Fatalf("cevapsız geri bildirim: %d %s", w.Code, w.Body)
	}

	w = as(h, "PUT", ev+"/safety-contact", `{"contact_user_id":"`+contactID+`","grace_minutes":90}`)
	got, _, _ = f.last()
	in := got.(*corev1.SetSafetyContactRequest)
	if w.Code != http.StatusOK || in.GetContactUserId() != contactID || in.GetGraceMinutes() != 90 || in.GetEventId() != eventID {
		t.Fatalf("güvenlik kişisi: %d %s", w.Code, w.Body)
	}
	if w = as(h, "PUT", ev+"/safety-contact", `{"contact_user_id":"`+contactID+`"}`); w.Code != http.StatusOK {
		t.Fatalf("varsayılan tolerans: %d", w.Code)
	}
	if got, _, _ = f.last(); got.(*corev1.SetSafetyContactRequest).GetGraceMinutes() != 0 {
		t.Fatal("tolerans yokken 0 (core'da 60) gitmeli")
	}
	if w = as(h, "GET", ev+"/safety-contact", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), "contact\"") {
		t.Fatalf("seçilmemiş: %d %s", w.Code, w.Body)
	}
	if w = as(h, "POST", ev+"/safety-contact/return", ""); w.Code != http.StatusOK || decodeBody[safetyContactJSON](t, w).ReturnedAt == nil {
		t.Fatalf("döndüm: %d %s", w.Code, w.Body)
	}
	if w = as(h, "DELETE", ev+"/safety-contact", ""); w.Code != http.StatusNoContent {
		t.Fatalf("kaldırma: %d", w.Code)
	}

	w = as(h, "GET", "/v1/me/safety-shares", "")
	v := decodeBody[safetyShareListJSON](t, w)
	if w.Code != http.StatusOK || len(v.Shares) != 1 || v.Shares[0].State != "alert_sent" || v.Shares[0].AlertSentAt == nil ||
		v.Shares[0].GraceMinutes != 60 || v.Shares[0].Participant == nil {
		t.Fatalf("paylaşımlar: %d %s", w.Code, w.Body)
	}
	for _, banned := range []string{"lat", "lng", "description", "tayfa", "event", "title", "meeting", "city", "district"} {
		if strings.Contains(w.Body.String(), `"`+banned) {
			t.Errorf("paylaşımda %q var", banned)
		}
	}
}

func TestDiscoverRoutesValidateInput(t *testing.T) {
	f := &fakeCore{}
	h := coreRouter(f)
	ev := "/v1/tayfas/" + tayfaID + "/events/" + eventID
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/discover/tayfas/x/join", ""},
		{"PUT", "/v1/tayfas/" + tayfaID + "/discover", `{}`},
		{"PUT", "/v1/tayfas/" + tayfaID + "/discover", `{"enabled":"evet"}`},
		{"PUT", ev + "/feedback", `{"answer":"harika"}`},
		{"PUT", ev + "/feedback", `{"answer":"ok","text":"metin yok"}`},
		{"PUT", ev + "/safety-contact", `{"contact_user_id":"x"}`},
		{"PUT", ev + "/safety-contact", `{"contact_user_id":"` + contactID + `","grace_minutes":0}`},
		{"PUT", ev + "/safety-contact", `{"contact_user_id":"` + contactID + `","lat":41.1}`},
		{"GET", "/v1/tayfas/" + tayfaID + "/events/x/feedback", ""},
	} {
		if w := as(h, c.method, c.path, c.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s %s: %d", c.method, c.path, c.body, w.Code)
		}
	}
	if _, _, n := f.last(); n != 0 {
		t.Fatalf("core çağrıldı: %d", n)
	}
}

// Members see joined_via_discover only when core fills it (staff).
func TestMembershipDiscoverLabel(t *testing.T) {
	m := testMembership()
	b, err := json.Marshal(membershipFrom(m))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "joined_via_discover") {
		t.Fatal("etiket boşken alan dönmemeli")
	}
	m.JoinedViaDiscover = true
	if !membershipFrom(m).JoinedViaDiscover {
		t.Fatal("etiket taşınmadı")
	}
}
