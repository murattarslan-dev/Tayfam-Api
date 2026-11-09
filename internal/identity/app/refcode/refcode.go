// Package refcode makes and reads users' referral codes such as TYF-8K2M4Q
// (data model v14 "Arkadaşlık ve Davet (v11)"). A code has six characters
// from an alphabet without easily confused characters (0/O, 1/I), the same
// as the users_ref_code_format CHECK.
package refcode

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// alphabet has 32 characters, so a random byte maps to it without bias.
const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

const prefix = "TYF-"

// format is the users_ref_code_format CHECK.
var format = regexp.MustCompile(`^TYF-[2-9A-HJ-NP-Z]{6}$`)

// ErrNotAssigned: the user has no code to replace (not registered, deleted).
var ErrNotAssigned = errors.New("referans kodu yok")

// New returns a random code.
func New() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	out := []byte(prefix)
	for _, x := range b {
		out = append(out, alphabet[int(x)%len(alphabet)])
	}
	return string(out)
}

// Normalize reads a code as a person types or a link carries it: spaces
// around it are dropped, letters are upper-cased and the TYF- prefix is
// optional. ok is false for anything that cannot be a code.
func Normalize(s string) (string, bool) {
	if len(s) > 32 {
		return "", false
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, prefix) {
		s = prefix + s
	}
	if !format.MatchString(s) {
		return "", false
	}
	return s, true
}

// Assign gives user a new code inside tx: the first one at registration
// (rotate false) or a replacement (rotate true), after which the old code
// finds nobody. A clash with an existing code is retried inside a
// savepoint.
func Assign(ctx context.Context, tx pgx.Tx, user id.ID, rotate bool) (string, error) {
	for range 5 {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", fmt.Errorf("savepoint: %w", err)
		}
		code := New()
		q := store.New(sp)
		var n int64
		if rotate {
			n, err = q.RotateRefCode(ctx, store.RotateRefCodeParams{ID: user.PG(), RefCode: pgtype.Text{String: code, Valid: true}})
		} else {
			n, err = q.SetRefCode(ctx, store.SetRefCodeParams{ID: user.PG(), RefCode: pgtype.Text{String: code, Valid: true}})
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = sp.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return "", fmt.Errorf("referans kodu: %w", err)
		}
		if err := sp.Commit(ctx); err != nil {
			return "", fmt.Errorf("savepoint: %w", err)
		}
		if n != 1 {
			if rotate {
				return "", ErrNotAssigned
			}
			// Registration gives the code once; an existing one stays.
			return "", nil
		}
		return code, nil
	}
	return "", errors.New("referans kodu üretilemedi")
}
