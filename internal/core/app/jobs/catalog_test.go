package jobs

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// TestPublishCatalog: the job publishes the catalog once per version
// (issue #280, ADR-0042 §5); while another pod holds the lock it does
// nothing.
func TestPublishCatalog(t *testing.T) {
	e := setup(t)
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	ok, err := store.New(tx).TryJobLock(ctx, "tayfam.core.job."+JobCatalogPublish)
	must(t, err)
	if !ok {
		t.Fatal("kilit alınamadı")
	}
	run(t, "kilitliyken", e.at(t, 0).PublishCatalog, 0)
	must(t, tx.Rollback(ctx))

	run(t, "ilk çalışma", e.at(t, 0).PublishCatalog, 1)
	run(t, "sürüm değişmeden", e.at(t, 0).PublishCatalog, 0)
	if _, err := e.admin.Exec(ctx, `UPDATE catalog_meta SET catalog_version = catalog_version + 1`); err != nil {
		t.Fatal(err)
	}
	run(t, "yeni sürüm", e.at(t, 0).PublishCatalog, 1)
	if n := len(e.events(t, catalog.EventChanged)); n != 2 {
		t.Fatalf("catalog.changed = %d", n)
	}
}
