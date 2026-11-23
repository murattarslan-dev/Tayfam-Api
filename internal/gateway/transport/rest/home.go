package rest

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// The tayfa home page (issue #268; ADR-0027 §3–§4). core decides
// everything; the gateway only keeps junk out early.

const (
	homeWidgetsPath = "/v1/tayfas/{tayfa_id}/home-widgets"
	// maxHomeWidgets bounds a layout request (the list is core's).
	maxHomeWidgets = 64
	// maxHomeWidgetsBody fits the whole list with every widget's settings.
	maxHomeWidgetsBody = 32 << 10
)

// registerTayfaHome is the home page's line in the registry
// (componentRoutes). Its routes are the tayfa's, not an event's.
func registerTayfaHome(mux *http.ServeMux, h componentHandlers) {
	mux.HandleFunc("GET "+homeWidgetsPath, h.authed(h.getHomeWidgets))
	mux.HandleFunc("PUT "+homeWidgetsPath, h.authed(h.setHomeWidgets))
	mux.HandleFunc("GET "+homeWidgetsPath+"/{widget_key}/data", h.authed(h.getHomeWidgetData))
}

func (h componentHandlers) home() corev1.TayfaHomeServiceClient {
	return corev1.NewTayfaHomeServiceClient(h.conn)
}

type statsWidgetConfigJSON struct {
	MetricKeys []string `json:"metric_keys"`
	Period     string   `json:"period"`
}

type goalWidgetConfigJSON struct {
	MetricKey string  `json:"metric_key"`
	Target    float64 `json:"target"`
	// Year is absent for the current year.
	Year *int32 `json:"year,omitempty"`
}

type countdownWidgetConfigJSON struct {
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

type championsWidgetConfigJSON struct {
	EventTypes []string `json:"event_types"`
	Period     string   `json:"period"`
}

type logSummaryWidgetConfigJSON struct {
	MetricKey string `json:"metric_key"`
	Period    string `json:"period"`
}

// homeWidgetConfigJSON holds a widget's settings: at most the field of the
// widget's key.
type homeWidgetConfigJSON struct {
	Stats      *statsWidgetConfigJSON      `json:"stats,omitempty"`
	Goal       *goalWidgetConfigJSON       `json:"goal,omitempty"`
	Countdown  *countdownWidgetConfigJSON  `json:"countdown,omitempty"`
	Champions  *championsWidgetConfigJSON  `json:"champions,omitempty"`
	LogSummary *logSummaryWidgetConfigJSON `json:"log_summary,omitempty"`
}

type homeWidgetJSON struct {
	Key      string                `json:"key"`
	Kind     string                `json:"kind"`
	Module   string                `json:"module,omitempty"`
	Enabled  bool                  `json:"enabled"`
	Position int32                 `json:"position"`
	Config   *homeWidgetConfigJSON `json:"config,omitempty"`
}

type homeWidgetsJSON struct {
	Widgets []homeWidgetJSON `json:"widgets"`
}

var homeWidgetKinds = newEnum(map[string]corev1.HomeWidgetKind{
	"section": corev1.HomeWidgetKind_HOME_WIDGET_KIND_SECTION,
	"display": corev1.HomeWidgetKind_HOME_WIDGET_KIND_DISPLAY,
	"module":  corev1.HomeWidgetKind_HOME_WIDGET_KIND_MODULE,
})

func homeWidgetConfigFrom(c *corev1.HomeWidgetConfig) *homeWidgetConfigJSON {
	if c == nil {
		return nil
	}
	out := &homeWidgetConfigJSON{}
	if s := c.GetStats(); s != nil {
		out.Stats = &statsWidgetConfigJSON{MetricKeys: nonNil(s.GetMetricKeys()), Period: s.GetPeriod()}
	}
	if g := c.GetGoal(); g != nil {
		out.Goal = &goalWidgetConfigJSON{MetricKey: g.GetMetricKey(), Target: g.GetTarget()}
		if y := g.GetYear(); y != 0 {
			out.Goal.Year = &y
		}
	}
	if cd := c.GetCountdown(); cd != nil {
		out.Countdown = &countdownWidgetConfigJSON{Title: cd.GetTitle(), At: cd.GetTargetTime().AsTime()}
	}
	if ch := c.GetChampions(); ch != nil {
		out.Champions = &championsWidgetConfigJSON{EventTypes: nonNil(ch.GetEventTypes()), Period: ch.GetPeriod()}
	}
	if l := c.GetLogSummary(); l != nil {
		out.LogSummary = &logSummaryWidgetConfigJSON{MetricKey: l.GetMetricKey(), Period: l.GetPeriod()}
	}
	return out
}

// homeWidgetsFrom converts a layout, or the widgets the home page shows
// (GET /v1/tayfas/{id}).
func homeWidgetsFrom(ws []*corev1.HomeWidget) []homeWidgetJSON {
	out := make([]homeWidgetJSON, 0, len(ws))
	for _, w := range ws {
		out = append(out, homeWidgetJSON{
			Key: w.GetKey(), Kind: homeWidgetKinds.name(w.GetKind()), Module: w.GetModule(),
			Enabled: w.GetEnabled(), Position: w.GetPosition(), Config: homeWidgetConfigFrom(w.GetConfig()),
		})
	}
	return out
}

// homeWidgetInputJSON is one widget of a new layout.
type homeWidgetInputJSON struct {
	Key     string                `json:"key"`
	Enabled bool                  `json:"enabled"`
	Config  *homeWidgetConfigJSON `json:"config"`
}

type homeWidgetsBody struct {
	Widgets []homeWidgetInputJSON `json:"widgets"`
}

// homeWidgetConfigPB converts a request's settings; core checks them.
func homeWidgetConfigPB(c *homeWidgetConfigJSON) *corev1.HomeWidgetConfig {
	if c == nil {
		return nil
	}
	out := &corev1.HomeWidgetConfig{}
	if s := c.Stats; s != nil {
		out.Stats = &corev1.StatsWidgetConfig{MetricKeys: s.MetricKeys, Period: s.Period}
	}
	if g := c.Goal; g != nil {
		out.Goal = &corev1.GoalWidgetConfig{MetricKey: g.MetricKey, Target: g.Target}
		if g.Year != nil {
			out.Goal.Year = *g.Year
		}
	}
	if cd := c.Countdown; cd != nil {
		out.Countdown = &corev1.CountdownWidgetConfig{Title: cd.Title, TargetTime: timestamppb.New(cd.At)}
	}
	if ch := c.Champions; ch != nil {
		out.Champions = &corev1.ChampionsWidgetConfig{EventTypes: ch.EventTypes, Period: ch.Period}
	}
	if l := c.LogSummary; l != nil {
		out.LogSummary = &corev1.LogSummaryWidgetConfig{MetricKey: l.MetricKey, Period: l.Period}
	}
	return out
}

func (h componentHandlers) getHomeWidgets(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	resp, err := h.home().GetHomeWidgets(ctx, &corev1.GetHomeWidgetsRequest{TayfaId: tf})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, homeWidgetsJSON{Widgets: homeWidgetsFrom(resp.GetWidgets())})
}

