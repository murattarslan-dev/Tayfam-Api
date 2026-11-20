package joinqr

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/instant"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }

// countLimiter allows n attempts per key.
type countLimiter struct {
	n    int
	seen map[string]int
}

func (l *countLimiter) Allow(k string) bool {
	l.seen[k]++
	return l.seen[k] <= l.n
}

type env struct {
	pool     *pgxpool.Pool
	admin    *pgxpool.Pool
	svc      *Service
	tayfas   *tayfa.Service
	events   *event.Service
	members  *membership.Service
	instants *instant.Service
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tf, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	ev, err := event.New(event.Config{Pool: d.App, Log: log})
	must(t, err)
	ms, err := membership.New(membership.Config{Pool: d.App, JoinLimiter: allowAll{}})
	must(t, err)
	in, err := instant.New(instant.Config{Pool: d.App, Log: log, Tayfas: tf, Events: ev, Memberships: ms})
	must(t, err)
	svc, err := New(Config{Pool: d.App, Events: ev, Memberships: ms, JoinLimiter: allowAll{}})
	must(t, err)
	return &env{pool: d.App, admin: d.Admin, svc: svc, tayfas: tf, events: ev, members: ms, instants: in}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: hata = %v, beklenen %v", what, err, want)
	}
}

// exec runs fixture SQL as the superuser with triggers off.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	_, err = tx.Exec(ctx, sql, args...)
	must(t, err)
	must(t, tx.Commit(ctx))
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// seed writes an active membership with triggers off.
func (e *env) seed(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, $3, 'active', 'code', now())`,
		tf.PG(), u.PG(), role.String())
	return u
}

func (e *env) membership(t *testing.T, tf, u id.ID) string {
	t.Helper()
	var st, via string
	err := e.pool.QueryRow(ctx, `SELECT status, joined_via FROM memberships WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), u.PG()).Scan(&st, &via)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	must(t, err)
	return st + "/" + via
}

func (e *env) rsvp(t *testing.T, ev, u id.ID) string {
	t.Helper()
	var st, att string
	var method pgtype.Text
	err := e.pool.QueryRow(ctx, `SELECT status, attendance, attendance_method FROM event_rsvps WHERE event_id = $1 AND user_id = $2`, ev.PG(), u.PG()).Scan(&st, &att, &method)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	must(t, err)
	return st + "/" + att + "/" + method.String
}

func inPerson() event.Location {
	p := &event.Point{Lat: 38.4192, Lng: 27.1287}
	return event.Location{Mode: event.ModeInPerson, MeetingPointName: "Kordon", MeetingPoint: p, City: "İzmir", District: "Konak"}
}

func nowInput() event.Input {
	return event.Input{
		Type: "general", Title: "Batak masası", Location: inPerson(),
		ClientRequestID: id.New().String(), StartsNow: true,
	}
}

// standard creates a standard tayfa with the given join policy and a now
// event of its admin.
func (e *env) standard(t *testing.T, policy string) (tf, admin, ev id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Batak Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	e.exec(t, `UPDATE tayfas SET join_policy = $2 WHERE id = $1`, v.ID.PG(), policy)
	got, err := e.events.Create(ctx, admin, v.ID, nowInput())
	must(t, err)
	return v.ID, admin, got.ID
}

// instantNow creates an instant now event.
func (e *env) instantNow(t *testing.T) (tf, org, ev id.ID) {
	t.Helper()
	org = id.New()
	r, err := e.instants.Create(ctx, org, instant.Input{Event: nowInput()})
	must(t, err)
	return r.Tayfa.ID, org, r.Event.ID
}

func (e *env) token(t *testing.T, user, tf, ev id.ID) string {
	t.Helper()
	q, err := e.svc.Create(ctx, user, tf, ev)
	must(t, err)
	return q.Token
}

