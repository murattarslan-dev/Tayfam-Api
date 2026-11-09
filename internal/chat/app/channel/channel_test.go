package channel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
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

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("hata = %v, %v bekleniyordu", err, want)
	}
}

// fakeMembers stands in for core's GetMembership.
type fakeMembers struct {
	mu sync.Mutex
	m  map[[2]id.ID]Member
}

func (f *fakeMembers) Member(_ context.Context, tayfa, user id.ID) (Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[[2]id.ID{tayfa, user}], nil
}

func (f *fakeMembers) set(tayfa, user id.ID, m Member) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[[2]id.ID{tayfa, user}] = m
}

type env struct {
	pool    *pgxpool.Pool
	svc     *Service
	members *fakeMembers
	logs    *bytes.Buffer
	shift   time.Duration
	tayfa   id.ID
	admin   id.ID
	mod     id.ID
	member  id.ID
	other   id.ID // another plain member
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, chatdb.Migrations())
	e := &env{
		pool: pool, members: &fakeMembers{m: map[[2]id.ID]Member{}}, logs: &bytes.Buffer{},
		tayfa: id.New(), admin: id.New(), mod: id.New(), member: id.New(), other: id.New(),
	}
	e.members.set(e.tayfa, e.admin, Member{Active: true, Role: RoleAdmin})
	e.members.set(e.tayfa, e.mod, Member{Active: true, Role: RoleModerator})
	e.members.set(e.tayfa, e.member, Member{Active: true, Role: RoleMember})
	e.members.set(e.tayfa, e.other, Member{Active: true, Role: RoleMember})
	svc, err := New(Config{
		Pool: pool, Log: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Members: e.members, Cipher: channelcrypt.Ephemeral(),
		Now: func() time.Time { return time.Now().Add(e.shift) },
	})
	must(t, err)
	e.svc = svc
	return e
}

func (e *env) channel(t *testing.T, in ChannelInput) Channel {
	t.Helper()
	c, err := e.svc.Create(ctx, e.mod, e.tayfa, in)
	must(t, err)
	return c
}

