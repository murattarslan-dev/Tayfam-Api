package catalog

import (
	"context"
	"fmt"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventChanged is written to core's outbox when the catalog changes (issue
// #280, ADR-0042, ADR-0025 §10). core's relay delivers it to identity's
// CatalogCopyService (package catalogrelay), which keeps identity's copy of
// the interests that profiles are checked against.
//
// Payload (no personal data, no subject, no actor):
//
//	catalog_version  catalog_meta.catalog_version
//	interests        every interest of the catalog, retired ones included:
//	                 [{key, status (active|retired)}]
const EventChanged = "catalog.changed"

// ChangedInterest is one interest of a catalog.changed payload.
type ChangedInterest struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

// Changed is the payload of catalog.changed.
type Changed struct {
	CatalogVersion int64             `json:"catalog_version"`
	Interests      []ChangedInterest `json:"interests"`
}

// PublishChanged writes catalog.changed with the catalog as it is now. q
// must run in a transaction; call it as the last write of the transaction
// that changed the catalog and its catalog_version (outbox.Write's rule).
//
// The version is read before the interests: under READ COMMITTED a change
// committing in between can only make the content newer than its version,
// never older; the newer version's event follows.
func PublishChanged(ctx context.Context, q *store.Queries) error {
	version, err := q.GetCatalogVersion(ctx)
	if err != nil {
		return fmt.Errorf("katalog sürümü: %w", err)
	}
	rows, err := q.ListCatalogInterests(ctx)
	if err != nil {
		return fmt.Errorf("ilgi alanları: %w", err)
	}
	p := Changed{CatalogVersion: version, Interests: make([]ChangedInterest, 0, len(rows))}
	for _, r := range rows {
		p.Interests = append(p.Interests, ChangedInterest{Key: r.Key, Status: r.Status})
	}
	if err := txn.Event(ctx, q, EventChanged, id.ID{}, id.ID{}, txn.Target{}, map[string]any{
		"catalog_version": p.CatalogVersion, "interests": p.Interests,
	}); err != nil {
		return fmt.Errorf("katalog olayı: %w", err)
	}
	return nil
}

// PublishDue writes catalog.changed when catalog_meta.catalog_version is
// above the version of the last catalog.changed, and reports whether it
// did. The catalog-publish job calls it (package jobs): catalog migrations
// only change the catalog and its version, and the job publishes it. q must
// run in a transaction.
func PublishDue(ctx context.Context, q *store.Queries) (bool, error) {
	current, err := q.GetCatalogVersion(ctx)
	if err != nil {
		return false, fmt.Errorf("katalog sürümü: %w", err)
	}
	published, err := q.GetLastPublishedCatalogVersion(ctx)
	if err != nil {
		return false, fmt.Errorf("yayınlanan katalog sürümü: %w", err)
	}
	if current <= published {
		return false, nil
	}
	if err := PublishChanged(ctx, q); err != nil {
		return false, err
	}
	return true, nil
}
