package event

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The event side of joining by QR (issue #264, ADR-0038 §3). The organizer
// (or an admin of the tayfa) shows a short-lived token as a QR; package
// joinqr redeems it, with the checks and writes here running inside its
// transaction. The token is returned once by CreateJoinQR and otherwise
// exists only as its SHA-256 hash: it never reaches a log, an audit record,
// an event or an error.

const (
	// JoinQRTTL is a join token's lifetime; the client asks for a new one
	// before it ends (event_join_qr_tokens_ttl CHECK).
	JoinQRTTL = 2 * time.Minute
	// MaxQRJoins bounds the joins by QR of one event
	// (event_join_qr_counts_joins CHECK).
	MaxQRJoins = 30
	// joinQRTokenLen is 128 random bits: 22 characters in base64url
	// without padding.
	joinQRTokenLen = 16
)

// JoinQR is a new join token.
type JoinQR struct {
	// Token is returned only here; core keeps its hash.
	Token     string
	ExpiresAt time.Time
}

// CreateJoinQR creates a join token for an in-person event that has not
// ended: the organizer for their own event (EventJoinQRCreate), the admins
// for every event (EventJoinQRCreateAny). An event that already had
// MaxQRJoins joins by QR gets apperr.ErrJoinQRLimit. Earlier tokens stay
// valid until they expire.
func (s *Service) CreateJoinQR(ctx context.Context, user, tayfa, event id.ID) (JoinQR, error) {
	var out JoinQR
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, _ *batch) error {
		ev, err := getEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := c.manage(ev, authz.EventJoinQRCreate, authz.EventJoinQRCreateAny); err != nil {
			return err
		}
		if err := changeable(ev, c.now); err != nil {
			return err
		}
		// The QR stands for being there in person (ADR-0038 §3).
		if ev.Mode != ModeInPerson {
			return apperr.Validation("mode")
		}
		n, err := q.GetJoinQRCount(ctx, ev.ID)
		if err != nil {
			return fmt.Errorf("QR katılım sayısı: %w", err)
		}
		if n >= MaxQRJoins {
			return apperr.ErrJoinQRLimit
		}
		raw := make([]byte, joinQRTokenLen)
		if _, err := rand.Read(raw); err != nil {
			return fmt.Errorf("QR token: %w", err)
		}
		hash := sha256.Sum256(raw)
		expires := c.now.Add(JoinQRTTL)
		if err := q.CreateJoinQRToken(ctx, store.CreateJoinQRTokenParams{
			ID: id.New().PG(), EventID: ev.ID, CreatedBy: user.PG(), TokenHash: hash[:],
			CreatedAt: ts(c.now), ExpiresAt: ts(expires),
		}); err != nil {
			return fmt.Errorf("QR token: %w", err)
		}
		out = JoinQR{Token: base64.RawURLEncoding.EncodeToString(raw), ExpiresAt: expires}
		return nil
	})
	if err != nil {
		return JoinQR{}, err
	}
	return out, nil
}

// HashJoinQRToken checks the shape of a join token and returns its
// SHA-256; ok is false for a malformed token.
func HashJoinQRToken(token string) (hash []byte, ok bool) {
	if len(token) != base64.RawURLEncoding.EncodedLen(joinQRTokenLen) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != joinQRTokenLen {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}

// CheckJoinQRTx checks, inside the joining caller's transaction q, that the
// event of tayfa may be joined by QR now, and locks the tayfa row and then
// the event row (the order of every event write): the tayfa is live and
// not read-only, its events module is on, and the event is in person, not
// cancelled and not ended. Every refusal is apperr.ErrInvalidJoinQR.
func (s *Service) CheckJoinQRTx(ctx context.Context, q *store.Queries, tayfa, event id.ID) error {
	t, err := q.GetTayfaForUpdate(ctx, tayfa.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.ErrInvalidJoinQR
	case err != nil:
		return fmt.Errorf("tayfa: %w", err)
	}
	if access.ReadOnly(access.Tayfa(t)) {
		return apperr.ErrInvalidJoinQR
	}
	if err := access.RequireModule(ctx, q, tayfa, access.ModuleEvents); err != nil {
		if errors.Is(err, apperr.ErrModuleDisabled) {
			return apperr.ErrInvalidJoinQR
		}
		return err
	}
	ev, err := lockEvent(ctx, q, tayfa, event)
	if err != nil {
		if errors.Is(err, apperr.ErrEventNotFound) {
			return apperr.ErrInvalidJoinQR
		}
		return err
	}
	if changeable(ev, s.now()) != nil || ev.Mode != ModeInPerson {
		return apperr.ErrInvalidJoinQR
	}
	return nil
}

// AttendByQRTx answers going (waitlist when full) for user, an active
// member of tayfa who has just joined by QR or scanned it, inside the
// caller's transaction q after CheckJoinQRTx. With checkIn the attendance
// is also checked in (MethodQR) while the check-in window is open, if the
// answer is going and the member may check in. changed reports whether
// anything was written; the outbox events are written here.
func (s *Service) AttendByQRTx(ctx context.Context, q *store.Queries, user, tayfa, event id.ID, checkIn bool) (rsvp string, att Attendance, changed bool, err error) {
	c, err := s.load(ctx, q, user, tayfa, true)
	if err != nil {
		return "", Attendance{}, false, err
	}
	if err := c.authorize(authz.EventRSVP); err != nil {
		return "", Attendance{}, false, err
	}
	ev, err := lockEvent(ctx, q, tayfa, event)
	if err != nil {
		return "", Attendance{}, false, err
	}
	b := &batch{tayfa: tayfa, actor: user}
	rsvp, changed, err = setRSVPIn(ctx, q, c, b, ev, RSVPGoing)
	if err != nil {
		return "", Attendance{}, false, err
	}
	att = Attendance{Status: AttendanceUnknown}
	r, ok, err := rsvpRow(ctx, q, event, user)
	if err != nil {
		return "", Attendance{}, false, err
	}
	if ok {
		att = attendanceOf(r)
	}
	if checkIn && rsvp == RSVPGoing && att.Status != AttendanceAttended &&
		c.authorize(authz.EventCheckIn) == nil && checkInOpen(ev, c.now) == nil {
		if err := markAttended(ctx, q, c, b, ev, user, MethodQR, false); err != nil {
			return "", Attendance{}, false, err
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, c.now); err != nil {
			return "", Attendance{}, false, err
		}
		att, changed = Attendance{Status: AttendanceAttended, Method: MethodQR}, true
	}
	if err := flush(ctx, q, b); err != nil {
		return "", Attendance{}, false, err
	}
	return rsvp, att, changed, nil
}
