package dataexport

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/retention"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore/objstoretest"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type fakeGoogle struct{}

func (fakeGoogle) Verify(_ context.Context, tok, nonce string) (oidc.Identity, error) {
	if strings.HasPrefix(tok, "ok:") && nonce == "n" {
		return oidc.Identity{Provider: "google", Subject: tok[3:], Email: tok[3:] + "@example.com", Name: "Ayşe"}, nil
	}
	return oidc.Identity{}, oidc.ErrInvalidToken
}

var docs = consent.Documents{PrivacyNotice: "2026-11-08", Terms: "2026-11-08"}

// syncBuffer collects log output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeStorage is core's export storage over the fake object storage.
type fakeStorage struct {
	mu      sync.Mutex
	client  *objstore.Client
	keys    map[id.ID]string
	sizes   map[id.ID]int64
	reserve error
}

func (f *fakeStorage) Reserve(_ context.Context, request id.ID) (id.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reserve != nil {
		return id.ID{}, f.reserve
	}
	u := id.New()
	f.keys[u] = "exports/" + strings.ReplaceAll(request.String(), "-", "")
	return u, nil
}

func (f *fakeStorage) UploadURL(_ context.Context, upload id.ID, size int64) (string, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes[upload] = size
	p, err := f.client.PresignPut(f.keys[upload], "application/octet-stream", size, 5*time.Minute)
	return p.URL, p.Headers, err
}

func (f *fakeStorage) Complete(ctx context.Context, upload id.ID) (time.Time, error) {
	f.mu.Lock()
	key, size := f.keys[upload], f.sizes[upload]
	f.mu.Unlock()
	o, err := f.client.Head(ctx, key)
	if err != nil || o.Size != size {
		return time.Time{}, errors.New("boyut")
	}
	return time.Time{}, nil
}

func (f *fakeStorage) DownloadURL(_ context.Context, upload id.ID) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.keys[upload]
	if !ok {
		return "", time.Time{}, ErrStorageNotFound
	}
	p, err := f.client.PresignGet(key, 5*time.Minute)
	return p.URL, p.Expires, err
}

// fakeSource emits fixed records, or a lot of incompressible ones.
type fakeSource struct {
	dir   string
	files []string
	recs  map[string][][]byte
	huge  bool
	err   error
}

func (f *fakeSource) Files() []string { return f.files }