func (e *env) send(t *testing.T, user id.ID, ch id.ID, body string) Message {
	t.Helper()
	m, err := e.svc.Send(ctx, user, e.tayfa, ch, SendInput{Body: body, ClientRequestID: id.New()})
	must(t, err)
	return m
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func TestChannelManagement(t *testing.T) {
	e := setup(t)
	_, err := e.svc.Create(ctx, e.member, e.tayfa, ChannelInput{Name: "genel"})
	is(t, err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, id.New(), e.tayfa, ChannelInput{Name: "genel"})
	is(t, err, apperr.ErrTayfaNotFound)

	c := e.channel(t, ChannelInput{Name: "İzmir Gezisi"})
	if c.Visibility != VisibilityMembers || c.CreatedBy != e.mod || c.TayfaID != e.tayfa {
		t.Fatalf("kanal = %+v", c)
	}
	// Turkish case: "izmir gezisi" is the same name.
	_, err = e.svc.Create(ctx, e.admin, e.tayfa, ChannelInput{Name: "izmir gezisi"})
	is(t, err, apperr.ErrChannelNameTaken)
	// "IZMIR" lowers to "ızmır", a different name.
	e.channel(t, ChannelInput{Name: "IZMIR GEZISI"})

	for name, in := range map[string]ChannelInput{
		"name":               {Name: ""},
		"name ":              {Name: " genel"},
		"name  ":             {Name: "a  b"},
		"name   ":            {Name: strings.Repeat("ş", NameMax+1)},
		"name    ":           {Name: "a\x00b"},
		"visibility":         {Name: "x", Visibility: "public"},
		"slow_mode_seconds":  {Name: "x", SlowModeSeconds: 3601},
		"slow_mode_seconds ": {Name: "x", SlowModeSeconds: -1},
	} {
		var v *apperr.ValidationError
		if _, err := e.svc.Create(ctx, e.mod, e.tayfa, in); !errors.As(err, &v) || v.Field != strings.TrimSpace(name) {
			t.Errorf("%q: %v", name, err)
		}
	}
	e.channel(t, ChannelInput{Name: strings.Repeat("ş", NameMax), SlowModeSeconds: 3600})

	slow := int32(30)
	staff := VisibilityStaff
	u, err := e.svc.Update(ctx, e.admin, e.tayfa, c.ID, ChannelUpdate{SlowModeSeconds: &slow, Visibility: &staff})
	must(t, err)
	if u.SlowModeSeconds != 30 || u.Visibility != VisibilityStaff {
		t.Fatalf("güncelleme = %+v", u)
	}
	_, err = e.svc.Update(ctx, e.member, e.tayfa, c.ID, ChannelUpdate{SlowModeSeconds: &slow})
	is(t, err, apperr.ErrPermissionDenied)
	// The channel is staff now: a member does not even see it.
	_, err = e.svc.Get(ctx, e.member, e.tayfa, c.ID)
	is(t, err, apperr.ErrChannelNotFound)
	_, err = e.svc.Update(ctx, e.admin, e.tayfa, c.ID, ChannelUpdate{})
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "update_mask" {
		t.Fatalf("boş güncelleme: %v", err)
	}
	// Another tayfa's moderator cannot reach the channel by ID.
	tf2, mod2 := id.New(), id.New()
	e.members.set(tf2, mod2, Member{Active: true, Role: RoleModerator})
	_, err = e.svc.Update(ctx, mod2, tf2, c.ID, ChannelUpdate{SlowModeSeconds: &slow})
	is(t, err, apperr.ErrChannelNotFound)
	_, err = e.svc.Get(ctx, mod2, tf2, c.ID)
	is(t, err, apperr.ErrChannelNotFound)

	// Archive frees the name; unarchiving then fails.
	a, err := e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, true)
	must(t, err)
	if a.ArchivedAt.IsZero() {
		t.Fatal("arşivlenmedi")
	}
	e.channel(t, ChannelInput{Name: "izmir gezisi"})
	_, err = e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, false)
	is(t, err, apperr.ErrChannelNameTaken)

	// Changes are recorded with IDs only.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type IN ('channel.created', 'channel.updated') AND payload::text ILIKE '%izmir%'`); n != 0 {
		t.Fatal("olayda kanal adı var")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'channel.updated' AND subject_id = $1`, c.ID.PG()); n != 2 {
		t.Fatalf("%d güncelleme olayı", n)
	}
}

func TestStaffChannelVisibility(t *testing.T) {
	e := setup(t)
	pub := e.channel(t, ChannelInput{Name: "genel"})
	staff := e.channel(t, ChannelInput{Name: "yönetim", Visibility: VisibilityStaff})
	e.send(t, e.mod, staff.ID, "yalnızca yönetim")

	list, err := e.svc.List(ctx, e.member, e.tayfa)
	must(t, err)
	if len(list) != 1 || list[0].ID != pub.ID {
		t.Fatalf("üye listesi = %+v", list)
	}
	list, err = e.svc.List(ctx, e.admin, e.tayfa)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("yönetici listesi = %d", len(list))
	}
	_, err = e.svc.Get(ctx, e.member, e.tayfa, staff.ID)
	is(t, err, apperr.ErrChannelNotFound)
	_, _, err = e.svc.Messages(ctx, e.member, e.tayfa, staff.ID, ListInput{})
	is(t, err, apperr.ErrChannelNotFound)
	_, err = e.svc.Send(ctx, e.member, e.tayfa, staff.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelNotFound)

	// A moderator demoted in core loses the channel (after the cache TTL in
	// production; at once here).
	e.members.set(e.tayfa, e.mod, Member{Active: true, Role: RoleMember})
	_, _, err = e.svc.Messages(ctx, e.mod, e.tayfa, staff.ID, ListInput{})
	is(t, err, apperr.ErrChannelNotFound)
}

func TestSendIsEncryptedAndIdempotent(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	const body = "Cumartesi buluşma Kordon'da, saat 09:00"
	req := id.New()
	m, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: body, ClientRequestID: req})
	must(t, err)
	if m.Body != body || m.AuthorID != e.member || m.ChannelID != c.ID {
		t.Fatalf("mesaj = %+v", m)
	}
	// The database holds ciphertext only, with the key version.
	if n := e.count(t, `SELECT count(*) FROM channel_messages WHERE id = $1 AND key_version = 1 AND position('Kordon'::bytea IN body_encrypted) = 0`, m.ID.PG()); n != 1 {
		t.Fatal("veritabanında açık metin ya da sürümsüz kayıt")
	}
	// A retry returns the same message and writes nothing.
	again, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: "farklı", ClientRequestID: req})
	must(t, err)
	if again.ID != m.ID || again.Body != body {
		t.Fatalf("tekrar = %+v", again)
	}
	if n := e.count(t, `SELECT count(*) FROM channel_messages`); n != 1 {
		t.Fatalf("%d mesaj", n)
	}
	// The same request ID in another channel is refused.
	c2 := e.channel(t, ChannelInput{Name: "ikinci"})
	_, err = e.svc.Send(ctx, e.member, e.tayfa, c2.ID, SendInput{Body: body, ClientRequestID: req})
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Fatalf("başka kanal: %v", err)
	}

	list, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)
	if len(list) != 1 || list[0].Body != body {
		t.Fatalf("okunan = %+v", list)
	}
	// One event, IDs only.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'channel.message_created' AND subject_id = $1 AND actor_id = $2
		AND payload = jsonb_build_object('tayfa_id', $3::text, 'channel_id', $4::text)`, m.ID.PG(), e.member.PG(), e.tayfa.String(), c.ID.String()); n != 1 {
		t.Fatal("olay yok ya da beklenmeyen alan içeriyor")
	}
}

