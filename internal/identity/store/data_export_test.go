package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tests for 00009_data_export.sql and db/identity/queries/data_export.sql
// (issue #144, ADR-0021).

func tsAt(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func TestDataExportRequests(t *testing.T) {
	_, owner, q := setupWithOwner(t)
	ctx := context.Background()
	user, other := newUser(t, q), newUser(t, q)
	now := time.Now()
	crid := id.New()
	r, err := q.CreateDataExportRequest(ctx, CreateDataExportRequestParams{
		ID: id.New().PG(), UserID: user.PG(), UploadID: id.New().PG(), ClientRequestID: crid.PG(), Now: tsAt(now),
	})
	if err != nil || r.Status != "pending" {
		t.Fatalf("talep: %+v %v", r, err)
	}
	// The same client request opens nothing new.
	if _, err := q.CreateDataExportRequest(ctx, CreateDataExportRequestParams{
		ID: id.New().PG(), UserID: user.PG(), ClientRequestID: crid.PG(), Now: tsAt(now),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	// One active request per user.
	if _, err := q.CreateDataExportRequest(ctx, CreateDataExportRequestParams{
		ID: id.New().PG(), UserID: user.PG(), ClientRequestID: id.New().PG(), Now: tsAt(now),
	}); constraintOf(err) != "data_export_requests_one_active_key" {
		t.Fatalf("ikinci etkin talep: %v", err)
	}
	// IDOR: another user does not find it.
	if _, err := q.GetOwnDataExport(ctx, GetOwnDataExportParams{ID: r.ID, UserID: other.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("başkasının talebi: %v", err)
	}
	if active, err := q.HasActiveDataExport(ctx, user.PG()); err != nil || !active {
		t.Fatalf("etkin: %v %v", active, err)
	}
	if n, err := q.MarkDataExportProcessing(ctx, MarkDataExportProcessingParams{ID: r.ID, Now: tsAt(now)}); err != nil || n != 1 {
		t.Fatalf("hazırlanıyor: %d %v", n, err)
	}
	wrapped := bytes.Repeat([]byte{1}, 61)
	if n, err := q.MarkDataExportReady(ctx, MarkDataExportReadyParams{
		ID: r.ID, WrappedKey: wrapped, SizeBytes: pgtype.Int8{Int64: 100, Valid: true}, Now: tsAt(now), ExpiresAt: tsAt(now.Add(time.Hour)),
	}); err != nil || n != 1 {
		t.Fatalf("hazır: %d %v", n, err)
	}
	last, err := q.LastCountedDataExport(ctx, user.PG())
	if err != nil || !last.Valid {
		t.Fatalf("sayılan son talep: %v %v", last, err)
	}
	if last, _ := q.LastCountedDataExport(ctx, other.PG()); last.Valid {
		t.Fatal("talebi olmayanın son talebi olmamalı")
	}

	for name, stmt := range map[string]string{
		"anahtarsız hazır": `INSERT INTO data_export_requests (id, user_id, status, upload_id, size_bytes, ready_at, expires_at, started_at, finished_at, client_request_id)
			VALUES (gen_random_uuid(), $1, 'ready', gen_random_uuid(), 100, now(), now(), now(), now(), gen_random_uuid())`,
		"bekleyende anahtar": `INSERT INTO data_export_requests (id, user_id, wrapped_key, client_request_id)
			VALUES (gen_random_uuid(), $1, decode(repeat('ab', 61), 'hex'), gen_random_uuid())`,
		"sebepsiz başarısız": `INSERT INTO data_export_requests (id, user_id, status, finished_at, client_request_id)
			VALUES (gen_random_uuid(), $1, 'failed', now(), gen_random_uuid())`,
		"bilinmeyen sebep": `INSERT INTO data_export_requests (id, user_id, status, failure_reason, finished_at, client_request_id)
			VALUES (gen_random_uuid(), $1, 'failed', 'x', now(), gen_random_uuid())`,
		"16 MiB üstü": `INSERT INTO data_export_requests (id, user_id, size_bytes, client_request_id)
			VALUES (gen_random_uuid(), $1, 16777246, gen_random_uuid())`,
		"dosyalı süresi dolmuş": `INSERT INTO data_export_requests (id, user_id, status, upload_id, ready_at, started_at, finished_at, client_request_id)
			VALUES (gen_random_uuid(), $1, 'expired', gen_random_uuid(), now(), now(), now(), gen_random_uuid())`,
	} {
		if _, err := owner.Exec(ctx, stmt, other.PG()); constraintOf(err) == "" {
			t.Errorf("%s reddedilmeli: %v", name, err)
		}
	}
}
