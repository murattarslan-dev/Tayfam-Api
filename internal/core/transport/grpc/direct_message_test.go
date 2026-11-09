package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/directmessage"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDirectMessage struct {
	caller, recipient id.ID
	a                 directmessage.Answer
	err               error
}

func (f *fakeDirectMessage) Check(_ context.Context, caller, recipient id.ID) (directmessage.Answer, error) {
	f.caller, f.recipient = caller, recipient
	return f.a, f.err
}

func TestDirectMessageServer(t *testing.T) {
	user, other := id.New(), id.New()
	f := &fakeDirectMessage{}
	srv := NewDirectMessageServer(f, discard)

	if _, err := srv.CheckDirectMessage(context.Background(), &corev1.CheckDirectMessageRequest{RecipientId: other.String()}); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	ctx := callerCtx(user)
	for _, c := range []struct {
		a     directmessage.Answer
		want  corev1.DirectMessageDecision
		limit int32
	}{
		{directmessage.Answer{Decision: directmessage.Denied}, corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED, 0},
		{directmessage.Answer{Decision: directmessage.Active}, corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_ACTIVE, 0},
		{directmessage.Answer{Decision: directmessage.Request, RequestLimit: 5}, corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_REQUEST, 5},
		// An unknown decision is never a permission.
		{directmessage.Answer{}, corev1.DirectMessageDecision_DIRECT_MESSAGE_DECISION_DENIED, 0},
	} {
		f.a = c.a
		resp, err := srv.CheckDirectMessage(ctx, &corev1.CheckDirectMessageRequest{RecipientId: other.String()})
		if err != nil {
			t.Fatal(err)
		}
		if f.caller != user || f.recipient != other {
			t.Fatalf("app girdisi = %+v", f)
		}
		if resp.GetDecision() != c.want || resp.GetDailyRequestLimit() != c.limit {
			t.Errorf("%v: yanıt = %v", c.a, resp)
		}
	}
	_, err := srv.CheckDirectMessage(ctx, &corev1.CheckDirectMessageRequest{RecipientId: "x"})
	if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != "recipient_id" {
		t.Fatalf("geçersiz alıcı: %v", err)
	}
	f.err = apperr.Validation("recipient_id")
	_, err = srv.CheckDirectMessage(ctx, &corev1.CheckDirectMessageRequest{RecipientId: user.String()})
	if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != "recipient_id" {
		t.Fatalf("kendisi: %v", err)
	}
}