// The token is 128 random bits; only its hash is stored, it lives two
// minutes, and only the organizer and the admins open it, for an in-person
// event that has not ended.
func TestCreate(t *testing.T) {
	e := setup(t)
	tf, admin, ev := e.standard(t, tayfa.JoinApproval)
	member := e.seed(t, tf, authz.Member)
	moderator := e.seed(t, tf, authz.Moderator)

	before := time.Now()
	q, err := e.svc.Create(ctx, admin, tf, ev)
	must(t, err)
	if len(q.Token) != 22 || q.ExpiresAt.Before(before.Add(event.JoinQRTTL-time.Second)) || q.ExpiresAt.After(time.Now().Add(event.JoinQRTTL)) {
		t.Fatalf("token = %d karakter, bitiş %v", len(q.Token), q.ExpiresAt)
	}
	hash, ok := event.HashJoinQRToken(q.Token)
	if !ok || e.count(t, `SELECT count(*) FROM event_join_qr_tokens WHERE token_hash = $1 AND event_id = $2 AND created_by = $3`, hash, ev.PG(), admin.PG()) != 1 {
		t.Fatal("token özeti saklanmadı")
	}
	if e.count(t, `SELECT count(*) FROM event_join_qr_tokens WHERE position($1::bytea IN token_hash) > 0`, []byte(q.Token)) != 0 {
		t.Fatal("token açık saklandı")
	}
	q2, err := e.svc.Create(ctx, admin, tf, ev)
	must(t, err)
	if q2.Token == q.Token {
		t.Fatal("aynı token iki kez üretildi")
	}

	// Members who did not organize it, moderators too, may not; the
	// organizer of their own event may.
	_, err = e.svc.Create(ctx, member, tf, ev)
	wantErr(t, "üye", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, moderator, tf, ev)
	wantErr(t, "moderatör", err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, id.New(), tf, ev)
	wantErr(t, "yabancı", err, apperr.ErrTayfaNotFound)
	e.exec(t, `UPDATE tayfas SET members_can_create_events = true WHERE id = $1`, tf.PG())
	own, err := e.events.Create(ctx, member, tf, func() event.Input {
		in := nowInput()
		in.StartsNow = false
		in.StartsAt = time.Now().Add(time.Hour)
		in.EndsAt = in.StartsAt.Add(time.Hour)
		return in
	}())
	must(t, err)
	if _, err := e.svc.Create(ctx, member, tf, own.ID); err != nil {
		t.Fatalf("organizatör üye: %v", err)
	}
	if _, err := e.svc.Create(ctx, admin, tf, own.ID); err != nil {
		t.Fatalf("yönetici: %v", err)
	}

	// Online events have no QR.
	online := nowInput()
	online.StartsNow = false
	online.StartsAt = time.Now().Add(time.Hour)
	online.EndsAt = online.StartsAt.Add(time.Hour)
	online.Location = event.Location{Mode: event.ModeOnline, OnlineURL: "https://example.org/oda"}
	on, err := e.events.Create(ctx, admin, tf, online)
	must(t, err)
	_, err = e.svc.Create(ctx, admin, tf, on.ID)
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "mode" {
		t.Fatalf("çevrimiçi: %v", err)
	}

	// After "Bitir" the QR closes.
	_, err = e.events.Finish(ctx, admin, tf, ev)
	must(t, err)
	_, err = e.svc.Create(ctx, admin, tf, ev)
	wantErr(t, "bitmiş", err, apperr.ErrEventEnded)
}

