package membership

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type allowAll struct{}

func (allowAll) Allow(string) bool { return true }

type countLimiter struct {
	mu   sync.Mutex
	left map[string]int
	max  int
}

func (c *countLimiter) Allow(k string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.left == nil {
		c.left = map[string]int{}
	}
	if c.left[k] >= c.max {
		return false
	}
	c.left[k]++
	return true
}

type env struct {
	svc  *Service
	pool *pgxpool.Pool
	q    *store.Queries
}

func setup(t *testing.T) *env {
	t.Helper()
	return setupWith(t, allowAll{})
}

func setupWith(t *testing.T, l Limiter) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	svc, err := New(Config{Pool: pool, JoinLimiter: l})
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, pool: pool, q: store.New(pool)}
}

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// errValidation stands for any *apperr.ValidationError in wantErr.
var errValidation = errors.New("doğrulama hatası")

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if errors.Is(want, errValidation) {
		wantInvalid(t, what, err)
		return
	}
	if !errors.Is(err, want) {
		t.Fatalf("%s: hata = %v, beklenen %v", what, err, want)
	}
}

func wantInvalid(t *testing.T, what string, err error) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("%s: hata = %v, beklenen doğrulama hatası", what, err)
	}
}

// tayfa is a test tayfa with its admin.
type tayfa struct {
	id    id.ID
	code  string
	admin id.ID
}

var codeSeq int

// newTayfa creates a tayfa the way TayfaService does: the tayfa row, then
// the creator's admin membership with the creator as actor.
func (e *env) newTayfa(t *testing.T, policy string) tayfa {
	t.Helper()
	codeSeq++
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := []byte("TEST")
	n := codeSeq
	for range 4 {
		b = append(b, alphabet[n%len(alphabet)])
		n /= len(alphabet)
	}
	tf := tayfa{id: id.New(), code: string(b), admin: id.New()}
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	must(t, q.SetActor(ctx, tf.admin.String()))
	must(t, q.CreateTayfa(ctx, store.CreateTayfaParams{
		ID: tf.id.PG(), Name: "Kamp Tayfası", NameNormalized: "kamp tayfası",
		JoinCode: tf.code, JoinPolicy: policy, CreatedBy: tf.admin.PG(),
	}))
	must(t, q.CreateMembership(ctx, store.CreateMembershipParams{
		TayfaID: tf.id.PG(), UserID: tf.admin.PG(), Role: "admin", Status: "active",
		JoinedVia: "creator", JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}))
	must(t, tx.Commit(ctx))
	return tf
}

// seed writes a membership directly with triggers disabled, to set up a
// state for a test (the test connection is a superuser).
func (e *env) seed(t *testing.T, tf id.ID, role authz.Role, st authz.Status) id.ID {
	t.Helper()
	u := id.New()
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SET LOCAL session_replication_role = replica")
	must(t, err)
	p := store.CreateMembershipParams{TayfaID: tf.PG(), UserID: u.PG(), Role: role.String(), Status: string(st), JoinedVia: "code"}
	if st == authz.StatusActive {
		p.JoinedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	}
	must(t, store.New(tx).CreateMembership(ctx, p))
	must(t, tx.Commit(ctx))
	return u
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := e.pool.Exec(ctx, sql, args...)
	must(t, err)
}

func (e *env) status(t *testing.T, tf, u id.ID) string {
	t.Helper()
	m, err := e.q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tf.PG(), UserID: u.PG()})
	if err != nil {
		return "none"
	}
	return m.Status + "/" + m.Role
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) suspend(t *testing.T, tf id.ID) {
	t.Helper()
	e.exec(t, `UPDATE tayfas SET suspended_at = now(), suspended_reason = 'test', suspended_by = $2 WHERE id = $1`, tf.PG(), id.New().PG())
}

// ---------------------------------------------------------------------------

