package home

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/guest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/teams"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/localday"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/stats"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Bounds of the champions card.
const (
	// MaxChampions bounds the celebrations the card lists.
	MaxChampions = 10
	// championRows bounds the celebrations read to fill it: some may have
	// no winner to show.
	championRows = 50
	// last30Days is PeriodLast30Days.
	last30Days = 30 * 24 * time.Hour
)

// MetricValue is one metric of the tayfa's statistics; a metric nothing
// counted yet is 0.
type MetricValue struct {
	Metric string
	Value  float64
	// Events is how many events made the value.
	Events int32
}

// StatsData is the statistics card: the metrics of its settings, in their
// order.
type StatsData struct {
	Period string
	// Year is the Europe/Istanbul year; stats.AllTime (0) for all time.
	Year   int32
	Values []MetricValue
	// ComputedAt is zero when nothing was computed yet.
	ComputedAt time.Time
}

// GoalData is the yearly goal with its progress, read from the statistics
// of the year (frozen events, ADR-0026 §2).
type GoalData struct {
	Metric  string
	Target  float64
	Year    int32
	Value   float64
	Events  int32
	Reached bool
}

// LogSummaryData is a log statistic of the tayfa.
type LogSummaryData struct {
	Metric string
	Period string
	Year   int32
	Value  float64
	Events int32
}

// ChampionWinner is one winner of a celebration: a person, a team (with its
// name and place) or a guest player (with their nickname).
type ChampionWinner struct {
	User          id.ID
	Team          id.ID
	TeamName      string
	TeamPosition  int32
	Guest         id.ID
	GuestNickname string
}

// Champion is one announced celebration with a winner.
type Champion struct {
	Event       id.ID
	Title       string
	Type        string
	StartsAt    time.Time
	Component   string
	AnnouncedAt time.Time
	Winners     []ChampionWinner
}

// ChampionsData is the champions card, latest announcement first.
type ChampionsData struct {
	Period    string
	Champions []Champion
}

// Data is one display widget's data: the field of its key is set.
type Data struct {
	Key        string
	Stats      *StatsData
	Goal       *GoalData
	Countdown  *CountdownConfig
	Champions  *ChampionsData
	LogSummary *LogSummaryData
}

// periodYear is the statistics year of a period: the current
// Europe/Istanbul year, or stats.AllTime.
func periodYear(period string, now time.Time) int32 {
	if period == PeriodAllTime {
		return stats.AllTime
	}
	return int32(localday.Today(now).Year()) //nolint:gosec // a year fits
}

// Data returns the data of the display widget key as the tayfa's home page
// shows it. Open to every active member, muted ones included
// (authz.TayfaView). Refused: anyone but an active member
// (apperr.ErrTayfaNotFound), a widget the page does not show or whose data
// is read elsewhere (apperr.ErrHomeWidgetNotFound).
func (s *Service) Data(ctx context.Context, user, tayfa id.ID, key string) (Data, error) {
	var out Data
	err := s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		q := sc.Q()
		shown, err := Shown(ctx, q, tayfa, sc.Now)
		if err != nil {
			return err
		}
		for _, it := range shown {
			if it.Key == key {
				out, err = read(ctx, q, tayfa, it, sc.Now)
				return err
			}
		}
		return apperr.ErrHomeWidgetNotFound
	})
	if err != nil {
		return Data{}, err
	}
	return out, nil
}

// read builds the data of a shown widget.
func read(ctx context.Context, q *store.Queries, tayfa id.ID, it Item, now time.Time) (Data, error) {
	out := Data{Key: it.Key}
	c := it.Config
	switch {
	case it.Key == KeyStats && c.Stats != nil:
		year := periodYear(c.Stats.Period, now)
		totals, at, err := totalsOf(ctx, q, tayfa, year)
		if err != nil {
			return Data{}, err
		}
		d := &StatsData{Period: c.Stats.Period, Year: year, Values: make([]MetricValue, 0, len(c.Stats.Metrics)), ComputedAt: at}
		for _, m := range c.Stats.Metrics {
			d.Values = append(d.Values, totals.value(m))
		}
		out.Stats = d
	case it.Key == KeyGoal && c.Goal != nil:
		year := c.Goal.Year
		if year == 0 {
			year = periodYear(PeriodThisYear, now)
		}
		totals, _, err := totalsOf(ctx, q, tayfa, year)
		if err != nil {
			return Data{}, err
		}
		v := totals.value(c.Goal.Metric)
		out.Goal = &GoalData{
			Metric: c.Goal.Metric, Target: c.Goal.Target, Year: year,
			Value: v.Value, Events: v.Events, Reached: v.Value >= c.Goal.Target,
		}
	case it.Key == KeyCountdown && c.Countdown != nil:
		cd := *c.Countdown
		out.Countdown = &cd
	case it.Key == KeyChampions && c.Champions != nil:
		d, err := champions(ctx, q, tayfa, *c.Champions, now)
		if err != nil {
			return Data{}, err
		}
		out.Champions = d
	case it.Key == KeyLogSummary && c.LogSummary != nil:
		year := periodYear(c.LogSummary.Period, now)
		totals, _, err := totalsOf(ctx, q, tayfa, year)
		if err != nil {
			return Data{}, err
		}
		v := totals.value(c.LogSummary.Metric)
		out.LogSummary = &LogSummaryData{Metric: v.Metric, Period: c.LogSummary.Period, Year: year, Value: v.Value, Events: v.Events}
	default:
		// Sections and module widgets read their own endpoints.
		return Data{}, apperr.ErrHomeWidgetNotFound
	}
	return out, nil
}

