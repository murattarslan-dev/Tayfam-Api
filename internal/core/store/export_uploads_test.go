package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tests for 00018_data_export_uploads.sql and the export queries in
// db/core/queries/uploads.sql (issue #144, ADR-0021).

func newExport(t *testing.T, q *Queries, owner, request id.ID) id.ID {
	t.Helper()
	got, err := q.CreateExportUpload(context.Background(), CreateExportUploadParams{
		ID: id.New().PG(), UserID: owner.PG(), R2Key: r2Key(t), ClientRequestID: request.PG(),
	})
	if err != nil {
		t.Fatalf("dışa aktarma yuvası: %v", err)
	}
	return id.ID(got.Bytes)
}

func TestExportUploadLifecycle(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner, other := id.New(), id.New()
	request := id.New()
	exp := newExport(t, q, owner, request)

	// The same request opens nothing new; another user cannot see it.
	if _, err := q.CreateExportUpload(ctx, CreateExportUploadParams{
		ID: id.New().PG(), UserID: owner.PG(), R2Key: r2Key(t), ClientRequestID: request.PG(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim yeni kayıt açmamalı: %v", err)
	}
	if _, err := q.GetOwnExport(ctx, GetOwnExportParams{ID: exp.PG(), UserID: other.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("başkasının dışa aktarması bulunmamalı: %v", err)
	}
	// Image paths never see an export.
	if _, err := q.GetOwnUpload(ctx, GetOwnUploadParams{ID: exp.PG(), UserID: owner.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("görüntü yolu dışa aktarmayı görmemeli: %v", err)
	}

	// Not ready without a size; the size is written once.
	if n, err := q.MarkExportReady(ctx, MarkExportReadyParams{ID: exp.PG(), UserID: owner.PG(), Now: ts(time.Now()), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil || n != 0 {
		t.Fatalf("boyutsuz hazır olmamalı: %d %v", n, err)
	}
	if n, err := q.SetExportBytes(ctx, SetExportBytesParams{ID: exp.PG(), UserID: owner.PG(), ExportBytes: pgtype.Int8{Int64: 1000, Valid: true}}); err != nil || n != 1 {
		t.Fatalf("boyut: %d %v", n, err)
	}
	if n, err := q.SetExportBytes(ctx, SetExportBytesParams{ID: exp.PG(), UserID: owner.PG(), ExportBytes: pgtype.Int8{Int64: 1000, Valid: true}}); err != nil || n != 1 {
		t.Fatalf("aynı boyut tekrar yazılabilmeli: %d %v", n, err)
	}
	if n, err := q.SetExportBytes(ctx, SetExportBytesParams{ID: exp.PG(), UserID: owner.PG(), ExportBytes: pgtype.Int8{Int64: 2000, Valid: true}}); err != nil || n != 0 {
		t.Fatalf("başka boyut yazılmamalı: %d %v", n, err)
	}
	// The trigger refuses it even past the query's condition.
	_, err := adminOf(t, pool).Exec(ctx, "UPDATE uploads SET export_bytes = 3000 WHERE id = $1", exp.PG())
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "TF081" {
		t.Fatalf("boyut değişikliği trigger'la reddedilmeli: %v", err)
	}
	if n, err := q.MarkExportReady(ctx, MarkExportReadyParams{ID: exp.PG(), UserID: owner.PG(), Now: ts(time.Now()), ExpiresAt: ts(time.Now().Add(time.Hour))}); err != nil || n != 1 {
		t.Fatalf("hazır: %d %v", n, err)
	}
	if got := uploadStatus(t, pool, exp); got != "ready" {
		t.Fatalf("durum %q", got)
	}
	// A ready export is neither served as media nor counted as quota.
	rows, err := q.ListMediaForViewer(ctx, ListMediaForViewerParams{ViewerID: owner.PG(), Ids: []pgtype.UUID{exp.PG()}})
	if err != nil || len(rows) != 0 {
		t.Fatalf("dışa aktarma medya olarak sunulmamalı: %d %v", len(rows), err)
	}
	if used, err := q.UserStoredBytes(ctx, owner.PG()); err != nil || used != 0 {
		t.Fatalf("kota: %d %v", used, err)
	}
	if n, err := q.CountUploadsSince(ctx, CountUploadsSinceParams{UserID: owner.PG(), Since: ts(time.Now().Add(-time.Hour))}); err != nil || n != 0 {
		t.Fatalf("sıklık: %d %v", n, err)
	}

	// One active export per user: a second slot is refused until the old
	// one is released.
	if _, err := q.CreateExportUpload(ctx, CreateExportUploadParams{
		ID: id.New().PG(), UserID: owner.PG(), R2Key: r2Key(t), ClientRequestID: id.New().PG(),
	}); constraintOf(err) != "uploads_one_export_key" {
		t.Fatalf("ikinci etkin dışa aktarma reddedilmeli: %v", err)
	}
	next := id.New()
	if n, err := q.ReleaseOtherExports(ctx, ReleaseOtherExportsParams{UserID: owner.PG(), ClientRequestID: next.PG()}); err != nil || n != 1 {
		t.Fatalf("eski yuva: %d %v", n, err)
	}
	newExport(t, q, owner, next)
	if got := uploadStatus(t, pool, exp); got != "deleting" {
		t.Fatalf("eski dışa aktarma silinmeli: %q", got)
	}
}

func TestExportUploadExpiryAndConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	exp := newExport(t, q, owner, id.New())
	if n, err := q.SetExportBytes(ctx, SetExportBytesParams{ID: exp.PG(), UserID: owner.PG(), ExportBytes: pgtype.Int8{Int64: 100, Valid: true}}); err != nil || n != 1 {
		t.Fatalf("boyut: %d %v", n, err)
	}
	now := time.Now()
	if n, err := q.MarkExportReady(ctx, MarkExportReadyParams{ID: exp.PG(), UserID: owner.PG(), Now: ts(now.Add(-8 * 24 * time.Hour)), ExpiresAt: ts(now.Add(-time.Hour))}); err != nil || n != 1 {
		t.Fatalf("hazır: %d %v", n, err)
	}
	// An unattached image older than 7 days and the expired export both
	// go; an unexpired export would not (attachment does not apply).
	ids, err := q.ExpireUploads(ctx, ExpireUploadsParams{
		PendingBefore: ts(now.Add(-24 * time.Hour)), RejectedBefore: ts(now.Add(-24 * time.Hour)),
		UnattachedBefore: ts(now.Add(-7 * 24 * time.Hour)), Now: ts(now), N: 10,
	})
	if err != nil || len(ids) != 1 || id.ID(ids[0].Bytes) != exp {
		t.Fatalf("süresi dolan dışa aktarma: %v %v", ids, err)
	}
	del, err := q.ListDeletingUploads(ctx, 10)
	if err != nil || len(del) != 1 || del[0].Purpose != "data_export" {
		t.Fatalf("silme kuyruğu: %v %v", del, err)
	}

	admin := adminOf(t, pool)
	for name, stmt := range map[string]string{
		"dışa aktarmada varyant": `INSERT INTO uploads (id, user_id, purpose, r2_key, preview_type, preview_bytes, full_type, full_bytes, client_request_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), 'data_export', md5(random()::text), 'image/jpeg', 1, 'image/jpeg', 1, gen_random_uuid())`,
		"dışa aktarmada tayfa": `INSERT INTO uploads (id, user_id, tayfa_id, purpose, r2_key, client_request_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), 'data_export', md5(random()::text), gen_random_uuid())`,
		"16 MiB üstü": `INSERT INTO uploads (id, user_id, purpose, r2_key, export_bytes, client_request_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), 'data_export', md5(random()::text), 16777246, gen_random_uuid())`,
		"süresiz hazır": `INSERT INTO uploads (id, user_id, purpose, r2_key, export_bytes, status, ready_at, client_request_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), 'data_export', md5(random()::text), 100, 'ready', now(), gen_random_uuid())`,
		"görüntüde süre": `INSERT INTO uploads (id, user_id, purpose, r2_key, preview_type, preview_bytes, full_type, full_bytes, expires_at, client_request_id)
			VALUES (gen_random_uuid(), gen_random_uuid(), 'avatar', md5(random()::text), 'image/jpeg', 1, 'image/jpeg', 1, now(), gen_random_uuid())`,
	} {
		if _, err := admin.Exec(ctx, stmt); constraintOf(err) == "" {
			t.Errorf("%s reddedilmeli: %v", name, err)
		}
	}
}
