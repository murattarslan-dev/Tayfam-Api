package grpc

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/reflect/protoreflect"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/discover"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeDiscover struct {
	caller, tayfa, event, contact id.ID
	answer                        string
	showcase                      bool
	err                           error
}

func (f *fakeDiscover) Feed(_ context.Context, v id.ID) ([]discover.Item, error) {
	f.caller = v
	if f.err != nil {
		return nil, f.err
	}
	return []discover.Item{{
		EventID: id.New(), Type: "hike", Title: "Yürüyüş", StartsAt: time.Unix(10, 0), EndsAt: time.Unix(20, 0),
		TayfaID: id.New(), TayfaName: "Tayfa", EventCount: 3, AttendanceCount: 9, FriendCount: 2,
		Friends: []discover.Person{{ID: id.New(), DisplayName: "Ayşe"}},
		Showcase: &stats.Showcase{
			Stats:  []stats.Stat{{Metric: "hike.distance", Source: stats.SourceMeasure, Value: 42.5, Events: 3}},
			Badges: []badge.Earned{{Key: "tayfa_events_10", EarnedAt: time.Unix(30, 0)}},
		},
	}, {
		EventID: id.New(), Type: "hike", Title: "Kart yok", StartsAt: time.Unix(10, 0), EndsAt: time.Unix(20, 0),
		TayfaID: id.New(), TayfaName: "Yeni", EventCount: 1,
	}}, nil
}
func (f *fakeDiscover) GetTayfa(_ context.Context, c, t id.ID) (discover.TayfaState, error) {
	f.caller, f.tayfa = c, t
	return discover.TayfaState{State: discover.StateUnderReview, Enabled: true}, f.err
}
func (f *fakeDiscover) SetTayfa(_ context.Context, c, t id.ID, on bool) (discover.TayfaState, error) {
	f.caller, f.tayfa = c, t
	return discover.TayfaState{State: discover.StatePending, Enabled: on}, f.err
}
func (f *fakeDiscover) SetStatsShowcase(_ context.Context, c, t id.ID, on bool) (discover.TayfaState, error) {
	f.caller, f.tayfa, f.showcase = c, t, on
	return discover.TayfaState{State: discover.StateListed, Enabled: true, StatsShowcase: on}, f.err
}
func (f *fakeDiscover) GetFeedback(_ context.Context, c, t, e id.ID) (discover.Feedback, error) {
	f.caller, f.tayfa, f.event = c, t, e
	return discover.Feedback{}, f.err
}
func (f *fakeDiscover) SetFeedback(_ context.Context, c, t, e id.ID, a string) (discover.Feedback, error) {
	f.caller, f.tayfa, f.event, f.answer = c, t, e, a
	return discover.Feedback{Answer: a, UpdatedAt: time.Unix(5, 0)}, f.err
}
func (f *fakeDiscover) GetSafetyContact(_ context.Context, c, t, e id.ID) (discover.SafetyContact, error) {
	f.caller, f.tayfa, f.event = c, t, e
	return discover.SafetyContact{}, f.err
}
func (f *fakeDiscover) SetSafetyContact(_ context.Context, c, t, e, k id.ID, _ int32) (discover.SafetyContact, error) {
	f.caller, f.tayfa, f.event, f.contact = c, t, e, k
	return discover.SafetyContact{Contact: discover.Person{ID: k, DisplayName: "Anne"}, GraceMinutes: 60}, f.err
}
func (f *fakeDiscover) ClearSafetyContact(_ context.Context, c, t, e id.ID) error {
	f.caller, f.tayfa, f.event = c, t, e
	return f.err
}
func (f *fakeDiscover) MarkReturned(_ context.Context, c, t, e id.ID) (discover.SafetyContact, error) {
	f.caller, f.tayfa, f.event = c, t, e
	return discover.SafetyContact{GraceMinutes: 60, ReturnedAt: time.Unix(7, 0)}, f.err
}
func (f *fakeDiscover) ListSafetyShares(_ context.Context, c id.ID) ([]discover.Share, error) {
	f.caller = c
	return nil, f.err
}
func (f *fakeDiscover) JoinFromDiscover(_ context.Context, c, t id.ID) (membership.Member, error) {
	f.caller, f.tayfa = c, t
	return membership.Member{TayfaID: t, UserID: c, Role: authz.Member, Status: authz.StatusPending}, f.err
}

