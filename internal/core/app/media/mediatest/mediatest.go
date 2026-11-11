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
// marks it checked (READY).
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
		ID: u.PG(), UserID: owner.PG(), TayfaID: tp, Purpose: purpose, R2Key: hex.EncodeToString(b[:]),
		PreviewType: "image/webp", PreviewBytes: 100, FullType: "image/jpeg", FullBytes: 1000, ClientRequestID: id.New().PG(),
	}); err != nil {
		t.Fatalf("yükleme: %v", err)
	}
	if n, err := q.MarkUploadReady(ctx, store.MarkUploadReadyParams{
		ID: u.PG(), Width: pgtype.Int4{Int32: 10, Valid: true}, Height: pgtype.Int4{Int32: 10, Valid: true},
		Now: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil || n != 1 {
		t.Fatalf("hazır: %v", err)
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
