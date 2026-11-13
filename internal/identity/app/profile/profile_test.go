package profile

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeRelations answers as core would for the viewer of the test.
type fakeRelations struct {
	rel   Relation
	err   error
	calls int
}

func (f *fakeRelations) Relation(context.Context, id.ID) (Relation, error) {
	f.calls++
	return f.rel, f.err
}

type env struct {
	pool   *pgxpool.Pool
	cipher *birthdate.Cipher
	rel    *fakeRelations
	svc    *Service
}

// now is 2026-11-13 in Istanbul; the test user was born 2008-03-04, so is
// 18.
var now = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func setup(t *testing.T) *env {
	t.Helper()
	c, err := birthdate.ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: dbtest.New(t, "identity", identitydb.Migrations()), cipher: c, rel: &fakeRelations{}}
	if e.svc, err = New(Config{Pool: e.pool, Cipher: c, Relations: e.rel, Now: func() time.Time { return now }}); err != nil {
		t.Fatal(err)
	}
	return e
}

// user creates a registered user born 2008-03-04 with the default
// visibility (no value given: the column default).
func (e *env) user(t *testing.T) id.ID {
	t.Helper()
	u := id.New()
	enc := e.cipher.Encrypt(u, birthdate.Date{Year: 2008, Month: time.March, Day: 4})
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO users (id, display_name, birth_date_enc, registered_at) VALUES ($1, 'Ece', $2, now())`, u.PG(), enc); err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) setVisibility(t *testing.T, u id.ID, v string) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET birth_date_visibility = $2 WHERE id = $1`, u.PG(), v); err != nil {
		t.Fatal(err)
	}
}

// TestBirthdayVisibility (issue #185): every setting against every
// relationship. Only friends see it, as much as the setting allows; the
// user always sees their own full date.
func TestBirthdayVisibility(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	owner := e.user(t)
	viewer := id.New()

	dayMonth := &Birthday{Month: time.March, Day: 4}
	full := &Birthday{Month: time.March, Day: 4, Year: 2008, Age: 18}
	relations := map[string]Relation{
		"kendisi":           {Self: true},
		"arkadaş":           {Friend: true},
		"arkadaş+ortak":     {Friend: true, Mutual: true},
		"ortak tayfa üyesi": {Mutual: true},
		"yabancı":           {},
	}
	want := map[string]map[string]*Birthday{
		"hidden":   {"kendisi": full},
		"birthday": {"kendisi": full, "arkadaş": dayMonth, "arkadaş+ortak": dayMonth},
		"full":     {"kendisi": full, "arkadaş": full, "arkadaş+ortak": full},
	}
	for vis, byRel := range want {
		e.setVisibility(t, owner, vis)
		for relName, rel := range relations {
			e.rel.rel, e.rel.err = rel, nil
			who := viewer
			if rel.Self {
				who = owner
			}
			d, err := e.svc.Get(ctx, who, owner)
			if err != nil {
				t.Fatalf("%s/%s: %v", vis, relName, err)
			}
			w := byRel[relName]
			switch {
			case w == nil && d.Birthday != nil:
				t.Errorf("%s/%s: gösterilmemeliydi: %+v", vis, relName, d.Birthday)
			case w != nil && (d.Birthday == nil || *d.Birthday != *w):
				t.Errorf("%s/%s: %+v, istenen %+v", vis, relName, d.Birthday, w)
			}
		}
	}
}

func TestDefaultIsDayMonth(t *testing.T) {
	e := setup(t)
	owner := e.user(t)
	e.rel.rel = Relation{Friend: true}
	d, err := e.svc.Get(context.Background(), id.New(), owner)
	if err != nil || d.Birthday == nil || d.Birthday.Year != 0 || d.Birthday.Age != 0 || d.Birthday.Day != 4 {
		t.Fatalf("varsayılan = %+v %v", d.Birthday, err)
	}
}

// TestNotShown: a block (core's USER_NOT_FOUND), a deleted or unregistered
// user, and core unreachable return nothing.
func TestNotShown(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	owner := e.user(t)

	e.rel.rel, e.rel.err = Relation{Friend: true}, ErrUserNotFound
	if d, err := e.svc.Get(ctx, id.New(), owner); !errors.Is(err, ErrUserNotFound) || d.Birthday != nil {
		t.Fatalf("engelli: %+v %v", d, err)
	}
	e.rel.err = ErrUnavailable
	if d, err := e.svc.Get(ctx, id.New(), owner); !errors.Is(err, ErrUnavailable) || d.Birthday != nil {
		t.Fatalf("core yok: %+v %v", d, err)
	}

	e.rel.err = nil
	if _, err := e.pool.Exec(ctx, `UPDATE users SET deleted_at = now(), display_name = NULL, birth_date_enc = NULL WHERE id = $1`, owner.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, id.New(), owner); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("silinmiş: %v", err)
	}
	pending := id.New()
	if _, err := e.pool.Exec(ctx, `INSERT INTO users (id, display_name) VALUES ($1, 'Kayıtsız')`, pending.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Get(ctx, id.New(), pending); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("kayıtsız: %v", err)
	}
}

// TestRelationMustMatchCaller: core's answer about "self" must agree with
// the caller; a mismatch is an error, not a disclosure.
func TestRelationMustMatchCaller(t *testing.T) {
	e := setup(t)
	owner := e.user(t)
	e.rel.rel = Relation{Self: true}
	if d, err := e.svc.Get(context.Background(), id.New(), owner); err == nil || d.Birthday != nil {
		t.Fatalf("uyuşmayan ilişki: %+v %v", d, err)
	}
}
