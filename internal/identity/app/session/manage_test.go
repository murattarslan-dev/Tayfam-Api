package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var testDocs = consent.Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}

func caller(t Tokens) Caller { return Caller{User: t.UserID, Session: t.SessionID} }

func (e *env) reason(t *testing.T, session id.ID) string {
	t.Helper()
	var r *string
	if err := e.pool.QueryRow(context.Background(), `SELECT revoked_reason FROM sessions WHERE id = $1`, session.PG()).Scan(&r); err != nil {
		t.Fatal(err)
	}
	if r == nil {
		return ""
	}
	return *r
}

func TestConsentRequiredForNewVersion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	pending := e.signIn(t, "sub-1")
	if pending.ConsentRequired {
		t.Fatal("kayıt bekleyen hesapta onay ayrıca istenmez; kayıt akışı onayı alır")
	}
	e.register(t, pending.UserID)
	q := store.New(e.pool)
	if err := consent.Record(ctx, q, pending.UserID, consent.Documents{PrivacyNotice: "2026-01-01", Terms: "2026-11-08"}); err != nil {
		t.Fatal(err)
	}
	// Aydınlatma metninin yeni sürümü onaylanmamış.
	tok := e.signIn(t, "sub-1")
	if !tok.ConsentRequired {
		t.Fatal("yeni metin sürümünde onay istenmedi")
	}
	if err := consent.Record(ctx, q, pending.UserID, testDocs); err != nil {
		t.Fatal(err)
	}
	r, err := e.svc.Refresh(ctx, tok.RefreshToken)
	if err != nil || r.ConsentRequired {
		t.Fatalf("onaydan sonra yenileme: %+v %v", r.ConsentRequired, err)
	}
	// Geri çekilen onay yeniden istenir. Geri çekmenin henüz sorgusu yok;
	// identity_app'in consent_records'ta UPDATE yetkisi de yok.
	if _, err := e.admin.Exec(ctx, `UPDATE consent_records SET withdrawn_at = now() WHERE user_id = $1 AND document_type = 'terms'`, pending.UserID.PG()); err != nil {
		t.Fatal(err)
	}
	if r, err = e.svc.Refresh(ctx, r.RefreshToken); err != nil || !r.ConsentRequired {
		t.Fatalf("geri çekilen onay: %+v %v", r.ConsentRequired, err)
	}
}

func TestListSessionsShowsOnlyOwn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a1 := e.signIn(t, "sub-a")
	a2, err := e.svc.SignIn(ctx, SignInInput{Provider: "google", IDToken: "ok:sub-a", Nonce: "n", Platform: "web"})
	if err != nil {
		t.Fatal(err)
	}
	e.signIn(t, "sub-b")
	gone := e.signIn(t, "sub-a")
	if err := e.svc.SignOut(ctx, gone.SessionID); err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.List(ctx, caller(a1))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("oturum sayısı = %d, 2 bekleniyordu (kapalı ve başkasının oturumu görünmez)", len(list))
	}
	seen := map[id.ID]Info{}
	for _, s := range list {
		seen[s.ID] = s
	}
	if s := seen[a1.SessionID]; !s.Current || s.Platform != "android" || s.CreatedAt.IsZero() || s.LastSeenAt.IsZero() {
		t.Fatalf("şu anki oturum: %+v", s)
	}
	if s := seen[a2.SessionID]; s.Current || s.Platform != "web" {
		t.Fatalf("diğer oturum: %+v", s)
	}
}

func TestRevokeOtherUsersSessionIsNotFound(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	a, b := e.signIn(t, "sub-a"), e.signIn(t, "sub-b")
	for name, target := range map[string]id.ID{"başkasının oturumu": b.SessionID, "bilinmeyen": id.New()} {
		if err := e.svc.Revoke(ctx, caller(a), target); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if r := e.reason(t, b.SessionID); r != "" {
		t.Fatalf("başka kullanıcının oturumu kapandı: %q", r)
	}
	if _, err := e.svc.Refresh(ctx, b.RefreshToken); err != nil {
		t.Fatalf("başka kullanıcının oturumu etkilendi: %v", err)
	}
	// Kullanıcı kimliği token'dan gelir; oturum kimliği başkasınınsa çağıran
	// oturumu doğrulanamaz.
	if _, err := e.svc.List(ctx, Caller{User: a.UserID, Session: b.SessionID}); !errors.Is(err, ErrCallerSession) {
		t.Fatalf("başkasının oturum kimliğiyle liste: %v", err)
	}
}

func TestRevokeRemoteSession(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	here, there := e.signIn(t, "sub-a"), e.signIn(t, "sub-a")
	if err := e.svc.Revoke(ctx, caller(here), there.SessionID); err != nil {
		t.Fatal(err)
	}
	if r := e.reason(t, there.SessionID); r != "user_signout_remote" {
		t.Fatalf("iptal nedeni = %q", r)
	}
	if _, err := e.svc.Refresh(ctx, there.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("kapatılan oturum yenilendi: %v", err)
	}
	// Kapatılan cihazın hâlâ süresi dolmamış erişim token'ı oturum
	// yönetemez.
	if _, err := e.svc.List(ctx, caller(there)); !errors.Is(err, ErrCallerSession) {
		t.Fatalf("kapalı oturumla liste: %v", err)
	}
	if err := e.svc.Revoke(ctx, caller(there), here.SessionID); !errors.Is(err, ErrCallerSession) {
		t.Fatalf("kapalı oturumla iptal: %v", err)
	}
	if err := e.svc.Revoke(ctx, caller(here), there.SessionID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ikinci iptal: %v", err)
	}
	// Kendi oturumunu kapatmak çıkıştır.
	if err := e.svc.Revoke(ctx, caller(here), here.SessionID); err != nil {
		t.Fatal(err)
	}
	if r := e.reason(t, here.SessionID); r != "logout" {
		t.Fatalf("kendi oturumu nedeni = %q", r)
	}
}

func TestRevokeOthersKeepsCurrent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	here := e.signIn(t, "sub-a")
	o1, o2 := e.signIn(t, "sub-a"), e.signIn(t, "sub-a")
	other := e.signIn(t, "sub-b")
	n, err := e.svc.RevokeOthers(ctx, caller(here))
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	for _, s := range []Tokens{o1, o2} {
		if r := e.reason(t, s.SessionID); r != "user_signout_remote" {
			t.Fatalf("iptal nedeni = %q", r)
		}
	}
	if r := e.reason(t, here.SessionID); r != "" {
		t.Fatal("şu anki oturum kapandı")
	}
	if r := e.reason(t, other.SessionID); r != "" {
		t.Fatal("başka kullanıcının oturumu kapandı")
	}
	if _, err := e.svc.Refresh(ctx, here.RefreshToken); err != nil {
		t.Fatalf("şu anki oturum yenilenemedi: %v", err)
	}
}

func TestRefreshTouchesDevice(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	tok := e.signIn(t, "sub-a")
	if _, err := e.pool.Exec(ctx, `UPDATE devices SET last_seen_at = now() - interval '80 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Refresh(ctx, tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	var seen time.Time
	if err := e.pool.QueryRow(ctx, `SELECT last_seen_at FROM devices`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if time.Since(seen) > time.Minute {
		t.Fatalf("yenileme cihazın son görülmesini güncellemedi: %v", seen)
	}
}
