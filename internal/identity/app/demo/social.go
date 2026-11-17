package demo

import (
	"context"
	"fmt"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// SocialResult is what the run did around the platform owner's account.
type SocialResult struct {
	Friends int // ownerFriends who are the owner's friends after the run
	Blocks  int // blocks between the owner and demo accounts, both ways
	// Skipped: friend requests a block (or a deleted account) refused.
	// Those accounts are left as they are.
	Skipped int
}

// seedSocial makes ownerFriends the owner's friends and sets the blocks
// between the owner and ownerBlocks / blocksOwner (ADR-0035). Everything
// goes through SocialService with the account's own token: a demo account
// sends its friend request, the owner's token accepts it; the owner's token
// blocks ownerBlocks; blocksOwner block the owner with theirs.
//
// The owner's token is used only to accept a waiting request from a demo
// friend, to read the owner's own block list and to block an account of
// ownerBlocks not blocked yet. A second run finds the friendships and
// blocks and writes nothing with it.
func (s *Seeder) seedSocial(ctx context.Context, users map[string]id.ID, owner id.ID) (SocialResult, error) {
	var res SocialResult
	social := s.cfg.Core.Social
	for _, k := range ownerFriends {
		friend := users[k]
		sent, err := call(ctx, s, friend, func(ctx context.Context) (*corev1.SendFriendRequestResponse, error) {
			return social.SendFriendRequest(ctx, &corev1.SendFriendRequestRequest{UserId: owner.String()})
		})
		switch {
		case is(err, "ERROR_REASON_ALREADY_FRIENDS"):
			res.Friends++
			continue
		case is(err, "ERROR_REASON_USER_NOT_FOUND"):
			// A block either way (the owner may have blocked the account
			// by hand) or a deleted account: left as it is.
			res.Skipped++
			continue
		case is(err, "ERROR_REASON_FRIEND_REQUEST_PENDING"):
			// The request of an earlier run waits for the owner.
		case err != nil:
			return res, fmt.Errorf("arkadaşlık isteği: %w", err)
		case sent.GetFriend().GetState() == corev1.FriendState_FRIEND_STATE_FRIENDS:
			// The owner's own request was waiting and is now accepted.
			res.Friends++
			continue
		}
		_, err = call(ctx, s, owner, func(ctx context.Context) (*corev1.AcceptFriendRequestResponse, error) {
			return social.AcceptFriendRequest(ctx, &corev1.AcceptFriendRequestRequest{UserId: friend.String()})
		})
		if err != nil {
			return res, fmt.Errorf("arkadaşlık kabulü: %w", err)
		}
		res.Friends++
	}

	blocked, err := s.blockedBy(ctx, owner)
	if err != nil {
		return res, err
	}
	for _, k := range ownerBlocks {
		u := users[k]
		if !blocked[u.String()] {
			_, err := call(ctx, s, owner, func(ctx context.Context) (*corev1.BlockUserResponse, error) {
				return social.BlockUser(ctx, &corev1.BlockUserRequest{UserId: u.String()})
			})
			if err != nil {
				return res, fmt.Errorf("sahibin engeli: %w", err)
			}
		}
		res.Blocks++
	}
	for _, k := range blocksOwner {
		u := users[k]
		// Blocking again succeeds and changes nothing.
		_, err := call(ctx, s, u, func(ctx context.Context) (*corev1.BlockUserResponse, error) {
			return social.BlockUser(ctx, &corev1.BlockUserRequest{UserId: owner.String()})
		})
		if err != nil {
			return res, fmt.Errorf("demo hesabının engeli: %w", err)
		}
		res.Blocks++
	}
	return res, nil
}

// blockedBy lists the users user blocked (user's own list).
func (s *Seeder) blockedBy(ctx context.Context, user id.ID) (map[string]bool, error) {
	out := map[string]bool{}
	token := ""
	for {
		resp, err := call(ctx, s, user, func(ctx context.Context) (*corev1.ListBlockedUsersResponse, error) {
			return s.cfg.Core.Social.ListBlockedUsers(ctx, &corev1.ListBlockedUsersRequest{PageSize: 100, PageToken: token})
		})
		if err != nil {
			return nil, fmt.Errorf("engel listesi: %w", err)
		}
		for _, b := range resp.GetUsers() {
			out[b.GetUserId()] = true
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}
