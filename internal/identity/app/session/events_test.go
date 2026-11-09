package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// revokedEvent is one stored session.revoked event.
type revokedEvent struct {
	user, session id.ID
}

// revokedEvents returns the session.revoked events in ID order. Every
// payload must be exactly {"session_id": "<uuid>"}: no reason, no device,
// no token, no e-mail (issue #92).
func (e *env) revokedEvents(t *testing.T) []revokedEvent {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT subject_type, subject_id, actor_id IS NULL, payload FROM domain_events WHERE type = 'session.revoked' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []revokedEvent
	for rows.Next() {
		var (
			subjectType string
			subject     [16]byte
			noActor     bool
			payload     []byte
		)
		if err := rows.Scan(&subjectType, &subject, &noActor, &payload); err != nil {
			t.Fatal(err)
		}
		var p map[string]string
		if err := json.Unmarshal(payload, &p); err != nil || len(p) != 1 {
			t.Fatalf("olay yükü yalnızca session_id taşımalı: %s", payload)
		}
		sid, ok := id.Parse(p["session_id"])
		if !ok || subjectType != "user" || !noActor {
			t.Fatalf("olay biçimi: %s %s actor_yok=%v", subjectType, payload, noActor)
		}
		out = append(out, revokedEvent{user: id.ID(subject), session: sid})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) wantRevoked(t *testing.T, want ...revokedEvent) {
	t.Helper()
	got := e.revokedEvents(t)
	if len(got) != len(want) {
		t.Fatalf("session.revoked olayları = %v, beklenen %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("olay %d = %v, beklenen %v", i, got[i], want[i])
		}
	}
}

func ev(t Tokens) revokedEvent { return revokedEvent{user: t.UserID, session: t.SessionID} }

func TestSignOutWritesSessionRevoked(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-1")
	e.wantRevoked(t) // sign-in writes none
	if err := e.svc.SignOut(ctx, tok.SessionID); err != nil {
		t.Fatal(err)
	}
	// An already ended session writes no second event.
	if err := e.svc.SignOut(ctx, tok.SessionID); err != nil {
		t.Fatal(err)
	}
	// An unknown session neither.
	if err := e.svc.SignOut(ctx, id.New()); err != nil {
		t.Fatal(err)
	}
	e.wantRevoked(t, ev(tok))
}

func TestRevokeWritesSessionRevoked(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	here, there := e.signIn(t, "sub-a"), e.signIn(t, "sub-a")
	stranger := e.signIn(t, "sub-b")
	// Another user's session: refused, nothing written.
	if err := e.svc.Revoke(ctx, caller(here), stranger.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
	e.wantRevoked(t)
	if err := e.svc.Revoke(ctx, caller(here), there.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Revoke(ctx, caller(here), there.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
	// Ending one's own session is a sign-out and also an event.
	if err := e.svc.Revoke(ctx, caller(here), here.SessionID); err != nil {
		t.Fatal(err)
	}
	e.wantRevoked(t, ev(there), ev(here))
}

func TestRevokeOthersWritesOneEventPerSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	here := e.signIn(t, "sub-a")
	o1, o2 := e.signIn(t, "sub-a"), e.signIn(t, "sub-a")
	e.signIn(t, "sub-b")
	if n, err := e.svc.RevokeOthers(ctx, caller(here)); err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	got := e.revokedEvents(t)
	if len(got) != 2 {
		t.Fatalf("olaylar = %v", got)
	}
	seen := map[revokedEvent]bool{}
	for _, g := range got {
		seen[g] = true
	}
	if !seen[ev(o1)] || !seen[ev(o2)] {
		t.Fatalf("olaylar = %v", got)
	}
	// Nothing left to end: no further events.
	if n, err := e.svc.RevokeOthers(ctx, caller(here)); err != nil || n != 0 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if got := e.revokedEvents(t); len(got) != 2 {
		t.Fatalf("olaylar = %v", got)
	}
}

func TestTokenReuseWritesSessionRevoked(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	t0 := e.signIn(t, "sub-1")
	if _, err := e.svc.Refresh(ctx, t0.RefreshToken); err != nil {
		t.Fatal(err)
	}
	// A duplicate within the grace period does not end the session.
	*e.clock = e.now.Add(reuseGrace / 2)
	if _, err := e.svc.Refresh(ctx, t0.RefreshToken); !errors.Is(err, ErrRefreshConflict) {
		t.Fatal(err)
	}
	e.wantRevoked(t)
	*e.clock = e.now.Add(reuseGrace + time.Second)
	if _, err := e.svc.Refresh(ctx, t0.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatal(err)
	}
	if r := e.reason(t, t0.SessionID); r != reasonTokenReuse {
		t.Fatalf("iptal nedeni = %q", r)
	}
	e.wantRevoked(t, ev(t0))
}

// A session that expires unused writes nothing: its access tokens expired
// 90 days earlier, so chat has no connection of it (ADR-0014).
func TestExpiryWritesNoEvent(t *testing.T) {
	e := setup(t)
	tok := e.signIn(t, "sub-1")
	*e.clock = e.now.Add(sessionTTL + time.Second)
	if _, err := e.svc.Refresh(context.Background(), tok.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatal(err)
	}
	e.wantRevoked(t)
}
