package library_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/library/lending"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"  Kamp   ocağı ":    "Kamp ocağı",
		"Çadır\n(4 kişilik)": "Çadır (4 kişilik)",
	} {
		if got, ok := library.CleanName(in, library.NameMax); !ok || got != want {
			t.Errorf("CleanName(%q) = %q %v, %q bekleniyordu", in, got, ok, want)
		}
	}
	for name, in := range map[string]string{
		"boş":             "  \t ",
		"denetim kodu":    "Çadır\x00",
		"görünmez işaret": "Ça\u200bdır",
		"geçersiz UTF-8":  "Ça\xffdır",
		"81 karakter":     strings.Repeat("ş", 81),
	} {
		if _, ok := library.CleanName(in, library.NameMax); ok {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	if _, ok := library.CleanName(strings.Repeat("k", 61), library.BookNameMax); ok {
		t.Error("kitapta 61 karakter kabul edildi")
	}
}

func TestCleanNote(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"Kılıfıyla\r\n\r\n\r\nverilir": "Kılıfıyla\n\nverilir",
	} {
		if got, ok := library.CleanNote(in); !ok || got != want {
			t.Errorf("CleanNote(%q) = %q %v, %q bekleniyordu", in, got, ok, want)
		}
	}
	for _, in := range []string{"a\x07b", "a\u202eb", strings.Repeat("a", 301)} {
		if _, ok := library.CleanNote(in); ok {
			t.Errorf("CleanNote(%q) kabul edildi", in)
		}
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }

type env struct {
	svc     *library.Service
	tayfas  *tayfa.Service
	members *membership.Service
	pool    *pgxpool.Pool
	admin   *pgxpool.Pool
	clock   *clock
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
		t.Errorf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func wantField(t *testing.T, what string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Errorf("%s: %v, %q alanında doğrulama hatası bekleniyordu", what, err, field)
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &clock{t: time.Date(2026, time.November, 21, 9, 0, 0, 0, time.UTC)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: c.now})
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	ms, err := membership.New(membership.Config{Pool: d.App, JoinLimiter: allowAll{}, Now: c.now})
	must(t, err)
	return &env{svc: library.New(fw), tayfas: tayfas, members: ms, pool: d.App, admin: d.Admin, clock: c}
}

// exec runs a fixture statement as a superuser with triggers off.
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
	must(t, e.admin.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Kamp Tayfası", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID, admin
}

func (e *env) member(t *testing.T, tf id.ID, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.admin.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	must(t, store.New(tx).CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.PG(), UserID: u.PG(), Role: role.String(), Status: "active", JoinedVia: "code",
		JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return u
}

func (e *env) add(t *testing.T, owner, tf id.ID, kind, name string) library.Item {
	t.Helper()
	it, err := e.svc.Create(ctx, owner, tf, library.Input{Kind: kind, Name: name}, id.New().String())
	must(t, err)
	return it
}

func (e *env) item(t *testing.T, user, tf, item id.ID) library.Item {
	t.Helper()
	it, err := e.svc.Get(ctx, user, tf, item)
	must(t, err)
	return it
}

// notices returns the recipients of the library notifications of typ.
func (e *env) notices(t *testing.T, typ string) []string {
	t.Helper()
	rows, err := e.admin.Query(ctx, `SELECT r FROM domain_events d, jsonb_array_elements_text(d.payload -> 'recipient_ids') AS r
		WHERE d.type = 'notification.requested' AND d.payload ->> 'type' = $1 ORDER BY r`, typ)
	must(t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	return out
}

// TestLoanFlow: request → approve → hand over → take back, the queue
// behind an active loan, who sees what.
func TestLoanFlow(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	b := e.member(t, tf, authz.Member)
	c := e.member(t, tf, authz.Member)

	tent, err := e.svc.Create(ctx, owner, tf, library.Input{Kind: library.KindCampingGear, Name: " 4 kişilik  çadır ", Note: "Kazıklar\nkılıfında"}, id.New().String())
	must(t, err)
	if tent.Name != "4 kişilik çadır" || tent.Note != "Kazıklar\nkılıfında" || tent.Owner != owner || tent.Status != library.StatusAvailable {
		t.Fatalf("kalem: %+v", tent)
	}

	// A asks first and gets the loan request; B waits in the queue; asking
	// again changes nothing.
	got, err := e.svc.Request(ctx, a, tf, tent.ID)
	must(t, err)
	if got.ActiveLoan == nil || got.ActiveLoan.State != library.StateRequested || got.ActiveLoan.Borrower != a || got.ActiveLoan.CallerIsOwner {
		t.Fatalf("A'nın isteği: %+v", got.ActiveLoan)
	}
	_, err = e.svc.Request(ctx, a, tf, tent.ID)
	must(t, err)
	got, err = e.svc.Request(ctx, b, tf, tent.ID)
	must(t, err)
	if got.ActiveLoan != nil || got.CallerQueueRank != 1 || got.QueueLength != 1 {
		t.Fatalf("B sırada: %+v", got)
	}
	_, err = e.svc.Request(ctx, owner, tf, tent.ID)
	wantErr(t, "kendi kalemi", err, apperr.ErrLibraryOwnItem)

	// Only the two sides see the loan; the owner sees the queue too.
	if v := e.item(t, c, tf, tent.ID); v.ActiveLoan != nil || v.Queue != nil || v.QueueLength != 1 || v.CallerQueueRank != 0 {
		t.Fatalf("üçüncü üye: %+v", v)
	}
	ov := e.item(t, owner, tf, tent.ID)
	if ov.ActiveLoan == nil || !ov.ActiveLoan.CallerIsOwner || len(ov.Queue) != 1 || ov.Queue[0].User != b || ov.Queue[0].Rank != 1 {
		t.Fatalf("sahibin görünümü: %+v", ov)
	}
	loan := ov.ActiveLoan.ID

	// The owner's steps, in order; others are refused.
	_, err = e.svc.Approve(ctx, a, tf, loan)
	wantErr(t, "borç alan onaylayamaz", err, apperr.ErrLibraryLoanNotFound)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, time.Time{})
	wantErr(t, "onaysız teslim", err, apperr.ErrLibraryLoanState)
	l, err := e.svc.Approve(ctx, owner, tf, loan)
	must(t, err)
	if l.State != library.StateApproved || l.ApprovedAt.IsZero() {
		t.Fatalf("onay: %+v", l)
	}
	_, err = e.svc.Approve(ctx, owner, tf, loan)
	wantErr(t, "iki kez onay", err, apperr.ErrLibraryLoanState)
	now := e.clock.now()
	_, err = e.svc.HandOver(ctx, owner, tf, loan, now.Add(91*24*time.Hour))
	wantField(t, "91 gün", err, library.FieldDueTime)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, now.Add(-time.Hour))
	wantField(t, "geçmiş tarih", err, library.FieldDueTime)
	due := now.Add(14 * 24 * time.Hour)
	l, err = e.svc.HandOver(ctx, owner, tf, loan, due)
	must(t, err)
	if l.State != library.StateHandedOver || !l.DueAt.Equal(due) {
		t.Fatalf("teslim: %+v", l)
	}
	if v := e.item(t, c, tf, tent.ID); v.Status != library.StatusOnLoan {
		t.Fatalf("ödünçte: %+v", v)
	}
	wantErr(t, "teslimdeyken geri çekme", e.svc.Withdraw(ctx, owner, tf, tent.ID), apperr.ErrLibraryItemOnLoan)
	_, err = e.svc.Cancel(ctx, a, tf, tent.ID)
	wantErr(t, "teslimden sonra iptal", err, apperr.ErrLibraryLoanState)

	// Taking it back: the item is available and B gets the request.
	l, err = e.svc.Return(ctx, owner, tf, loan)
	must(t, err)
	if l.State != library.StateReturned || l.EndedAt.IsZero() {
		t.Fatalf("iade: %+v", l)
	}
	bv := e.item(t, b, tf, tent.ID)
	if bv.Status != library.StatusAvailable || bv.ActiveLoan == nil || bv.ActiveLoan.Borrower != b || bv.QueueLength != 0 || bv.CallerQueueRank != 0 {
		t.Fatalf("sıradaki B: %+v", bv)
	}

	// B takes it back; nobody waits any more.
	bv, err = e.svc.Cancel(ctx, b, tf, tent.ID)
	must(t, err)
	if bv.ActiveLoan != nil {
		t.Fatalf("iptal: %+v", bv)
	}
	// Each side's history; the third member has none.
	la, err := e.svc.Loans(ctx, a, tf)
	must(t, err)
	lo, err := e.svc.Loans(ctx, owner, tf)
	must(t, err)
	lc, err := e.svc.Loans(ctx, c, tf)
	must(t, err)
	if len(la) != 1 || la[0].State != library.StateReturned || len(lo) != 2 || len(lc) != 0 {
		t.Fatalf("geçmiş: A %+v, sahip %+v, C %+v", la, lo, lc)
	}
	// Decline lets the next one in too.
	_, err = e.svc.Request(ctx, a, tf, tent.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, c, tf, tent.ID)
	must(t, err)
	ov = e.item(t, owner, tf, tent.ID)
	l, err = e.svc.Decline(ctx, owner, tf, ov.ActiveLoan.ID)
	must(t, err)
	if l.State != library.StateDeclined {
		t.Fatalf("ret: %+v", l)
	}
	if v := e.item(t, c, tf, tent.ID); v.ActiveLoan == nil || v.ActiveLoan.Borrower != c {
		t.Fatalf("retten sonra sıradaki: %+v", v)
	}
}

// TestBooks: a book's name is the tayfa vocabulary's.
func TestBooks(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	m := e.member(t, tf, authz.Member)
	first := e.add(t, owner, tf, library.KindBook, "Kürk Mantolu Madonna")
	second := e.add(t, m, tf, library.KindBook, " kürk  mantolu madonna ")
	if first.VocabularyID == (id.ID{}) || second.VocabularyID != first.VocabularyID || second.Name != "Kürk Mantolu Madonna" {
		t.Fatalf("sözlük: %+v / %+v", first, second)
	}
	gear := e.add(t, owner, tf, library.KindTool, "Matkap")
	if gear.VocabularyID != (id.ID{}) {
		t.Fatal("alet sözlüğe bağlandı")
	}
	_, err := e.svc.Create(ctx, owner, tf, library.Input{Kind: library.KindBook, Name: strings.Repeat("k", 61)}, id.New().String())
	wantField(t, "uzun kitap adı", err, library.FieldName)
	l, err := e.svc.List(ctx, m, tf, library.KindBook, false)
	must(t, err)
	if len(l.Items) != 2 || !l.CallerCanAdd {
		t.Fatalf("kitaplar: %+v", l)
	}
	l, err = e.svc.List(ctx, m, tf, "", true)
	must(t, err)
	if len(l.Items) != 1 || l.Items[0].ID != second.ID {
		t.Fatalf("benimkiler: %+v", l)
	}
	_, err = e.svc.List(ctx, m, tf, "weapon", false)
	wantField(t, "bilinmeyen tür", err, library.FieldKind)
}

// TestPermissions: membership, ownership, moderation, mute, module, limits.
func TestPermissions(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	mod := e.member(t, tf, authz.Moderator)
	m := e.member(t, tf, authz.Member)
	stranger := id.New()

	it := e.add(t, owner, tf, library.KindBoardGame, "Catan")
	_, err := e.svc.List(ctx, stranger, tf, "", false)
	wantErr(t, "üye olmayan", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.Update(ctx, m, tf, it.ID, library.Input{Name: "x"})
	wantErr(t, "başkasının kalemi", err, apperr.ErrLibraryItemNotFound)
	wantErr(t, "üye kaldıramaz", e.svc.Withdraw(ctx, m, tf, it.ID), apperr.ErrPermissionDenied)

	// Validation.
	for field, in := range map[string]library.Input{
		library.FieldKind: {Kind: "weapon", Name: "x"},
		library.FieldName: {Kind: library.KindOther, Name: " "},
		library.FieldNote: {Kind: library.KindOther, Name: "x", Note: strings.Repeat("a", 301)},
	} {
		_, err := e.svc.Create(ctx, m, tf, in, id.New().String())
		wantField(t, field, err, field)
	}
	_, err = e.svc.Create(ctx, m, tf, library.Input{Kind: library.KindOther, Name: "x"}, "x")
	wantField(t, "istek kimliği", err, library.FieldClientRequestID)
	crid := id.New().String()
	one, err := e.svc.Create(ctx, m, tf, library.Input{Kind: library.KindOther, Name: "Termos"}, crid)
	must(t, err)
	again, err := e.svc.Create(ctx, m, tf, library.Input{Kind: library.KindOther, Name: "Başka"}, crid)
	must(t, err)
	if again.ID != one.ID {
		t.Fatal("tekrar gönderim yeni kalem ekledi")
	}
	up, err := e.svc.Update(ctx, m, tf, one.ID, library.Input{Name: "Çelik termos", Note: "1 litre"})
	must(t, err)
	if up.Name != "Çelik termos" || up.Note != "1 litre" || up.Kind != library.KindOther {
		t.Fatalf("düzenleme: %+v", up)
	}

	// A muted member neither adds nor asks, but takes back and lends.
	_, err = e.svc.Request(ctx, m, tf, it.ID)
	must(t, err)
	e.exec(t, `UPDATE memberships SET muted_until = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), m.PG(),
		pgtype.Timestamptz{Time: e.clock.now().Add(time.Hour), Valid: true})
	_, err = e.svc.Create(ctx, m, tf, library.Input{Kind: library.KindOther, Name: "y"}, id.New().String())
	wantErr(t, "susturulmuş ekleme", err, apperr.ErrMuted)
	_, err = e.svc.Request(ctx, m, tf, it.ID)
	wantErr(t, "susturulmuş istek", err, apperr.ErrMuted)
	_, err = e.svc.Cancel(ctx, m, tf, it.ID)
	must(t, err)

	// A moderator removes anyone's item; it is gone for everyone.
	must(t, e.svc.Withdraw(ctx, mod, tf, it.ID))
	_, err = e.svc.Get(ctx, owner, tf, it.ID)
	wantErr(t, "kaldırılan", err, apperr.ErrLibraryItemNotFound)
	wantErr(t, "iki kez", e.svc.Withdraw(ctx, mod, tf, it.ID), apperr.ErrLibraryItemNotFound)
	// The owner withdraws their own, muted or not.
	must(t, e.svc.Withdraw(ctx, m, tf, one.ID))
	if n := e.count(t, `SELECT count(*) FROM library_items WHERE id = $1 AND note IS NULL AND withdrawn_by = $2`, one.ID.PG(), m.PG()); n != 1 {
		t.Fatal("geri çekilen kalemin notu silinmedi")
	}

	// The queue holds at most MaxQueue.
	big := e.add(t, owner, tf, library.KindTool, "Jeneratör")
	_, err = e.svc.Request(ctx, mod, tf, big.ID)
	must(t, err)
	for range library.MaxQueue {
		_, err := e.svc.Request(ctx, e.member(t, tf, authz.Member), tf, big.ID)
		must(t, err)
	}
	_, err = e.svc.Request(ctx, e.member(t, tf, authz.Member), tf, big.ID)
	wantErr(t, "dolu sıra", err, apperr.ErrLibraryQueueFull)

	// At most MaxItemsPerOwner live items.
	e.exec(t, `INSERT INTO library_items (id, tayfa_id, owner_id, kind, name, client_request_id, created_at, updated_at)
		SELECT gen_random_uuid(), $1, $2, 'other', 'x', gen_random_uuid(), now(), now() FROM generate_series(1, $3::int)`,
		tf.PG(), mod.PG(), library.MaxItemsPerOwner)
	_, err = e.svc.Create(ctx, mod, tf, library.Input{Kind: library.KindOther, Name: "fazla"}, id.New().String())
	wantErr(t, "kalem sınırı", err, apperr.ErrLibraryItemLimit)

	// The module off: every method is refused.
	e.exec(t, `UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'library'`, tf.PG())
	_, err = e.svc.List(ctx, owner, tf, "", false)
	wantErr(t, "modül kapalı", err, apperr.ErrModuleDisabled)
	_, err = e.svc.Request(ctx, mod, tf, big.ID)
	wantErr(t, "modül kapalı, istek", err, apperr.ErrModuleDisabled)
}

// TestDeparture: a member who leaves (ADR-0040 §1): their items are
// withdrawn, waiting requests end, a handed-over loan stays open with a
// warning to both sides and closes after 30 days; the owner may still
// take it back.
func TestDeparture(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	owner := e.member(t, tf, authz.Member)
	borrower := e.member(t, tf, authz.Member)
	waiter := e.member(t, tf, authz.Member)

	stove := e.add(t, owner, tf, library.KindCampingGear, "Kamp ocağı")
	lamp := e.add(t, owner, tf, library.KindCampingGear, "Fener")
	mine := e.add(t, admin, tf, library.KindTool, "Testere")

	// The stove is handed over to the borrower; the waiter queues for it
	// and asks for the lamp; the borrower also asked for the admin's saw.
	_, err := e.svc.Request(ctx, borrower, tf, stove.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, waiter, tf, stove.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, waiter, tf, lamp.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, borrower, tf, mine.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, waiter, tf, mine.ID)
	must(t, err)
	loan := e.item(t, owner, tf, stove.ID).ActiveLoan.ID
	_, err = e.svc.Approve(ctx, owner, tf, loan)
	must(t, err)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, time.Time{})
	must(t, err)

	// The borrower leaves: their saw request ends (the waiter gets it), the
	// stove stays with them, open, and both sides are warned.
	must(t, e.members.LeaveTayfa(ctx, borrower, tf))
	if v := e.item(t, waiter, tf, mine.ID); v.ActiveLoan == nil || v.ActiveLoan.Borrower != waiter {
		t.Fatalf("testere sıradakine geçmedi: %+v", v)
	}
	// The waiter hears that their turn came (issue #332).
	if got := e.notices(t, "library_queue_turn"); !slices.Equal(got, []string{waiter.String()}) {
		t.Fatalf("sıra sende alıcıları: %v", got)
	}
	l, err := e.svc.Loans(ctx, owner, tf)
	must(t, err)
	var open []library.Loan
	for _, x := range l {
		if x.Item == stove.ID {
			open = append(open, x)
		}
	}
	if len(open) != 1 || open[0].State != library.StateHandedOver || open[0].OrphanedAt.IsZero() {
		t.Fatalf("açık ödünç: %+v", l)
	}
	want := []string{borrower.String(), owner.String()}
	slices.Sort(want)
	if got := e.notices(t, "library_loan_orphaned"); !slices.Equal(got, want) {
		t.Fatalf("uyarı alıcıları: %v, %v bekleniyordu", got, want)
	}

	// The owner leaves too: their items are withdrawn, the waiter's lamp
	// request and stove place end.
	must(t, e.members.LeaveTayfa(ctx, owner, tf))
	l2, err := e.svc.List(ctx, waiter, tf, "", false)
	must(t, err)
	if len(l2.Items) != 1 || l2.Items[0].ID != mine.ID {
		t.Fatalf("geri çekilmedi: %+v", l2.Items)
	}
	if n := e.count(t, `SELECT count(*) FROM library_loans WHERE item_id = $1 AND state = 'cancelled'`, lamp.ID.PG()); n != 1 {
		t.Fatalf("fener isteği iptal edilmedi: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM library_queue WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), waiter.PG()); n != 0 {
		t.Fatalf("sıra yeri kaldı: %d", n)
	}

	// 30 days later the open loan closes; the next pass finds nothing.
	run := func(now time.Time) int {
		t.Helper()
		var n int
		must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			n, err = lending.CloseOrphaned(ctx, store.New(tx), now, 100)
			return err
		}))
		return n
	}
	if n := run(e.clock.now().Add(lending.OrphanWindow - time.Minute)); n != 0 {
		t.Fatalf("erken kapandı: %d", n)
	}
	if n := run(e.clock.now().Add(lending.OrphanWindow)); n != 1 {
		t.Fatalf("kapanmadı: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM library_loans WHERE id = $1 AND state = 'closed' AND ended_at IS NOT NULL`, loan.PG()); n != 1 {
		t.Fatal("kapanan ödünç")
	}
	if n := run(e.clock.now().Add(2 * lending.OrphanWindow)); n != 0 {
		t.Fatalf("tekrar kapandı: %d", n)
	}
}

// TestReturnAfterBorrowerLeft: the owner still takes the item back.
func TestReturnAfterBorrowerLeft(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	borrower := e.member(t, tf, authz.Member)
	it := e.add(t, owner, tf, library.KindTool, "Merdiven")
	_, err := e.svc.Request(ctx, borrower, tf, it.ID)
	must(t, err)
	loan := e.item(t, owner, tf, it.ID).ActiveLoan.ID
	_, err = e.svc.Approve(ctx, owner, tf, loan)
	must(t, err)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, time.Time{})
	must(t, err)
	must(t, e.members.LeaveTayfa(ctx, borrower, tf))
	l, err := e.svc.Return(ctx, owner, tf, loan)
	must(t, err)
	if l.State != library.StateReturned || l.OrphanedAt.IsZero() {
		t.Fatalf("iade: %+v", l)
	}
	if v := e.item(t, owner, tf, it.ID); v.Status != library.StatusAvailable {
		t.Fatalf("durum: %+v", v)
	}
}

// TestDueReminder: one in-app reminder to the borrower once the return
// date passed.
func TestDueReminder(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	borrower := e.member(t, tf, authz.Member)
	it := e.add(t, owner, tf, library.KindBook, "Simyacı")
	_, err := e.svc.Request(ctx, borrower, tf, it.ID)
	must(t, err)
	loan := e.item(t, owner, tf, it.ID).ActiveLoan.ID
	_, err = e.svc.Approve(ctx, owner, tf, loan)
	must(t, err)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, e.clock.now().Add(48*time.Hour))
	must(t, err)
	remind := func(now time.Time) int {
		t.Helper()
		var n int
		must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			var err error
			n, err = lending.RemindDue(ctx, store.New(tx), now, 100)
			return err
		}))
		return n
	}
	if n := remind(e.clock.now().Add(47 * time.Hour)); n != 0 {
		t.Fatalf("erken hatırlatma: %d", n)
	}
	if n := remind(e.clock.now().Add(49 * time.Hour)); n != 1 {
		t.Fatalf("hatırlatma: %d", n)
	}
	if n := remind(e.clock.now().Add(72 * time.Hour)); n != 0 {
		t.Fatalf("ikinci hatırlatma: %d", n)
	}
	if got := e.notices(t, "library_due"); !slices.Equal(got, []string{borrower.String()}) {
		t.Fatalf("alıcı: %v", got)
	}
}

