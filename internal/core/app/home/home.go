package home

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Audit action and outbox event of a layout change. They carry the keys
// of the widgets that are on, never their settings (a countdown's title).
const (
	AuditUpdated = "tayfa.home_updated"
	// EventUpdated is the tayfa's own change event, with "home_widgets"
	// among the fields.
	EventUpdated = "tayfa.updated"
)

// FieldWidgets is the validation field of the widget list.
const FieldWidgets = "widgets"

// Item is one widget of a tayfa's layout.
type Item struct {
	Key    string
	Kind   Kind
	Module string
	// Enabled is the admin's switch; a widget whose module is off is not
	// shown even when enabled.
	Enabled bool
	// Position is the widget's place, from 0, after "Sıradaki".
	Position int32
	// Config is the widget's settings, with the defaults of the widgets
	// that may go without (the statistics card, the champions card).
	Config Config
}

// Input is one widget of a new layout.
type Input struct {
	Key     string
	Enabled bool
	Config  Config
}

// Service is the home page layout and the display widgets' data, built on
// the component framework (component.Service.Tayfa).
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// layout reads the tayfa's whole layout: the widgets with a row in their
// order, then every other widget of the list, off. Rows of widgets the code
// does not know are skipped. Positions are renumbered from 0.
func layout(ctx context.Context, q *store.Queries, tayfa id.ID) ([]Item, error) {
	rows, err := q.ListHomeWidgets(ctx, tayfa.PG())
	if err != nil {
		return nil, fmt.Errorf("ana sayfa düzeni: %w", err)
	}
	out := make([]Item, 0, len(Widgets()))
	seen := map[string]bool{}
	for _, r := range rows {
		w, ok := Lookup(r.WidgetKey)
		if !ok || seen[w.Key] {
			continue
		}
		seen[w.Key] = true
		c, valid := decodeConfig(w, r.Config)
		enabled := r.Enabled
		if !valid || (w.NeedsConfig && c.empty()) {
			// Settings that no longer pass cannot be shown.
			c, enabled = Config{}, false
		}
		out = append(out, Item{Key: w.Key, Kind: w.Kind, Module: w.Module, Enabled: enabled, Config: withDefaults(w.Key, c)})
	}
	for _, w := range Widgets() {
		if !seen[w.Key] {
			out = append(out, Item{Key: w.Key, Kind: w.Kind, Module: w.Module, Config: withDefaults(w.Key, Config{})})
		}
	}
	for i := range out {
		out[i].Position = int32(i) //nolint:gosec // bounded by the widget list
	}
	return out, nil
}

// Shown returns the widgets the tayfa's home page shows, in order: on,
// their module on (a module without a row is off), a countdown not past
// its moment. Positions are renumbered from 0. For GET /v1/tayfas/{id}
// (package tayfa); whether the reader may see the tayfa is the caller's.
func Shown(ctx context.Context, q *store.Queries, tayfa id.ID, now time.Time) ([]Item, error) {
	items, err := layout(ctx, q, tayfa)
	if err != nil {
		return nil, err
	}
	mods, err := q.ListTayfaModules(ctx, tayfa.PG())
	if err != nil {
		return nil, fmt.Errorf("modüller: %w", err)
	}
	on := map[string]bool{}
	for _, m := range mods {
		on[m.Module] = m.Enabled
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if !it.Enabled || (it.Module != "" && !on[it.Module]) {
			continue
		}
		if it.Key == KeyCountdown && it.Config.Countdown != nil && !it.Config.Countdown.At.After(now) {
			continue
		}
		it.Position = int32(len(out)) //nolint:gosec // bounded by the widget list
		out = append(out, it)
	}
	return out, nil
}