func TestJoinByCode(t *testing.T) {
	e := setup(t)
	open := e.newTayfa(t, "open")
	approval := e.newTayfa(t, "approval")

	u := id.New()
	m, err := e.svc.JoinByCode(ctx, u, open.code)
	if err != nil || m.Status != authz.StatusActive || m.Role != authz.Member || m.JoinedAt.IsZero() {
		t.Fatalf("açık tayfa: %+v %v", m, err)
	}
	_, err = e.svc.JoinByCode(ctx, u, open.code)
	wantErr(t, "tekrar katılım", err, apperr.ErrAlreadyMember)

	// Küçük harfle de çalışır; onaylı tayfada başvuru bekler.
	lower := []byte(approval.code)
	for i := range lower {
		if lower[i] >= 'A' && lower[i] <= 'Z' {
			lower[i] += 'a' - 'A'
		}
	}
	m, err = e.svc.JoinByCode(ctx, u, string(lower))
	if err != nil || m.Status != authz.StatusPending || !m.JoinedAt.IsZero() {
		t.Fatalf("onaylı tayfa: %+v %v", m, err)
	}
	_, err = e.svc.JoinByCode(ctx, u, approval.code)
	wantErr(t, "bekleyen başvuru", err, apperr.ErrAlreadyPending)

	// Yanlış biçim, olmayan kod, banlı kullanıcı, salt okunur tayfa: aynı hata.
	banned := e.seed(t, open.id, authz.Member, authz.StatusBanned)
	_, err = e.svc.JoinByCode(ctx, banned, open.code)
	wantErr(t, "banlı", err, apperr.ErrInvalidJoinCode)
	for _, code := range []string{"", "KISA", "TEST0OI1", "ZZZZZZZZ", open.code + "X"} {
		_, err = e.svc.JoinByCode(ctx, id.New(), code)
		wantErr(t, "kod "+code, err, apperr.ErrInvalidJoinCode)
	}
	e.suspend(t, approval.id)
	_, err = e.svc.JoinByCode(ctx, id.New(), approval.code)
	wantErr(t, "askıdaki tayfa", err, apperr.ErrInvalidJoinCode)

	// Denetim kaydı ve olay aynı transaction'da; olayda yalnızca ID'ler.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action = 'member.joined' AND actor_id = $2`, open.id.PG(), u.PG()); n != 1 {
		t.Fatalf("denetim kaydı = %d", n)
	}
	var payload []byte
	must(t, e.pool.QueryRow(ctx, `SELECT payload FROM domain_events WHERE type = 'membership.changed' AND tayfa_id = $1 AND subject_id = $2`, approval.id.PG(), u.PG()).Scan(&payload))
	var p map[string]string
	must(t, json.Unmarshal(payload, &p))
	if len(p) != 3 || p["change"] != "requested" || p["user_id"] != u.String() || p["tayfa_id"] != approval.id.String() {
		t.Fatalf("olay = %s", payload)
	}
}

func TestJoinByCodeRateLimit(t *testing.T) {
	e := setupWith(t, &countLimiter{max: 10})
	tf := e.newTayfa(t, "open")
	u := id.New()
	for range 10 {
		_, err := e.svc.JoinByCode(ctx, u, "ZZZZZZZZ")
		wantErr(t, "yanlış kod", err, apperr.ErrInvalidJoinCode)
	}
	_, err := e.svc.JoinByCode(ctx, u, tf.code)
	wantErr(t, "sınır", err, apperr.ErrRateLimited)
	if _, err := e.svc.JoinByCode(ctx, id.New(), tf.code); err != nil {
		t.Fatalf("başka kullanıcı etkilendi: %v", err)
	}
}

func TestInviteLinkJoin(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)

	for name, tc := range map[string]struct {
		ttl  time.Duration
		max  int32
		want string
	}{
		"kısa süre":  {59 * time.Minute, 1, "ttl"},
		"uzun süre":  {31 * 24 * time.Hour, 1, "ttl"},
		"sıfır kişi": {time.Hour, 0, "max_uses"},
		"çok kişi":   {time.Hour, 101, "max_uses"},
	} {
		_, _, err := e.svc.CreateInviteLink(ctx, tf.admin, tf.id, tc.ttl, tc.max)
		var fe *apperr.ValidationError
		if !errors.As(err, &fe) || fe.Field != tc.want {
			t.Fatalf("%s: %v", name, err)
		}
	}

	modInv, modTok, err := e.svc.CreateInviteLink(ctx, mod, tf.id, 24*time.Hour, 5)
	must(t, err)
	if len(modTok) != 43 || modInv.MaxUses != 5 || modInv.CreatedBy != mod || modInv.ExpiresAt.Sub(modInv.CreatedAt) < 23*time.Hour {
		t.Fatalf("davet = %+v token uzunluğu %d", modInv, len(modTok))
	}
	_, memTok, err := e.svc.CreateInviteLink(ctx, member, tf.id, time.Hour, 5)
	must(t, err)

	// Token açık halde hiçbir yere yazılmaz.
	for _, tok := range []string{modTok, memTok} {
		if n := e.count(t, `SELECT
			(SELECT count(*) FROM invites WHERE encode(token_hash, 'escape') LIKE '%' || $1 || '%') +
			(SELECT count(*) FROM audit_logs WHERE metadata::text LIKE '%' || $1 || '%') +
			(SELECT count(*) FROM domain_events WHERE payload::text LIKE '%' || $1 || '%')`, tok); n != 0 {
			t.Fatal("token açık halde saklanmış")
		}
	}

	u1, u2 := id.New(), id.New()
	pv, err := e.svc.PreviewInvite(ctx, u1, modTok)
	if err != nil || pv.TayfaID != tf.id || pv.Name != "Kamp Tayfası" || pv.MemberCount != 3 || pv.AlreadyMember {
		t.Fatalf("önizleme = %+v %v", pv, err)
	}
	// Moderatörün linki onaylı tayfada doğrudan üye yapar; üyeninki başvuru.
	m, err := e.svc.JoinByInvite(ctx, u1, modTok)
	if err != nil || m.Status != authz.StatusActive {
		t.Fatalf("moderatör linki: %+v %v", m, err)
	}
	m, err = e.svc.JoinByInvite(ctx, u2, memTok)
	if err != nil || m.Status != authz.StatusPending {
		t.Fatalf("üye linki: %+v %v", m, err)
	}
	_, err = e.svc.JoinByInvite(ctx, u1, memTok)
	wantErr(t, "zaten üye", err, apperr.ErrAlreadyMember)
	_, err = e.svc.JoinByInvite(ctx, u2, modTok)
	wantErr(t, "zaten bekliyor", err, apperr.ErrAlreadyPending)
	pv, err = e.svc.PreviewInvite(ctx, u2, modTok)
	if err != nil || !pv.AlreadyMember {
		t.Fatalf("bekleyenin önizlemesi = %+v %v", pv, err)
	}

	// Kullanım yalnızca üyelik oluşunca sayılır.
	got, err := e.q.GetInvite(ctx, store.GetInviteParams{TayfaID: tf.id.PG(), ID: modInv.ID.PG()})
	must(t, err)
	if got.UseCount != 1 {
		t.Fatalf("use_count = %d", got.UseCount)
	}

	// Başvuruda kimin daveti görünür.
	page, err := e.svc.ListPending(ctx, mod, tf.id, 0, "")
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].UserID != u2 || page.Items[0].InviterID != member ||
		page.Items[0].JoinedVia != "invite_link" || page.Items[0].InviteID == (id.ID{}) {
		t.Fatalf("bekleyenler = %+v", page.Items)
	}
}