func TestSendValidationAndRules(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	for _, body := range []string{"", "   \n", strings.Repeat("a", BodyMax+1), "a\x00", "a\x1b[31m", string([]byte{0xff})} {
		var v *apperr.ValidationError
		if _, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: body, ClientRequestID: id.New()}); !errors.As(err, &v) || v.Field != "body" {
			t.Errorf("gövde %q: %v", body, err)
		}
	}
	e.send(t, e.member, c.ID, strings.Repeat("ğ", BodyMax))
	e.send(t, e.member, c.ID, "satır 1\nsatır 2\tsekme")
	var v *apperr.ValidationError
	if _, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: "x"}); !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Errorf("istek kimliği yok: %v", err)
	}

	// Not a member, pending (not active), another tayfa.
	_, err := e.svc.Send(ctx, id.New(), e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrTayfaNotFound)
	tf2, u2 := id.New(), id.New()
	e.members.set(tf2, u2, Member{Active: true, Role: RoleMember})
	_, err = e.svc.Send(ctx, u2, tf2, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelNotFound)

	// Muted: reads, cannot write; a retry of an earlier message still
	// returns it.
	req := id.New()
	_, err = e.svc.Send(ctx, e.other, e.tayfa, c.ID, SendInput{Body: "önce", ClientRequestID: req})
	must(t, err)
	e.members.set(e.tayfa, e.other, Member{Active: true, Role: RoleMember, MutedUntil: time.Now().Add(time.Hour)})
	_, err = e.svc.Send(ctx, e.other, e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrMuted)
	_, err = e.svc.Send(ctx, e.other, e.tayfa, c.ID, SendInput{Body: "önce", ClientRequestID: req})
	must(t, err)
	_, _, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)

	// Archived: no new messages, even from admins.
	_, err = e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, true)
	must(t, err)
	_, err = e.svc.Send(ctx, e.admin, e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelArchived)
}

