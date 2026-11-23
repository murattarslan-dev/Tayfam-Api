package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeStats struct {
	err         error
	user, tayfa id.ID
	year        int32
	view        stats.View
	calls       int
}

func (f *fakeStats) Tayfa(_ context.Context, user, tayfa id.ID, year int32) (stats.View, error) {
	f.user, f.tayfa, f.year, f.calls = user, tayfa, year, f.calls+1
	return f.view, f.err
}

func TestTayfaStatsMethods(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	f := &fakeStats{view: stats.View{
		Year: 2025, Years: []int32{2026, 2025},
		Totals:     []stats.Stat{{Metric: "events", Source: stats.SourceCommon, Value: 4, Events: 4}},
		Own:        []stats.Stat{{Metric: "hike.distance", Source: stats.SourceMeasure, Value: 12.5, Events: 2}},
		ComputedAt: at,
	}}
	s := NewTayfaStatsServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	got, err := s.GetTayfaStats(ctx, &corev1.GetTayfaStatsRequest{TayfaId: tf.String(), Year: 2025})
	if err != nil || f.user != user || f.tayfa != tf || f.year != 2025 {
		t.Fatalf("istek: %v %+v", err, f)
	}
	if got.GetYear() != 2025 || len(got.GetYears()) != 2 || !got.GetComputeTime().AsTime().Equal(at) {
		t.Fatalf("yanıt: %v", got)
	}
	if st := got.GetTotals(); len(st) != 1 || st[0].GetMetricKey() != "events" || st[0].GetSource() != "common" || st[0].GetValue() != 4 || st[0].GetEventCount() != 4 {
		t.Fatalf("toplamlar: %v", st)
	}
	if c := got.GetOwnContributions(); len(c) != 1 || c[0].GetMetricKey() != "hike.distance" || c[0].GetSource() != "measure" || c[0].GetValue() != 12.5 || c[0].GetEventCount() != 2 {
		t.Fatalf("kendi katkıları: %v", c)
	}
	// Nobody's contribution goes in the deprecated list (issue #322).
	if c := got.GetContributions(); len(c) != 0 { //nolint:staticcheck // the deprecated field must stay empty
		t.Fatalf("eski katkı listesi dolu: %v", c)
	}

	// Nothing computed yet: no time, empty lists.
	f.view = stats.View{}
	got, err = s.GetTayfaStats(ctx, &corev1.GetTayfaStatsRequest{TayfaId: tf.String()})
	if err != nil || got.GetComputeTime() != nil || got.GetYears() == nil || len(got.GetTotals()) != 0 || len(got.GetOwnContributions()) != 0 {
		t.Fatalf("boş: %v %v", got, err)
	}

	calls := f.calls
	for name, req := range map[string]*corev1.GetTayfaStatsRequest{
		"tayfa":    {TayfaId: "x"},
		"eski yıl": {TayfaId: tf.String(), Year: 1999},
		"ileri":    {TayfaId: tf.String(), Year: 3000},
	} {
		if _, err := s.GetTayfaStats(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.calls != calls {
		t.Fatal("geçersiz istek uygulamaya ulaştı")
	}
	f.err = apperr.ErrTayfaNotFound
	if _, err := s.GetTayfaStats(ctx, &corev1.GetTayfaStatsRequest{TayfaId: tf.String()}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("üye olmayan: %v", err)
	}
	if _, err := s.GetTayfaStats(context.Background(), &corev1.GetTayfaStatsRequest{TayfaId: tf.String()}); err == nil {
		t.Fatal("kimliksiz çağrı kabul edildi")
	}
}

func TestRegisterTayfaStats(t *testing.T) {
	srv := grpc.NewServer()
	if err := registerTayfaStats(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.GetServiceInfo()["tayfam.core.v1.TayfaStatsService"]; !ok {
		t.Fatal("servis kayıtlı değil")
	}
}