// Instant tayfa: the scanning user becomes an active member at once
// (joined_via qr) and answers going; nobody is notified of a join request.
func TestJoinInstant(t *testing.T) {
	e := setup(t)
	tf, org, ev := e.instantNow(t)
	tok := e.token(t, org, tf, ev)

	u := id.New()
	r, err := e.svc.Join(ctx, u, tok)
	must(t, err)
	if r.TayfaID != tf || r.EventID != ev || r.Pending || r.RSVP != event.RSVPGoing || r.Attendance.Status != event.AttendanceUnknown {
		t.Fatalf("sonuç = %+v", r)
	}
	if got := e.membership(t, tf, u); got != "active/qr" {
		t.Fatalf("üyelik = %s", got)
	}
	if got := e.rsvp(t, ev, u); got != "going/unknown/" {
		t.Fatalf("katılım = %s", got)
	}
	if e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND payload ->> 'type' = 'join_request'`) != 0 {
		t.Error("anlık tayfada katılım isteği bildirimi yazıldı")
	}

	// Scanning again as a member: going stays and the attendance is
	// checked in.
	r, err = e.svc.Join(ctx, u, tok)
	must(t, err)
	if r.Pending || r.RSVP != event.RSVPGoing || r.Attendance.Status != event.AttendanceAttended || r.Attendance.Method != event.MethodQR {
		t.Fatalf("ikinci okutma = %+v", r)
	}
	if got := e.rsvp(t, ev, u); got != "going/attended/qr" {
		t.Fatalf("check-in = %s", got)
	}
	// A third scan changes nothing and does not count.
	_, err = e.svc.Join(ctx, u, tok)
	must(t, err)
	if n := e.count(t, `SELECT joins FROM event_join_qr_counts WHERE event_id = $1`, ev.PG()); n != 2 {
		t.Fatalf("sayaç = %d", n)
	}

	// The organizer's own token does not join the organizer.
	_, err = e.svc.Join(ctx, org, tok)
	wantErr(t, "organizatör", err, apperr.ErrInvalidJoinQR)

	// The token never reaches audit records or events.
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%'`, tok) != 0 ||
		e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%' || $1 || '%'`, tok) != 0 {
		t.Fatal("token kayda girdi")
	}
}

// Standard tayfa: the join policy applies. Open: member and going;
// approval: a join request and nothing else; a waiting request answers
// pending again.
func TestJoinStandardPolicy(t *testing.T) {
	e := setup(t)

	tf, admin, ev := e.standard(t, tayfa.JoinOpen)
	u := id.New()
	r, err := e.svc.Join(ctx, u, e.token(t, admin, tf, ev))
	must(t, err)
	if r.Pending || r.RSVP != event.RSVPGoing || e.membership(t, tf, u) != "active/qr" || e.rsvp(t, ev, u) != "going/unknown/" {
		t.Fatalf("açık tayfa: %+v %s %s", r, e.membership(t, tf, u), e.rsvp(t, ev, u))
	}

	tf, admin, ev = e.standard(t, tayfa.JoinApproval)
	tok := e.token(t, admin, tf, ev)
	r, err = e.svc.Join(ctx, u, tok)
	must(t, err)
	if !r.Pending || r.RSVP != "" || e.membership(t, tf, u) != "pending/qr" || e.rsvp(t, ev, u) != "" {
		t.Fatalf("onaylı tayfa: %+v %s %s", r, e.membership(t, tf, u), e.rsvp(t, ev, u))
	}
	if e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND payload ->> 'type' = 'join_request'`) != 1 {
		t.Error("katılım isteği yöneticilere bildirilmedi")
	}
	r, err = e.svc.Join(ctx, u, tok)
	must(t, err)
	if !r.Pending || e.count(t, `SELECT joins FROM event_join_qr_counts WHERE event_id = $1`, ev.PG()) != 1 {
		t.Fatalf("bekleyen istek: %+v", r)
	}

	// A member of the approval tayfa is checked in.
	m := e.seed(t, tf, authz.Member)
	r, err = e.svc.Join(ctx, m, tok)
	must(t, err)
	if r.RSVP != event.RSVPGoing || r.Attendance.Status != event.AttendanceAttended || e.rsvp(t, ev, m) != "going/attended/qr" {
		t.Fatalf("üye: %+v %s", r, e.rsvp(t, ev, m))
	}
	// A direct invitation waiting for an answer is answered with
	// AcceptInvitation.
	inv := id.New()
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, invited_by_user_id) VALUES ($1, $2, 'member', 'invited', 'direct_invite', $3)`,
		tf.PG(), inv.PG(), admin.PG())
	_, err = e.svc.Join(ctx, inv, tok)
	wantErr(t, "davetli", err, apperr.ErrAlreadyPending)
}