// AddDefaults switches on, for a tayfa, the widgets that have no row yet:
// with sections the former fixed sections (a new or converted tayfa), and
// the default widgets of interests (catalog_interests.default_widgets) in
// catalog order. A row that exists is left alone: a widget the admin
// switched off stays off. Keys the code does not know, and widgets that
// need settings, are skipped. Runs in the caller's transaction.
func AddDefaults(ctx context.Context, q *store.Queries, tayfa id.ID, interests []string, sections bool, now time.Time) error {
	var keys []string
	if sections {
		keys = append(keys, Sections()...)
	}
	if len(interests) > 0 {
		rows, err := q.ListInterestDefaultWidgets(ctx, interests)
		if err != nil {
			return fmt.Errorf("varsayılan bileşenler: %w", err)
		}
		for _, r := range rows {
			keys = append(keys, r.DefaultWidgets...)
		}
	}
	var done []string
	for _, k := range keys {
		w, ok := Lookup(k)
		if !ok || w.NeedsConfig || slices.Contains(done, k) {
			continue
		}
		done = append(done, k)
		if err := q.AddHomeWidgetDefault(ctx, store.AddHomeWidgetDefaultParams{TayfaID: tayfa.PG(), WidgetKey: k, Now: ts(now)}); err != nil {
			return fmt.Errorf("varsayılan bileşen: %w", err)
		}
	}
	return nil
}

// Layout returns the tayfa's whole layout, for the settings screen: every
// widget of the list, those off included. Open to every active member,
// muted ones included (authz.TayfaView); anyone else gets
// apperr.ErrTayfaNotFound.
func (s *Service) Layout(ctx context.Context, user, tayfa id.ID) ([]Item, error) {
	var out []Item
	err := s.fw.Tayfa(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		var err error
		out, err = layout(ctx, sc.Q(), tayfa)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// cleanInputs checks a new layout: every widget of the list exactly once
// (the whole list, ADR-0027 §3), each with settings it takes.
func cleanInputs(in []Input) ([]Input, error) {
	ws := Widgets()
	if len(in) != len(ws) {
		return nil, apperr.Validation(FieldWidgets)
	}
	out := make([]Input, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		w, ok := Lookup(v.Key)
		if !ok || seen[v.Key] {
			return nil, apperr.Validation(FieldWidgets)
		}
		seen[v.Key] = true
		c, err := cleanConfig(w, v.Enabled, v.Config)
		if err != nil {
			return nil, err
		}
		out = append(out, Input{Key: v.Key, Enabled: v.Enabled, Config: c})
	}
	return out, nil
}

// Set replaces the tayfa's layout with in, in its order: every widget of
// the list (Widgets) exactly once, whether on and with its settings. A
// widget whose module is off may be switched on; it shows once the module
// is on. Admins only (authz.TayfaHomeUpdate); the change is written to the
// audit log. Refused: anyone but an active member
// (apperr.ErrTayfaNotFound), another role (apperr.ErrPermissionDenied), a
// read-only tayfa, a list that is not the whole list or has a bad setting
// (validation error on widgets or config).
func (s *Service) Set(ctx context.Context, user, tayfa id.ID, in []Input) ([]Item, error) {
	clean, err := cleanInputs(in)
	if err != nil {
		return nil, err
	}
	var out []Item
	err = s.fw.Tayfa(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.TayfaHomeUpdate); err != nil {
			return err
		}
		q := sc.Q()
		enabled := []string{}
		for i, v := range clean {
			raw, err := encodeConfig(v.Key, v.Config)
			if err != nil {
				return fmt.Errorf("bileşen ayarı: %w", err)
			}
			if err := q.SetHomeWidget(ctx, store.SetHomeWidgetParams{
				TayfaID: tayfa.PG(), WidgetKey: v.Key, Enabled: v.Enabled, Position: int32(i), //nolint:gosec // bounded by the widget list
				Config: raw, Now: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("ana sayfa düzeni: %w", err)
			}
			if v.Enabled {
				enabled = append(enabled, v.Key)
			}
		}
		target := txn.Target{Type: "tayfa", ID: tayfa}
		if err := sc.Audit(ctx, AuditUpdated, target, map[string]any{"enabled": enabled}); err != nil {
			return err
		}
		if err := sc.Event(ctx, EventUpdated, target, map[string]any{"fields": []string{"home_widgets"}}); err != nil {
			return err
		}
		items, err := layout(ctx, q, tayfa)
		out = items
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
