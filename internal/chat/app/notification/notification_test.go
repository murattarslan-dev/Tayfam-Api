package notification

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fakeMembers stands in for core's GetMembership.
type fakeMembers struct {
	active map[[2]id.ID]bool
	err    error
}

func (f *fakeMembers) IsActiveMember(_ context.Context, tayfa, user id.ID) (bool, error) {
	return f.active[[2]id.ID{tayfa, user}], f.err
}

type env struct {
	pool    *pgxpool.Pool
	svc     *Service
	members *fakeMembers
	tayfa   id.ID
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, chatdb.Migrations())
	m := &fakeMembers{active: map[[2]id.ID]bool{}}
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Members: m})
	must(t, err)
	return &env{pool: pool, svc: svc, members: m, tayfa: id.New()}
}

// store writes a notification request as chat's consumer does.
func (e *env) store(t *testing.T, r Request) {
	t.Helper()
	if r.Event == (id.ID{}) {
		r.Event = id.New()
	}
	if r.Tayfa == (id.ID{}) {
		r.Tayfa = e.tayfa
	}
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return Store(ctx, store.New(tx), r) }))
}

func (e *env) announce(t *testing.T, actor id.ID, to ...id.ID) {
	t.Helper()
	e.store(t, Request{Type: TypeAnnouncement, Actor: actor, TargetType: TargetAnnouncement, Target: id.New(), Recipients: to})
}

func TestListCarriesIDsOnly(t *testing.T) {
	e := setup(t)
	u, actor, q, a, hiker, ev := id.New(), id.New(), id.New(), id.New(), id.New(), id.New()
	e.store(t, Request{Type: TypeAnswerReceived, Actor: actor, TargetType: TargetAnswer, Target: a, Question: q, Recipients: []id.ID{u}})
	e.store(t, Request{Type: TypeHikeOverdue, TargetType: TargetEvent, Target: ev, Subject: hiker, Recipients: []id.ID{u}})
	e.store(t, Request{Type: TypeTayfaWarned, TargetType: TargetTayfa, Target: e.tayfa, Reason: ReasonInactive, Recipients: []id.ID{u}})

	list, next, err := e.svc.List(ctx, u, 10, "")
	must(t, err)
	if len(list) != 3 || next != "" {
		t.Fatalf("liste = %+v %q", list, next)
	}
	warned, hike, answer := list[0], list[1], list[2]
	if warned.Type != TypeTayfaWarned || warned.Reason != ReasonInactive || warned.Actor != (id.ID{}) {
		t.Errorf("uyarı = %+v", warned)
	}
	if hike.Type != TypeHikeOverdue || hike.Subject != hiker || hike.Target != ev {
		t.Errorf("alarm = %+v", hike)
	}
	if answer.Type != TypeAnswerReceived || answer.Question != q || answer.Target != a || answer.Actor != actor ||
		answer.Tayfa != e.tayfa || !answer.ReadAt.IsZero() {
		t.Errorf("cevap = %+v", answer)
	}
}

// A user only ever sees and changes their own notifications.
func TestReadMarksAreOwnOnly(t *testing.T) {
	e := setup(t)
	a, b := id.New(), id.New()
	for range 3 {
		e.announce(t, id.New(), a, b)
	}
	aList, next, err := e.svc.List(ctx, a, 2, "")
	must(t, err)
	if len(aList) != 2 || next == "" {
		t.Fatalf("ilk sayfa: %d %q", len(aList), next)
	}
	rest, next, err := e.svc.List(ctx, a, 2, next)
	must(t, err)
	if len(rest) != 1 || next != "" || bytes.Compare(rest[0].ID[:], aList[1].ID[:]) >= 0 {
		t.Fatalf("ikinci sayfa: %+v %q", rest, next)
	}

	// b cannot mark a's notification: same as a missing one.
	if err := e.svc.MarkRead(ctx, b, aList[0].ID); !errors.Is(err, apperr.ErrNotificationNotFound) {
		t.Fatalf("başkasının bildirimi: %v", err)
	}
	if err := e.svc.MarkRead(ctx, a, id.New()); !errors.Is(err, apperr.ErrNotificationNotFound) {
		t.Fatalf("olmayan bildirim: %v", err)
	}
	must(t, e.svc.MarkRead(ctx, a, aList[0].ID))
	must(t, e.svc.MarkRead(ctx, a, aList[0].ID))
	for u, want := range map[id.ID]int32{a: 2, b: 3} {
		n, err := e.svc.UnreadCount(ctx, u)
		must(t, err)
		if n != want {
			t.Errorf("%v okunmamış = %d, beklenen %d", u, n, want)
		}
	}
	must(t, e.svc.MarkAllRead(ctx, a))
	if n, _ := e.svc.UnreadCount(ctx, a); n != 0 {
		t.Fatalf("tümü okundu sonrası %d", n)
	}
	if n, _ := e.svc.UnreadCount(ctx, b); n != 3 {
		t.Fatalf("başkasının okunmamışı değişti: %d", n)
	}
	for _, tok := range []string{"x", "00000000"} {
		var v *apperr.ValidationError
		if _, _, err := e.svc.List(ctx, a, 0, tok); !errors.As(err, &v) {
			t.Errorf("token %q: %v", tok, err)
		}
	}
}