func (h componentHandlers) setHomeWidgets(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	var b homeWidgetsBody
	if !decodeLimit(w, r, &b, maxHomeWidgetsBody) {
		return
	}
	if b.Widgets == nil || len(b.Widgets) > maxHomeWidgets {
		badRequest(w, r)
		return
	}
	req := &corev1.SetHomeWidgetsRequest{TayfaId: tf, Widgets: make([]*corev1.HomeWidgetInput, 0, len(b.Widgets))}
	for _, in := range b.Widgets {
		if !catalogKeyPattern.MatchString(in.Key) {
			badRequest(w, r)
			return
		}
		req.Widgets = append(req.Widgets, &corev1.HomeWidgetInput{Key: in.Key, Enabled: in.Enabled, Config: homeWidgetConfigPB(in.Config)})
	}
	resp, err := h.home().SetHomeWidgets(ctx, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, homeWidgetsJSON{Widgets: homeWidgetsFrom(resp.GetWidgets())})
}

type homeMetricValueJSON struct {
	MetricKey  string  `json:"metric_key"`
	Value      float64 `json:"value"`
	EventCount int32   `json:"event_count"`
}

type statsWidgetDataJSON struct {
	Period string `json:"period"`
	// Year is absent for all time.
	Year       *int32                `json:"year,omitempty"`
	Values     []homeMetricValueJSON `json:"values"`
	ComputedAt *time.Time            `json:"computed_at,omitempty"`
}

type goalWidgetDataJSON struct {
	MetricKey  string  `json:"metric_key"`
	Target     float64 `json:"target"`
	Year       int32   `json:"year"`
	Value      float64 `json:"value"`
	EventCount int32   `json:"event_count"`
	Reached    bool    `json:"reached"`
}

type countdownWidgetDataJSON struct {
	Title string     `json:"title"`
	At    *time.Time `json:"at,omitempty"`
}

type championWinnerJSON struct {
	UserID        string `json:"user_id,omitempty"`
	TeamID        string `json:"team_id,omitempty"`
	TeamName      string `json:"team_name,omitempty"`
	TeamPosition  int32  `json:"team_position,omitempty"`
	GuestID       string `json:"guest_id,omitempty"`
	GuestNickname string `json:"guest_nickname,omitempty"`
}

