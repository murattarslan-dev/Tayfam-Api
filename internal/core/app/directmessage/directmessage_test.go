package directmessage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/social"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type env struct {
	svc   *Service
	pool  *pgxpool.Pool
	codes int
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	svc, err := New(Config{Pool: pool, Now: func() time.Time { return now }})
	must(t, err)
	return &env{svc: svc, pool: pool}
}

// exec writes fixture rows with triggers off (superuser test connection),
// so the rules are tested apart from how the rows came to be.
func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	must(t, tx.Commit(ctx))
}

func (e *env) user(t *testing.T) id.ID {
	t.Helper()
	u := id.New()
	e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES ($1, 'Kişi', $2)`, u.PG(), id.New().PG())
	return u
}

// tayfa creates a tayfa; extra is a SET clause applied afterwards ("" for
// a plain active tayfa).
func (e *env) tayfa(t *testing.T, extra string) id.ID {
	t.Helper()
	tf := id.New()
	e.codes++
	code := fmt.Sprintf("ABCD%04d", e.codes)
	code = strings.NewReplacer("0", "Z", "1", "Y").Replace(code)
	e.exec(t, `INSERT INTO tayfas (id, name, name_normalized, join_code) VALUES ($1, 'Tayfa', 'tayfa', $2)`, tf.PG(), code)
	if extra != "" {
		e.exec(t, `UPDATE tayfas SET `+extra+` WHERE id = $1`, tf.PG())
	}
	return tf
}

func (e *env) member(t *testing.T, tf, u id.ID, status string, joined time.Time) {
	t.Helper()
	if status == "active" {
		e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, status, joined_via, joined_at) VALUES ($1, $2, 'active', 'code', $3)`, tf.PG(), u.PG(), joined)
		return
	}
	e.exec(t, `INSERT INTO memberships (tayfa_id, user_id, status, joined_via) VALUES ($1, $2, $3, 'code')`, tf.PG(), u.PG(), status)
}

// event creates an online event in tf ending at end; cancelled and deleted
// change its state.
func (e *env) event(t *testing.T, tf id.ID, end time.Time, cancelled, deleted bool) id.ID {
	t.Helper()
	ev := id.New()
	e.exec(t, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url) VALUES ($1, $2, 'general', 'online', 'Buluşma', $3, $4, 'https://example.test/e')`,
		ev.PG(), tf.PG(), end.Add(-2*time.Hour), end)
	if cancelled {
		e.exec(t, `UPDATE events SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, ev.PG(), end.Add(-3*time.Hour))
	}
	if deleted {
		e.exec(t, `UPDATE events SET deleted_at = $2 WHERE id = $1`, ev.PG(), end)
	}
	return ev
}

func (e *env) attend(t *testing.T, ev, u id.ID, attendance string) {
	t.Helper()
	method := any(nil)
	if attendance == "attended" {
		method = "online"
	}
	e.exec(t, `INSERT INTO event_rsvps (event_id, user_id, status, attendance, attendance_method) VALUES ($1, $2, 'going', $3, $4)`, ev.PG(), u.PG(), attendance, method)
}

func (e *env) friends(t *testing.T, a, b id.ID, status string) {
	t.Helper()
	low, high := social.Pair(a, b)
	e.exec(t, `INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ($1, $2, $3, $1, CASE WHEN $3 = 'accepted' THEN now() END)`,
		low.PG(), high.PG(), status)
}

func (e *env) block(t *testing.T, blocker, blocked id.ID) {
	t.Helper()
	e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, blocker.PG(), blocked.PG())
}