func TestInviteLinkErrors(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	newLink := func(max int32) (Invite, string) {
		inv, tok, err := e.svc.CreateInviteLink(ctx, tf.admin, tf.id, time.Hour, max)
		must(t, err)
		return inv, tok
	}

	expired, expiredTok := newLink(5)
	e.exec(t, `UPDATE invites SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = $1`, expired.ID.PG())
	revoked, revokedTok := newLink(5)
	must(t, e.svc.RevokeInviteLink(ctx, tf.admin, tf.id, revoked.ID))
	must(t, e.svc.RevokeInviteLink(ctx, tf.admin, tf.id, revoked.ID)) // ikinci kez: değişiklik yok
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'invite.revoked'`); n != 1 {
		t.Fatalf("iptal denetim kaydı = %d", n)
	}
	_, oneTok := newLink(1)
	if _, err := e.svc.JoinByInvite(ctx, id.New(), oneTok); err != nil {
		t.Fatal(err)
	}
	_, okTok := newLink(5)

	for name, tc := range map[string]struct {
		tok  string
		want error
	}{
		"süresi dolmuş": {expiredTok, apperr.ErrInviteExpired},
		"iptal":         {revokedTok, apperr.ErrInviteRevoked},
		"tükenmiş":      {oneTok, apperr.ErrInviteExhausted},
		"bilinmeyen":    {"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", apperr.ErrInviteInvalid},
		"bozuk":         {"bu-bir-token-degil", apperr.ErrInviteInvalid},
		"boş":           {"", errValidation},
	} {
		u := id.New()
		_, err := e.svc.PreviewInvite(ctx, u, tc.tok)
		wantErr(t, name+" önizleme", err, tc.want)
		_, err = e.svc.JoinByInvite(ctx, u, tc.tok)
		wantErr(t, name+" katılım", err, tc.want)
		if s := e.status(t, tf.id, u); s != "none" {
			t.Fatalf("%s: üyelik oluştu: %s", name, s)
		}
	}

	banned := e.seed(t, tf.id, authz.Member, authz.StatusBanned)
	_, err := e.svc.PreviewInvite(ctx, banned, okTok)
	wantErr(t, "banlı önizleme", err, apperr.ErrBanned)
	_, err = e.svc.JoinByInvite(ctx, banned, okTok)
	wantErr(t, "banlı katılım", err, apperr.ErrBanned)
	if s := e.status(t, tf.id, banned); s != "banned/member" {
		t.Fatalf("ban kaydı = %s", s)
	}

	e.suspend(t, tf.id)
	_, err = e.svc.JoinByInvite(ctx, id.New(), okTok)
	wantErr(t, "askıdaki tayfa", err, apperr.ErrInviteInvalid)
}

// Two users race for the last use of a link: exactly one gets in.
func TestInviteLastUseConcurrent(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	for range 5 {
		inv, tok, err := e.svc.CreateInviteLink(ctx, tf.admin, tf.id, time.Hour, 1)
		must(t, err)
		users := []id.ID{id.New(), id.New()}
		errs := make([]error, len(users))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, u := range users {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = e.svc.JoinByInvite(ctx, u, tok)
			}()
		}
		close(start)
		wg.Wait()
		ok, exhausted := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, apperr.ErrInviteExhausted):
				exhausted++
			default:
				t.Fatalf("beklenmeyen hata: %v", err)
			}
		}
		if ok != 1 || exhausted != 1 {
			t.Fatalf("giren %d, reddedilen %d", ok, exhausted)
		}
		if n := e.count(t, `SELECT count(*) FROM memberships WHERE invite_id = $1`, inv.ID.PG()); n != 1 {
			t.Fatalf("linkle giren = %d", n)
		}
		if n := e.count(t, `SELECT use_count FROM invites WHERE id = $1`, inv.ID.PG()); n != 1 {
			t.Fatalf("use_count = %d", n)
		}
	}
}

func TestListAndRevokeInviteScope(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	mod2 := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	adminInv, _, err := e.svc.CreateInviteLink(ctx, tf.admin, tf.id, time.Hour, 3)
	must(t, err)
	modInv, _, err := e.svc.CreateInviteLink(ctx, mod, tf.id, time.Hour, 3)
	must(t, err)
	memInv, _, err := e.svc.CreateInviteLink(ctx, member, tf.id, time.Hour, 3)
	must(t, err)

	all, err := e.svc.ListInviteLinks(ctx, tf.admin, tf.id)
	if err != nil || len(all) != 3 || all[0].ID != memInv.ID {
		t.Fatalf("yöneticinin listesi = %d %v", len(all), err)
	}
	own, err := e.svc.ListInviteLinks(ctx, mod, tf.id)
	if err != nil || len(own) != 1 || own[0].ID != modInv.ID {
		t.Fatalf("moderatörün listesi = %+v %v", own, err)
	}
	mine, err := e.svc.ListInviteLinks(ctx, member, tf.id)
	if err != nil || len(mine) != 1 || mine[0].ID != memInv.ID {
		t.Fatalf("üyenin kendi listesi = %+v %v", mine, err)
	}

	// Başkasının linki görünmez; yönetici hepsini iptal eder.
	wantErr(t, "moderatör yöneticinin linki", e.svc.RevokeInviteLink(ctx, mod2, tf.id, adminInv.ID), apperr.ErrInviteNotFound)
	wantErr(t, "olmayan link", e.svc.RevokeInviteLink(ctx, mod, tf.id, id.New()), apperr.ErrInviteNotFound)
	must(t, e.svc.RevokeInviteLink(ctx, member, tf.id, memInv.ID))
	must(t, e.svc.RevokeInviteLink(ctx, tf.admin, tf.id, modInv.ID))
	all, err = e.svc.ListInviteLinks(ctx, tf.admin, tf.id)
	if err != nil || len(all) != 1 || all[0].ID != adminInv.ID {
		t.Fatalf("iptal sonrası = %d %v", len(all), err)
	}

	// Susturulmuş üye link açamaz ama kendi linkini iptal edebilir.
	muted := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	mutedInv, _, err := e.svc.CreateInviteLink(ctx, muted, tf.id, time.Hour, 1)
	must(t, err)
	_, err = e.svc.MuteMember(ctx, mod, tf.id, muted, time.Now().Add(time.Hour))
	must(t, err)
	_, _, err = e.svc.CreateInviteLink(ctx, muted, tf.id, time.Hour, 1)
	wantErr(t, "susturulmuş link", err, apperr.ErrMuted)
	must(t, e.svc.RevokeInviteLink(ctx, muted, tf.id, mutedInv.ID))

	// Salt okunur tayfada link açılmaz, iptal edilir.
	e.suspend(t, tf.id)
	_, _, err = e.svc.CreateInviteLink(ctx, tf.admin, tf.id, time.Hour, 1)
	wantErr(t, "askıda link", err, apperr.ErrTayfaReadOnly)
	must(t, e.svc.RevokeInviteLink(ctx, tf.admin, tf.id, adminInv.ID))
}

func TestApproveRejectBanRemove(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	p1 := e.seed(t, tf.id, authz.Member, authz.StatusPending)
	p2 := e.seed(t, tf.id, authz.Member, authz.StatusPending)
	p3 := e.seed(t, tf.id, authz.Member, authz.StatusPending)

	_, err := e.svc.ApproveMember(ctx, member, tf.id, p1)
	wantErr(t, "üye onaylayamaz", err, apperr.ErrPermissionDenied)
	m, err := e.svc.ApproveMember(ctx, mod, tf.id, p1)
	if err != nil || m.Status != authz.StatusActive || m.JoinedAt.IsZero() {
		t.Fatalf("onay: %+v %v", m, err)
	}
	_, err = e.svc.ApproveMember(ctx, mod, tf.id, p1)
	wantErr(t, "ikinci onay", err, apperr.ErrMemberNotFound)
	_, err = e.svc.ApproveMember(ctx, mod, tf.id, id.New())
	wantErr(t, "olmayan", err, apperr.ErrMemberNotFound)

	must(t, e.svc.RejectMember(ctx, mod, tf.id, p2))
	if s := e.status(t, tf.id, p2); s != "none" {
		t.Fatalf("reddedilen başvuru silinmedi: %s", s)
	}
	wantErr(t, "aktif üyeyi reddetme", e.svc.RejectMember(ctx, mod, tf.id, member), apperr.ErrMemberNotFound)

	// Bekleyen başvuru üye sayılır ve banlanabilir; kayıt korunur.
	must(t, e.svc.BanMember(ctx, mod, tf.id, p3))
	must(t, e.svc.BanMember(ctx, mod, tf.id, member))
	if e.status(t, tf.id, p3) != "banned/member" || e.status(t, tf.id, member) != "banned/member" {
		t.Fatal("ban kaydı yok")
	}
	wantErr(t, "banlıyı tekrar banlama", e.svc.BanMember(ctx, mod, tf.id, member), apperr.ErrMemberNotFound)
	wantErr(t, "banlıyı çıkarma", e.svc.RemoveMember(ctx, mod, tf.id, member), apperr.ErrMemberNotFound)

	must(t, e.svc.RemoveMember(ctx, mod, tf.id, p1))
	if s := e.status(t, tf.id, p1); s != "none" {
		t.Fatalf("çıkarılan: %s", s)
	}

	var changes []string
	rows, err := e.pool.Query(ctx, `SELECT payload->>'change' FROM domain_events WHERE type = 'membership.changed' AND tayfa_id = $1 ORDER BY id`, tf.id.PG())
	must(t, err)
	for rows.Next() {
		var c string
		must(t, rows.Scan(&c))
		changes = append(changes, c)
	}
	must(t, rows.Err())
	want := []string{"approved", "rejected", "banned", "banned", "removed"}
	if len(changes) != len(want) {
		t.Fatalf("olaylar = %v", changes)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Fatalf("olaylar = %v", changes)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action LIKE 'member.%' AND actor_id = $2`, tf.id.PG(), mod.PG()); n != 5 {
		t.Fatalf("denetim kaydı = %d", n)
	}
}

