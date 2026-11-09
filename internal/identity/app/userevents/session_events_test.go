package userevents

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

// session.revoked carries the session ID only and user.sanctioned nothing:
// no reason, no sanction type, no device, no token (issue #92).
func TestSessionEventPayloads(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	ctx := context.Background()
	user, s1, s2 := id.New(), id.New(), id.New()
	inTx(t, pool, func(tx pgx.Tx) error { return SessionsRevoked(ctx, tx, user, s1, s2) })
	inTx(t, pool, func(tx pgx.Tx) error { return SessionsRevoked(ctx, tx, user) }) // none: nothing written
	inTx(t, pool, func(tx pgx.Tx) error { return Sanctioned(ctx, tx, user) })

	for _, s := range []id.ID{s1, s2} {
		if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE type = 'session.revoked' AND subject_type = 'user' AND subject_id = $1 AND actor_id IS NULL AND payload = jsonb_build_object('session_id', $2::text)`, user.PG(), s.String()); n != 1 {
			t.Fatalf("oturum %s için olay sayısı %d", s, n)
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events WHERE type = 'user.sanctioned' AND subject_id = $1 AND actor_id IS NULL AND payload = '{}'`, user.PG()); n != 1 {
		t.Fatalf("yaptırım olayı sayısı %d", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM domain_events`); n != 3 {
		t.Fatalf("toplam olay %d", n)
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return SessionsRevoked(ctx, tx, user, id.ID{}) })
	if err == nil {
		t.Fatal("boş oturum kimliği kabul edildi")
	}
}

// The session events go to chat only; core does not use them (issue #92).
func TestSessionEventsGoToChatOnly(t *testing.T) {
	pool := dbtest.New(t, identitydb.Migrations())
	ctx := context.Background()
	user, sess := id.New(), id.New()
	inTx(t, pool, func(tx pgx.Tx) error { return SessionsRevoked(ctx, tx, user, sess) })
	inTx(t, pool, func(tx pgx.Tx) error { return Sanctioned(ctx, tx, user) })

	core, chat := &fakeCore{}, &fakeChat{}
	cc, hc := CoreConsumer(core), ChatConsumer(chat)
	r, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Consumers: []outbox.Consumer{cc, hc}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.Step(ctx, cc); err != nil || n != 0 || len(core.got) != 0 {
		t.Fatalf("core: n = %d, err = %v, got = %v", n, err, core.got)
	}
	if n, err := r.Step(ctx, hc); err != nil || n != 2 {
		t.Fatalf("chat: n = %d, err = %v", n, err)
	}
	if len(chat.got) != 2 {
		t.Fatalf("chat'e giden = %v", chat.got)
	}
	rv, sn := chat.got[0], chat.got[1]
	if rv.GetUserId() != user.String() || rv.GetSessionRevoked().GetSessionId() != sess.String() || rv.GetEventId() == "" || rv.GetOccurredAt() == nil {
		t.Fatalf("session_revoked = %v", rv)
	}
	if sn.GetUserId() != user.String() || sn.GetSanctioned() == nil {
		t.Fatalf("sanctioned = %v", sn)
	}
}

func TestChatSessionRequestRejectsMalformed(t *testing.T) {
	user := id.New()
	for name, e := range map[string]outbox.Event{
		"konu yok":        {ID: id.New(), Type: TypeSessionRevoked, Payload: []byte(`{"session_id":"` + id.New().String() + `"}`)},
		"konu başka":      {ID: id.New(), Type: TypeSanctioned, SubjectType: "tayfa", SubjectID: user, Payload: []byte(`{}`)},
		"yük bozuk":       {ID: id.New(), Type: TypeSessionRevoked, SubjectType: subjectUser, SubjectID: user, Payload: []byte(`[`)},
		"oturum geçersiz": {ID: id.New(), Type: TypeSessionRevoked, SubjectType: subjectUser, SubjectID: user, Payload: []byte(`{"session_id":"x"}`)},
		"oturum yok":      {ID: id.New(), Type: TypeSessionRevoked, SubjectType: subjectUser, SubjectID: user, Payload: []byte(`{}`)},
	} {
		if _, err := chatRequest(e); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	// core never receives them, and would refuse them.
	if _, err := coreRequest(outbox.Event{ID: id.New(), Type: TypeSessionRevoked, SubjectType: subjectUser, SubjectID: user, Payload: []byte(`{}`)}); err == nil {
		t.Fatal("core oturum olayını kabul etti")
	}
}
