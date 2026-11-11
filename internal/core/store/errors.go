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
	// CodePollVote: the poll_votes trigger refused a vote (poll missing,
	// deleted or closed, voter not an unmuted active member, or a second
	// vote in a single-choice poll). Its message names neither the voter nor
	// the option.
	CodePollVote = "TF070"
	// CodePollFrozen: a change the poll triggers forbid (fewer than two
	// options, identity fields, the anonymous or multiple-choice setting
	// after the first vote, an option edit).
	CodePollFrozen = "TF071"
	// CodeMediaRef: content was pointed at an upload that is not a READY
	// upload of the right purpose attached to that content (uploads_check_ref,
	// issue #135).
	CodeMediaRef = "TF080"
	// CodeUniqueViolation is Postgres' unique_violation.
	CodeUniqueViolation = "23505"
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

// Constraint returns the constraint a Postgres error names, or "".
func Constraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