func TestRoleHierarchy(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	admin2 := e.seed(t, tf.id, authz.Admin, authz.StatusActive)
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	mod2 := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	member2 := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	soon := time.Now().Add(time.Hour)

	for name, err := range map[string]error{
		"moderatör moderatörü susturamaz":          second(e.svc.MuteMember(ctx, mod, tf.id, mod2, soon)),
		"moderatör yöneticiyi susturamaz":          second(e.svc.MuteMember(ctx, mod, tf.id, tf.admin, soon)),
		"kendini susturamaz":                       second(e.svc.MuteMember(ctx, mod, tf.id, mod, soon)),
		"moderatör moderatörü banlayamaz":          e.svc.BanMember(ctx, mod, tf.id, mod2),
		"moderatör moderatörü çıkaramaz":           e.svc.RemoveMember(ctx, mod, tf.id, mod2),
		"yönetici yöneticiyi banlayamaz":           e.svc.BanMember(ctx, tf.admin, tf.id, admin2),
		"yönetici yöneticinin rolünü değiştiremez": second(e.svc.ChangeRole(ctx, tf.admin, tf.id, admin2, authz.Member)),
	} {
		wantErr(t, name, err, apperr.ErrRoleHierarchy)
	}
	for name, err := range map[string]error{
		"üye susturamaz":             second(e.svc.MuteMember(ctx, member, tf.id, member2, soon)),
		"üye banlayamaz":             e.svc.BanMember(ctx, member, tf.id, member2),
		"moderatör rol veremez": second(e.svc.ChangeRole(ctx, mod, tf.id, member, authz.Moderator)),
	} {
		wantErr(t, name, err, apperr.ErrPermissionDenied)
	}
	// Moderatör kendi rolünü bırakabilir; yükseltemez.
	stepper := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	_, err0 := e.svc.ChangeRole(ctx, stepper, tf.id, stepper, authz.Admin)
	wantErr(t, "moderatör kendini yükseltemez", err0, apperr.ErrRoleHierarchy)
	if m, err := e.svc.ChangeRole(ctx, stepper, tf.id, stepper, authz.Member); err != nil || m.Role != authz.Member {
		t.Fatalf("moderatörün rolü bırakması: %+v %v", m, err)
	}
	_, err := e.svc.ChangeRole(ctx, tf.admin, tf.id, member, 0)
	wantInvalid(t, "geçersiz rol", err)
	_, err = e.svc.ChangeRole(ctx, tf.admin, tf.id, member, authz.Member)
	wantInvalid(t, "aynı rol", err)

	// Yönetici üyeyi yönetici yapabilir (ikinci yönetici, v14).
	m, err := e.svc.ChangeRole(ctx, tf.admin, tf.id, member, authz.Admin)
	if err != nil || m.Role != authz.Admin {
		t.Fatalf("yükseltme: %+v %v", m, err)
	}
	m, err = e.svc.ChangeRole(ctx, tf.admin, tf.id, mod2, authz.Member)
	if err != nil || m.Role != authz.Member {
		t.Fatalf("düşürme: %+v %v", m, err)
	}

	// Mute: süre sınırı, susturulan moderatör yazamaz, yine okur.
	_, err = e.svc.MuteMember(ctx, mod, tf.id, member2, time.Now().Add(-time.Minute))
	wantInvalid(t, "geçmiş süre", err)
	_, err = e.svc.MuteMember(ctx, mod, tf.id, member2, time.Now().Add(31*24*time.Hour))
	wantInvalid(t, "31 gün", err)
	m, err = e.svc.MuteMember(ctx, tf.admin, tf.id, mod, soon)
	if err != nil || m.MutedUntil.IsZero() {
		t.Fatalf("susturma: %+v %v", m, err)
	}
	_, err = e.svc.MuteMember(ctx, mod, tf.id, member2, soon)
	wantErr(t, "susturulmuş moderatör", err, apperr.ErrMuted)
	if _, err := e.svc.ListMembers(ctx, mod, tf.id, authz.StatusBanned, 0, ""); err != nil {
		t.Fatalf("susturulmuş moderatör banlıları okur: %v", err)
	}
	m, err = e.svc.UnmuteMember(ctx, tf.admin, tf.id, mod)
	if err != nil || !m.MutedUntil.IsZero() {
		t.Fatalf("susturma kaldırma: %+v %v", m, err)
	}
	if _, err := e.svc.UnmuteMember(ctx, tf.admin, tf.id, mod); err != nil {
		t.Fatalf("susturulmamışı açma: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'member.unmuted'`); n != 1 {
		t.Fatalf("susturma kaldırma kaydı = %d", n)
	}
}

func second[T any](_ T, err error) error { return err }

// The database trigger is the second line of defence: even if the app
// check were skipped, it refuses the change.
func TestTriggerBacksHierarchy(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	mod2 := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	tx, err := e.pool.Begin(ctx)
	must(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)
	must(t, q.SetActor(ctx, mod.String()))
	_, err = q.BanMembership(ctx, store.BanMembershipParams{TayfaID: tf.id.PG(), UserID: mod2.PG()})
	if store.SQLState(err) != store.CodeRoleHierarchy {
		t.Fatalf("trigger: %v", err)
	}
}

func TestLastAdmin(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)

	wantErr(t, "son yönetici ayrılamaz", e.svc.LeaveTayfa(ctx, tf.admin, tf.id), apperr.ErrLastAdmin)
	_, err := e.svc.ChangeRole(ctx, tf.admin, tf.id, tf.admin, authz.Moderator)
	wantErr(t, "son yönetici rol bırakamaz", err, apperr.ErrLastAdmin)

	_, err = e.svc.ChangeRole(ctx, tf.admin, tf.id, member, authz.Admin)
	must(t, err)
	m, err := e.svc.ChangeRole(ctx, tf.admin, tf.id, tf.admin, authz.Moderator)
	if err != nil || m.Role != authz.Moderator {
		t.Fatalf("rol bırakma: %+v %v", m, err)
	}
	_, err = e.svc.ChangeRole(ctx, tf.admin, tf.id, tf.admin, authz.Admin)
	wantErr(t, "moderatör kendini yükseltemez", err, apperr.ErrRoleHierarchy)
	must(t, e.svc.LeaveTayfa(ctx, tf.admin, tf.id))
	wantErr(t, "yeni son yönetici", e.svc.LeaveTayfa(ctx, member, tf.id), apperr.ErrLastAdmin)
}

