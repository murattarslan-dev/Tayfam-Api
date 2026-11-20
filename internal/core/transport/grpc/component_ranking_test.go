package grpc

import (
	"context"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/ranking"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeRankings struct {
	err   error
	ref   component.Ref
	user  id.ID
	value float64
	out   ranking.Ranking
	calls int
	last  string
}

func (f *fakeRankings) Get(_ context.Context, ref component.Ref) (ranking.Ranking, error) {
	f.ref, f.calls, f.last = ref, f.calls+1, "get"
	return f.out, f.err
}

func (f *fakeRankings) SetEntry(_ context.Context, ref component.Ref, user id.ID, value float64) (ranking.Ranking, error) {
	f.ref, f.user, f.value, f.calls, f.last = ref, user, value, f.calls+1, "set"
	return f.out, f.err
}

func (f *fakeRankings) RemoveEntry(_ context.Context, ref component.Ref, user id.ID) (ranking.Ranking, error) {
	f.ref, f.user, f.calls, f.last = ref, user, f.calls+1, "remove"
	return f.out, f.err
}

func TestRankingComponentMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	a, b := id.New(), id.New()
	f := &fakeRankings{out: ranking.Ranking{
		Metric: ranking.MetricDuration, Direction: ranking.DirectionAsc, Decimals: 2, MeasureEnabled: true, CallerCanEdit: true,
		Entries: []ranking.Entry{{User: a, Value: 61.5, Rank: 1, UpdatedAt: at}, {User: b, Value: 61.5, Rank: 1, UpdatedAt: at}},
	}}
	s := NewRankingComponentServer(f, discard)
	user, tf, ev := id.New(), id.New(), id.New()
	ctx := callerCtx(user)
	ref := &corev1.ComponentRef{TayfaId: tf.String(), EventId: ev.String(), ComponentKey: "ranking"}
	want := component.Ref{User: user, Tayfa: tf, Event: ev, Key: "ranking"}

	got, err := s.GetRanking(ctx, &corev1.GetRankingRequest{Ref: ref})
	if err != nil {
		t.Fatal(err)
	}
	r := got.GetRanking()
	if f.ref != want || r.GetSource() != "" || r.GetMetric() != corev1.RankingMetric_RANKING_METRIC_DURATION ||
		r.GetDirection() != corev1.RankingDirection_RANKING_DIRECTION_ASC || r.GetDecimals() != 2 || !r.GetMeasureEnabled() ||
		!r.GetCallerCanEdit() || r.GetHidden() || len(r.GetEntries()) != 2 || r.GetEntries()[1].GetUserId() != b.String() ||
		r.GetEntries()[1].GetRank() != 1 || r.GetEntries()[0].GetValue() != 61.5 || !r.GetEntries()[0].GetUpdateTime().AsTime().Equal(at) {
		t.Fatalf("okuma: %v (%+v)", r, f.ref)
	}

	if _, err := s.SetRankingEntry(ctx, &corev1.SetRankingEntryRequest{Ref: ref, UserId: a.String(), Value: 59.25}); err != nil ||
		f.last != "set" || f.user != a || f.value != 59.25 || f.ref != want {
		t.Fatalf("yazma: %v %+v", err, f)
	}
	if _, err := s.RemoveRankingEntry(ctx, &corev1.RemoveRankingEntryRequest{Ref: ref, UserId: b.String()}); err != nil || f.last != "remove" || f.user != b {
		t.Fatalf("kaldırma: %v %+v", err, f)
	}

	// A computed ranking: the source, no metric, hidden.
	f.out = ranking.Ranking{Source: "net", Direction: ranking.DirectionDesc, Unit: "net", Hidden: true, MeasureEnabled: true}
	got, err = s.GetRanking(ctx, &corev1.GetRankingRequest{Ref: ref})
	if r := got.GetRanking(); err != nil || r.GetSource() != "net" || r.GetMetric() != corev1.RankingMetric_RANKING_METRIC_UNSPECIFIED ||
		r.GetDirection() != corev1.RankingDirection_RANKING_DIRECTION_DESC || r.GetUnit() != "net" || !r.GetHidden() || len(r.GetEntries()) != 0 {
		t.Fatalf("ölçümden derece: %v %v", r, err)
	}

	// The app's errors keep their reasons.
	for err, reason := range map[error]corev1.ErrorReason{
		apperr.ErrRankingNotStarted:     corev1.ErrorReason_ERROR_REASON_RANKING_NOT_STARTED,
		apperr.ErrRankingNotParticipant: corev1.ErrorReason_ERROR_REASON_RANKING_NOT_PARTICIPANT,
		apperr.ErrRankingFromMeasure:    corev1.ErrorReason_ERROR_REASON_RANKING_FROM_MEASURE,
		apperr.ErrComponentFrozen:       corev1.ErrorReason_ERROR_REASON_COMPONENT_FROZEN,
	} {
		f.err = err
		_, got := s.SetRankingEntry(ctx, &corev1.SetRankingEntryRequest{Ref: ref, UserId: a.String(), Value: 1})
		if code, info := infoOf(t, got); code != codes.FailedPrecondition || info.GetReason() != reason.String() {
			t.Errorf("%v: %v", err, got)
		}
	}
}

// Junk is refused before the app layer is asked.
func TestRankingComponentValidation(t *testing.T) {
	f := &fakeRankings{}
	s := NewRankingComponentServer(f, discard)
	tf, ev := id.New().String(), id.New().String()
	ctx := callerCtx(id.New())
	ref := &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "ranking"}
	u := id.New().String()

	for name, req := range map[string]*corev1.SetRankingEntryRequest{
		"başvurusuz":    {UserId: u, Value: 1},
		"bozuk anahtar": {Ref: &corev1.ComponentRef{TayfaId: tf, EventId: ev, ComponentKey: "Ranking"}, UserId: u, Value: 1},
		"kişisiz":       {Ref: ref, Value: 1},
		"bozuk kişi":    {Ref: ref, UserId: "x", Value: 1},
		"NaN":           {Ref: ref, UserId: u, Value: math.NaN()},
		"sonsuz":        {Ref: ref, UserId: u, Value: math.Inf(1)},
	} {
		if _, err := s.SetRankingEntry(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.RemoveRankingEntry(ctx, &corev1.RemoveRankingEntryRequest{Ref: ref}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("kişisiz kaldırma: %v", err)
	}
	if _, err := s.GetRanking(ctx, &corev1.GetRankingRequest{}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("başvurusuz okuma: %v", err)
	}
	if f.calls != 0 {
		t.Fatalf("bozuk istek app katmanına ulaştı: %d", f.calls)
	}
	// No verified caller, no call.
	if _, err := s.SetRankingEntry(context.Background(), &corev1.SetRankingEntryRequest{Ref: ref, UserId: u, Value: 1}); codeOf(t, err) != codes.Unauthenticated || f.calls != 0 {
		t.Fatalf("çağıransız: %v", err)
	}
}

// The registry serves the ranking component.
func TestRankingComponentRegistered(t *testing.T) {
	srv := grpc.NewServer()
	if err := RegisterComponents(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	info, ok := srv.GetServiceInfo()["tayfam.core.v1.RankingComponentService"]
	if !ok || len(info.Methods) != 3 {
		t.Fatalf("derece servisi: %v", info)
	}
}
