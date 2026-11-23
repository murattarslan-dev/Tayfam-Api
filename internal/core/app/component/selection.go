package component

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// AuditConfigured is the audit action of a changed selection. The record
// names the component and which parts changed, never more.
const AuditConfigured = "event.component_configured"

// Selection is the organizer's selection for one component (ADR-0037 §2).
type Selection struct {
	// Key is the component's key in the event's type version.
	Key string
	// Enabled switches an optional component on or off; nil leaves it as
	// it is. A component that is not optional accepts only true.
	Enabled *bool
	// Choices maps a parameter the definition leaves to the organizer to
	// the chosen option, as text ("swiss", "3"). Parameters left out keep
	// their value.
	Choices map[string]string
}

// Choice is a parameter the definition leaves to the organizer.
type Choice struct {
	Param string
	// Value is the option in force, as text: the organizer's choice or
	// the definition's default.
	Value string
	// Options are the definition's options, in its order.
	Options []string
}

// State is one component of an event as it holds for the event.
type State struct {
	Key  string
	Kind string
	// Optional: the organizer may switch the component.
	Optional bool
	Enabled  bool
	// Choices lists every parameter left to the organizer, by name.
	Choices []Choice
}

// States is the component setup of an event.
type States struct {
	// Components follows the order of the type version's definition.
	Components []State
	// Locked: the selections can no longer change (the event started, was
	// cancelled, or its tayfa is read-only).
	Locked bool
	// CallerCanManage: the caller may change the selections while they are
	// not locked.
	CallerCanManage bool
}

// optionText is an option's text form: the string itself or the integer's
// digits.
func optionText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

// choiceOf finds the option of p with this text form.
func choiceOf(p definition.Param, text string) (any, bool) {
	for _, c := range p.Choices {
		if optionText(c) == text {
			return c, true
		}
	}
	return nil, false
}

