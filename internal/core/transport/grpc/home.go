package grpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/home"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Home is what TayfaHomeServer needs from the app layer (home.Service).
type Home interface {
	Layout(ctx context.Context, user, tayfa id.ID) ([]home.Item, error)
	Set(ctx context.Context, user, tayfa id.ID, in []home.Input) ([]home.Item, error)
	Data(ctx context.Context, user, tayfa id.ID, key string) (home.Data, error)
}

// TayfaHomeServer implements corev1.TayfaHomeServiceServer (issue #268;
// ADR-0027 §3–§4). Every method needs a verified token (auth.Guard); the
// acting user is always the token's. Settings (a countdown's title) and
// values are never logged.
type TayfaHomeServer struct {
	corev1.UnimplementedTayfaHomeServiceServer
	app Home
	log *slog.Logger
}

// NewTayfaHomeServer returns a TayfaHomeServer.
func NewTayfaHomeServer(app Home, log *slog.Logger) *TayfaHomeServer {
	return &TayfaHomeServer{app: app, log: log}
}

// registerTayfaHome is the home page's line in the registry
// (componentRegistrars): its display widgets read the components' data.
func registerTayfaHome(srv grpc.ServiceRegistrar, fw *component.Service, log *slog.Logger) error {
	corev1.RegisterTayfaHomeServiceServer(srv, NewTayfaHomeServer(home.New(fw), log))
	return nil
}

// maxHomeWidgets bounds a layout request; the list itself is checked by
// the app layer.
const maxHomeWidgets = 64

var homeWidgetKinds = map[home.Kind]corev1.HomeWidgetKind{
	home.KindSection: corev1.HomeWidgetKind_HOME_WIDGET_KIND_SECTION,
	home.KindDisplay: corev1.HomeWidgetKind_HOME_WIDGET_KIND_DISPLAY,
	home.KindModule:  corev1.HomeWidgetKind_HOME_WIDGET_KIND_MODULE,
}

// homeConfigPB is a widget's settings; nil when it has none.
func homeConfigPB(c home.Config) *corev1.HomeWidgetConfig {
	out := &corev1.HomeWidgetConfig{}
	empty := true
	if c.Stats != nil {
		out.Stats = &corev1.StatsWidgetConfig{MetricKeys: append([]string{}, c.Stats.Metrics...), Period: c.Stats.Period}
		empty = false
	}
	if c.Goal != nil {
		out.Goal = &corev1.GoalWidgetConfig{MetricKey: c.Goal.Metric, Target: c.Goal.Target, Year: c.Goal.Year}
		empty = false
	}
	if c.Countdown != nil {
		out.Countdown = &corev1.CountdownWidgetConfig{Title: c.Countdown.Title, TargetTime: tsOrNil(c.Countdown.At)}
		empty = false
	}
	if c.Champions != nil {
		out.Champions = &corev1.ChampionsWidgetConfig{EventTypes: append([]string{}, c.Champions.EventTypes...), Period: c.Champions.Period}
		empty = false
	}
	if c.LogSummary != nil {
		out.LogSummary = &corev1.LogSummaryWidgetConfig{MetricKey: c.LogSummary.Metric, Period: c.LogSummary.Period}
		empty = false
	}
	if empty {
		return nil
	}
	return out
}

// homeWidgetsPB converts a layout (or the widgets the page shows).
func homeWidgetsPB(items []home.Item) []*corev1.HomeWidget {
	out := make([]*corev1.HomeWidget, 0, len(items))
	for _, it := range items {
		out = append(out, &corev1.HomeWidget{
			Key: it.Key, Kind: homeWidgetKinds[it.Kind], Module: it.Module,
			Enabled: it.Enabled, Position: it.Position, Config: homeConfigPB(it.Config),
		})
	}
	return out
}

// homeConfigFrom reads a request's settings; the app layer checks them.
// A countdown without a time is refused here.
func homeConfigFrom(c *corev1.HomeWidgetConfig) (home.Config, bool) {
	var out home.Config
	if c == nil {
		return out, true
	}
	if s := c.GetStats(); s != nil {
		out.Stats = &home.StatsConfig{Metrics: append([]string{}, s.GetMetricKeys()...), Period: s.GetPeriod()}
	}
	if g := c.GetGoal(); g != nil {
		out.Goal = &home.GoalConfig{Metric: g.GetMetricKey(), Target: g.GetTarget(), Year: g.GetYear()}
	}
	if cd := c.GetCountdown(); cd != nil {
		if cd.GetTargetTime() == nil || cd.GetTargetTime().CheckValid() != nil {
			return home.Config{}, false
		}
		out.Countdown = &home.CountdownConfig{Title: cd.GetTitle(), At: cd.GetTargetTime().AsTime()}
	}
	if ch := c.GetChampions(); ch != nil {
		out.Champions = &home.ChampionsConfig{EventTypes: append([]string{}, ch.GetEventTypes()...), Period: ch.GetPeriod()}
	}
	if l := c.GetLogSummary(); l != nil {
		out.LogSummary = &home.LogSummaryConfig{Metric: l.GetMetricKey(), Period: l.GetPeriod()}
	}
	return out, true
}

