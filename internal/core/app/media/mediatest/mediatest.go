// Package mediatest creates READY uploads directly in core's database for
// the tests of the content that attaches them (tayfa and event covers,
// catch photos, avatars), without object storage.
package mediatest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Ready opens an upload of owner for purpose (tayfa zero for avatars) and
// takes it through the processing queue to READY.
func Ready(t testing.TB, pool *pgxpool.Pool, owner id.ID, purpose string, tayfa id.ID) id.ID {
	t.Helper()
	ctx := context.Background()
	q := store.New(pool)
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	var tp pgtype.UUID
	if tayfa != (id.ID{}) {
		tp = tayfa.PG()
	}
	u := id.New()
	if _, err := q.CreateUpload(ctx, store.CreateUploadParams{
		ID: u.PG(), UserID: owner.PG(), TayfaID: tp, Purpose: purpose, ContentType: "image/jpeg",
		SizeBytes: 1000, R2Key: hex.EncodeToString(b[:]), ClientRequestID: id.New().PG(),
	}); err != nil {
		t.Fatalf("yükleme: %v", err)
	}
	ts := func(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
	// Far in the past, so this upload is the one leased next.
	if _, err := q.MarkUploadReceived(ctx, store.MarkUploadReceivedParams{ID: u.PG(), Now: ts(time.Unix(0, 0))}); err != nil {
		t.Fatal(err)
	}
	l, err := q.LeaseUpload(ctx, store.LeaseUploadParams{Now: ts(time.Now()), LeaseUntil: ts(time.Now().Add(time.Minute))})
	if err != nil || id.ID(l.ID.Bytes) != u {
		t.Fatalf("kiralama: %v", err)
	}
	if n, err := q.FinishUpload(ctx, store.FinishUploadParams{
		ID: u.PG(), Attempts: l.Attempts, OutputType: pgtype.Text{String: "image/jpeg", Valid: true},
		StoredBytes: pgtype.Int8{Int64: 100, Valid: true}, Width: pgtype.Int4{Int32: 10, Valid: true},
		Height: pgtype.Int4{Int32: 10, Valid: true}, Now: ts(time.Now()),
	}); err != nil || n != 1 {
		t.Fatalf("bitirme: %v", err)
	}
	return u
}

// Status returns the upload's status ("" when the row is gone).
func Status(t testing.TB, pool *pgxpool.Pool, u id.ID) string {
	t.Helper()
	var s string
	err := pool.QueryRow(context.Background(), "SELECT status FROM uploads WHERE id = $1", u.PG()).Scan(&s)
	if err != nil {
		return ""
	}
	return s
}
