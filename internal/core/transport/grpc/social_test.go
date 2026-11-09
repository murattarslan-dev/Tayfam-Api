package grpc

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeSocial struct {
	err      error
	caller   id.ID
	other    id.ID
	incoming bool
	called   bool
}

func (f *fakeSocial) friend() social.Friend {
	return social.Friend{UserID: f.other, DisplayName: "Ece", State: social.StateOutgoing, RequestedAt: time.Unix(1, 0).UTC()}
}
func (f *fakeSocial) SendFriendRequest(_ context.Context, c, o id.ID) (social.Friend, error) {
	f.caller, f.other, f.called = c, o, true
	return f.friend(), f.err
}
func (f *fakeSocial) AcceptFriendRequest(_ context.Context, c, o id.ID) (social.Friend, error) {
	f.caller, f.other, f.called = c, o, true
	fr := f.friend()
	fr.State, fr.AcceptedAt = social.StateFriends, time.Unix(2, 0).UTC()
	return fr, f.err
}
func (f *fakeSocial) RemoveFriendship(_ context.Context, c, o id.ID) error {
	f.caller, f.other, f.called = c, o, true
	return f.err
}
func (f *fakeSocial) ListFriends(_ context.Context, c id.ID, _ int32, _ string) (social.Page[social.Friend], error) {
	f.caller, f.called = c, true
	return social.Page[social.Friend]{Items: []social.Friend{f.friend()}, Next: "sonraki"}, f.err
}
func (f *fakeSocial) ListFriendRequests(_ context.Context, c id.ID, in bool, _ int32, _ string) (social.Page[social.Friend], error) {
	f.caller, f.incoming, f.called = c, in, true
	return social.Page[social.Friend]{}, f.err
}
func (f *fakeSocial) BlockUser(_ context.Context, c, o id.ID) error {
	f.caller, f.other, f.called = c, o, true
	return f.err
}
func (f *fakeSocial) UnblockUser(_ context.Context, c, o id.ID) error {
	f.caller, f.other, f.called = c, o, true
	return f.err
}
func (f *fakeSocial) ListBlockedUsers(_ context.Context, c id.ID, _ int32, _ string) (social.Page[social.Blocked], error) {
	f.caller, f.called = c, true
	return social.Page[social.Blocked]{Items: []social.Blocked{{UserID: id.New(), BlockedAt: time.Unix(1, 0).UTC()}}}, f.err
}

func newSocialServer(f *fakeSocial) *SocialServer {
	var buf bytes.Buffer
	return NewSocialServer(f, slog.New(slog.NewJSONHandler(&buf, nil)))
}

func TestSocialMethods(t *testing.T) {
	f := &fakeSocial{}
	srv := newSocialServer(f)
	u, other := id.New(), id.New()
	ctx := callerCtx(u)

	r, err := srv.SendFriendRequest(ctx, &corev1.SendFriendRequestRequest{UserId: other.String()})
	if err != nil || f.caller != u || f.other != other || r.GetFriend().GetState() != corev1.FriendState_FRIEND_STATE_OUTGOING || r.GetFriend().GetAcceptTime() != nil {
		t.Fatalf("istek: %v %v", r, err)
	}
	a, err := srv.AcceptFriendRequest(ctx, &corev1.AcceptFriendRequestRequest{UserId: other.String()})
	if err != nil || a.GetFriend().GetState() != corev1.FriendState_FRIEND_STATE_FRIENDS || a.GetFriend().GetAcceptTime() == nil {
		t.Fatalf("kabul: %v %v", a, err)
	}
	l, err := srv.ListFriends(ctx, &corev1.ListFriendsRequest{})
	if err != nil || len(l.GetFriends()) != 1 || l.GetNextPageToken() != "sonraki" {
		t.Fatalf("liste: %v %v", l, err)
	}
	if _, err := srv.ListFriendRequests(ctx, &corev1.ListFriendRequestsRequest{Direction: corev1.RequestDirection_REQUEST_DIRECTION_INCOMING}); err != nil || !f.incoming {
		t.Fatalf("gelen: %v", err)
	}
	if _, err := srv.ListFriendRequests(ctx, &corev1.ListFriendRequestsRequest{Direction: corev1.RequestDirection_REQUEST_DIRECTION_OUTGOING}); err != nil || f.incoming {
		t.Fatalf("giden: %v", err)
	}
	if _, err := srv.BlockUser(ctx, &corev1.BlockUserRequest{UserId: other.String()}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.UnblockUser(ctx, &corev1.UnblockUserRequest{UserId: other.String()}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.RemoveFriendship(ctx, &corev1.RemoveFriendshipRequest{UserId: other.String()}); err != nil {
		t.Fatal(err)
	}
	b, err := srv.ListBlockedUsers(ctx, &corev1.ListBlockedUsersRequest{})
	if err != nil || len(b.GetUsers()) != 1 || b.GetUsers()[0].GetDisplayName() != "" || b.GetUsers()[0].GetAvatarUploadId() != "" {
		t.Fatalf("engeller: %v %v", b, err)
	}
}

func TestSocialValidationAndAuth(t *testing.T) {
	f := &fakeSocial{}
	srv := newSocialServer(f)
	ctx := callerCtx(id.New())
	if _, err := srv.ListFriendRequests(ctx, &corev1.ListFriendRequestsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("yön: %v", err)
	}
	if _, err := srv.SendFriendRequest(ctx, &corev1.SendFriendRequestRequest{UserId: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("kimlik: %v", err)
	}
	if _, err := srv.BlockUser(context.Background(), &corev1.BlockUserRequest{UserId: id.New().String()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız: %v", err)
	}
	if _, err := srv.ListFriends(context.Background(), &corev1.ListFriendsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token'sız liste: %v", err)
	}
	if f.called {
		t.Fatal("uygulama katmanı çağrıldı")
	}

	srv = newSocialServer(&fakeSocial{err: apperr.ErrUserNotFound})
	_, err := srv.SendFriendRequest(ctx, &corev1.SendFriendRequestRequest{UserId: id.New().String()})
	if code, reason, _ := reasonOf(t, err); code != codes.NotFound || reason != "ERROR_REASON_USER_NOT_FOUND" {
		t.Fatalf("bulunamadı: %v %q", code, reason)
	}
}
