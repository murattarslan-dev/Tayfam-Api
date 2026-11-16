package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/instant"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeInstants records what transport passed on.
type fakeInstants struct {
	err   error
	calls int
	user  id.ID
	in    instant.Input
}

func (f *fakeInstants) Create(_ context.Context, u id.ID, in instant.Input) (instant.Result, error) {
	f.calls++
	f.user, f.in = u, in
	if f.err != nil {
		return instant.Result{}, f.err
	}
	tf, ev := id.New(), id.New()
	return instant.Result{
		Tayfa: tayfa.View{ID: tf, Name: in.Event.Title, Kind: "instant", InstantEventID: ev, JoinPolicy: "approval", Status: "active"},
		Event: event.Event{ID: ev, TayfaID: tf, Type: "general", Title: in.Event.Title, Status: "scheduled"},
	}, nil
}

func instantReq(invitees ...string) *corev1.CreateInstantEventRequest {
	r := createReq("")
	return &corev1.CreateInstantEventRequest{Event: r, InviteeUserIds: invitees}
}

func TestCreateInstantEvent(t *testing.T) {
	f := &fakeInstants{}
	srv := NewEventServer(&fakeEvents{}, discard).WithInstant(f)
	user, friend := id.New(), id.New()

	// No token, no call.
	_, err := srv.CreateInstantEvent(context.Background(), instantReq())
	if code, _ := infoOf(t, err); code != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("token'sız: %v", err)
	}

	ctx := callerCtx(user)
	resp, err := srv.CreateInstantEvent(ctx, instantReq(friend.String()))
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.in.Event.Title != "Buluşma" || len(f.in.Invitees) != 1 || f.in.Invitees[0] != friend {
		t.Fatalf("app'e giden = %+v", f.in)
	}
	if resp.GetTayfa().GetKind() != corev1.TayfaKind_TAYFA_KIND_INSTANT || resp.GetTayfa().GetInstantEventId() != resp.GetEvent().GetId() {
		t.Fatalf("cevap = %v", resp)
	}

	// Malformed requests never reach the app layer.
	f.calls = 0
	withTayfa := instantReq()
	withTayfa.Event.TayfaId = id.New().String()
	withRec := instantReq()
	withRec.Event.Recurrence = &corev1.Recurrence{Rrule: "FREQ=WEEKLY"}
	withCover := instantReq()
	withCover.Event.CoverUploadId = id.New().String()
	many := instantReq()
	for range instant.MaxInvitees + 1 {
		many.InviteeUserIds = append(many.InviteeUserIds, id.New().String())
	}
	for name, tc := range map[string]struct {
		req   *corev1.CreateInstantEventRequest
		field string
	}{
		"etkinlik yok":   {&corev1.CreateInstantEventRequest{}, "event"},
		"tayfa verilmiş": {withTayfa, "event.tayfa_id"},
		"tekrar":         {withRec, "event.recurrence"},
		"kapak":          {withCover, "event.cover_upload_id"},
		"bozuk davetli":  {instantReq("x"), "invitee_user_ids"},
		"fazla davetli":  {many, "invitee_user_ids"},
	} {
		_, err := srv.CreateInstantEvent(ctx, tc.req)
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetMetadata()["field"] != tc.field {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != 0 {
		t.Fatal("geçersiz istek app katmanına ulaştı")
	}

	// The open limit's reason.
	f.err = apperr.ErrInstantEventLimit
	_, err = srv.CreateInstantEvent(ctx, instantReq())
	if code, info := infoOf(t, err); code != codes.ResourceExhausted || info.GetReason() != corev1.ErrorReason_ERROR_REASON_INSTANT_EVENT_LIMIT.String() {
		t.Fatalf("sınır: %v", err)
	}

	// Without the instant service the method is not served.
	_, err = NewEventServer(&fakeEvents{}, discard).CreateInstantEvent(ctx, instantReq())
	if code, _ := infoOf(t, err); code != codes.Unimplemented {
		t.Fatalf("servis yok: %v", err)
	}
}

func TestConvertInstantTayfa(t *testing.T) {
	f := &fakeTayfas{view: tayfa.View{ID: id.New(), Name: "Tayfa", Kind: "standard", JoinPolicy: "open", Status: "active"}}
	srv := NewTayfaServer(f, discard)
	user, tf := id.New(), id.New()
	resp, err := srv.ConvertInstantTayfa(callerCtx(user), &corev1.ConvertInstantTayfaRequest{
		TayfaId: tf.String(), Name: "Bisikletçiler", Description: "Hafta sonu",
		Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_SPORTS}, JoinPolicy: corev1.JoinPolicy_JOIN_POLICY_OPEN,
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != tf || f.convert.Name != "Bisikletçiler" || f.convert.Description != "Hafta sonu" ||
		len(f.convert.Interests) != 1 || f.convert.Interests[0] != "sports" || f.convert.JoinPolicy != "open" {
		t.Fatalf("app'e giden = %+v", f.convert)
	}
	if resp.GetTayfa().GetKind() != corev1.TayfaKind_TAYFA_KIND_STANDARD {
		t.Fatalf("tür = %v", resp.GetTayfa().GetKind())
	}
	_, err = srv.ConvertInstantTayfa(callerCtx(user), &corev1.ConvertInstantTayfaRequest{TayfaId: tf.String(), Name: "X", Interests: []corev1.EventType{0}})
	if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != "interests" {
		t.Fatalf("ilgi alanı: %v", err)
	}
}
