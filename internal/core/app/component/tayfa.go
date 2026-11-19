package component

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Component data that belongs to the tayfa rather than to one event (the
// tayfa vocabulary, issue #244; ADR-0037 §4): the components fill it, the
// tayfa reads and manages it. Tayfa resolves the caller's standing; no
// module is required, since more than one module uses such data (a log of
// the events module, the library).

// TayfaScope is the caller's standing in a tayfa, handed to a tayfa-level
// function by Service.Tayfa.
type TayfaScope struct {
	User  id.ID
	Tayfa id.ID
	// Now is the time of the request, UTC.
	Now time.Time

	q        *store.Queries
	tayfa    access.Tayfa
	m        authz.Membership
	settings authz.Settings
}

// Q returns the queries of the scope: in a write's transaction, or the
// pool's for a read.
func (ts *TayfaScope) Q() *store.Queries { return ts.q }

// Authorize asks the permission table for the caller's action.
func (ts *TayfaScope) Authorize(a authz.Action) error {
	return access.AuthorizeIn(ts.m, a, ts.Now, ts.settings)
}

// ReadOnly reports whether the tayfa refuses writes (suspended, archived or
// awaiting deletion); a write's scope never sees one.
func (ts *TayfaScope) ReadOnly() bool { return access.ReadOnly(ts.tayfa) }

// Audit writes an audit record of the tayfa about target. meta holds IDs
// and classification only.
func (ts *TayfaScope) Audit(ctx context.Context, action string, target txn.Target, meta map[string]any) error {
	return txn.Audit(ctx, ts.q, ts.Tayfa, ts.User, action, target, meta)
}

// Event writes an outbox event of the tayfa; it must be the transaction's
// last write (txn.Event). The payload holds IDs and classification only.
func (ts *TayfaScope) Event(ctx context.Context, typ string, subject txn.Target, payload map[string]any) error {
	return txn.Event(ctx, ts.q, typ, ts.Tayfa, ts.User, subject, payload)
}

// Tayfa runs fn with the caller's standing in the tayfa: an active member
// (anyone else gets apperr.ErrTayfaNotFound). With write, fn runs in a
// transaction as the caller with the tayfa row locked, and a read-only
// tayfa is refused (apperr.ErrTayfaReadOnly); the caller's permission is
// fn's to ask (TayfaScope.Authorize).
func (s *Service) Tayfa(ctx context.Context, user, tayfa id.ID, write bool, fn func(ts *TayfaScope) error) error {
	run := func(q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, user, write)
		if err != nil {
			return err
		}
		if write && access.ReadOnly(t) {
			return apperr.ErrTayfaReadOnly
		}
		return fn(&TayfaScope{
			User: user, Tayfa: tayfa, Now: s.now(), q: q, tayfa: t, m: m, settings: access.Settings(t),
		})
	}
	if !write {
		return run(s.q)
	}
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error { return run(q) })
}
