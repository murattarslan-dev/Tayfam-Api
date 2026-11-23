package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// JobCatalogPublish publishes catalog.changed when the catalog's version is
// above the last published one (issue #280, ADR-0042 §5): catalog
// migrations only change the catalog and catalog_meta.catalog_version, and
// this job tells identity. A write path that publishes in its own
// transaction (the platform panel) is not repeated; a duplicate would be
// harmless anyway (identity ignores a version that is not newer).
const JobCatalogPublish = "catalog-publish"

// DefaultCatalogPublishInterval: also runs at start, so a migration's change
// reaches identity right after the deployment.
const DefaultCatalogPublishInterval = 5 * time.Minute

// PublishCatalog runs catalog.PublishDue under the job's lock and returns 1
// when it published, 0 otherwise.
func (s *Service) PublishCatalog(ctx context.Context) (int, error) {
	n, err := s.batch(ctx, JobCatalogPublish, func(q *store.Queries) (int, error) {
		published, err := catalog.PublishDue(ctx, q)
		if err != nil || !published {
			return 0, err
		}
		return 1, nil
	})
	if errors.Is(err, errLocked) {
		return 0, nil
	}
	return n, err
}
