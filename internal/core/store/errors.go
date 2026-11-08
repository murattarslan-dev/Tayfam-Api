package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATE codes raised by core's triggers (db/core/migrations). The app
// layer maps them to API errors; the database is the second line of defence
// behind the permission table (ADR-0004).
const (
	// CodeNoActor: a membership change without app.actor_id, or a system job
	// attempting something outside its narrow allowance.
	CodeNoActor = "TF001"
	// CodeRoleHierarchy: the actor may not make this change (target role not
	// below the actor's, assigning a higher role, a forbidden transition).
	CodeRoleHierarchy = "TF002"
	// CodeLastAdmin: the change would leave the tayfa without an active admin.
	CodeLastAdmin = "TF003"
	// CodeAppendOnly: an UPDATE, DELETE or TRUNCATE on audit_logs.
	CodeAppendOnly = "TF004"
)

// SystemJobAccountDeletion is the only system job the membership trigger
// recognises (identity's user.deleted event).
const SystemJobAccountDeletion = "account_deletion"

// SQLState returns the Postgres error code of err, or "".
func SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