// choosable returns the names of the parameters comp leaves to the
// organizer, sorted.
func choosable(comp definition.Component) []string {
	var names []string
	for name, p := range comp.Params {
		if len(p.Choices) > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func stateOf(comp definition.Component, st stored, has bool) State {
	out := State{Key: comp.Key, Kind: comp.Kind, Optional: comp.Optional != nil, Enabled: defaultEnabled(comp), Choices: []Choice{}}
	if has {
		out.Enabled = st.enabled
	}
	for _, name := range choosable(comp) {
		p := comp.Params[name]
		c := Choice{Param: name, Value: optionText(p.Value), Options: make([]string, 0, len(p.Choices))}
		if v, ok := st.chosen[name]; ok {
			c.Value = optionText(v)
		}
		for _, o := range p.Choices {
			c.Options = append(c.Options, optionText(o))
		}
		out.Choices = append(out.Choices, c)
	}
	return out
}

// states builds the setup of a loaded event.
func (b *base) states(ctx context.Context, q *store.Queries) (States, error) {
	event := id.ID(b.ev.ID.Bytes)
	rows, err := q.ListEventComponents(ctx, event.PG())
	if err != nil {
		return States{}, fmt.Errorf("bileşen seçimleri: %w", err)
	}
	byKey := make(map[string]store.ListEventComponentsRow, len(rows))
	for _, r := range rows {
		byKey[r.ComponentKey] = r
	}
	out := States{
		Components:      make([]State, 0, len(b.et.Parsed.Components)),
		Locked:          b.selectable() != nil,
		CallerCanManage: b.manages(authz.EventComponentManageOwn, authz.EventComponentManageAny) == nil,
	}
	for _, comp := range b.et.Parsed.Components {
		var st stored
		r, has := byKey[comp.Key]
		if has {
			if st, err = decodeStored(comp, r.Enabled, r.Params); err != nil {
				return States{}, err
			}
		}
		out.Components = append(out.Components, stateOf(comp, st, has))
	}
	return out, nil
}

// selectable refuses a change of the selections: a read-only tayfa, a
// cancelled event, and an event that has started (ADR-0037 §2).
func (b *base) selectable() error {
	switch {
	case access.ReadOnly(b.tayfa):
		return apperr.ErrTayfaReadOnly
	case b.ev.Status == statusCancelled:
		return apperr.ErrEventCancelled
	case !b.now.Before(b.ev.StartsAt.Time):
		return apperr.ErrComponentLocked
	}
	return nil
}

// List returns the component setup of an event for a member who may read
// its components: every component of the event's type version with its
// state for this event.
func (s *Service) List(ctx context.Context, user, tayfa, event id.ID) (States, error) {
	b, err := s.load(ctx, s.q, user, tayfa, event, false)
	if err != nil {
		return States{}, err
	}
	if err := b.authorize(authz.EventComponentView); err != nil {
		return States{}, err
	}
	return b.states(ctx, s.q)
}

// merged returns the row to write for sel on top of what the component
// has: the complete state, checked against the definition. The error names
// the request field ("enabled", "choices"), never a value.
func merged(comp definition.Component, cur stored, has bool, sel Selection) (stored, error) {
	out := stored{enabled: defaultEnabled(comp), chosen: map[string]any{}}
	if has {
		out.enabled = cur.enabled
		maps.Copy(out.chosen, cur.chosen)
	}
	if sel.Enabled != nil {
		if !*sel.Enabled && comp.Optional == nil {
			return stored{}, apperr.Validation("enabled")
		}
		out.enabled = *sel.Enabled
	}
	if len(sel.Choices) > len(comp.Params) {
		return stored{}, apperr.Validation("choices")
	}
	for name, text := range sel.Choices {
		p, ok := comp.Params[name]
		if !ok || len(p.Choices) == 0 {
			return stored{}, apperr.Validation("choices")
		}
		v, ok := choiceOf(p, text)
		if !ok {
			return stored{}, apperr.Validation("choices")
		}
		out.chosen[name] = v
	}
	return out, nil
}

// write stores a component's selections.
func write(ctx context.Context, q *store.Queries, event id.ID, key string, st stored) error {
	params, err := json.Marshal(st.chosen)
	if err != nil {
		return fmt.Errorf("bileşen seçimi: %w", err)
	}
	if err := q.SetEventComponent(ctx, store.SetEventComponentParams{
		EventID: event.PG(), ComponentKey: key, Enabled: st.enabled, Params: params,
	}); err != nil {
		return fmt.Errorf("bileşen seçimi: %w", err)
	}
	return nil
}

// Set changes the organizer's selection for one component and returns the
// event's setup. Only a manager of the event may, and only before the
// event starts (apperr.ErrComponentLocked). The change applies to this
// event only, also when it belongs to a series.
func (s *Service) Set(ctx context.Context, user, tayfa, event id.ID, sel Selection) (States, error) {
	if !definition.ValidKey(sel.Key) {
		return States{}, apperr.Validation("component_key")
	}
	var out States
	err := txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		b, err := s.load(ctx, q, user, tayfa, event, true)
		if err != nil {
			return err
		}
		if err := b.manages(authz.EventComponentManageOwn, authz.EventComponentManageAny); err != nil {
			return err
		}
		comp, ok := b.et.Parsed.Component(sel.Key)
		if !ok {
			return apperr.ErrComponentNotFound
		}
		if err := b.selectable(); err != nil {
			return err
		}
		cur, has, err := storedOf(ctx, q, event, comp)
		if err != nil {
			return err
		}
		next, err := merged(comp, cur, has, sel)
		if err != nil {
			return err
		}
		if err := write(ctx, q, event, comp.Key, next); err != nil {
			return err
		}
		if out, err = b.states(ctx, q); err != nil {
			return err
		}
		var parts []string
		if sel.Enabled != nil {
			parts = append(parts, "enabled")
		}
		if len(sel.Choices) > 0 {
			parts = append(parts, "choices")
		}
		if err := txn.Audit(ctx, q, tayfa, user, AuditConfigured, txn.Target{Type: "event", ID: event},
			map[string]any{"component_key": comp.Key, "fields": parts}); err != nil {
			return err
		}
		return changed(ctx, q, tayfa, user, event, comp.Key)
	})
	if err != nil {
		return States{}, err
	}
	return out, nil
}

// Apply writes the selections a new event is created with, inside the
// transaction that creates it (package event). They are checked against
// the type version the event is bound to; the error names the request
// field ("components"). The caller has authorized the creation; nothing
// is locked yet for a new event.
func Apply(ctx context.Context, q *store.Queries, et catalog.EventType, event id.ID, sels []Selection) error {
	if len(sels) > definition.MaxComponents {
		return apperr.Validation("components")
	}
	seen := make(map[string]bool, len(sels))
	for _, sel := range sels {
		comp, ok := et.Parsed.Component(sel.Key)
		if !ok || seen[sel.Key] {
			return apperr.Validation("components")
		}
		seen[sel.Key] = true
		st, err := merged(comp, stored{}, false, sel)
		if err != nil {
			return apperr.Validation("components")
		}
		if err := write(ctx, q, event, comp.Key, st); err != nil {
			return err
		}
	}
	return nil
}