// Two admins leaving at once cannot leave the tayfa without an admin.
func TestLastAdminConcurrentLeave(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	admin2 := e.seed(t, tf.id, authz.Admin, authz.StatusActive)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, u := range []id.ID{tf.admin, admin2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.svc.LeaveTayfa(ctx, u, tf.id)
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("hatalar = %v", errs)
	}
	for _, err := range errs {
		if err != nil {
			wantErr(t, "ikinci ayrılan", err, apperr.ErrLastAdmin)
		}
	}
}

func TestLeaveTayfa(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	pending := e.seed(t, tf.id, authz.Member, authz.StatusPending)
	banned := e.seed(t, tf.id, authz.Member, authz.StatusBanned)

	e.suspend(t, tf.id) // salt okunur tayfadan da ayrılınır
	must(t, e.svc.LeaveTayfa(ctx, member, tf.id))
	must(t, e.svc.LeaveTayfa(ctx, pending, tf.id))
	wantErr(t, "banlı", e.svc.LeaveTayfa(ctx, banned, tf.id), apperr.ErrTayfaNotFound)
	wantErr(t, "üye değil", e.svc.LeaveTayfa(ctx, id.New(), tf.id), apperr.ErrTayfaNotFound)
	wantErr(t, "olmayan tayfa", e.svc.LeaveTayfa(ctx, member, id.New()), apperr.ErrTayfaNotFound)
	if e.status(t, tf.id, member) != "none" || e.status(t, tf.id, pending) != "none" || e.status(t, tf.id, banned) != "banned/member" {
		t.Fatal("ayrılma sonucu yanlış")
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE payload->>'change' IN ('left', 'withdrawn')`); n != 2 {
		t.Fatalf("ayrılma olayı = %d", n)
	}
}

// A's admin cannot act in B, and non-members learn nothing about B.
func TestIDOR(t *testing.T) {
	e := setup(t)
	a := e.newTayfa(t, "open")
	b := e.newTayfa(t, "approval")
	bMember := e.seed(t, b.id, authz.Member, authz.StatusActive)
	bPending := e.seed(t, b.id, authz.Member, authz.StatusPending)
	bInv, _, err := e.svc.CreateInviteLink(ctx, b.admin, b.id, time.Hour, 1)
	must(t, err)
	soon := time.Now().Add(time.Hour)

	for _, outsider := range []id.ID{a.admin, id.New(), bPending} {
		for name, err := range map[string]error{
			"CreateInviteLink": third(e.svc.CreateInviteLink(ctx, outsider, b.id, time.Hour, 1)),
			"ListInviteLinks":  second(e.svc.ListInviteLinks(ctx, outsider, b.id)),
			"RevokeInviteLink": e.svc.RevokeInviteLink(ctx, outsider, b.id, bInv.ID),
			"ListMembers":      second(e.svc.ListMembers(ctx, outsider, b.id, "", 0, "")),
			"ListPending":      second(e.svc.ListPending(ctx, outsider, b.id, 0, "")),
			"ApproveMember":    second(e.svc.ApproveMember(ctx, outsider, b.id, bPending)),
			"RejectMember":     e.svc.RejectMember(ctx, outsider, b.id, bPending),
			"ChangeRole":       second(e.svc.ChangeRole(ctx, outsider, b.id, bMember, authz.Moderator)),
			"MuteMember":       second(e.svc.MuteMember(ctx, outsider, b.id, bMember, soon)),
			"UnmuteMember":     second(e.svc.UnmuteMember(ctx, outsider, b.id, bMember)),
			"BanMember":        e.svc.BanMember(ctx, outsider, b.id, bMember),
			"RemoveMember":     e.svc.RemoveMember(ctx, outsider, b.id, bMember),
		} {
			wantErr(t, name, err, apperr.ErrTayfaNotFound)
		}
	}
	// Olmayan tayfa ile aynı cevap.
	_, err = e.svc.ListMembers(ctx, a.admin, id.New(), "", 0, "")
	wantErr(t, "olmayan tayfa", err, apperr.ErrTayfaNotFound)
	// B'nin davetine A üzerinden ulaşılamaz.
	wantErr(t, "başka tayfanın daveti", e.svc.RevokeInviteLink(ctx, a.admin, a.id, bInv.ID), apperr.ErrInviteNotFound)
	// B'nin üyesine A üzerinden dokunulamaz.
	wantErr(t, "başka tayfanın üyesi", e.svc.BanMember(ctx, a.admin, a.id, bMember), apperr.ErrMemberNotFound)
	if e.status(t, b.id, bMember) != "active/member" || e.status(t, b.id, bPending) != "pending/member" {
		t.Fatal("B'de değişiklik oldu")
	}
}

func third[A, B any](_ A, _ B, err error) error { return err }

func TestGetMembership(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	pending := e.seed(t, tf.id, authz.Member, authz.StatusPending)
	m, err := e.svc.GetMembership(ctx, pending, tf.id, pending)
	if err != nil || m.Status != authz.StatusPending {
		t.Fatalf("kendi üyeliği: %+v %v", m, err)
	}
	_, err = e.svc.GetMembership(ctx, pending, tf.id, tf.admin)
	wantErr(t, "başkası", err, apperr.ErrPermissionDenied)
	_, err = e.svc.GetMembership(ctx, id.New(), tf.id, id.ID{})
	wantErr(t, "başkası (boş)", err, apperr.ErrPermissionDenied)
	u := id.New()
	_, err = e.svc.GetMembership(ctx, u, tf.id, u)
	wantErr(t, "üyeliği yok", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.GetMembership(ctx, u, id.New(), u)
	wantErr(t, "olmayan tayfa", err, apperr.ErrTayfaNotFound)
	e.exec(t, `UPDATE tayfas SET deleted_at = now() WHERE id = $1`, tf.id.PG())
	_, err = e.svc.GetMembership(ctx, pending, tf.id, pending)
	wantErr(t, "silinmiş tayfa", err, apperr.ErrTayfaNotFound)
}

func TestListMembersOrderPagingAndMuteVisibility(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "open")
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	var members []id.ID
	for range 5 {
		members = append(members, e.seed(t, tf.id, authz.Member, authz.StatusActive))
	}
	e.seed(t, tf.id, authz.Member, authz.StatusBanned)
	e.seed(t, tf.id, authz.Member, authz.StatusPending)
	_, err := e.svc.MuteMember(ctx, mod, tf.id, members[0], time.Now().Add(time.Hour))
	must(t, err)

	var got []Member
	token := ""
	pages := 0
	for {
		p, err := e.svc.ListMembers(ctx, members[1], tf.id, authz.StatusActive, 3, token)
		must(t, err)
		got = append(got, p.Items...)
		pages++
		if p.Next == "" {
			break
		}
		token = p.Next
	}
	if pages != 3 || len(got) != 7 || got[0].Role != authz.Admin || got[1].Role != authz.Moderator {
		t.Fatalf("sayfa %d, üye %d, ilk roller %v %v", pages, len(got), got[0].Role, got[1].Role)
	}
	seen := map[id.ID]bool{}
	for _, m := range got {
		if seen[m.UserID] || m.Status != authz.StatusActive {
			t.Fatalf("tekrar ya da yanlış durum: %+v", m)
		}
		seen[m.UserID] = true
		if !m.MutedUntil.IsZero() {
			t.Fatal("üye başkasının susturulduğunu görmemeli")
		}
	}

	p, err := e.svc.ListMembers(ctx, members[0], tf.id, "", 0, "")
	must(t, err)
	for _, m := range p.Items {
		if m.UserID == members[0] && m.MutedUntil.IsZero() {
			t.Fatal("üye kendi susturmasını görmeli")
		}
	}
	p, err = e.svc.ListMembers(ctx, mod, tf.id, "", 0, "")
	must(t, err)
	mutedSeen := false
	for _, m := range p.Items {
		mutedSeen = mutedSeen || !m.MutedUntil.IsZero()
	}
	if !mutedSeen || p.Next != "" {
		t.Fatal("moderatör susturmayı görmeli")
	}

	_, err = e.svc.ListMembers(ctx, members[1], tf.id, authz.StatusBanned, 0, "")
	wantErr(t, "üye banlıları göremez", err, apperr.ErrPermissionDenied)
	b, err := e.svc.ListMembers(ctx, mod, tf.id, authz.StatusBanned, 0, "")
	if err != nil || len(b.Items) != 1 || b.Items[0].Status != authz.StatusBanned {
		t.Fatalf("banlılar = %+v %v", b, err)
	}
	_, err = e.svc.ListMembers(ctx, mod, tf.id, authz.StatusPending, 0, "")
	wantInvalid(t, "bekleyen durum", err)
	_, err = e.svc.ListMembers(ctx, mod, tf.id, "", -1, "")
	wantInvalid(t, "negatif sayfa", err)
	_, err = e.svc.ListMembers(ctx, mod, tf.id, "", 0, "bozuk")
	wantInvalid(t, "bozuk imleç", err)
	// Başka bir istek için üretilmiş imleç kabul edilmez.
	first, err := e.svc.ListMembers(ctx, mod, tf.id, "", 2, "")
	must(t, err)
	_, err = e.svc.ListMembers(ctx, mod, tf.id, authz.StatusBanned, 2, first.Next)
	wantInvalid(t, "başka durumun imleci", err)
	_, err = e.svc.ListPending(ctx, mod, tf.id, 2, first.Next)
	wantInvalid(t, "başka metodun imleci", err)
}

func TestReadOnlyTayfa(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	member := e.seed(t, tf.id, authz.Member, authz.StatusActive)
	pending := e.seed(t, tf.id, authz.Member, authz.StatusPending)
	e.exec(t, `UPDATE tayfas SET deletion_requested_at = now(), deletion_requested_by = $2, deletion_scheduled_for = now() + interval '72 hours' WHERE id = $1`, tf.id.PG(), tf.admin.PG())

	_, err := e.svc.ApproveMember(ctx, tf.admin, tf.id, pending)
	wantErr(t, "onay", err, apperr.ErrTayfaReadOnly)
	wantErr(t, "ban", e.svc.BanMember(ctx, tf.admin, tf.id, member), apperr.ErrTayfaReadOnly)
	_, err = e.svc.ChangeRole(ctx, tf.admin, tf.id, member, authz.Moderator)
	wantErr(t, "rol", err, apperr.ErrTayfaReadOnly)
	if _, err := e.svc.ListMembers(ctx, member, tf.id, "", 0, ""); err != nil {
		t.Fatalf("okuma çalışmalı: %v", err)
	}
}