func (f *fakeSource) Export(_ context.Context, emit func(string, [][]byte) error) error {
	if f.err != nil {
		return f.err
	}
	if f.huge {
		// Random base64 compresses to about 3/4: over 20 MiB of zip.
		for range 28 {
			b := make([]byte, 768<<10)
			_, _ = rand.Read(b)
			rec, _ := json.Marshal(base64.StdEncoding.EncodeToString(b))
			if err := emit(f.dir+"/big.json", [][]byte{rec}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, name := range f.files {
		if r, ok := f.recs[name]; ok {
			if err := emit(name, r); err != nil {
				return err
			}
		}
	}
	return nil
}

type env struct {
	pool     *pgxpool.Pool
	svc      *Service
	sessions *session.Service
	verifier *auth.Verifier
	storage  *fakeStorage
	s3       *objstoretest.Server
	core     *fakeSource
	logs     *syncBuffer
	clock    *clock
	birth    *birthdate.Cipher
	keys     *Keys
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, "identity", identitydb.Migrations())
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "dataexport-test")
	key, err := auth.ParseSigningKey("k1", base64.StdEncoding.EncodeToString(seed))
	must(t, err)
	signer := func(aud string) *auth.Signer {
		s, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-identity", Audience: aud, TTL: 15 * time.Minute})
		must(t, err)
		return s
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions, err := session.New(session.Config{
		Pool: pool, Log: quiet, Verifiers: map[string]oidc.Verifier{"google": fakeGoogle{}},
		API: signer(session.AudienceAPI), Registration: signer(session.AudienceRegistration), Documents: docs,
	})
	must(t, err)
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{"k1": key.Public().Key}, Issuer: "tayfam-identity", Audience: session.AudienceAPI})
	must(t, err)
	birth, err := birthdate.ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	must(t, err)
	keys, err := ParseKeys("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	must(t, err)
	s3 := objstoretest.New(t)
	e := &env{
		pool: pool, sessions: sessions, verifier: v, s3: s3, logs: &syncBuffer{}, clock: &clock{t: time.Now().UTC()},
		birth: birth, keys: keys,
		storage: &fakeStorage{client: s3.Client(t), keys: map[id.ID]string{}, sizes: map[id.ID]int64{}},
		core: &fakeSource{dir: "core", files: []string{"core/events.json", "core/poll_votes.json"}, recs: map[string][][]byte{
			"core/events.json": {[]byte(`{"id":"e1","title":"Kamp"}`), []byte(`{"id":"e2","title":"Yürüyüş"}`)},
		}},
	}
	e.svc = e.service(t, func(*Config) {})
	return e
}

func (e *env) service(t *testing.T, mut func(*Config)) *Service {
	t.Helper()
	chat := &fakeSource{dir: "chat", files: []string{"chat/channel_messages.json"}, recs: map[string][][]byte{
		"chat/channel_messages.json": {[]byte(`{"id":"m1","body":"gizli mesaj metni"}`)},
	}}
	cfg := Config{
		Pool: e.pool, Log: slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Keys: e.keys, Birth: e.birth, Storage: e.storage, Core: e.core, Chat: chat,
		AnyStorageHost: true, Now: e.clock.now,
	}
	mut(&cfg)
	s, err := New(cfg)
	must(t, err)
	return s
}

// user signs a new user in, registers them with a birth date and returns
// the caller (a fresh sign-in) and a context carrying the verified token.
func (e *env) user(t *testing.T) (Caller, session.Tokens) {
	t.Helper()
	sub := id.New().String()
	in := session.SignInInput{Provider: "google", IDToken: "ok:" + sub, Nonce: "n", Platform: "android"}
	tok, err := e.sessions.SignIn(ctx, in)
	must(t, err)
	q := store.New(e.pool)
	d, err := birthdate.Parse("2000-02-29")
	must(t, err)
	if _, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: tok.UserID.PG(), BirthDateEnc: e.birth.Encrypt(tok.UserID, d)}); err != nil {
		t.Fatal(err)
	}
	must(t, consent.Record(ctx, q, tok.UserID, docs))
	tok, err = e.sessions.SignIn(ctx, in)
	must(t, err)
	return e.caller(t, tok), tok
}

func (e *env) caller(t *testing.T, tok session.Tokens) Caller {
	t.Helper()
	c, err := e.verifier.Verify(ctx, tok.AccessToken)
	must(t, err)
	return Caller{User: tok.UserID, Session: tok.SessionID, IssuedAt: c.IssuedAt, ExpiresAt: c.ExpiresAt}
}

// run processes the queued jobs and stops.
func (e *env) run(t *testing.T, s *Service) {
	t.Helper()
	for {
		select {
		case j := <-s.jobs:
			s.process(ctx, j)
		default:
			return
		}
	}
}

func (e *env) fetch(t *testing.T, d Download) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	must(t, err)
	r, err := http.DefaultClient.Do(req)
	must(t, err)
	defer func() { _ = r.Body.Close() }()
	b, err := io.ReadAll(r.Body)
	must(t, err)
	if r.StatusCode != http.StatusOK || int64(len(b)) != d.Size {
		t.Fatalf("indirme: %d, %d bayt", r.StatusCode, len(b))
	}
	return b
}

func readZip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	must(t, err)
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		must(t, err)
		data, err := io.ReadAll(rc)
		must(t, err)
		_ = rc.Close()
		out[f.Name] = data
	}
	return out
}