type homeChampionJSON struct {
	EventID      string               `json:"event_id"`
	EventTitle   string               `json:"event_title"`
	EventType    string               `json:"event_type"`
	StartsAt     *time.Time           `json:"starts_at,omitempty"`
	ComponentKey string               `json:"component_key"`
	AnnouncedAt  *time.Time           `json:"announced_at,omitempty"`
	Winners      []championWinnerJSON `json:"winners"`
}

type championsWidgetDataJSON struct {
	Period    string             `json:"period"`
	Champions []homeChampionJSON `json:"champions"`
}

type logSummaryWidgetDataJSON struct {
	MetricKey string `json:"metric_key"`
	Period    string `json:"period"`
	// Year is absent for all time.
	Year       *int32  `json:"year,omitempty"`
	Value      float64 `json:"value"`
	EventCount int32   `json:"event_count"`
}

type homeWidgetDataJSON struct {
	Key        string                    `json:"key"`
	Stats      *statsWidgetDataJSON      `json:"stats,omitempty"`
	Goal       *goalWidgetDataJSON       `json:"goal,omitempty"`
	Countdown  *countdownWidgetDataJSON  `json:"countdown,omitempty"`
	Champions  *championsWidgetDataJSON  `json:"champions,omitempty"`
	LogSummary *logSummaryWidgetDataJSON `json:"log_summary,omitempty"`
}

func optYear(y int32) *int32 {
	if y == 0 {
		return nil
	}
	return &y
}

func homeWidgetDataFrom(resp *corev1.GetHomeWidgetDataResponse) homeWidgetDataJSON {
	out := homeWidgetDataJSON{Key: resp.GetKey()}
	if s := resp.GetStats(); s != nil {
		d := &statsWidgetDataJSON{Period: s.GetPeriod(), Year: optYear(s.GetYear()), ComputedAt: optTime(s.GetComputeTime()), Values: make([]homeMetricValueJSON, 0, len(s.GetValues()))}
		for _, v := range s.GetValues() {
			d.Values = append(d.Values, homeMetricValueJSON{MetricKey: v.GetMetricKey(), Value: v.GetValue(), EventCount: v.GetEventCount()})
		}
		out.Stats = d
	}
	if g := resp.GetGoal(); g != nil {
		out.Goal = &goalWidgetDataJSON{
			MetricKey: g.GetMetricKey(), Target: g.GetTarget(), Year: g.GetYear(),
			Value: g.GetValue(), EventCount: g.GetEventCount(), Reached: g.GetReached(),
		}
	}
	if c := resp.GetCountdown(); c != nil {
		out.Countdown = &countdownWidgetDataJSON{Title: c.GetTitle(), At: optTime(c.GetTargetTime())}
	}
	if ch := resp.GetChampions(); ch != nil {
		d := &championsWidgetDataJSON{Period: ch.GetPeriod(), Champions: make([]homeChampionJSON, 0, len(ch.GetChampions()))}
		for _, c := range ch.GetChampions() {
			hc := homeChampionJSON{
				EventID: c.GetEventId(), EventTitle: c.GetEventTitle(), EventType: c.GetEventType(),
				StartsAt: optTime(c.GetStartTime()), ComponentKey: c.GetComponentKey(), AnnouncedAt: optTime(c.GetAnnounceTime()),
				Winners: make([]championWinnerJSON, 0, len(c.GetWinners())),
			}
			for _, w := range c.GetWinners() {
				hc.Winners = append(hc.Winners, championWinnerJSON{
					UserID: w.GetUserId(), TeamID: w.GetTeamId(), TeamName: w.GetTeamName(), TeamPosition: w.GetTeamPosition(),
					GuestID: w.GetGuestId(), GuestNickname: w.GetGuestNickname(),
				})
			}
			d.Champions = append(d.Champions, hc)
		}
		out.Champions = d
	}
	if l := resp.GetLogSummary(); l != nil {
		out.LogSummary = &logSummaryWidgetDataJSON{
			MetricKey: l.GetMetricKey(), Period: l.GetPeriod(), Year: optYear(l.GetYear()),
			Value: l.GetValue(), EventCount: l.GetEventCount(),
		}
	}
	return out
}

func (h componentHandlers) getHomeWidgetData(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	tf, ok := pathUUID(w, r, "tayfa_id")
	if !ok {
		return
	}
	key := r.PathValue("widget_key")
	if !catalogKeyPattern.MatchString(key) {
		badRequest(w, r)
		return
	}
	resp, err := h.home().GetHomeWidgetData(ctx, &corev1.GetHomeWidgetDataRequest{TayfaId: tf, Key: key})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, homeWidgetDataFrom(resp))
}
