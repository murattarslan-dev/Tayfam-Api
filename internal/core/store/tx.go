package store

import "github.com/jackc/pgx/v5"

// Tx returns the transaction q runs in; ok is false when q runs on a pool
// or a plain connection. Outbox events can only be written inside a
// transaction (internal/platform/outbox.Write).
func (q *Queries) Tx() (tx pgx.Tx, ok bool) {
	tx, ok = q.db.(pgx.Tx)
	return tx, ok
}