func TestSlowModeInSend(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "yavaş", SlowModeSeconds: 60})
	e.send(t, e.member, c.ID, "bir")
	_, err := e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: "iki", ClientRequestID: id.New()})
	var slow *apperr.SlowModeError
	if !errors.As(err, &slow) || slow.RetryAfter <= 0 || slow.RetryAfter > time.Minute {
		t.Fatalf("yavaş mod: %v", err)
	}
	// Other members and moderators are not held back.
	e.send(t, e.other, c.ID, "başka üye")
	e.send(t, e.mod, c.ID, "moderatör 1")
	e.send(t, e.mod, c.ID, "moderatör 2")
	// A refused send (archived) does not use up the slot.
	e.shift = time.Minute
	_, err = e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, true)
	must(t, err)
	_, err = e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelArchived)
	_, err = e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, false)
	must(t, err)
	e.send(t, e.member, c.ID, "bir dakika sonra")
}

func TestPaging(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	var sent []Message
	for i := range 5 {
		sent = append(sent, e.send(t, e.member, c.ID, strings.Repeat("m", i+1)))
	}
	page, next, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{PageSize: 2})
	must(t, err)
	if len(page) != 2 || page[0].ID != sent[4].ID || next != sent[3].ID.String() {
		t.Fatalf("ilk sayfa: %d %q", len(page), next)
	}
	page, next, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{PageSize: 2, PageToken: next})
	must(t, err)
	if len(page) != 2 || page[0].ID != sent[2].ID {
		t.Fatal("ikinci sayfa")
	}
	page, next, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{PageSize: 2, PageToken: next})
	must(t, err)
	if len(page) != 1 || next != "" {
		t.Fatalf("son sayfa: %d %q", len(page), next)
	}

	// Catching up from sent[1]: oldest first.
	page, next, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{After: sent[1].ID, PageSize: 2})
	must(t, err)
	if len(page) != 2 || page[0].ID != sent[2].ID || page[1].ID != sent[3].ID || next != sent[3].ID.String() {
		t.Fatal("sonrası, ilk sayfa")
	}
	page, next, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{After: sent[1].ID, PageSize: 2, PageToken: next})
	must(t, err)
	if len(page) != 1 || page[0].ID != sent[4].ID || next != "" {
		t.Fatal("sonrası, son sayfa")
	}
	// A token older than after does not go back.
	var v *apperr.ValidationError
	if _, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{After: sent[3].ID, PageToken: sent[0].ID.String()}); !errors.As(err, &v) || v.Field != "page_token" {
		t.Fatalf("geri giden imleç: %v", err)
	}
	for _, in := range []ListInput{{PageSize: -1}, {PageToken: "x"}} {
		if _, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, in); !errors.As(err, &v) {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestDeleteAndRestore(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	own := e.send(t, e.member, c.ID, "kendi mesajım")
	bad := e.send(t, e.member, c.ID, "kural dışı")

	// A member cannot delete someone else's message.
	is(t, e.svc.Delete(ctx, e.other, e.tayfa, c.ID, own.ID), apperr.ErrPermissionDenied)
	is(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, id.New()), apperr.ErrMessageNotFound)

	// The author's own deletion erases the body at once; it cannot be
	// restored.
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, own.ID))
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, own.ID))
	if n := e.count(t, `SELECT count(*) FROM channel_messages WHERE id = $1 AND body_encrypted IS NULL AND key_version IS NULL AND deleted_by = $2`, own.ID.PG(), e.member.PG()); n != 1 {
		t.Fatal("yazar silmesi metni bıraktı")
	}
	_, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, own.ID)
	is(t, err, apperr.ErrMessageNotFound)

	// A moderator hides; members see a deleted message without body; only
	// admins see it as restorable.
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, bad.ID))
	for _, u := range []id.ID{e.member, e.mod, e.admin} {
		list, _, err := e.svc.Messages(ctx, u, e.tayfa, c.ID, ListInput{})
		must(t, err)
		for _, m := range list {
			if !m.Deleted || m.Body != "" {
				t.Fatalf("silinen mesaj görünüyor: %+v", m)
			}
			if want := u == e.admin && m.ID == bad.ID; m.Restorable != want {
				t.Fatalf("geri alınabilir = %v (kullanıcı %v)", m.Restorable, u == e.admin)
			}
		}
	}
	_, err = e.svc.Restore(ctx, e.mod, e.tayfa, c.ID, bad.ID)
	is(t, err, apperr.ErrPermissionDenied)
	r, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, bad.ID)
	must(t, err)
	if r.Deleted || r.Body != "kural dışı" {
		t.Fatalf("geri alınan = %+v", r)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'channel.message_restored' AND actor_id = $1 AND subject_id = $2 AND payload->>'deleted_by' = $3`,
		e.admin.PG(), bad.ID.PG(), e.mod.String()); n != 1 {
		t.Fatal("geri alma kaydı yok")
	}
	// Restoring a visible message changes nothing.
	_, err = e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, bad.ID)
	must(t, err)

	// The restore window.
	must(t, e.svc.Delete(ctx, e.admin, e.tayfa, c.ID, bad.ID))
	e.shift = RestoreWindow + time.Minute
	_, err = e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, bad.ID)
	is(t, err, apperr.ErrMessageNotFound)

	// The author may still erase a hidden message.
	e.shift = 0
	hidden := e.send(t, e.member, c.ID, "gizlenecek")
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, hidden.ID))
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, hidden.ID))
	if n := e.count(t, `SELECT count(*) FROM channel_messages WHERE id = $1 AND body_encrypted IS NULL`, hidden.ID.PG()); n != 1 {
		t.Fatal("yazar gizlenen mesajı silemedi")
	}

	// A muted member may still delete their own message, not others'.
	mine := e.send(t, e.other, c.ID, "susturulmadan önce")
	e.members.set(e.tayfa, e.mod, Member{Active: true, Role: RoleModerator, MutedUntil: time.Now().Add(time.Hour)})
	e.members.set(e.tayfa, e.other, Member{Active: true, Role: RoleMember, MutedUntil: time.Now().Add(time.Hour)})
	must(t, e.svc.Delete(ctx, e.other, e.tayfa, c.ID, mine.ID))
	another := e.send(t, e.member, c.ID, "bir mesaj daha")
	is(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, another.ID), apperr.ErrMuted)
}

func TestPurgeDeleted(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	old := e.send(t, e.member, c.ID, "eski")
	fresh := e.send(t, e.member, c.ID, "yeni")
	keep := e.send(t, e.member, c.ID, "kalıcı")
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, old.ID))
	e.shift = 10 * 24 * time.Hour
	must(t, e.svc.Delete(ctx, e.member, e.tayfa, c.ID, fresh.ID))
	e.shift = RestoreWindow + time.Hour
	n, err := e.svc.PurgeDeleted(ctx)
	must(t, err)
	if n != 1 {
		t.Fatalf("%d mesaj silindi", n)
	}
	if e.count(t, `SELECT count(*) FROM channel_messages WHERE id = $1`, old.ID.PG()) != 0 ||
		e.count(t, `SELECT count(*) FROM channel_messages WHERE id IN ($1, $2)`, fresh.ID.PG(), keep.ID.PG()) != 2 {
		t.Fatal("yanlış mesajlar silindi")
	}
}

func TestReadMarkersAndUnread(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	c2 := e.channel(t, ChannelInput{Name: "ikinci"})
	var last Message
	for range MaxUnread + 5 {
		last = e.send(t, e.member, c.ID, "x")
	}
	e.send(t, e.other, c.ID, "kendi mesajım sayılmaz")

	unread := func() Channel {
		t.Helper()
		list, err := e.svc.List(ctx, e.other, e.tayfa)
		must(t, err)
		for _, ch := range list {
			if ch.ID == c.ID {
				return ch
			}
		}
		t.Fatal("kanal yok")
		return Channel{}
	}
	if got := unread(); got.Unread != MaxUnread || got.LastRead != (id.ID{}) {
		t.Fatalf("okunmamış = %d", got.Unread)
	}
	must(t, e.svc.MarkRead(ctx, e.other, e.tayfa, c.ID, last.ID))
	if got := unread(); got.Unread != 0 || got.LastRead != last.ID {
		t.Fatalf("okunduktan sonra = %+v", got)
	}
	// A message of another channel is not a valid marker there.
	m2 := e.send(t, e.member, c2.ID, "x")
	is(t, e.svc.MarkRead(ctx, e.other, e.tayfa, c.ID, m2.ID), apperr.ErrMessageNotFound)
	// Muted members still mark read.
	e.members.set(e.tayfa, e.other, Member{Active: true, Role: RoleMember, MutedUntil: time.Now().Add(time.Hour)})
	must(t, e.svc.MarkRead(ctx, e.other, e.tayfa, c2.ID, m2.ID))
}

func TestCleanupKeepsContent(t *testing.T) {
	e := setup(t)
	c := e.channel(t, ChannelInput{Name: "genel"})
	m := e.send(t, e.member, c.ID, "hesabım silinecek")
	must(t, e.svc.MarkRead(ctx, e.member, e.tayfa, c.ID, m.ID))
	must(t, Cleanup(ctx, store.New(e.pool), e.member))
	list, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	must(t, err)
	if len(list) != 1 || list[0].AuthorID != (id.ID{}) || list[0].Body != "hesabım silinecek" {
		t.Fatalf("hesap silme sonrası = %+v", list)
	}
	if e.count(t, `SELECT count(*) FROM channel_reads WHERE user_id = $1`, e.member.PG()) != 0 {
		t.Fatal("okunma kaydı kaldı")
	}
	if e.count(t, `SELECT count(*) FROM channels WHERE created_by = $1`, e.mod.PG()) != 1 {
		t.Fatal("başkasının kaydı değişti")
	}
}

// A row moved to another channel no longer decrypts (AAD).
func TestMovedRowDoesNotDecrypt(t *testing.T) {
	e := setup(t)
	a := e.channel(t, ChannelInput{Name: "a"})
	b := e.channel(t, ChannelInput{Name: "b"})
	m := e.send(t, e.member, a.ID, "taşınamaz")
	_, err := e.pool.Exec(ctx, `UPDATE channel_messages SET channel_id = $1 WHERE id = $2`, b.ID.PG(), m.ID.PG())
	must(t, err)
	_, _, err = e.svc.Messages(ctx, e.member, e.tayfa, b.ID, ListInput{})
	is(t, err, channelcrypt.ErrDecrypt)
}

func TestWithoutKeyEverythingIsUnavailable(t *testing.T) {
	e := setup(t)
	svc, err := New(Config{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Members: e.members})
	must(t, err)
	_, err = svc.List(ctx, e.admin, e.tayfa)
	is(t, err, apperr.ErrChannelsUnavailable)
	_, err = svc.Create(ctx, e.admin, e.tayfa, ChannelInput{Name: "x"})
	is(t, err, apperr.ErrChannelsUnavailable)
	_, err = svc.Send(ctx, e.admin, e.tayfa, id.New(), SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelsUnavailable)
	// The purge job needs no key.
	_, err = svc.PurgeDeleted(ctx)
	must(t, err)
}

// No body text reaches logs or events, whatever happens (CLAUDE.md rule 3).
func TestBodyNeverLeaves(t *testing.T) {
	e := setup(t)
	const secret = "ÇokGizliMesajMetni"
	c := e.channel(t, ChannelInput{Name: "genel", SlowModeSeconds: 60})
	m := e.send(t, e.member, c.ID, secret)
	_, _ = e.svc.Send(ctx, e.member, e.tayfa, c.ID, SendInput{Body: secret + "2", ClientRequestID: id.New()}) // slow mode
	_, _ = e.svc.Send(ctx, id.New(), e.tayfa, c.ID, SendInput{Body: secret, ClientRequestID: id.New()})       // not a member
	must(t, e.svc.Delete(ctx, e.mod, e.tayfa, c.ID, m.ID))
	_, err := e.svc.Restore(ctx, e.admin, e.tayfa, c.ID, m.ID)
	must(t, err)
	e.shift = RestoreWindow * 2
	_, _ = e.svc.PurgeDeleted(ctx)
	if strings.Contains(e.logs.String(), "Gizli") {
		t.Fatal("log mesaj metni içeriyor")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%Gizli%'`); n != 0 {
		t.Fatal("olay mesaj metni içeriyor")
	}
}