// TestCheck is the rule table of v14 "Birebir mesaj kuralları".
func TestCheck(t *testing.T) {
	old := now.Add(-30 * day)
	cases := []struct {
		name  string
		setup func(t *testing.T, e *env, s, r id.ID)
		want  Answer
	}{
		{"ortak tayfa yok, arkadaş değil", func(t *testing.T, e *env, s, r id.ID) {
			e.member(t, e.tayfa(t, ""), s, "active", old)
			e.member(t, e.tayfa(t, ""), r, "active", old)
		}, Answer{Decision: Denied}},
		{"arkadaş, ortak tayfa yok", func(t *testing.T, e *env, s, r id.ID) {
			e.friends(t, s, r, "accepted")
		}, Answer{Decision: Active}},
		{"bekleyen arkadaşlık isteği arkadaşlık değildir", func(t *testing.T, e *env, s, r id.ID) {
			e.friends(t, s, r, "pending")
		}, Answer{Decision: Denied}},
		{"gönderen engellemiş", func(t *testing.T, e *env, s, r id.ID) {
			e.friends(t, s, r, "accepted")
			e.block(t, s, r)
		}, Answer{Decision: Denied}},
		{"alıcı engellemiş, ortak geçmiş olsa da", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(-day), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
			e.block(t, r, s)
		}, Answer{Decision: Denied}},
		{"ortak tayfa, geçmiş yok, eski üye", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", now)
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
		{"ortak tayfa, geçmiş yok, yeni üye", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", now.Add(-6*day))
			e.member(t, tf, r, "active", old)
		}, Answer{Decision: Request, RequestLimit: RequestsPerDayNew}},
		{"ortak tayfalardan birinde eski üye", func(t *testing.T, e *env, s, r id.ID) {
			for _, j := range []time.Time{now.Add(-day), now.Add(-8 * day)} {
				tf := e.tayfa(t, "")
				e.member(t, tf, s, "active", j)
				e.member(t, tf, r, "active", old)
			}
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
		{"başka tayfada eski üyelik sayılmaz", func(t *testing.T, e *env, s, r id.ID) {
			e.member(t, e.tayfa(t, ""), s, "active", old)
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", now.Add(-day))
			e.member(t, tf, r, "active", old)
		}, Answer{Decision: Request, RequestLimit: RequestsPerDayNew}},
		{"ortak etkinlik geçmişi", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", now.Add(-day))
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(-time.Hour), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Active}},
		{"geçmiş başka tayfadan da sayılır", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, e.tayfa(t, ""), now.Add(-time.Hour), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Active}},
		{"geçmiş ortak tayfa yerine geçmez", func(t *testing.T, e *env, s, r id.ID) {
			ev := e.event(t, e.tayfa(t, ""), now.Add(-time.Hour), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Denied}},
		{"bitmemiş etkinlik geçmiş değildir", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(time.Hour), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
		{"iptal edilmiş etkinlik geçmiş değildir", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(-day), true, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
		{"silinmiş etkinlik geçmiş değildir", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(-day), false, true)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "attended")
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
		{"yalnızca biri katılmış", func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, "")
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, "active", old)
			ev := e.event(t, tf, now.Add(-day), false, false)
			e.attend(t, ev, s, "attended")
			e.attend(t, ev, r, "no_show")
		}, Answer{Decision: Request, RequestLimit: RequestsPerDay}},
	}
	// A tayfa membership that is not "shared active".
	for _, c := range []struct{ name, tayfa, status string }{
		{"alıcı bekleyen üye", "", "pending"},
		{"alıcı davetli", "", "invited"},
		{"alıcı uzaklaştırılmış", "", "banned"},
		{"tayfa silinmiş", "deleted_at = now()", "active"},
		{"tayfa askıda", "suspended_at = now(), suspended_reason = 'kural'", "active"},
		{"tayfa silme bekliyor", "deletion_requested_at = now(), deletion_scheduled_for = now() + interval '7 days'", "active"},
		{"tayfa arşivlenmiş", "status = 'archived', archived_at = now()", "active"},
	} {
		cases = append(cases, struct {
			name  string
			setup func(t *testing.T, e *env, s, r id.ID)
			want  Answer
		}{c.name, func(t *testing.T, e *env, s, r id.ID) {
			tf := e.tayfa(t, c.tayfa)
			e.member(t, tf, s, "active", old)
			e.member(t, tf, r, c.status, old)
		}, Answer{Decision: Denied}})
	}
	e := setup(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, r := e.user(t), e.user(t)
			c.setup(t, e, s, r)
			got, err := e.svc.Check(ctx, s, r)
			must(t, err)
			if got != c.want {
				t.Fatalf("karar = %+v, beklenen %+v", got, c.want)
			}
		})
	}
}

// An unknown or deleted recipient looks like a block.
func TestCheckRecipient(t *testing.T) {
	e := setup(t)
	s, r := e.user(t), e.user(t)
	e.friends(t, s, r, "accepted")
	e.exec(t, `UPDATE user_profiles SET deleted_at = now(), display_name = NULL WHERE user_id = $1`, r.PG())
	for name, rec := range map[string]id.ID{"silinmiş": r, "bilinmeyen": id.New()} {
		got, err := e.svc.Check(ctx, s, rec)
		must(t, err)
		if got != (Answer{Decision: Denied}) {
			t.Errorf("%s alıcı: %+v", name, got)
		}
	}
	for name, rec := range map[string]id.ID{"kendisi": s, "boş": {}} {
		var v *apperr.ValidationError
		if _, err := e.svc.Check(ctx, s, rec); !errors.As(err, &v) || v.Field != "recipient_id" {
			t.Errorf("%s: hata = %v", name, err)
		}
	}
}