func TestExportFlow(t *testing.T) {
	e := setup(t)
	c, _ := e.user(t)
	crid := id.New()
	r, err := e.svc.Create(ctx, c, crid)
	must(t, err)
	if r.Status != StatusPending {
		t.Fatalf("durum %q", r.Status)
	}
	// A repeat returns the same request.
	if again, err := e.svc.Create(ctx, c, crid); err != nil || again.ID != r.ID {
		t.Fatalf("tekrar gönderim: %v %v", again, err)
	}
	// One at a time.
	if _, err := e.svc.Create(ctx, c, id.New()); !errors.Is(err, ErrInProgress) {
		t.Fatalf("eşzamanlı talep: %v", err)
	}
	e.run(t, e.svc)
	got, err := e.svc.Get(ctx, c.User, r.ID)
	must(t, err)
	if got.Status != StatusReady || got.SizeBytes <= 0 || got.ExpiresAt.Sub(got.ReadyAt) != Lifetime {
		t.Fatalf("hazır değil: %+v", got)
	}

	d, err := e.svc.Download(ctx, c, r.ID)
	must(t, err)
	if len(d.Key) != KeySize || d.Format != Format {
		t.Fatalf("indirme: %+v", d)
	}
	file := e.fetch(t, d)
	// The stored file is encrypted: no plain text of the archive in it.
	if bytes.Contains(file, []byte("Kamp")) || bytes.Contains(file, []byte("PK\x03\x04")) {
		t.Fatal("dosya şifreli değil")
	}
	if _, err := Open(file, bytes.Repeat([]byte{1}, 32), r.ID); err == nil {
		t.Fatal("başka anahtarla açılmamalı")
	}
	if _, err := Open(file, d.Key, id.New()); err == nil {
		t.Fatal("başka talebin ek verisiyle açılmamalı")
	}
	plain, err := Open(file, d.Key, r.ID)
	must(t, err)
	files := readZip(t, plain)
	for _, name := range append(slicesOf(identityFiles), "core/events.json", "core/poll_votes.json", "chat/channel_messages.json", "manifest.json") {
		if _, ok := files[name]; !ok {
			t.Errorf("%s yok", name)
		}
	}
	var profile []map[string]any
	must(t, json.Unmarshal(files["identity/profile.json"], &profile))
	if len(profile) != 1 || profile[0]["birth_date"] != "2000-02-29" || profile[0]["user_id"] != c.User.String() {
		t.Fatalf("profil: %v", profile)
	}
	var events, votes []map[string]any
	must(t, json.Unmarshal(files["core/events.json"], &events))
	must(t, json.Unmarshal(files["core/poll_votes.json"], &votes))
	if len(events) != 2 || events[1]["title"] != "Yürüyüş" || len(votes) != 0 {
		t.Fatalf("core: %v %v", events, votes)
	}
	var m manifest
	must(t, json.Unmarshal(files["manifest.json"], &m))
	if m.FormatVersion != 1 || m.DataExportID != r.ID.String() || m.UserID != c.User.String() {
		t.Fatalf("manifest: %+v", m)
	}
	var signIns []map[string]any
	must(t, json.Unmarshal(files["identity/sign_in_identities.json"], &signIns))
	if len(signIns) != 1 || signIns[0]["provider"] != "google" {
		t.Fatalf("giriş kimlikleri: %v", signIns)
	}

	// chat heard about it through the outbox, with the request ID only.
	var payload string
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text FROM domain_events WHERE type = 'user.data_export_ready' AND subject_id = $1`, c.User.PG()).Scan(&payload))
	if !strings.Contains(payload, r.ID.String()) || len(payload) > 80 {
		t.Fatalf("olay: %s", payload)
	}

	// Nothing secret or personal reached the log.
	logs := e.logs.String()
	for _, secret := range []string{d.URL, base64.StdEncoding.EncodeToString(d.Key), "Kamp", "gizli mesaj", "2000-02-29", "X-Amz-Signature", "example.com"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log %q içermemeli", secret)
		}
	}
	if !strings.Contains(logs, r.ID.String()) {
		t.Error("log talep kimliğini taşımalı")
	}

	// One counted request in 30 days.
	if _, err := e.svc.Create(ctx, c, id.New()); !errors.Is(err, ErrLimit) {
		t.Fatalf("30 gün sınırı: %v", err)
	}
	list, next, err := e.svc.List(ctx, c.User)
	must(t, err)
	if len(list) != 1 || next.Sub(r.RequestedAt) != Interval {
		t.Fatalf("liste: %v %v", list, next)
	}
	e.clock.add(Interval + time.Minute)
	c.ExpiresAt = e.clock.now().Add(15 * time.Minute)
	if _, err := e.svc.Create(ctx, c, id.New()); err != nil {
		t.Fatalf("30 gün sonra yeni talep açılmalı: %v", err)
	}
}

func slicesOf(s []string) []string { return append([]string(nil), s...) }

// Another user's request is not found, in any method (IDOR).
func TestExportIDOR(t *testing.T) {
	e := setup(t)
	owner, _ := e.user(t)
	other, _ := e.user(t)
	r, err := e.svc.Create(ctx, owner, id.New())
	must(t, err)
	e.run(t, e.svc)
	if _, err := e.svc.Get(ctx, other.User, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("başkasının talebi: %v", err)
	}
	if _, err := e.svc.Download(ctx, other, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("başkasının indirmesi: %v", err)
	}
	if list, _, err := e.svc.List(ctx, other.User); err != nil || len(list) != 0 {
		t.Fatalf("başkasının listesi: %v %v", list, err)
	}
	// A request ID copied into another user's row does not open the key.
	var wrapped []byte
	must(t, e.pool.QueryRow(ctx, `SELECT wrapped_key FROM data_export_requests WHERE id = $1`, r.ID.PG()).Scan(&wrapped))
	if _, err := e.keys.Unwrap(r.ID, other.User, wrapped); err == nil {
		t.Fatal("anahtar başka kullanıcı için açılmamalı")
	}
}

// Failed requests do not count; a too large archive fails as too_large.
func TestExportFailures(t *testing.T) {
	e := setup(t)
	c, _ := e.user(t)
	e.core.huge = true
	r, err := e.svc.Create(ctx, c, id.New())
	must(t, err)
	e.run(t, e.svc)
	got, err := e.svc.Get(ctx, c.User, r.ID)
	must(t, err)
	if got.Status != StatusFailed || got.FailureReason != FailTooLarge {
		t.Fatalf("16 MiB üstü: %+v", got)
	}
	if !strings.Contains(e.logs.String(), `"reason":"too_large"`) {
		t.Error("too_large log'a yazılmalı")
	}
	if _, err := e.svc.Download(ctx, c, r.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("başarısız indirilemez: %v", err)
	}

	// A service that cannot answer: unavailable, and still not counted.
	e.core.huge = false
	e.core.err = errors.New("core kapalı")
	r2, err := e.svc.Create(ctx, c, id.New())
	must(t, err)
	e.run(t, e.svc)
	if got, _ := e.svc.Get(ctx, c.User, r2.ID); got.Status != StatusFailed {
		t.Fatalf("core hatası: %+v", got)
	}
	e.core.err = nil
	r3, err := e.svc.Create(ctx, c, id.New())
	must(t, err)
	e.run(t, e.svc)
	if got, _ := e.svc.Get(ctx, c.User, r3.ID); got.Status != StatusReady {
		t.Fatalf("başarısızlardan sonra hazır olmalı: %+v", got)
	}

	// Storage off: refused at once, nothing written.
	off := setup(t)
	oc, _ := off.user(t)
	off.storage.reserve = errors.New("r2 yok")
	if _, err := off.svc.Create(ctx, oc, id.New()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("depolama yokken: %v", err)
	}
	disabled := off.service(t, func(c *Config) { c.Keys = nil })
	if _, err := disabled.Create(ctx, oc, id.New()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("anahtar yokken: %v", err)
	}
	if list, _, _ := off.svc.List(ctx, oc.User); len(list) != 0 {
		t.Fatalf("kayıt açılmamalı: %v", list)
	}

	// A token about to expire: the client refreshes first.
	soon := oc
	soon.ExpiresAt = off.clock.now().Add(30 * time.Second)
	off.storage.reserve = nil
	if _, err := off.svc.Create(ctx, soon, id.New()); !errors.Is(err, ErrTokenExpiring) {
		t.Fatalf("süresi dolmak üzere: %v", err)
	}

	// An upload link outside R2 is refused when not local.
	strict := off.service(t, func(c *Config) { c.AnyStorageHost = false })
	sr, err := strict.Create(ctx, oc, id.New())
	must(t, err)
	off.run(t, strict)
	if got, _ := strict.Get(ctx, oc.User, sr.ID); got.Status != StatusFailed {
		t.Fatalf("R2 dışı adres reddedilmeli: %+v", got)
	}
}

// The download needs a sign-in with the provider in the last 10 minutes.
func TestExportDownloadNeedsReauth(t *testing.T) {
	e := setup(t)
	c, tok := e.user(t)
	r, err := e.svc.Create(ctx, c, id.New())
	must(t, err)
	e.run(t, e.svc)
	e.clock.add(ReauthWindow + time.Minute)
	if _, err := e.svc.Download(ctx, c, r.ID); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("eski giriş: %v", err)
	}
	// A refreshed token (new iat) of the old session is not enough.
	refreshed, err := e.sessions.Refresh(ctx, tok.RefreshToken)
	must(t, err)
	rc := e.caller(t, refreshed)
	rc.IssuedAt = e.clock.now()
	if _, err := e.svc.Download(ctx, rc, r.ID); !errors.Is(err, ErrReauthenticationRequired) {
		t.Fatalf("yenilenmiş token yetmemeli: %v", err)
	}
}

// Retention: the key and the file record go after 7 days, a stuck request
// fails, the row goes after 30 days; account deletion removes all at once.
func TestExportRetentionAndDeletion(t *testing.T) {
	e := setup(t)
	c, tok := e.user(t)
	r, err := e.svc.Create(ctx, c, id.New())
	must(t, err)
	e.run(t, e.svc)

	ret, err := retention.New(retention.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock.now().Add(Lifetime + time.Minute) }})
	must(t, err)
	res, err := ret.RunOnce(ctx)
	must(t, err)
	if res.ExportsExpired != 1 {
		t.Fatalf("süre dolumu: %+v", res)
	}
	var keyNull, uploadNull bool
	var st string
	must(t, e.pool.QueryRow(ctx, `SELECT status, wrapped_key IS NULL, upload_id IS NULL FROM data_export_requests WHERE id = $1`, r.ID.PG()).Scan(&st, &keyNull, &uploadNull))
	if st != StatusExpired || !keyNull || !uploadNull {
		t.Fatalf("anahtar silinmeli: %s %v %v", st, keyNull, uploadNull)
	}
	e.clock.add(Lifetime + time.Minute)
	if _, err := e.svc.Download(ctx, c, r.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("süresi dolan: %v", err)
	}

	// The row stays for the limit, then goes.
	ret, err = retention.New(retention.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return time.Now().Add(RowRetention + time.Hour) }})
	must(t, err)
	res, err = ret.RunOnce(ctx)
	must(t, err)
	if res.ExportsDeleted != 1 {
		t.Fatalf("satır silme: %+v", res)
	}

	// A request left pending (identity restarted) fails as timeout.
	c2, tok2 := e.user(t)
	c2.ExpiresAt = e.clock.now().Add(15 * time.Minute)
	stuck, err := e.svc.Create(ctx, c2, id.New())
	must(t, err)
	<-e.svc.jobs // lost with the restart
	ret, err = retention.New(retention.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.clock.now().Add(StaleAfter + time.Minute) }})
	must(t, err)
	_, err = ret.RunOnce(ctx)
	must(t, err)
	if got, _ := e.svc.Get(ctx, c2.User, stuck.ID); got.Status != StatusFailed || got.FailureReason != FailTimeout {
		t.Fatalf("takılı talep: %+v", got)
	}

	// Account deletion removes the requests and their keys at once.
	del, err := deletion.New(deletion.Config{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	fresh, err := e.sessions.SignIn(ctx, session.SignInInput{Provider: "google", IDToken: "ok:" + subjectOf(t, e, tok2), Nonce: "n", Platform: "android"})
	must(t, err)
	fc := e.caller(t, fresh)
	must(t, del.Delete(ctx, deletion.Caller{User: fc.User, Session: fc.Session, IssuedAt: time.Now()}))
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM data_export_requests WHERE user_id = $1`, c2.User.PG()).Scan(&n))
	if n != 0 {
		t.Fatalf("hesap silinince talepler silinmeli: %d", n)
	}
	_ = tok
}

func subjectOf(t *testing.T, e *env, tok session.Tokens) string {
	t.Helper()
	var sub string
	must(t, e.pool.QueryRow(ctx, `SELECT subject FROM auth_identities WHERE user_id = $1`, tok.UserID.PG()).Scan(&sub))
	return sub
}
