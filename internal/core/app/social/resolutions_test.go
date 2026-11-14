package social

import (
	"slices"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestFriendRequestResolutions (issue #192): the recipient learns how each
// request stands; a declined and a withdrawn request differ only for the
// recipient, who declined it themself; a new request is pending again.
func TestFriendRequestResolutions(t *testing.T) {
	e := setup(t)
	r := e.user(t, "Alıcı")
	accepted, declined, withdrawn, waiting, unfriended := e.user(t, "A"), e.user(t, "B"), e.user(t, "C"), e.user(t, "D"), e.user(t, "E")
	for _, u := range []id.ID{accepted, declined, withdrawn, waiting, unfriended} {
		_, err := e.svc.SendFriendRequest(ctx, u, r)
		must(t, err)
	}
	_, err := e.svc.AcceptFriendRequest(ctx, r, accepted)
	must(t, err)
	must(t, e.svc.RemoveFriendship(ctx, r, declined))
	must(t, e.svc.RemoveFriendship(ctx, withdrawn, r))
	_, err = e.svc.AcceptFriendRequest(ctx, r, unfriended)
	must(t, err)
	must(t, e.svc.RemoveFriendship(ctx, unfriended, r))

	refs := []RequestRef{}
	for _, u := range []id.ID{accepted, declined, withdrawn, waiting, unfriended, id.New()} {
		refs = append(refs, RequestRef{Kind: KindFriendRequest, ID: u})
	}
	got, err := e.svc.Resolutions(ctx, r, refs)
	want := []Resolution{ResolutionAccepted, ResolutionDeclined, ResolutionWithdrawn, ResolutionPending, ResolutionAccepted, ResolutionExpired}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("çözümler = %v %v, %v bekleniyordu", got, err, want)
	}

	// Only the recipient's own requests: the requester asking about the
	// same pair sees nothing of it.
	got, err = e.svc.Resolutions(ctx, declined, []RequestRef{{Kind: KindFriendRequest, ID: r}})
	if err != nil || got[0] != ResolutionExpired {
		t.Fatalf("isteyen tarafı = %v %v", got, err)
	}

	// A new request after a decline is pending again.
	_, err = e.svc.SendFriendRequest(ctx, declined, r)
	must(t, err)
	got, err = e.svc.Resolutions(ctx, r, []RequestRef{{Kind: KindFriendRequest, ID: declined}})
	if err != nil || got[0] != ResolutionPending {
		t.Fatalf("yeni istek = %v %v", got, err)
	}

	for name, refs := range map[string][]RequestRef{
		"boş":        nil,
		"tür yok":    {{ID: r}},
		"kimlik yok": {{Kind: KindFriendRequest}},
		"101":        make([]RequestRef, MaxResolutionRefs+1),
	} {
		if _, err := e.svc.Resolutions(ctx, r, refs); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
	// Account deletion takes the user's outcomes with it.
	must(t, e.q.DeleteUserRequestOutcomes(ctx, r.PG()))
	if n := e.count(t, `SELECT count(*) FROM friend_request_outcomes WHERE recipient_id = $1`, r.PG()); n != 0 {
		t.Fatalf("silinmeyen sonuç = %d", n)
	}
}
