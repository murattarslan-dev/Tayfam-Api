package tayfa

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
)

// joinCodeAlphabet is [2-9A-HJ-NP-Z]: 32 characters without 0/O and 1/I
// (tayfas_join_code_format).
const joinCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

const joinCodeLength = 8

// newJoinCode returns a random join code from crypto/rand. The alphabet has
// 32 characters, so taking 5 bits of each byte keeps every character equally
// likely.
func newJoinCode() string {
	var b [joinCodeLength]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	for i := range b {
		b[i] = joinCodeAlphabet[b[i]&31]
	}
	return string(b[:])
}

// withJoinCode runs fn with a fresh join code inside a savepoint and retries
// with a new code when the code is already taken.
func withJoinCode(ctx context.Context, tx pgx.Tx, fn func(q *store.Queries, code string) error) error {
	for range joinCodeAttempts {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("savepoint: %w", err)
		}
		err = fn(store.New(sp), newJoinCode())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "tayfas_join_code_key" {
			_ = sp.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return fmt.Errorf("savepoint: %w", err)
		}
		return nil
	}
	return errors.New("benzersiz katılım kodu üretilemedi")
}
