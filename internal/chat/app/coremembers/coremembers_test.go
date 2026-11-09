package coremembers

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channel"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeCore struct {
	resp *corev1.GetMembershipResponse
	err  error
	got  *corev1.GetMembershipRequest
}

func (f *fakeCore) GetMembership(_ context.Context, in *corev1.GetMembershipRequest, _ ...grpc.CallOption) (*corev1.GetMembershipResponse, error) {
	f.got = in
	return f.resp, f.err
}

func withReason(code codes.Code, domain, reason string) error {
	st, _ := status.New(code, "x").WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	return st.Err()
}

func member(st corev1.MembershipStatus) *corev1.GetMembershipResponse {
	return &corev1.GetMembershipResponse{Membership: &corev1.Membership{Status: st}}
}

func TestIsActiveMember(t *testing.T) {
	tf, u := id.New(), id.New()
	notFound := corev1.ErrorReason_ERROR_REASON_TAYFA_NOT_FOUND.String()
	for _, tc := range []struct {
		name    string
		core    fakeCore
		want    bool
		wantErr error
		anyErr  bool
	}{
		{"etkin", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE)}, true, nil, false},
		{"bekleyen", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING)}, false, nil, false},
		{"banlı", fakeCore{resp: member(corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED)}, false, nil, false},
		{"üye değil", fakeCore{err: withReason(codes.NotFound, "core.tayfam.app", notFound)}, false, nil, false},
		{"başka alan adı", fakeCore{err: withReason(codes.NotFound, "chat.tayfam.app", notFound)}, false, nil, true},
		{"core kapalı", fakeCore{err: status.Error(codes.Unavailable, "x")}, false, apperr.ErrUnavailable, true},
		{"yetki", fakeCore{err: status.Error(codes.PermissionDenied, "x")}, false, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(&tc.core).IsActiveMember(context.Background(), tf, u)
			if got != tc.want || (err != nil) != tc.anyErr || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Fatalf("= %v %v", got, err)
			}
			if tc.core.got.GetTayfaId() != tf.String() || tc.core.got.GetUserId() != u.String() {
				t.Fatalf("istek = %v", tc.core.got)
			}
		})
	}
}

func TestMemberRoleAndMute(t *testing.T) {
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	core := &fakeCore{resp: &corev1.GetMembershipResponse{Membership: &corev1.Membership{
		Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, Role: corev1.Role_ROLE_MODERATOR,
		MuteExpireTime: timestamppb.New(until),
	}}}
	m, err := New(core).Member(context.Background(), id.New(), id.New())
	if err != nil || m != (channel.Member{Active: true, Role: channel.RoleModerator, MutedUntil: until}) {
		t.Fatalf("= %+v, %v", m, err)
	}
	// Not active: no role at all.
	core.resp.Membership.Status = corev1.MembershipStatus_MEMBERSHIP_STATUS_BANNED
	if m, _ := New(core).Member(context.Background(), id.New(), id.New()); m != (channel.Member{}) {
		t.Fatalf("banlı = %+v", m)
	}
}

// countingCore counts calls and can be switched to failing.
type countingCore struct {
	calls int
	fail  bool
}

func (c *countingCore) GetMembership(_ context.Context, _ *corev1.GetMembershipRequest, _ ...grpc.CallOption) (*corev1.GetMembershipResponse, error) {
	c.calls++
	if c.fail {
		return nil, status.Error(codes.Unavailable, "x")
	}
	return member(corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE), nil
}

func TestCacheKeepsAnswersForTTL(t *testing.T) {
	core := &countingCore{}
	c := NewCached(core, 5*time.Second)
	now := time.Now()
	c.now = func() time.Time { return now }
	tf, u := id.New(), id.New()
	for range 3 {
		if ok, err := c.IsActiveMember(context.Background(), tf, u); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if core.calls != 1 {
		t.Fatalf("%d çağrı", core.calls)
	}
	// Another user or tayfa is asked separately.
	_, _ = c.IsActiveMember(context.Background(), tf, id.New())
	if core.calls != 2 {
		t.Fatalf("%d çağrı", core.calls)
	}
	// After the TTL core is asked again; errors are not cached.
	now = now.Add(5 * time.Second)
	core.fail = true
	if _, err := c.IsActiveMember(context.Background(), tf, u); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("hata = %v", err)
	}
	core.fail = false
	if ok, err := c.IsActiveMember(context.Background(), tf, u); !ok || err != nil || core.calls != 4 {
		t.Fatalf("yeniden = %v %v (%d çağrı)", ok, err, core.calls)
	}
}
