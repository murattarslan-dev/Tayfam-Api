// Package badge earns and shows badges (issue #258; ADR-0039). The
// definitions are catalog data (catalog_badges; package catalog,
// badges.go); this package writes what was earned (user_badges,
// tayfa_badges) and reads it back for the profile and the tayfa.
//
// Earning (ADR-0039 §2) happens only from frozen data, inside the job that
// computed it:
//
//   - threshold and first: the statistics job (ADR-0026, issue #266) calls
//     Usable once, then Due with each person's and each tayfa's frozen
//     totals, and GrantUser / GrantTayfa with the result, in the same
//     transaction as the totals. Until that job exists nothing calls them.
//   - champion: the champion-badges job (package jobs) calls
//     AwardChampions for every event whose celebration froze (the end plus
//     component.FreezeAfter), once per event (event_badge_runs). A winner
//     changed within the correction window counts as it stands at the
//     freeze; a later change does not move the badge.
//
// A badge is never taken back: the tables keep it when the event or the
// statistic goes, and a retired definition still names it. Guest players
// earn nothing. Nothing sensitive earns a badge: catalog.CheckBadge refuses
// such a rule, Usable skips a definition that does not pass it (assume
// breach), and a celebration of a type with sensitive attendance has no
// winner.
//
// Each new badge sends one in-app notification of type badge_earned (no
// push, ADR-0039 §4) that names no badge: a personal badge to its earner
// (target: the earner), a tayfa badge to the tayfa's admins and moderators
// (target: the tayfa).
//
// Visibility (ADR-0039 §3): one always sees one's own badges; another
// person's follow the personal statistics setting (ADR-0026 §4, issue
// #267: until it exists, nobody else sees them: the setting's default is
// off). A tayfa's badges are its members'; the Keşfet showcase (#267)
// shows them only when stats_showcase is on and the showcase threshold is
// met. There is no ranking and no "most badges" list.
package badge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component/celebration"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// PeriodAll is the stored period of every badge but a yearly threshold,
// whose period is the year (YYYY).
const PeriodAll = "all"

// Earned is one earned badge.
type Earned struct {
	Key string
	// Year is the calendar year of a yearly threshold; 0 for the others.
	Year int32
	// Tayfa and Event are a champion badge's event and its tayfa; zero
	// for the others.
	Tayfa    id.ID
	Event    id.ID
	EarnedAt time.Time
}

// Award is a badge Due found earned.
type Award struct {
	Key string
	// Year is set for a yearly threshold.
	Year int32
}

func periodKey(year int32) string {
	if year == 0 {
		return PeriodAll
	}
	return strconv.Itoa(int(year))
}

func parsePeriod(s string) (int32, error) {
	if s == PeriodAll {
		return 0, nil
	}
	y, err := strconv.ParseInt(s, 10, 32)
	if err != nil || y < 1 || y > 9999 {
		return 0, errors.New("rozet: geçersiz dönem")
	}
	return int32(y), nil //nolint:gosec // parsed with bitSize 32 and bounded
}

func optID(v pgtype.UUID) id.ID {
	if !v.Valid {
		return id.ID{}
	}
	return id.ID(v.Bytes)
}

