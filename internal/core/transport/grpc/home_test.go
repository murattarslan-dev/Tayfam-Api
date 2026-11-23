package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/home"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeHome struct {
	err         error
	user, tayfa id.ID
	in          []home.Input
	key         string
	items       []home.Item
	data        home.Data
	calls       int
}

func (f *fakeHome) Layout(_ context.Context, user, tayfa id.ID) ([]home.Item, error) {
	f.user, f.tayfa, f.calls = user, tayfa, f.calls+1
	return f.items, f.err
}

func (f *fakeHome) Set(_ context.Context, user, tayfa id.ID, in []home.Input) ([]home.Item, error) {
	f.user, f.tayfa, f.in, f.calls = user, tayfa, in, f.calls+1
	return f.items, f.err
}

func (f *fakeHome) Data(_ context.Context, user, tayfa id.ID, key string) (home.Data, error) {
	f.user, f.tayfa, f.key, f.calls = user, tayfa, key, f.calls+1
	return f.data, f.err
}

func TestTayfaHomeMethods(t *testing.T) {
	at := time.Date(2027, 5, 19, 9, 0, 0, 0, time.UTC)
	f := &fakeHome{items: []home.Item{
		{Key: home.KeyUpcoming, Kind: home.KindSection, Module: home.ModuleEvents, Enabled: true},
		{Key: home.KeyStats, Kind: home.KindDisplay, Position: 1, Config: home.Config{Stats: &home.StatsConfig{Metrics: []string{"events"}, Period: home.PeriodAllTime}}},
		{Key: home.KeyCountdown, Kind: home.KindDisplay, Position: 2, Enabled: true, Config: home.Config{Countdown: &home.CountdownConfig{Title: "Bayram", At: at}}},
	}}
	s := NewTayfaHomeServer(f, discard)
	user, tf := id.New(), id.New()
	ctx := callerCtx(user)

	got, err := s.GetHomeWidgets(ctx, &corev1.GetHomeWidgetsRequest{TayfaId: tf.String()})
	if err != nil || f.user != user || f.tayfa != tf {
		t.Fatalf("düzen: %v %+v", err, f)
	}
	ws := got.GetWidgets()
	if len(ws) != 3 || ws[0].GetKind() != corev1.HomeWidgetKind_HOME_WIDGET_KIND_SECTION || ws[0].GetModule() != "events" || ws[0].GetConfig() != nil {
		t.Fatalf("bölüm: %v", ws)
	}
	if c := ws[1].GetConfig().GetStats(); c == nil || len(c.GetMetricKeys()) != 1 || c.GetPeriod() != "all_time" || ws[1].GetEnabled() || ws[1].GetPosition() != 1 {
		t.Fatalf("istatistik: %v", ws[1])
	}
	if c := ws[2].GetConfig().GetCountdown(); c == nil || c.GetTitle() != "Bayram" || !c.GetTargetTime().AsTime().Equal(at) {
		t.Fatalf("geri sayım: %v", ws[2])
	}

	_, err = s.SetHomeWidgets(ctx, &corev1.SetHomeWidgetsRequest{TayfaId: tf.String(), Widgets: []*corev1.HomeWidgetInput{
		{Key: "upcoming", Enabled: true},
		{Key: "goal", Enabled: true, Config: &corev1.HomeWidgetConfig{Goal: &corev1.GoalWidgetConfig{MetricKey: "events", Target: 12, Year: 2027}}},
		{Key: "countdown", Config: &corev1.HomeWidgetConfig{Countdown: &corev1.CountdownWidgetConfig{Title: "Bayram", TargetTime: timestamppb.New(at)}}},
	}})
	if err != nil || len(f.in) != 3 || !f.in[0].Enabled || f.in[2].Enabled {
		t.Fatalf("değiştirme: %v %+v", err, f.in)
	}
	if g := f.in[1].Config.Goal; g == nil || *g != (home.GoalConfig{Metric: "events", Target: 12, Year: 2027}) {
		t.Fatalf("hedef: %+v", f.in[1])
	}
	if c := f.in[2].Config.Countdown; c == nil || c.Title != "Bayram" || !c.At.Equal(at) {
		t.Fatalf("geri sayım: %+v", f.in[2])
	}

	f.data = home.Data{Key: home.KeyGoal, Goal: &home.GoalData{Metric: "events", Target: 12, Year: 2027, Value: 13, Events: 13, Reached: true}}
	d, err := s.GetHomeWidgetData(ctx, &corev1.GetHomeWidgetDataRequest{TayfaId: tf.String(), Key: "goal"})
	if err != nil || f.key != "goal" || d.GetKey() != "goal" || !d.GetGoal().GetReached() || d.GetGoal().GetValue() != 13 || d.GetStats() != nil {
		t.Fatalf("veri: %v %v", d, err)
	}
	u, team := id.New(), id.New()
	f.data = home.Data{Key: home.KeyChampions, Champions: &home.ChampionsData{Period: home.PeriodThisYear, Champions: []home.Champion{{
		Event: tf, Title: "Turnuva", Type: "chess", StartsAt: at, Component: "celebration", AnnouncedAt: at,
		Winners: []home.ChampionWinner{{User: u}, {Team: team, TeamName: "Kartallar", TeamPosition: 1}},
	}}}}
	d, err = s.GetHomeWidgetData(ctx, &corev1.GetHomeWidgetDataRequest{TayfaId: tf.String(), Key: "champions"})
	cs := d.GetChampions().GetChampions()
	if err != nil || len(cs) != 1 || cs[0].GetEventTitle() != "Turnuva" || len(cs[0].GetWinners()) != 2 {
		t.Fatalf("şampiyonlar: %v %v", d, err)
	}
	if w := cs[0].GetWinners(); w[0].GetUserId() != u.String() || w[0].GetTeamId() != "" || w[1].GetTeamId() != team.String() || w[1].GetTeamName() != "Kartallar" || w[1].GetUserId() != "" {
		t.Fatalf("kazananlar: %v", w)
	}

	calls := f.calls
	if _, err := s.GetHomeWidgets(ctx, &corev1.GetHomeWidgetsRequest{TayfaId: "x"}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("tayfa: %v", err)
	}
	for name, req := range map[string]*corev1.SetHomeWidgetsRequest{
		"anahtar":   {TayfaId: tf.String(), Widgets: []*corev1.HomeWidgetInput{{Key: "Stats"}}},
		"saatsiz":   {TayfaId: tf.String(), Widgets: []*corev1.HomeWidgetInput{{Key: "countdown", Config: &corev1.HomeWidgetConfig{Countdown: &corev1.CountdownWidgetConfig{Title: "A"}}}}},
		"çok uzun":  {TayfaId: tf.String(), Widgets: make([]*corev1.HomeWidgetInput, maxHomeWidgets+1)},
		"tayfa yok": {TayfaId: "x"},
	} {
		if _, err := s.SetHomeWidgets(ctx, req); codeOf(t, err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.GetHomeWidgetData(ctx, &corev1.GetHomeWidgetDataRequest{TayfaId: tf.String(), Key: "-"}); codeOf(t, err) != codes.InvalidArgument {
		t.Errorf("anahtar: %v", err)
	}
	if f.calls != calls {
		t.Fatal("geçersiz istek uygulamaya ulaştı")
	}
	f.err = apperr.ErrHomeWidgetNotFound
	if _, err := s.GetHomeWidgetData(ctx, &corev1.GetHomeWidgetDataRequest{TayfaId: tf.String(), Key: "goal"}); codeOf(t, err) != codes.NotFound {
		t.Fatalf("gösterilmeyen: %v", err)
	}
	if _, err := s.GetHomeWidgets(context.Background(), &corev1.GetHomeWidgetsRequest{TayfaId: tf.String()}); err == nil {
		t.Fatal("kimliksiz çağrı kabul edildi")
	}
}

func TestRegisterTayfaHome(t *testing.T) {
	srv := grpc.NewServer()
	if err := registerTayfaHome(srv, nil, discard); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.GetServiceInfo()["tayfam.core.v1.TayfaHomeService"]; !ok {
		t.Fatal("servis kayıtlı değil")
	}
}