func TestRetention(t *testing.T) {
	e := setup(t)
	u := id.New()
	e.announce(t, id.New(), u)
	e.announce(t, id.New(), u)
	list, _, err := e.svc.List(ctx, u, 10, "")
	must(t, err)
	_, err = e.pool.Exec(ctx, `UPDATE notifications SET created_at = now() - interval '91 days' WHERE id = $1`, list[1].ID.PG())
	must(t, err)
	n, err := e.svc.PurgeOld(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("%d silindi", n)
	}
	if list, _, _ := e.svc.List(ctx, u, 10, ""); len(list) != 1 {
		t.Fatalf("kalan %d", len(list))
	}
}

// Muting needs core's word that the caller is an active member; muted
// notifications still appear in the list.
func TestTayfaPreference(t *testing.T) {
	e := setup(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	e.svc.cfg.Now = func() time.Time { return now }
	u, other := id.New(), id.New()
	e.members.active[[2]id.ID{e.tayfa, u}] = true

	until := now.Add(8 * time.Hour)
	p, err := e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelNone, until)
	must(t, err)
	if p.Level != LevelNone || !p.MutedUntil.Equal(until) {
		t.Fatalf("tercih = %+v", p)
	}
	prefs, err := e.svc.Preferences(ctx, u)
	must(t, err)
	if len(prefs) != 1 || prefs[0].Scope != e.tayfa || prefs[0].ScopeType != ScopeTayfa || !prefs[0].MutedUntil.Equal(until) {
		t.Fatalf("tercihler = %+v", prefs)
	}
	e.announce(t, id.New(), u)
	if list, _, _ := e.svc.List(ctx, u, 10, ""); len(list) != 1 {
		t.Fatal("sessizdeki bildirim listede yok")
	}
	_, err = e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelAll, time.Time{})
	must(t, err)
	if prefs, _ := e.svc.Preferences(ctx, u); len(prefs) != 0 {
		t.Fatal("varsayılana dönünce satır kaldı")
	}

	// Not an active member by core: TAYFA_NOT_FOUND, nothing written.
	if _, err := e.svc.SetTayfaPreference(ctx, other, e.tayfa, LevelNone, time.Time{}); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olmayan: %v", err)
	}
	// Core down: the error passes through, nothing written.
	e.members.err = apperr.ErrUnavailable
	if _, err := e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelNone, time.Time{}); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("core kapalı: %v", err)
	}
	e.members.err = nil
	if prefs, _ := e.svc.Preferences(ctx, other); len(prefs) != 0 {
		t.Fatal("reddedilen tercih yazıldı")
	}
	for name, err := range map[string]error{
		"seviye":         second(e.svc.SetTayfaPreference(ctx, u, e.tayfa, "loud", time.Time{})),
		"geçmiş":         second(e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelNone, now.Add(-time.Minute))),
		"çok uzak":       second(e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelNone, now.Add(MaxMuteAhead+time.Hour))),
		"all ile süreli": second(e.svc.SetTayfaPreference(ctx, u, e.tayfa, LevelAll, until)),
	} {
		var v *apperr.ValidationError
		if !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
