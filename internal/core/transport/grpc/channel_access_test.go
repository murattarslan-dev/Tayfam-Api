package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/channelaccess"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeChannelAccess struct {
	caller, tayfa, target id.ID
	action                authz.Action
	d                     channelaccess.Decision
	err                   error
}

func (f *fakeChannelAccess) Authorize(_ context.Context, caller, tayfa id.ID, a authz.Action, target id.ID) (channelaccess.Decision, error) {
	f.caller, f.tayfa, f.action, f.target = caller, tayfa, a, target
	return f.d, f.err
}

// Every channel action of authz has a contract value, and back.
func TestChannelActionsComplete(t *testing.T) {
	if len(channelActions) != len(channelaccess.Actions) {
		t.Fatalf("%d sözleşme değeri, %d işlem", len(channelActions), len(channelaccess.Actions))
	}
	for _, a := range channelaccess.Actions {
		if channelActionToProto(a) == corev1.ChannelAction_CHANNEL_ACTION_UNSPECIFIED {
			t.Errorf("%s sözleşmede yok", a)
		}
	}
}

func TestChannelAccessServer(t *testing.T) {
	user, tf, author := id.New(), id.New(), id.New()
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	all := map[authz.Action]channelaccess.Denial{}
	for _, a := range channelaccess.Actions {
		all[a] = channelaccess.DenialRole
	}
	all[authz.ChannelView] = channelaccess.DenialNone
	f := &fakeChannelAccess{d: channelaccess.Decision{Denial: channelaccess.DenialHierarchy, All: all, Role: authz.Moderator, MutedUntil: until, ReadOnly: true}}
	srv := NewChannelAccessServer(f, discard)

	if _, err := srv.AuthorizeChannelAction(context.Background(), &corev1.AuthorizeChannelActionRequest{}); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	ctx := callerCtx(user)
	resp, err := srv.AuthorizeChannelAction(ctx, &corev1.AuthorizeChannelActionRequest{
		TayfaId: tf.String(), Action: corev1.ChannelAction_CHANNEL_ACTION_MODERATE, TargetUserId: author.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.caller != user || f.tayfa != tf || f.action != authz.ChannelModerate || f.target != author {
		t.Fatalf("app girdisi = %+v", f)
	}
	if resp.GetAllowed() || resp.GetDenial() != corev1.ChannelDenial_CHANNEL_DENIAL_HIERARCHY || resp.GetRole() != corev1.Role_ROLE_MODERATOR ||
		!resp.GetTayfaReadOnly() || !resp.GetMuteExpireTime().AsTime().Equal(until) || len(resp.GetDecisions()) != len(channelaccess.Actions) ||
		resp.GetDecisions()[0].GetAction() != corev1.ChannelAction_CHANNEL_ACTION_VIEW || resp.GetDecisions()[0].GetDenial() != corev1.ChannelDenial_CHANNEL_DENIAL_UNSPECIFIED ||
		resp.GetDecisions()[1].GetDenial() != corev1.ChannelDenial_CHANNEL_DENIAL_ROLE {
		t.Fatalf("yanıt = %v", resp)
	}
	for name, req := range map[string]*corev1.AuthorizeChannelActionRequest{
		"tayfa_id":       {TayfaId: "x", Action: corev1.ChannelAction_CHANNEL_ACTION_VIEW},
		"action":         {TayfaId: tf.String()},
		"target_user_id": {TayfaId: tf.String(), Action: corev1.ChannelAction_CHANNEL_ACTION_MODERATE, TargetUserId: "x"},
	} {
		_, err := srv.AuthorizeChannelAction(ctx, req)
		if code, info := infoOf(t, err); code != codes.InvalidArgument || info.GetMetadata()["field"] != name {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.err = apperr.ErrTayfaNotFound
	_, err = srv.AuthorizeChannelAction(ctx, &corev1.AuthorizeChannelActionRequest{TayfaId: tf.String(), Action: corev1.ChannelAction_CHANNEL_ACTION_VIEW})
	if code, info := infoOf(t, err); code != codes.NotFound || info.GetReason() != "ERROR_REASON_TAYFA_NOT_FOUND" {
		t.Fatalf("üye değil: %v", err)
	}
}