func optPG(v id.ID) pgtype.UUID {
	if v == (id.ID{}) {
		return pgtype.UUID{}
	}
	return v.PG()
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// ---------------------------------------------------------------------------
// Reading

// ForUser returns the person's badges in the order earned. Whether the
// reader may see them is ProfileVisible's.
func ForUser(ctx context.Context, q *store.Queries, user id.ID) ([]Earned, error) {
	rows, err := q.ListUserBadges(ctx, user.PG())
	if err != nil {
		return nil, fmt.Errorf("kişinin rozetleri: %w", err)
	}
	out := make([]Earned, 0, len(rows))
	for _, r := range rows {
		y, err := parsePeriod(r.PeriodKey)
		if err != nil {
			return nil, err
		}
		out = append(out, Earned{Key: r.BadgeKey, Year: y, Tayfa: optID(r.TayfaID), Event: optID(r.EventID), EarnedAt: r.EarnedAt.Time.UTC()})
	}
	return out, nil
}

// ForTayfa returns the tayfa's badges in the order earned, for its members
// (the caller checked the membership).
func ForTayfa(ctx context.Context, q *store.Queries, tayfa id.ID) ([]Earned, error) {
	rows, err := q.ListTayfaBadges(ctx, tayfa.PG())
	if err != nil {
		return nil, fmt.Errorf("tayfanın rozetleri: %w", err)
	}
	out := make([]Earned, 0, len(rows))
	for _, r := range rows {
		y, err := parsePeriod(r.PeriodKey)
		if err != nil {
			return nil, err
		}
		out = append(out, Earned{Key: r.BadgeKey, Year: y, EarnedAt: r.EarnedAt.Time.UTC()})
	}
	return out, nil
}

// ProfileVisible reports whether a profile shows its owner's badges to the
// reader: always to the owner; to anyone else only when the owner's
// personal statistics are visible to them (ADR-0026 §4; the setting comes
// with issue #267 and is off by default).
func ProfileVisible(self, statsVisible bool) bool { return self || statsVisible }

// ---------------------------------------------------------------------------
// Earning

// Usable returns the active definitions that may be earned: each passes
// catalog.CheckBadge again, and one that does not (a tampered or outdated
// rule) is skipped, never earned.
func Usable(ctx context.Context, q *store.Queries) ([]catalog.Badge, error) {
	all, err := catalog.Badges(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Badge, 0, len(all))
	for _, b := range all {
		if b.Status != catalog.StatusActive {
			continue
		}
		if err := catalog.CheckBadge(ctx, q, b); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

// Totals are one person's or one tayfa's frozen statistics as the
// statistics job computed them, by metric key (catalog: common metrics and
// "<type>.<key>"). A metric that is not there is zero.
type Totals struct {
	// Scope is catalog.BadgeScopeUser or catalog.BadgeScopeTayfa.
	Scope   string
	AllTime map[string]float64
	// Year is the calendar year YearTotals are for; 0 when no year was
	// computed (yearly thresholds are then not looked at).
	Year       int32
	YearTotals map[string]float64
}

// Due returns the threshold and first badges of badges (from Usable) that
// t reaches. What was earned before is the database's to skip.
func Due(badges []catalog.Badge, t Totals) []Award {
	var out []Award
	for _, b := range badges {
		if b.Status != catalog.StatusActive || b.Scope != t.Scope {
			continue
		}
		switch b.Rule.Kind {
		case catalog.RuleThreshold:
			if b.Rule.Year() {
				if t.Year > 0 && t.YearTotals[b.Rule.Metric] >= b.Rule.Value {
					out = append(out, Award{Key: b.Key, Year: t.Year})
				}
			} else if t.AllTime[b.Rule.Metric] >= b.Rule.Value {
				out = append(out, Award{Key: b.Key})
			}
		case catalog.RuleFirst:
			m := catalog.MetricAttended
			if b.Rule.EventType != "" {
				m = b.Rule.EventType + "." + catalog.MetricAttended
			}
			if t.AllTime[m] >= 1 {
				out = append(out, Award{Key: b.Key})
			}
		}
	}
	return out
}

// GrantUser writes the person's awards inside the caller's transaction and
// notifies them once if any was new. It returns how many were new.
func GrantUser(ctx context.Context, q *store.Queries, user id.ID, awards []Award, now time.Time) (int, error) {
	return grantUser(ctx, q, user, awards, id.ID{}, id.ID{}, now)
}

func grantUser(ctx context.Context, q *store.Queries, user id.ID, awards []Award, tayfa, event id.ID, now time.Time) (int, error) {
	n := 0
	for _, a := range awards {
		k, err := q.AddUserBadge(ctx, store.AddUserBadgeParams{
			ID: id.New().PG(), UserID: user.PG(), BadgeKey: a.Key, PeriodKey: periodKey(a.Year),
			TayfaID: optPG(tayfa), EventID: optPG(event), EarnedAt: ts(now),
		})
		if err != nil {
			return 0, fmt.Errorf("kişi rozeti: %w", err)
		}
		if k > 0 {
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	err := notify.Write(ctx, q, notify.Request{Type: notify.TypeBadgeEarned, TargetType: notify.TargetUser, Target: user}, []id.ID{user})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// GrantTayfa writes the tayfa's awards inside the caller's transaction and
// notifies its admins and moderators once if any was new. It returns how
// many were new.
func GrantTayfa(ctx context.Context, q *store.Queries, tayfa id.ID, awards []Award, now time.Time) (int, error) {
	n := 0
	for _, a := range awards {
		k, err := q.AddTayfaBadge(ctx, store.AddTayfaBadgeParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), BadgeKey: a.Key, PeriodKey: periodKey(a.Year), EarnedAt: ts(now),
		})
		if err != nil {
			return 0, fmt.Errorf("tayfa rozeti: %w", err)
		}
		if k > 0 {
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	to, err := notify.Staff(ctx, q, tayfa, id.ID{})
	if err != nil {
		return 0, err
	}
	err = notify.Write(ctx, q, notify.Request{Type: notify.TypeBadgeEarned, Tayfa: tayfa, TargetType: notify.TargetTayfa, Target: tayfa}, to)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Event is an event whose celebration froze.
type Event struct {
	ID      id.ID
	Tayfa   id.ID
	Type    string
	Version int32
}

// AwardChampions gives the active champion badges of badges (from Usable)
// that fit the event's type to the winners of its announced celebrations,
// inside the caller's transaction, and marks the event done. A team's
// winners are its members; a guest player earns nothing. It returns how
// many badges were new.
func AwardChampions(ctx context.Context, q *store.Queries, ev Event, badges []catalog.Badge, now time.Time) (int, error) {
	var awards []Award
	for _, b := range badges {
		if b.Status == catalog.StatusActive && b.Scope == catalog.BadgeScopeUser && b.Rule.Kind == catalog.RuleChampion &&
			(b.Rule.EventType == "" || b.Rule.EventType == ev.Type) {
			awards = append(awards, Award{Key: b.Key})
		}
	}
	n := 0
	if len(awards) > 0 {
		users, err := champions(ctx, q, ev)
		if err != nil {
			return 0, err
		}
		for _, u := range users {
			k, err := grantUser(ctx, q, u, awards, ev.Tayfa, ev.ID, now)
			if err != nil {
				return 0, err
			}
			n += k
		}
	}
	if err := q.MarkEventBadgeRun(ctx, store.MarkEventBadgeRunParams{EventID: ev.ID.PG(), DoneAt: ts(now)}); err != nil {
		return 0, fmt.Errorf("şampiyonluk rozeti işareti: %w", err)
	}
	return n, nil
}

// champions returns the people the event's announced celebrations name,
// once each, in order.
func champions(ctx context.Context, q *store.Queries, ev Event) ([]id.ID, error) {
	t, err := catalog.TypeVersion(ctx, q, ev.Type, ev.Version)
	if err != nil {
		return nil, fmt.Errorf("etkinliğin tür sürümü: %w", err)
	}
	if t.Parsed.Flags.SensitiveAttendance {
		return nil, nil
	}
	var out []id.ID
	add := func(u id.ID) {
		if u != (id.ID{}) && !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	for _, c := range t.Parsed.Components {
		if c.Kind != definition.CompCelebration {
			continue
		}
		on, err := enabled(ctx, q, ev.ID, c)
		if err != nil {
			return nil, err
		}
		if !on {
			continue
		}
		r, err := celebration.Load(ctx, q, ev.ID, t.Parsed, c)
		if err != nil {
			return nil, err
		}
		if !r.Announced || r.Hidden {
			continue
		}
		for _, w := range r.Winners {
			switch {
			case w.User != (id.ID{}):
				add(w.User)
			case w.Team != (id.ID{}):
				members, err := q.ListBadgeTeamMembers(ctx, store.ListBadgeTeamMembersParams{EventID: ev.ID.PG(), TeamID: w.Team.PG()})
				if err != nil {
					return nil, fmt.Errorf("kazanan takımın üyeleri: %w", err)
				}
				for _, m := range members {
					add(optID(m))
				}
			}
		}
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