// totals are a tayfa's statistics of a year, by metric.
type totals map[string]stats.Stat

func totalsOf(ctx context.Context, q *store.Queries, tayfa id.ID, year int32) (totals, time.Time, error) {
	rows, at, err := stats.TayfaTotals(ctx, q, tayfa, year)
	if err != nil {
		return nil, time.Time{}, err
	}
	out := make(totals, len(rows))
	for _, r := range rows {
		out[r.Metric] = r
	}
	return out, at, nil
}

func (t totals) value(metric string) MetricValue {
	s := t[metric]
	return MetricValue{Metric: metric, Value: s.Value, Events: s.Events}
}

// champions reads the tayfa's announced celebrations of the period, latest
// first, as the celebration component shows them to members: nothing the
// source hides, nothing of a type whose attendance is sensitive, no
// celebration the organizer switched off, none without a winner.
func champions(ctx context.Context, q *store.Queries, tayfa id.ID, c ChampionsConfig, now time.Time) (*ChampionsData, error) {
	p := store.ListHomeChampionCelebrationsParams{
		TayfaID: tayfa.PG(), Since: ts(time.Unix(0, 0).UTC()), Now: ts(now),
		EventTypes: c.EventTypes, MaxRows: championRows,
	}
	if p.EventTypes == nil {
		p.EventTypes = []string{}
	}
	switch c.Period {
	case PeriodLast30Days:
		p.Since = ts(now.Add(-last30Days))
	default:
		p.Year = periodYear(PeriodThisYear, now)
	}
	rows, err := q.ListHomeChampionCelebrations(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("şampiyonlar: %w", err)
	}
	out := &ChampionsData{Period: c.Period, Champions: []Champion{}}
	for _, r := range rows {
		if len(out.Champions) >= MaxChampions {
			break
		}
		ev := id.ID(r.ID.Bytes)
		t, err := catalog.TypeVersion(ctx, q, r.Type, r.TypeVersion)
		if err != nil {
			return nil, fmt.Errorf("etkinliğin tür sürümü: %w", err)
		}
		comp, ok := t.Parsed.Component(r.ComponentKey)
		if !ok || comp.Kind != definition.CompCelebration {
			continue
		}
		on, err := enabled(ctx, q, ev, comp)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		res, err := celebration.Load(ctx, q, ev, t.Parsed, comp)
		if err != nil {
			return nil, err
		}
		if res.Hidden || len(res.Winners) == 0 {
			continue
		}
		winners, err := championWinners(ctx, q, ev, t.Parsed, res.Winners)
		if err != nil {
			return nil, err
		}
		out.Champions = append(out.Champions, Champion{
			Event: ev, Title: r.Title, Type: r.Type, StartsAt: r.StartsAt.Time.UTC(),
			Component: r.ComponentKey, AnnouncedAt: r.AnnouncedAt.Time.UTC(), Winners: winners,
		})
	}
	return out, nil
}

// enabled reports whether an optional component is switched on for the
// event.
func enabled(ctx context.Context, q *store.Queries, event id.ID, c definition.Component) (bool, error) {
	if c.Optional == nil {
		return true, nil
	}
	row, err := q.GetEventComponent(ctx, store.GetEventComponentParams{EventID: event.PG(), ComponentKey: c.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return *c.Optional, nil
	}
	if err != nil {
		return false, fmt.Errorf("kutlamanın seçimi: %w", err)
	}
	return row.Enabled, nil
}

// championWinners adds the team's name and place and the guest's nickname
// to the winners, read only when a winner needs them.
func championWinners(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, ws []celebration.Winner) ([]ChampionWinner, error) {
	var (
		byTeam  map[id.ID]teams.Team
		byGuest map[id.ID]string
	)
	out := make([]ChampionWinner, 0, len(ws))
	for _, w := range ws {
		cw := ChampionWinner{User: w.User, Team: w.Team, Guest: w.Guest}
		if w.Team != (id.ID{}) {
			if byTeam == nil {
				byTeam = map[id.ID]teams.Team{}
				for _, c := range def.Components {
					if c.Kind != definition.CompTeams {
						continue
					}
					list, err := teams.Load(ctx, q, event, c.Key)
					if err != nil {
						return nil, err
					}
					for _, t := range list {
						byTeam[t.ID] = t
					}
				}
			}
			t := byTeam[w.Team]
			cw.TeamName, cw.TeamPosition = t.Name, t.Position
		}
		if w.Guest != (id.ID{}) {
			if byGuest == nil {
				byGuest = map[id.ID]string{}
				gs, err := guest.List(ctx, q, event)
				if err != nil {
					return nil, err
				}
				for _, g := range gs {
					byGuest[g.ID] = g.Nickname
				}
			}
			cw.GuestNickname = byGuest[w.Guest]
		}
		out = append(out, cw)
	}
	return out, nil
}
