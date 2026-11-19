// Package route is the route component (kind "route", issue #239;
// ADR-0025 §4): the line an event follows on the map, as an ordered list
// of points. It replaces the Faz 1 convoy route for every type that has
// the component.
//
// The route is content the organizer shares on purpose (ADR-0008), not a
// device location: it is stored, but its points never reach a log, an
// audit record or an outbox event.
//
// Who may do what (the framework's actions, package component):
//
//	read    every active member who may see the event's components
//	write   managers: the organizer, and moderators and admins as for
//	        every event; until the write window closes
package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// Kind is the component kind this package serves.
const Kind = definition.CompRoute

// A route has no points (no route) or MinPoints to MaxPoints of them: the
// is_valid_route CHECK of event_routes.
const (
	MinPoints = 2
	MaxPoints = 500
)

// Audit actions of route changes. The records name the component only,
// never a point.
const (
	AuditSet     = "event.route_set"
	AuditRemoved = "event.route_removed"
)

// Point is a point in WGS 84 degrees.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Valid reports whether p is a finite point with lat -90..90 and
// lng -180..180.
func (p Point) Valid() bool {
	return !math.IsNaN(p.Lat) && !math.IsNaN(p.Lng) &&
		p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180
}

// Route is the route of one component of an event.
type Route struct {
	// Points are in order; empty without a route.
	Points []Point
	// UpdatedAt is when the route was last set; zero without a route.
	UpdatedAt time.Time
	// CallerCanEdit reports whether the caller may change the route now.
	CallerCanEdit bool
}

// Service is the route component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// canEdit reports whether Set would be allowed for the caller now, apart
// from the request itself.
func canEdit(sc *component.Scope) bool {
	return sc.IsManager() && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// Get returns the route; without one its points are empty. Refusals are
// those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Route, error) {
	out := Route{Points: []Point{}}
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		out.CallerCanEdit = canEdit(sc)
		r, err := sc.Q().GetEventRoute(ctx, store.GetEventRouteParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("rota: %w", err)
		}
		pts, err := decode(r.Points)
		if err != nil {
			return err
		}
		out.Points, out.UpdatedAt = pts, r.UpdatedAt.Time.UTC()
		return nil
	})
	if err != nil {
		return Route{}, err
	}
	return out, nil
}

// Set replaces the route with points, or removes it when points is empty.
// Managers only. Refused: one point, more than MaxPoints or an invalid
// point (validation error on "points"), and the refusals of
// component.Service.Change. Setting the route that is already there and
// removing one that is not change nothing and are not errors.
func (s *Service) Set(ctx context.Context, ref component.Ref, points []Point) (Route, error) {
	if n := len(points); n != 0 && (n < MinPoints || n > MaxPoints) {
		return Route{}, apperr.Validation("points")
	}
	for _, p := range points {
		if !p.Valid() {
			return Route{}, apperr.Validation("points")
		}
	}
	out := Route{Points: []Point{}}
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		out.CallerCanEdit = canEdit(sc)
		if len(points) == 0 {
			n, err := sc.Q().DeleteEventRoute(ctx, store.DeleteEventRouteParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
			if err != nil {
				return fmt.Errorf("rotayı kaldırma: %w", err)
			}
			if n == 0 {
				return nil
			}
			sc.Changed()
			return sc.Audit(ctx, AuditRemoved, nil)
		}
		// The same route again (a retried request) changes nothing.
		prev, err := sc.Q().GetEventRoute(ctx, store.GetEventRouteParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("rota: %w", err)
		default:
			if old, err := decode(prev.Points); err == nil && slices.Equal(old, points) {
				out.Points, out.UpdatedAt = old, prev.UpdatedAt.Time.UTC()
				return nil
			}
		}
		raw, err := json.Marshal(points)
		if err != nil {
			return fmt.Errorf("rota: %w", err)
		}
		if err := sc.Q().SetEventRoute(ctx, store.SetEventRouteParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, Points: raw,
			UpdatedAt: pgtype.Timestamptz{Time: sc.Now, Valid: true},
		}); err != nil {
			return fmt.Errorf("rotayı yazma: %w", err)
		}
		out.Points, out.UpdatedAt = append(out.Points, points...), sc.Now
		sc.Changed()
		return sc.Audit(ctx, AuditSet, nil)
	})
	if err != nil {
		return Route{}, err
	}
	return out, nil
}

// decode reads stored points and checks them again (ADR-0011): a stored
// route that breaks the rules is an internal error, not something to show.
func decode(raw []byte) ([]Point, error) {
	var pts []Point
	if err := json.Unmarshal(raw, &pts); err != nil {
		return nil, fmt.Errorf("rota okunamadı: %w", err)
	}
	if len(pts) < MinPoints || len(pts) > MaxPoints {
		return nil, errors.New("rota: kayıtlı nokta sayısı sınırların dışında")
	}
	for _, p := range pts {
		if !p.Valid() {
			return nil, errors.New("rota: kayıtlı nokta geçersiz")
		}
	}
	return pts, nil
}