// TestChecks: the database refuses what the app would never write.
func TestChecks(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	it := e.add(t, owner, tf, library.KindTool, "Pense")
	for name, sql := range map[string]string{
		"tür":             `INSERT INTO library_items (id, tayfa_id, owner_id, kind, name, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, $2, 'weapon', 'x', gen_random_uuid(), now(), now())`,
		"sözlüksüz kitap": `INSERT INTO library_items (id, tayfa_id, owner_id, kind, name, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, $2, 'book', 'x', gen_random_uuid(), now(), now())`,
		"denetim kodu":    `INSERT INTO library_items (id, tayfa_id, owner_id, kind, name, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, $2, 'tool', E'a\tb', gen_random_uuid(), now(), now())`,
		"sahipsiz":        `INSERT INTO library_items (id, tayfa_id, kind, name, client_request_id, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'tool', 'x', gen_random_uuid(), now(), now()) RETURNING $2::uuid`,
		"modül adı":       `INSERT INTO tayfa_modules (tayfa_id, module, enabled) VALUES ($1, 'weather', true) RETURNING $2::uuid`,
	} {
		if _, err := e.admin.Exec(ctx, sql, tf.PG(), owner.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	borrower := id.New()
	for name, sql := range map[string]string{
		"91 gün": `INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at, approved_at, handed_over_at, due_at)
			VALUES (gen_random_uuid(), $1, $2, $3, 'handed_over', now(), now(), now(), now() + interval '91 days')`,
		"bitişsiz iade": `INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at, approved_at, handed_over_at)
			VALUES (gen_random_uuid(), $1, $2, $3, 'returned', now(), now(), now())`,
		"başka tayfa": `INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at)
			VALUES (gen_random_uuid(), $1, gen_random_uuid(), $3, 'requested', now()) RETURNING $2::uuid`,
	} {
		if _, err := e.admin.Exec(ctx, sql, it.ID.PG(), tf.PG(), borrower.PG()); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	// One active loan per item.
	ins := `INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at) VALUES (gen_random_uuid(), $1, $2, gen_random_uuid(), 'requested', now())`
	if _, err := e.admin.Exec(ctx, ins, it.ID.PG(), tf.PG()); err != nil {
		t.Fatalf("ilk ödünç: %v", err)
	}
	if _, err := e.admin.Exec(ctx, ins, it.ID.PG(), tf.PG()); err == nil {
		t.Error("ikinci etkin ödünç kabul edildi")
	}
}

// notice is one library loan step notification as core requested it.
type notice struct {
	Type, Actor, TargetType, Target, Tayfa string
	To                                     []string
}

// stepNotices returns the loan step notifications (issue #321), oldest
// first.
func (e *env) stepNotices(t *testing.T) []notice {
	t.Helper()
	rows, err := e.admin.Query(ctx, `SELECT d.payload ->> 'type', COALESCE(d.actor_id::text, ''), d.payload ->> 'target_type',
		    d.payload ->> 'target_id', d.payload ->> 'tayfa_id', ARRAY(SELECT jsonb_array_elements_text(d.payload -> 'recipient_ids'))
		FROM domain_events d
		WHERE d.type = 'notification.requested' AND d.payload ->> 'type' = ANY($1::text[])
		ORDER BY d.occurred_at, d.id`,
		[]string{"library_loan_requested", "library_queue_joined", "library_loan_approved", "library_loan_declined", "library_loan_handed_over",
			"library_queue_turn"})
	must(t, err)
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (notice, error) {
		var n notice
		err := r.Scan(&n.Type, &n.Actor, &n.TargetType, &n.Target, &n.Tayfa, &n.To)
		return n, err
	})
	must(t, err)
	return out
}

// TestLoanStepNotifications (issue #321; ADR-0040 §1, revised
// 2026-11-23): a new request and a queue place tell the owner; approving,
// declining and handing over tell the borrower. Each is about the item,
// names the one who took the step as actor and goes to the other side
// only; nothing names the item or the people.
func TestLoanStepNotifications(t *testing.T) {
	e := setup(t)
	tf, owner := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	b := e.member(t, tf, authz.Member)
	tent := e.add(t, owner, tf, library.KindCampingGear, "Çadır")

	_, err := e.svc.Request(ctx, a, tf, tent.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, a, tf, tent.ID) // again: nothing new
	must(t, err)
	_, err = e.svc.Request(ctx, b, tf, tent.ID)
	must(t, err)
	_, err = e.svc.Request(ctx, b, tf, tent.ID) // already waiting
	must(t, err)
	loan := e.item(t, owner, tf, tent.ID).ActiveLoan.ID
	_, err = e.svc.Approve(ctx, owner, tf, loan)
	must(t, err)
	_, err = e.svc.HandOver(ctx, owner, tf, loan, time.Time{})
	must(t, err)
	// Taking back tells nobody; B's request opened from the queue tells B
	// that their turn came, without an actor (issue #332).
	_, err = e.svc.Return(ctx, owner, tf, loan)
	must(t, err)
	next := e.item(t, owner, tf, tent.ID).ActiveLoan.ID
	_, err = e.svc.Decline(ctx, owner, tf, next)
	must(t, err)

	it := tent.ID.String()
	want := []notice{
		{"library_loan_requested", a.String(), "library_item", it, tf.String(), []string{owner.String()}},
		{"library_queue_joined", b.String(), "library_item", it, tf.String(), []string{owner.String()}},
		{"library_loan_approved", owner.String(), "library_item", it, tf.String(), []string{a.String()}},
		{"library_loan_handed_over", owner.String(), "library_item", it, tf.String(), []string{a.String()}},
		{"library_queue_turn", "", "library_item", it, tf.String(), []string{b.String()}},
		{"library_loan_declined", owner.String(), "library_item", it, tf.String(), []string{b.String()}},
	}
	got := e.stepNotices(t)
	if len(got) != len(want) {
		t.Fatalf("bildirimler = %+v", got)
	}
	for i := range want {
		if got[i].Type != want[i].Type || got[i].Actor != want[i].Actor || got[i].TargetType != want[i].TargetType ||
			got[i].Target != want[i].Target || got[i].Tayfa != want[i].Tayfa || !slices.Equal(got[i].To, want[i].To) {
			t.Errorf("%d. bildirim = %+v, %+v bekleniyordu", i, got[i], want[i])
		}
	}
	// IDs only: never the item's name.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND payload::text LIKE '%Çadır%'`); n != 0 {
		t.Fatalf("bildirimde kalem adı var: %d", n)
	}
}

// TestLoanStepNotificationsBlocked: a block either way silences the loan
// step notifications both ways; the steps themselves still work.
func TestLoanStepNotificationsBlocked(t *testing.T) {
	for _, ownerBlocks := range []bool{true, false} {
		e := setup(t)
		tf, owner := e.newTayfa(t)
		a := e.member(t, tf, authz.Member)
		b := e.member(t, tf, authz.Member)
		if ownerBlocks {
			e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, owner.PG(), a.PG())
		} else {
			e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, a.PG(), owner.PG())
		}
		tent := e.add(t, owner, tf, library.KindCampingGear, "Çadır")
		_, err := e.svc.Request(ctx, a, tf, tent.ID)
		must(t, err)
		loan := e.item(t, owner, tf, tent.ID).ActiveLoan.ID
		_, err = e.svc.Approve(ctx, owner, tf, loan)
		must(t, err)
		_, err = e.svc.HandOver(ctx, owner, tf, loan, time.Time{})
		must(t, err)
		// B is not blocked: B's queue place still tells the owner.
		_, err = e.svc.Request(ctx, b, tf, tent.ID)
		must(t, err)
		got := e.stepNotices(t)
		if len(got) != 1 || got[0].Type != "library_queue_joined" || got[0].Actor != b.String() || !slices.Equal(got[0].To, []string{owner.String()}) {
			t.Fatalf("engel (sahip engelledi %v): %+v", ownerBlocks, got)
		}
	}
}

