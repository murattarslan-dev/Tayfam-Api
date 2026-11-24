package catalog

import (
	"context"
	"fmt"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// PanelType is an event type as the platform panel manages it (issue
// #262): its current version, retired or not, with its listing position.
type PanelType struct {
	EventType
	Position int32
}

// Panel is the catalog as the platform panel sees it: like Get, but with
// every event type, retired ones included.
type Panel struct {
	Version   int64
	Interests []Interest
	Types     []PanelType
	StudyTags []StudyTag
	Badges    []Badge
}

// ReadPanel reads the catalog for the platform panel inside the caller's
// transaction. Every row is checked as Get checks it (ADR-0011).
func ReadPanel(ctx context.Context, q *store.Queries) (Panel, error) {
	v, err := q.GetCatalogVersion(ctx)
	if err != nil {
		return Panel{}, fmt.Errorf("katalog sürümü: %w", err)
	}
	p := Panel{Version: v}
	if p.Interests, err = Interests(ctx, q); err != nil {
		return Panel{}, err
	}
	rows, err := q.AdminListCatalogEventTypes(ctx)
	if err != nil {
		return Panel{}, fmt.Errorf("türler: %w", err)
	}
	p.Types = make([]PanelType, 0, len(rows))
	for _, r := range rows {
		t, err := eventType(r.Key, r.Version, r.InterestKeys, r.Modes, r.Icon, r.Status, r.Labels, r.Definition)
		if err != nil {
			return Panel{}, err
		}
		p.Types = append(p.Types, PanelType{EventType: t, Position: r.Position})
	}
	if p.StudyTags, err = StudyTags(ctx, q); err != nil {
		return Panel{}, err
	}
	if p.Badges, err = Badges(ctx, q); err != nil {
		return Panel{}, err
	}
	return p, nil
}

// Interests reads every interest, retired ones included, by position, and
// checks each row.
func Interests(ctx context.Context, q *store.Queries) ([]Interest, error) {
	rows, err := q.ListCatalogInterests(ctx)
	if err != nil {
		return nil, fmt.Errorf("ilgi alanları: %w", err)
	}
	out := make([]Interest, 0, len(rows))
	for _, r := range rows {
		in := Interest{Key: r.Key, Position: r.Position, Icon: r.Icon, Status: r.Status, DefaultWidgets: r.DefaultWidgets}
		if in.Labels, err = entry(r.Key, r.Icon, r.Status, r.Labels); err != nil {
			return nil, fmt.Errorf("ilgi alanı %q: %w", r.Key, err)
		}
		for _, w := range r.DefaultWidgets {
			if !definition.ValidKey(w) {
				return nil, fmt.Errorf("ilgi alanı %q: geçersiz ana sayfa bileşeni", r.Key)
			}
		}
		out = append(out, in)
	}
	return out, nil
}

// StudyTags reads the study tag list, retired tags included, inside the
// caller's transaction (the platform panel checks the list it would
// publish with CheckStudyTags).
func StudyTags(ctx context.Context, q *store.Queries) ([]StudyTag, error) {
	return studyTags(ctx, q)
}
