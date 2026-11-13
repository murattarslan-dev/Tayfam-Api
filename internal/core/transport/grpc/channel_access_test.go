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
	chans                 []channelaccess.EventChannel
	d                     channelaccess.Decision
	err                   error
}

func (f *fakeChannelAccess) Authorize(_ context.Context, caller, tayfa id.ID, a authz.Action, target id.ID) (channelaccess.Decision, error) {
	f.caller, f.tayfa, f.action, f.target = caller, tayfa, a, target
	return f.d, f.err
}

func (f *fakeChannelAccess) EventChannels(_ context.Context, caller, tayfa id.ID, chans []channelaccess.EventChannel) ([]channelaccess.EventAccess, error) {
	f.caller, f.tayfa, f.chans = caller, tayfa, chans
	out := []channelaccess.EventAccess{}
	for _, c := range chans {
		out = append(out, channelaccess.EventAccess{EventChannel: c, Member: true, End: time.Unix(100, 0), MemberCount: 4})
	}
	return out, f.err
}

func (f *fakeChannelAccess) EventAudience(_ context.Context, caller, tayfa id.ID, ch channelaccess.EventChannel, size int32, token string) ([]id.ID, string, error) {
	f.caller, f.tayfa, f.chans = caller, tayfa, []channelaccess.EventChannel{ch}
	return []id.ID{caller}, "devam", f.err
}

func (f *fakeChannelAccess) ActiveMembers(_ context.Context, caller, tayfa id.ID, users []id.ID) ([]id.ID, error) {
	f.caller, f.tayfa = caller, tayfa
	return users[:1], f.err
}

// TestEventChannelAccessServer (issue #183): IDs are parsed, the caller
// comes from the token, the answer is passed through.
func TestEventChannelAccessServer(t *testing.T) {
	user, tf, ev, opener := id.New(), id.New(), id.New(), id.New()
	f := &fakeChannelAccess{}
	srv := NewChannelAccessServer(f, discard)
	if _, err := srv.GetEventChannelAccess(context.Background(), &corev1.GetEventChannelAccessRequest{}); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	ctx := callerCtx(user)
	resp, err := srv.GetEventChannelAccess(ctx, &corev1.GetEventChannelAccessRequest{TayfaId: tf.String(), Channels: []*corev1.EventChannelRef{
		{EventId: ev.String(), OpenerUserId: opener.String()}, {EventId: ev.String()},
	}})
	if err != nil || f.caller != user || f.tayfa != tf || len(f.chans) != 2 || f.chans[0].Opener != opener || f.chans[1].Opener != (id.ID{}) ||
		len(resp.GetChannels()) != 2 || !resp.GetChannels()[0].GetMember() || resp.GetChannels()[0].GetMemberCount() != 4 ||
		resp.GetChannels()[1].GetOpenerUserId() != "" {
		t.Fatalf("erişim = %v %v", resp, err)
	}
	for name, req := range map[string]*corev1.GetEventChannelAccessRequest{
		"boş":         {TayfaId: tf.String()},
		"bozuk olay":  {TayfaId: tf.String(), Channels: []*corev1.EventChannelRef{{EventId: "x"}}},
		"bozuk açan":  {TayfaId: tf.String(), Channels: []*corev1.EventChannelRef{{EventId: ev.String(), OpenerUserId: "x"}}},
		"101 kanal":   {TayfaId: tf.String(), Channels: make([]*corev1.EventChannelRef, 101)},
		"bozuk tayfa": {TayfaId: "x", Channels: []*corev1.EventChannelRef{{EventId: ev.String()}}},
	} {
		if _, err := srv.GetEventChannelAccess(ctx, req); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	lr, err := srv.ListEventChannelMembers(ctx, &corev1.ListEventChannelMembersRequest{TayfaId: tf.String(), EventId: ev.String(), OpenerUserId: opener.String()})
	if err != nil || len(lr.GetUserIds()) != 1 || lr.GetUserIds()[0] != user.String() || lr.GetNextPageToken() != "devam" {
		t.Fatalf("liste = %v %v", lr, err)
	}
	fr, err := srv.FilterActiveMembers(ctx, &corev1.FilterActiveMembersRequest{TayfaId: tf.String(), UserIds: []string{opener.String(), ev.String()}})
	if err != nil || len(fr.GetUserIds()) != 1 || fr.GetUserIds()[0] != opener.String() {
		t.Fatalf("süzme = %v %v", fr, err)
	}
	if _, err := srv.FilterActiveMembers(ctx, &corev1.FilterActiveMembersRequest{TayfaId: tf.String(), UserIds: []string{"x"}}); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.InvalidArgument {
		t.Fatalf("bozuk kimlik: %v", err)
	}
	f.err = apperr.ErrEventNotFound
	if _, err := srv.ListEventChannelMembers(ctx, &corev1.ListEventChannelMembersRequest{TayfaId: tf.String(), EventId: ev.String()}); func() codes.Code { c, _ := infoOf(t, err); return c }() != codes.NotFound {
		t.Fatalf("bilinmeyen etkinlik: %v", err)
	}
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