// GetHomeWidgets implements corev1.TayfaHomeServiceServer.
func (s *TayfaHomeServer) GetHomeWidgets(ctx context.Context, req *corev1.GetHomeWidgetsRequest) (*corev1.GetHomeWidgetsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	items, err := s.app.Layout(ctx, user, t)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.GetHomeWidgetsResponse{Widgets: homeWidgetsPB(items)}, nil
}

// SetHomeWidgets implements corev1.TayfaHomeServiceServer.
func (s *TayfaHomeServer) SetHomeWidgets(ctx context.Context, req *corev1.SetHomeWidgetsRequest) (*corev1.SetHomeWidgetsResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	if len(req.GetWidgets()) > maxHomeWidgets {
		return nil, validation(home.FieldWidgets)
	}
	in := make([]home.Input, 0, len(req.GetWidgets()))
	for _, w := range req.GetWidgets() {
		if !definition.ValidKey(w.GetKey()) {
			return nil, validation(home.FieldWidgets)
		}
		c, ok := homeConfigFrom(w.GetConfig())
		if !ok {
			return nil, validation(home.FieldConfig)
		}
		in = append(in, home.Input{Key: w.GetKey(), Enabled: w.GetEnabled(), Config: c})
	}
	items, err := s.app.Set(ctx, user, t, in)
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return &corev1.SetHomeWidgetsResponse{Widgets: homeWidgetsPB(items)}, nil
}

// GetHomeWidgetData implements corev1.TayfaHomeServiceServer.
func (s *TayfaHomeServer) GetHomeWidgetData(ctx context.Context, req *corev1.GetHomeWidgetDataRequest) (*corev1.GetHomeWidgetDataResponse, error) {
	user, t, _, err := scope(ctx, req.GetTayfaId(), nil)
	if err != nil {
		return nil, err
	}
	if !definition.ValidKey(req.GetKey()) {
		return nil, validation("key")
	}
	d, err := s.app.Data(ctx, user, t, req.GetKey())
	if err != nil {
		return nil, errorStatus(ctx, s.log, err)
	}
	return homeDataPB(d), nil
}

func homeMetricPB(v home.MetricValue) *corev1.HomeMetricValue {
	return &corev1.HomeMetricValue{MetricKey: v.Metric, Value: v.Value, EventCount: v.Events}
}

func homeDataPB(d home.Data) *corev1.GetHomeWidgetDataResponse {
	out := &corev1.GetHomeWidgetDataResponse{Key: d.Key}
	if st := d.Stats; st != nil {
		v := &corev1.StatsWidgetData{Period: st.Period, Year: st.Year, ComputeTime: tsOrNil(st.ComputedAt), Values: make([]*corev1.HomeMetricValue, 0, len(st.Values))}
		for _, m := range st.Values {
			v.Values = append(v.Values, homeMetricPB(m))
		}
		out.Stats = v
	}
	if g := d.Goal; g != nil {
		out.Goal = &corev1.GoalWidgetData{MetricKey: g.Metric, Target: g.Target, Year: g.Year, Value: g.Value, EventCount: g.Events, Reached: g.Reached}
	}
	if c := d.Countdown; c != nil {
		out.Countdown = &corev1.CountdownWidgetData{Title: c.Title, TargetTime: tsOrNil(c.At)}
	}
	if ch := d.Champions; ch != nil {
		v := &corev1.ChampionsWidgetData{Period: ch.Period, Champions: make([]*corev1.HomeChampion, 0, len(ch.Champions))}
		for _, c := range ch.Champions {
			hc := &corev1.HomeChampion{
				EventId: c.Event.String(), EventTitle: c.Title, EventType: c.Type, StartTime: tsOrNil(c.StartsAt),
				ComponentKey: c.Component, AnnounceTime: tsOrNil(c.AnnouncedAt),
				Winners: make([]*corev1.ChampionWinner, 0, len(c.Winners)),
			}
			for _, w := range c.Winners {
				hc.Winners = append(hc.Winners, &corev1.ChampionWinner{
					UserId: idString(w.User), TeamId: idString(w.Team), TeamName: w.TeamName, TeamPosition: w.TeamPosition,
					GuestId: idString(w.Guest), GuestNickname: w.GuestNickname,
				})
			}
			v.Champions = append(v.Champions, hc)
		}
		out.Champions = v
	}
	if l := d.LogSummary; l != nil {
		out.LogSummary = &corev1.LogSummaryWidgetData{MetricKey: l.Metric, Period: l.Period, Year: l.Year, Value: l.Value, EventCount: l.Events}
	}
	return out
}