// TestQueueTurnNotification (issue #332; ADR-0040 §1, revised
// 2026-11-23): whichever way the active loan ends, the member whose
// request opens from the queue hears that their turn came; a block either
// way between them and the item's owner silences it, the request still
// opens.
func TestQueueTurnNotification(t *testing.T) {
	for name, block := range map[string]string{"engelsiz": "", "sahip engelledi": "owner", "sıradaki engelledi": "waiter"} {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			tf, owner := e.newTayfa(t)
			a := e.member(t, tf, authz.Member)
			b := e.member(t, tf, authz.Member)
			c := e.member(t, tf, authz.Member)
			switch block {
			case "owner":
				e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, owner.PG(), b.PG())
			case "waiter":
				e.exec(t, `INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)`, b.PG(), owner.PG())
			}
			tent := e.add(t, owner, tf, library.KindCampingGear, "Çadır")
			for _, u := range []id.ID{a, b, c} {
				_, err := e.svc.Request(ctx, u, tf, tent.ID)
				must(t, err)
			}
			// A takes back their request: B's turn comes.
			_, err := e.svc.Cancel(ctx, a, tf, tent.ID)
			must(t, err)
			if v := e.item(t, owner, tf, tent.ID); v.ActiveLoan == nil || v.ActiveLoan.Borrower != b {
				t.Fatalf("sıradakine geçmedi: %+v", v)
			}
			// The owner declines B: C's turn comes.
			_, err = e.svc.Decline(ctx, owner, tf, e.item(t, owner, tf, tent.ID).ActiveLoan.ID)
			must(t, err)

			var turns []notice
			for _, n := range e.stepNotices(t) {
				if n.Type == "library_queue_turn" {
					turns = append(turns, n)
				}
			}
			want := []string{b.String(), c.String()}
			if block != "" {
				want = []string{c.String()}
			}
			if len(turns) != len(want) {
				t.Fatalf("sıra sende = %+v, alıcılar %v bekleniyordu", turns, want)
			}
			for i, n := range turns {
				if n.Actor != "" || n.TargetType != "library_item" || n.Target != tent.ID.String() || n.Tayfa != tf.String() ||
					!slices.Equal(n.To, []string{want[i]}) {
					t.Errorf("%d. sıra sende = %+v, alıcı %s bekleniyordu", i, n, want[i])
				}
			}
			if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND payload::text LIKE '%Çadır%'`); n != 0 {
				t.Fatalf("bildirimde kalem adı var: %d", n)
			}
		})
	}
}
