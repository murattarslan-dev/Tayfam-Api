package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func TestInvitationMethods(t *testing.T) {
	f := &fakeApp{}
	srv, _ := newServer(f)
	u, tayfa, friend := id.New(), id.New(), id.New()
	ctx := callerCtx(u)

	r, err := srv.InviteFriend(ctx, &corev1.InviteFriendRequest{TayfaId: tayfa.String(), UserId: friend.String()})
	if err != nil || f.caller != u || f.tayfa != tayfa || f.target != friend {
		t.Fatalf("davet: %v %v", r, err)
	}
	inv := r.GetInvitation()
	if inv.GetInviterUserId() != u.String() || inv.GetUserId() != friend.String() || inv.GetTayfaName() != "Kamp" || inv.GetInviteTime() == nil {
		t.Fatalf("davet = %v", inv)
	}
	my, err := srv.ListMyInvitations(ctx, &corev1.ListMyInvitationsRequest{})
	if err != nil || len(my.GetInvitations()) != 1 || my.GetInvitations()[0].GetUserId() != u.String() || my.GetInvitations()[0].GetInviterUserId() != "" || my.GetNextPageToken() != "sonraki" {
		t.Fatalf("davetlerim: %v %v", my, err)
	}
	if _, err := srv.AcceptInvitation(ctx, &corev1.AcceptInvitationRequest{TayfaId: tayfa.String()}); err != nil || f.caller != u || f.tayfa != tayfa {
		t.Fatalf("kabul: %v", err)
	}
	if _, err := srv.DeclineInvitation(ctx, &corev1.DeclineInvitationRequest{TayfaId: tayfa.String()}); err != nil {
		t.Fatalf("ret: %v", err)
	}
	if _, err := srv.ListInvitations(ctx, &corev1.ListInvitationsRequest{TayfaId: tayfa.String()}); err != nil {
		t.Fatalf("liste: %v", err)
	}
	if _, err := srv.CancelInvitation(ctx, &corev1.CancelInvitationRequest{TayfaId: tayfa.String(), UserId: friend.String()}); err != nil || f.target != friend {
		t.Fatalf("iptal: %v", err)
	}

	// Malformed IDs never reach the app.
	for name, call := range map[string]func() error{
		"invite tayfa": func() error {
			_, err := srv.InviteFriend(ctx, &corev1.InviteFriendRequest{TayfaId: "x", UserId: friend.String()})
			return err
		},
		"invite user": func() error {
			_, err := srv.InviteFriend(ctx, &corev1.InviteFriendRequest{TayfaId: tayfa.String(), UserId: "x"})
			return err
		},
		"accept": func() error {
			_, err := srv.AcceptInvitation(ctx, &corev1.AcceptInvitationRequest{TayfaId: ""})
			return err
		},
		"cancel": func() error {
			_, err := srv.CancelInvitation(ctx, &corev1.CancelInvitationRequest{TayfaId: tayfa.String()})
			return err
		},
	} {
		if code, reason, _ := reasonOf(t, call()); code != codes.InvalidArgument || reason != "ERROR_REASON_VALIDATION" {
			t.Errorf("%s: %v %q", name, code, reason)
		}
	}

	srv, _ = newServer(&fakeApp{err: apperr.ErrDirectInviteLimit})
	_, err = srv.InviteFriend(ctx, &corev1.InviteFriendRequest{TayfaId: tayfa.String(), UserId: friend.String()})
	if code, reason, _ := reasonOf(t, err); code != codes.ResourceExhausted || reason != "ERROR_REASON_DIRECT_INVITE_LIMIT" {
		t.Fatalf("sınır: %v %q", code, reason)
	}
}