// Every refusal is the same invalid-join-qr: malformed, unknown and expired
// tokens, an ended or cancelled event, a banned member, a block with the
// organizer either way, a deleted or platform banned account.
func TestJoinRefused(t *testing.T) {
	e := setup(t)
	tf, org, ev := e.instantNow(t)
	tok := e.token(t, org, tf, ev)

	for name, token := range map[string]string{
		"bozuk":      "kısa",
		"bilinmeyen": "AAAAAAAAAAAAAAAAAAAAAA",
		"fazla":      tok + "A",
	} {
		_, err := e.svc.Join(ctx, id.New(), token)
		wantErr(t, name, err, apperr.ErrInvalidJoinQR)
	}
	_, err := e.svc.Join(ctx, id.New(), "")
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("boş token: %v", err)
	}

	banned := id.New()
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ($1, $2, 'member', 'banned', 'code', now())`, tf.PG(), banned.PG())
	_, err = e.svc.Join(ctx, banned, tok)
	wantErr(t, "banlı", err, apperr.ErrInvalidJoinQR)

	blocker, blocked := id.New(), id.New()
	e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, blocker.PG(), org.PG())
	e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, org.PG(), blocked.PG())
	for name, u := range map[string]id.ID{"engelleyen": blocker, "engellenen": blocked} {
		_, err = e.svc.Join(ctx, u, tok)
		wantErr(t, name, err, apperr.ErrInvalidJoinQR)
	}

	deleted, platformBanned := id.New(), id.New()
	e.exec(t, `INSERT INTO user_profiles (user_id, source_event_id, deleted_at) VALUES ($1, $2, now())`, deleted.PG(), id.New().PG())
	e.exec(t, `INSERT INTO user_platform_bans (user_id, banned_at, event_id) VALUES ($1, now(), $2)`, platformBanned.PG(), id.New().PG())
	for name, u := range map[string]id.ID{"silinmiş": deleted, "platform banlı": platformBanned} {
		_, err = e.svc.Join(ctx, u, tok)
		wantErr(t, name, err, apperr.ErrInvalidJoinQR)
	}
	for _, u := range []id.ID{blocker, blocked, deleted, platformBanned} {
		if e.membership(t, tf, u) != "" {
			t.Fatal("reddedilen kişi üye oldu")
		}
	}

	// Two minutes: an expired token (a screenshot shared later) is refused.
	hash, _ := event.HashJoinQRToken(tok)
	e.exec(t, `UPDATE event_join_qr_tokens SET created_at = now() - interval '10 minutes', expires_at = now() - interval '8 minutes' WHERE token_hash = $1`, hash)
	_, err = e.svc.Join(ctx, id.New(), tok)
	wantErr(t, "süresi dolmuş", err, apperr.ErrInvalidJoinQR)

	// After "Bitir" a live token no longer joins.
	tok = e.token(t, org, tf, ev)
	_, err = e.events.Finish(ctx, org, tf, ev)
	must(t, err)
	late := id.New()
	_, err = e.svc.Join(ctx, late, tok)
	wantErr(t, "bitmiş", err, apperr.ErrInvalidJoinQR)

	// A cancelled event the same.
	tf2, admin2, ev2 := e.standard(t, tayfa.JoinOpen)
	tok2 := e.token(t, admin2, tf2, ev2)
	_, err = e.events.Cancel(ctx, admin2, tf2, ev2, "")
	must(t, err)
	_, err = e.svc.Join(ctx, late, tok2)
	wantErr(t, "iptal", err, apperr.ErrInvalidJoinQR)
	if e.membership(t, tf, late) != "" || e.membership(t, tf2, late) != "" {
		t.Fatal("reddedilen kişi üye oldu")
	}
}

// At most 30 joins by QR per event: the 31st is refused and changes
// nothing, and no new token is given.
func TestJoinLimit(t *testing.T) {
	e := setup(t)
	tf, org, ev := e.instantNow(t)
	tok := e.token(t, org, tf, ev)
	e.exec(t, `INSERT INTO event_join_qr_counts (event_id, joins) VALUES ($1, 29)`, ev.PG())
	_, err := e.svc.Join(ctx, id.New(), tok)
	must(t, err)
	late := id.New()
	_, err = e.svc.Join(ctx, late, tok)
	wantErr(t, "31.", err, apperr.ErrJoinQRLimit)
	if e.membership(t, tf, late) != "" {
		t.Fatal("sınır aşıldıktan sonra üye olundu")
	}
	_, err = e.svc.Create(ctx, org, tf, ev)
	wantErr(t, "yeni token", err, apperr.ErrJoinQRLimit)
}

// Five attempts per user (the limiter), malformed ones included.
func TestJoinRateLimited(t *testing.T) {
	e := setup(t)
	l := &countLimiter{n: 5, seen: map[string]int{}}
	svc, err := New(Config{Pool: e.pool, Events: e.events, Memberships: e.members, JoinLimiter: l})
	must(t, err)
	u := id.New()
	for i := 0; i < 5; i++ {
		_, err := svc.Join(ctx, u, "AAAAAAAAAAAAAAAAAAAAAA")
		wantErr(t, "deneme", err, apperr.ErrInvalidJoinQR)
	}
	_, err = svc.Join(ctx, u, "AAAAAAAAAAAAAAAAAAAAAA")
	wantErr(t, "altıncı", err, apperr.ErrRateLimited)
	// Another user is not limited.
	_, err = svc.Join(ctx, id.New(), "AAAAAAAAAAAAAAAAAAAAAA")
	wantErr(t, "başka kişi", err, apperr.ErrInvalidJoinQR)
}

// The memberships trigger refuses an active QR join of an instant tayfa
// without a live token of its organizer (ADR-0011: a second line).
func TestTriggerNeedsToken(t *testing.T) {
	e := setup(t)
	tf, org, ev := e.instantNow(t)
	insert := func(u id.ID, token string) error {
		return txn.WithActor(ctx, e.pool, u, func(_ pgx.Tx, q *store.Queries) error {
			if token != "" {
				if err := q.SetJoinQRToken(ctx, token); err != nil {
					return err
				}
			}
			return q.CreateMembership(ctx, store.CreateMembershipParams{
				TayfaID: tf.PG(), UserID: u.PG(), Role: "member", Status: "active", JoinedVia: "qr",
				JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			})
		})
	}
	if err := insert(id.New(), ""); err == nil {
		t.Fatal("token'sız QR katılımı kabul edildi")
	}
	if err := insert(id.New(), id.New().String()); err == nil {
		t.Fatal("bilinmeyen token'la QR katılımı kabul edildi")
	}
	e.token(t, org, tf, ev)
	var tokenID pgtype.UUID
	must(t, e.pool.QueryRow(ctx, `SELECT id FROM event_join_qr_tokens WHERE event_id = $1`, ev.PG()).Scan(&tokenID))
	tid := id.ID(tokenID.Bytes).String()
	if err := insert(id.New(), tid); err != nil {
		t.Fatalf("geçerli token: %v", err)
	}
	e.exec(t, `UPDATE event_join_qr_tokens SET created_at = now() - interval '10 minutes', expires_at = now() - interval '8 minutes' WHERE id = $1`, tokenID)
	if err := insert(id.New(), tid); err == nil {
		t.Fatal("süresi dolmuş token'la QR katılımı kabul edildi")
	}
	// A standard approval tayfa takes a QR join only as a request.
	stf, sadmin, sev := e.standard(t, tayfa.JoinApproval)
	e.token(t, sadmin, stf, sev)
	must(t, e.pool.QueryRow(ctx, `SELECT id FROM event_join_qr_tokens WHERE event_id = $1`, sev.PG()).Scan(&tokenID))
	su := id.New()
	err := txn.WithActor(ctx, e.pool, su, func(_ pgx.Tx, q *store.Queries) error {
		if err := q.SetJoinQRToken(ctx, id.ID(tokenID.Bytes).String()); err != nil {
			return err
		}
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: stf.PG(), UserID: su.PG(), Role: "member", Status: "active", JoinedVia: "qr",
			JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		})
	})
	if err == nil || !strings.Contains(err.Error(), "TF002") {
		t.Fatalf("onaylı tayfada etkin QR katılımı: %v", err)
	}
}