func newDiscoverServer(f *fakeDiscover) *DiscoverServer {
	return NewDiscoverServer(f, f, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDiscoverMethods(t *testing.T) {
	u, tf, ev, contact := id.New(), id.New(), id.New(), id.New()
	f := &fakeDiscover{}
	srv := newDiscoverServer(f)
	ctx := callerCtx(u)

	feed, err := srv.ListDiscoverFeed(ctx, &corev1.ListDiscoverFeedRequest{})
	if err != nil || f.caller != u || len(feed.GetItems()) != 2 || feed.GetItems()[0].GetFriendCount() != 2 ||
		feed.GetItems()[0].GetEvent().GetTypeKey() != "hike" || feed.GetItems()[0].GetEvent().GetType() != corev1.EventType_EVENT_TYPE_UNSPECIFIED {
		t.Fatalf("akış: %v %v", feed, err)
	}
	// Issue #267: the showcase is mapped as it is; a tayfa without one has
	// no field at all.
	sh := feed.GetItems()[0].GetTayfa().GetShowcase()
	if len(sh.GetStats()) != 1 || sh.GetStats()[0].GetMetricKey() != "hike.distance" || sh.GetStats()[0].GetValue() != 42.5 ||
		sh.GetStats()[0].GetEventCount() != 3 || len(sh.GetBadges()) != 1 || sh.GetBadges()[0].GetBadgeKey() != "tayfa_events_10" {
		t.Fatalf("vitrin: %v", sh)
	}
	if feed.GetItems()[1].GetTayfa().GetShowcase() != nil {
		t.Fatalf("vitrinsiz tayfada vitrin: %v", feed.GetItems()[1].GetTayfa())
	}
	ss, err := srv.SetTayfaStatsShowcase(ctx, &corev1.SetTayfaStatsShowcaseRequest{TayfaId: tf.String(), StatsShowcase: false})
	if err != nil || f.tayfa != tf || f.showcase || ss.GetStatsShowcase() || ss.GetState() != corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_LISTED {
		t.Fatalf("vitrin ayarı: %v %v", ss, err)
	}
	if _, err := srv.SetTayfaStatsShowcase(ctx, &corev1.SetTayfaStatsShowcaseRequest{TayfaId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("geçersiz tayfa: %v", err)
	}
	st, err := srv.GetTayfaDiscover(ctx, &corev1.GetTayfaDiscoverRequest{TayfaId: tf.String()})
	if err != nil || f.tayfa != tf || st.GetState() != corev1.TayfaDiscoverState_TAYFA_DISCOVER_STATE_UNDER_REVIEW {
		t.Fatalf("durum: %v %v", st, err)
	}
	fb, err := srv.SetEventFeedback(ctx, &corev1.SetEventFeedbackRequest{TayfaId: tf.String(), EventId: ev.String(), Answer: corev1.FeedbackAnswer_FEEDBACK_ANSWER_SERIOUS})
	if err != nil || f.answer != discover.AnswerSerious || f.event != ev || fb.GetAnswer() != corev1.FeedbackAnswer_FEEDBACK_ANSWER_SERIOUS {
		t.Fatalf("geri bildirim: %v %v", fb, err)
	}
	if _, err := srv.SetEventFeedback(ctx, &corev1.SetEventFeedbackRequest{TayfaId: tf.String(), EventId: ev.String()}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("cevapsız geri bildirim: %v", err)
	}
	sc, err := srv.SetSafetyContact(ctx, &corev1.SetSafetyContactRequest{TayfaId: tf.String(), EventId: ev.String(), ContactUserId: contact.String()})
	if err != nil || f.contact != contact || sc.GetSafetyContact().GetContact().GetUserId() != contact.String() {
		t.Fatalf("güvenlik kişisi: %v %v", sc, err)
	}
	if _, err := srv.SetSafetyContact(ctx, &corev1.SetSafetyContactRequest{TayfaId: tf.String(), EventId: ev.String(), ContactUserId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Fatalf("geçersiz kişi: %v", err)
	}
	g, err := srv.GetSafetyContact(ctx, &corev1.GetSafetyContactRequest{TayfaId: tf.String(), EventId: ev.String()})
	if err != nil || g.GetSafetyContact().GetContact() != nil {
		t.Fatalf("seçilmemiş kişi: %v %v", g, err)
	}
	j, err := srv.JoinFromDiscover(ctx, &corev1.JoinFromDiscoverRequest{TayfaId: tf.String()})
	if err != nil || j.GetMembership().GetStatus() != corev1.MembershipStatus_MEMBERSHIP_STATUS_PENDING {
		t.Fatalf("katılım: %v %v", j, err)
	}
}

// TestDiscoverFeatureDisabled: a closed feature is NOT_FOUND with
// FEATURE_DISABLED (REST 404 feature-disabled) and returns no data.
func TestDiscoverFeatureDisabled(t *testing.T) {
	f := &fakeDiscover{err: apperr.ErrFeatureDisabled}
	srv := newDiscoverServer(f)
	feed, err := srv.ListDiscoverFeed(callerCtx(id.New()), &corev1.ListDiscoverFeedRequest{})
	if code, reason, _ := reasonOf(t, err); feed != nil || code != codes.NotFound || reason != "ERROR_REASON_FEATURE_DISABLED" {
		t.Fatalf("kapalı akış: %v %v", feed, err)
	}
}

// TestDiscoverContractHasNoScore: no message of DiscoverService can carry
// the friendship score or a position derived from it (2026-11-16), nor a
// coordinate.
func TestDiscoverContractHasNoScore(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] || !strings.HasPrefix(string(m.FullName()), "tayfam.core.v1.") {
			return
		}
		seen[m.FullName()] = true
		fields := m.Fields()
		for i := range fields.Len() {
			fd := fields.Get(i)
			name := strings.ToLower(string(fd.Name()))
			for _, bad := range []string{"score", "rank", "position", "weight", "lat", "lng", "page_token", "trusted", "verified", "approved"} {
				if strings.Contains(name, bad) {
					t.Errorf("%s.%s", m.FullName(), fd.Name())
				}
			}
			if fd.Message() != nil {
				walk(fd.Message())
			}
		}
	}
	svc := corev1.File_tayfam_core_v1_discover_proto.Services().ByName("DiscoverService")
	for i := range svc.Methods().Len() {
		walk(svc.Methods().Get(i).Output())
	}
	if len(seen) < 10 {
		t.Fatalf("yalnızca %d mesaj tarandı", len(seen))
	}
	// The safety contact sees the least (ADR-0033 §10): who, when, state.
	share := (&corev1.SafetyShare{}).ProtoReflect().Descriptor().Fields()
	for i := range share.Len() {
		switch name := string(share.Get(i).Name()); name {
		case "participant", "start_time", "end_time", "grace_minutes", "return_time", "alert_time", "state":
		default:
			t.Errorf("SafetyShare.%s", name)
		}
	}
}
