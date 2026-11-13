package corerelation

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profile"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fake struct {
	p   *corev1.UserProfile
	err error
	got string
}

func (f *fake) GetUserProfile(_ context.Context, in *corev1.GetUserProfileRequest, _ ...grpc.CallOption) (*corev1.GetUserProfileResponse, error) {
	f.got = in.GetUserId()
	return &corev1.GetUserProfileResponse{Profile: f.p}, f.err
}

func withInfo(code codes.Code, domain, reason string) error {
	st, _ := status.New(code, "x").WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	return st.Err()
}

func TestRelation(t *testing.T) {
	u := id.New()
	for name, tc := range map[string]struct {
		p    *corev1.UserProfile
		want profile.Relation
	}{
		"yabancı":     {&corev1.UserProfile{UserId: u.String()}, profile.Relation{}},
		"kendisi":     {&corev1.UserProfile{UserId: u.String(), Self: true}, profile.Relation{Self: true}},
		"arkadaş":     {&corev1.UserProfile{UserId: u.String(), FriendState: corev1.FriendState_FRIEND_STATE_FRIENDS}, profile.Relation{Friend: true}},
		"istek":       {&corev1.UserProfile{UserId: u.String(), FriendState: corev1.FriendState_FRIEND_STATE_INCOMING}, profile.Relation{}},
		"ortak tayfa": {&corev1.UserProfile{UserId: u.String(), MutualTayfas: []*corev1.MutualTayfa{{TayfaId: id.New().String()}}}, profile.Relation{Mutual: true}},
	} {
		f := &fake{p: tc.p}
		got, err := New(f).Relation(context.Background(), u)
		if err != nil || got != tc.want || f.got != u.String() {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	for name, tc := range map[string]struct {
		f    *fake
		want error
	}{
		"engelli/bilinmeyen": {&fake{err: withInfo(codes.NotFound, coreDomain, "ERROR_REASON_USER_NOT_FOUND")}, profile.ErrUserNotFound},
		"başka alan adı":     {&fake{err: withInfo(codes.NotFound, "chat.tayfam.app", "ERROR_REASON_USER_NOT_FOUND")}, profile.ErrUnavailable},
		"kapalı":             {&fake{err: status.Error(codes.Unavailable, "x")}, profile.ErrUnavailable},
		"durumsuz":           {&fake{err: errors.New("x")}, profile.ErrUnavailable},
		"başka profil":       {&fake{p: &corev1.UserProfile{UserId: id.New().String(), FriendState: corev1.FriendState_FRIEND_STATE_FRIENDS}}, profile.ErrUnavailable},
	} {
		if _, err := New(tc.f).Relation(context.Background(), u); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
