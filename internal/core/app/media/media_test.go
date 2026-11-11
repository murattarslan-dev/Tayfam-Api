package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/imaging/imagingtest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore/objstoretest"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

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

type env struct {
	svc   *Service
	pool  *pgxpool.Pool
	s3    *objstoretest.Server
	logs  *syncBuffer
	clock *clock
	tayfa id.ID
	// owner created the tayfa (admin); member is an active member.
	owner, member, stranger id.ID
}

func setup(t *testing.T, mut ...func(*Config)) *env {
	t.Helper()
	pool := dbtest.New(t, "core", coredb.Migrations())
	s3 := objstoretest.New(t)
	logs := &syncBuffer{}
	c := &clock{t: time.Now().UTC()}
	cfg := Config{
		Pool: pool, Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Store: s3.Client(t), Now: c.now,
	}
	for _, m := range mut {
		m(&cfg)
	}
	svc, err := New(cfg)
	must(t, err)
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	e := &env{svc: svc, pool: pool, s3: s3, logs: logs, clock: c, owner: id.New(), member: id.New(), stranger: id.New()}
	v, err := tayfas.Create(ctx, e.owner, tayfa.CreateInput{Name: "Olta Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	e.tayfa = v.ID
	must(t, txn.WithActor(ctx, pool, e.member, func(_ pgx.Tx, q *store.Queries) error {
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: e.tayfa.PG(), UserID: e.member.PG(), Role: "member", Status: "active", JoinedVia: "code", JoinedAt: ts(time.Now()),
		})
	}))
	return e
}

func gpsJPEG() []byte { return imagingtest.JPEG(imagingtest.Quadrants(300, 200), 6) }

func (e *env) create(t *testing.T, user id.ID, purpose string, tayfa id.ID, body []byte) Created {
	t.Helper()
	c, err := e.svc.CreateUpload(ctx, user, CreateInput{
		Purpose: purpose, ContentType: "image/jpeg", SizeBytes: int64(len(body)), Tayfa: tayfa, ClientRequestID: id.New().String(),
	})
	must(t, err)
	return c
}

// upload runs the whole flow as a client would and returns the READY upload.
func (e *env) upload(t *testing.T, user id.ID, purpose string, tayfa id.ID, body []byte) Upload {
	t.Helper()
	c := e.create(t, user, purpose, tayfa, body)
	if code := objstoretest.Do(t, http.MethodPut, c.Link.URL, c.Link.Headers, body); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	u, err := e.svc.CompleteUpload(ctx, user, c.Upload.ID)
	must(t, err)
	if u.Status != StatusUploaded {
		t.Fatalf("durum: %s", u.Status)
	}
	if worked, err := e.svc.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("işleme: %v %v", worked, err)
	}
	u, err = e.svc.GetUpload(ctx, user, c.Upload.ID)
	must(t, err)
	return u
}

func (e *env) attach(t *testing.T, user, upload id.ID, purpose string, tayfa id.ID, target Target) error {
	t.Helper()
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return Attach(ctx, store.New(tx), user, upload, purpose, tayfa, target, time.Now())
	})
}

func TestUploadFlow(t *testing.T) {
	e := setup(t)
	body := gpsJPEG()
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, body)
	if c.Upload.Status != StatusPending || c.Link.URL == "" || c.Link.Headers["Content-Length"] == "" {
		t.Fatalf("oluşturma: %+v", c)
	}
	if d := c.Link.Expires.Sub(e.clock.now()); d <= 0 || d > UploadLinkTTL+time.Second {
		t.Fatalf("link süresi: %v", d)
	}
	// Completing before the PUT finds nothing.
	if _, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID); !errors.Is(err, apperr.ErrUploadNotReceived) {
		t.Fatalf("PUT'tan önce: %v", err)
	}
	if code := objstoretest.Do(t, http.MethodPut, c.Link.URL, c.Link.Headers, body); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	u, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	if u.Status != StatusUploaded || u.URLs != nil {
		t.Fatalf("tamamlama: %+v", u)
	}
	// A repeat changes nothing.
	if u, err = e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID); err != nil || u.Status != StatusUploaded {
		t.Fatalf("tekrar: %+v %v", u, err)
	}
	if worked, err := e.svc.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("işleme: %v %v", worked, err)
	}
	u, err = e.svc.GetUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	if u.Status != StatusReady || u.URLs == nil || u.URLs.ContentType != "image/jpeg" || u.URLs.Width != 200 || u.URLs.Height != 300 {
		t.Fatalf("hazır: %+v %+v", u, u.URLs)
	}
	// The served file is the re-encoded one: no EXIF, no GPS.
	for _, link := range []string{u.URLs.Full, u.URLs.Preview} {
		resp, err := http.Get(link)
		must(t, err)
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || bytes.Contains(got, []byte("Exif")) || bytes.Contains(got, []byte(imagingtest.Marker)) {
			t.Fatalf("sunulan dosya: %d, EXIF/GPS içermemeli", resp.StatusCode)
		}
	}
	// The raw file is gone; only the two outputs remain.
	keys := e.s3.Keys()
	if len(keys) != 2 || strings.Contains(strings.Join(keys, ","), "incoming/") {
		t.Fatalf("depodaki nesneler: %v", keys)
	}
	if worked, _ := e.svc.ProcessNext(ctx); worked {
		t.Fatal("kuyrukta iş kalmamalı")
	}
}

// A file named and declared as JPEG that is not an image is rejected.
func TestNotAnImageIsRejected(t *testing.T) {
	e := setup(t)
	body := []byte("<?php system($_GET['x']); ?> foto.jpg değil")
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, body)
	if code := objstoretest.Do(t, http.MethodPut, c.Link.URL, c.Link.Headers, body); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	_, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	_, err = e.svc.ProcessNext(ctx)
	must(t, err)
	u, err := e.svc.GetUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	if u.Status != StatusRejected || u.RejectReason != "not_image" || u.URLs != nil {
		t.Fatalf("ret: %+v", u)
	}
	if len(e.s3.Keys()) != 0 {
		t.Fatalf("ham dosya silinmeli: %v", e.s3.Keys())
	}
	// A rejected upload cannot be attached.
	if err := e.attach(t, e.owner, u.ID, PurposeAvatar, id.ID{}, Target{TargetUser, e.owner}); !errors.Is(err, apperr.ErrUploadNotReady) {
		t.Fatalf("reddedilen bağlanmamalı: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	base := CreateInput{Purpose: PurposeAvatar, ContentType: "image/jpeg", SizeBytes: 100}
	for name, tc := range map[string]struct {
		mut  func(*CreateInput)
		user id.ID
		want error
	}{
		"bilinmeyen tür":        {func(in *CreateInput) { in.ContentType = "image/gif" }, e.owner, apperr.Validation("content_type")},
		"webp":                  {func(in *CreateInput) { in.ContentType = "image/webp" }, e.owner, apperr.ErrMediaTypeUnsupported},
		"heic":                  {func(in *CreateInput) { in.ContentType = "image/heic" }, e.owner, apperr.ErrMediaTypeUnsupported},
		"boş":                   {func(in *CreateInput) { in.SizeBytes = 0 }, e.owner, apperr.Validation("size_bytes")},
		"10 MiB üstü":           {func(in *CreateInput) { in.SizeBytes = MaxUploadBytes + 1 }, e.owner, apperr.Validation("size_bytes")},
		"avatarda tayfa":        {func(in *CreateInput) { in.Tayfa = e.tayfa }, e.owner, apperr.Validation("tayfa_id")},
		"kapakta tayfa yok":     {func(in *CreateInput) { in.Purpose = PurposeCover }, e.owner, apperr.Validation("tayfa_id")},
		"bilinmeyen amaç":       {func(in *CreateInput) { in.Purpose = "data_export" }, e.owner, apperr.Validation("purpose")},
		"tekrar kimliği yok":    {func(in *CreateInput) { in.ClientRequestID = "x" }, e.owner, apperr.Validation("client_request_id")},
		"üye olmadığı tayfa":    {func(in *CreateInput) { in.Purpose, in.Tayfa = PurposeCatchPhoto, e.tayfa }, e.stranger, apperr.ErrTayfaNotFound},
		"olmayan tayfa":         {func(in *CreateInput) { in.Purpose, in.Tayfa = PurposeCover, id.New() }, e.owner, apperr.ErrTayfaNotFound},
		"üyenin av fotoğrafı":   {func(in *CreateInput) { in.Purpose, in.Tayfa = PurposeCatchPhoto, e.tayfa }, e.member, nil},
		"geçerli avatar isteği": {func(*CreateInput) {}, e.stranger, nil},
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			in.ClientRequestID = id.New().String()
			tc.mut(&in)
			c, err := e.svc.CreateUpload(ctx, tc.user, in)
			var want *apperr.ValidationError
			switch {
			case tc.want == nil:
				if err != nil || c.Link.URL == "" {
					t.Fatalf("kabul edilmeli: %v", err)
				}
			case errors.As(tc.want, &want):
				var got *apperr.ValidationError
				if !errors.As(err, &got) || got.Field != want.Field {
					t.Fatalf("%v, %v bekleniyordu", err, tc.want)
				}
			case !errors.Is(err, tc.want):
				t.Fatalf("%v, %v bekleniyordu", err, tc.want)
			}
		})
	}

	// A retry returns the same upload with a fresh link; a different
	// request with the same ID is refused.
	in := base
	in.ClientRequestID = id.New().String()
	first, err := e.svc.CreateUpload(ctx, e.owner, in)
	must(t, err)
	again, err := e.svc.CreateUpload(ctx, e.owner, in)
	if err != nil || again.Upload.ID != first.Upload.ID || again.Link.URL == "" {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	in.SizeBytes++
	var v *apperr.ValidationError
	if _, err := e.svc.CreateUpload(ctx, e.owner, in); !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Fatalf("farklı istek aynı kimlikle: %v", err)
	}
}

// Another user's uploads cannot be completed, read or attached, and are
// not served to those who may not see them (IDOR).
func TestIDOR(t *testing.T) {
	e := setup(t)
	body := gpsJPEG()
	avatar := e.upload(t, e.owner, PurposeAvatar, id.ID{}, body)
	if avatar.Status != StatusReady {
		t.Fatalf("avatar: %+v", avatar)
	}
	if _, err := e.svc.CompleteUpload(ctx, e.stranger, avatar.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının tamamlaması: %v", err)
	}
	if _, err := e.svc.GetUpload(ctx, e.stranger, avatar.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının okuması: %v", err)
	}
	if err := e.svc.AttachAvatar(ctx, e.stranger, avatar.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının avatarı bağlanmamalı: %v", err)
	}
	// Not attached yet: only the owner gets links.
	urls := func(viewer id.ID, ids ...id.ID) []URLs {
		t.Helper()
		out, err := e.svc.MediaURLs(ctx, viewer, ids)
		must(t, err)
		return out
	}
	if got := urls(e.stranger, avatar.ID); len(got) != 0 {
		t.Fatal("bağlanmamış dosya yalnızca sahibine sunulmalı")
	}
	if got := urls(e.owner, avatar.ID); len(got) != 1 {
		t.Fatal("sahibi kendi dosyasını görmeli")
	}
	must(t, e.svc.AttachAvatar(ctx, e.owner, avatar.ID))
	must(t, e.svc.AttachAvatar(ctx, e.owner, avatar.ID)) // a repeat succeeds
	if got := urls(e.stranger, avatar.ID); len(got) != 1 || got[0].Full == "" {
		t.Fatal("avatar oturum açmış herkese sunulmalı")
	}
	// A block, either way, hides it.
	_, err := e.pool.Exec(ctx, "INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)", e.stranger.PG(), e.owner.PG())
	must(t, err)
	if got := urls(e.stranger, avatar.ID); len(got) != 0 {
		t.Fatal("engelleyen avatarı görmemeli")
	}

	// A cover of the tayfa: members yes, strangers no.
	cover := e.upload(t, e.owner, PurposeCover, e.tayfa, body)
	if err := e.attach(t, e.member, cover.ID, PurposeCover, e.tayfa, Target{TargetTayfa, e.tayfa}); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("başkasının kapağı bağlanmamalı: %v", err)
	}
	if err := e.attach(t, e.owner, cover.ID, PurposeAvatar, id.ID{}, Target{TargetUser, e.owner}); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("kapak avatar olarak bağlanmamalı: %v", err)
	}
	must(t, e.attach(t, e.owner, cover.ID, PurposeCover, e.tayfa, Target{TargetTayfa, e.tayfa}))
	if err := e.attach(t, e.owner, cover.ID, PurposeCover, e.tayfa, Target{TargetEvent, id.New()}); !errors.Is(err, apperr.ErrUploadInUse) {
		t.Fatalf("bağlı dosya başka içeriğe bağlanmamalı: %v", err)
	}
	if got := urls(e.member, cover.ID, avatar.ID); len(got) != 2 || got[0].UploadID != cover.ID {
		t.Fatalf("üye kapağı görmeli, sıra korunmalı: %d", len(got))
	}
	if got := urls(e.stranger, cover.ID); len(got) != 0 {
		t.Fatal("üye olmayan kapağı görmemeli")
	}
	// Unknown IDs are left out; duplicates and too many are refused.
	if got := urls(e.member, id.New()); len(got) != 0 {
		t.Fatal("bilinmeyen dosya")
	}
	var v *apperr.ValidationError
	if _, err := e.svc.MediaURLs(ctx, e.member, []id.ID{cover.ID, cover.ID}); !errors.As(err, &v) {
		t.Fatalf("tekrarlı kimlik: %v", err)
	}
	many := make([]id.ID, MaxURLRequest+1)
	for i := range many {
		many[i] = id.New()
	}
	if _, err := e.svc.MediaURLs(ctx, e.member, many); !errors.As(err, &v) {
		t.Fatalf("100'den fazla: %v", err)
	}
}

func TestRateLimitAndQuota(t *testing.T) {
	e := setup(t, func(c *Config) { c.UploadsPerHour = 2; c.QuotaBytes = 1500 })
	in := func(size int64) CreateInput {
		return CreateInput{Purpose: PurposeAvatar, ContentType: "image/png", SizeBytes: size, ClientRequestID: id.New().String()}
	}
	_, err := e.svc.CreateUpload(ctx, e.owner, in(1000))
	must(t, err)
	if _, err := e.svc.CreateUpload(ctx, e.owner, in(600)); !errors.Is(err, apperr.ErrUploadQuotaExceeded) {
		t.Fatalf("kota: %v", err)
	}
	_, err = e.svc.CreateUpload(ctx, e.owner, in(400))
	must(t, err)
	if _, err := e.svc.CreateUpload(ctx, e.owner, in(1)); !errors.Is(err, apperr.ErrUploadRateLimited) {
		t.Fatalf("sıklık: %v", err)
	}
	// Others are not affected.
	_, err = e.svc.CreateUpload(ctx, e.member, in(1))
	must(t, err)
}

func TestMediaOff(t *testing.T) {
	pool := dbtest.New(t, "core", coredb.Migrations())
	svc, err := New(Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	if svc.Enabled() {
		t.Fatal("depo yokken kapalı olmalı")
	}
	u := id.New()
	_, e1 := svc.CreateUpload(ctx, u, CreateInput{Purpose: PurposeAvatar, ContentType: "image/jpeg", SizeBytes: 1, ClientRequestID: id.New().String()})
	_, e2 := svc.CompleteUpload(ctx, u, id.New())
	_, e3 := svc.GetUpload(ctx, u, id.New())
	_, e4 := svc.MediaURLs(ctx, u, []id.ID{id.New()})
	for _, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, apperr.ErrUnavailable) {
			t.Fatalf("kapalıyken UNAVAILABLE: %v", err)
		}
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.RunProcessor(c); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunCleanup(c); err != nil {
		t.Fatal(err)
	}
}

// A storage outage is retried with backoff; after five attempts the upload
// is rejected as failed.
func TestProcessingRetries(t *testing.T) {
	e := setup(t)
	body := gpsJPEG()
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, body)
	if code := objstoretest.Do(t, http.MethodPut, c.Link.URL, c.Link.Headers, body); code != http.StatusOK {
		t.Fatal(code)
	}
	_, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	e.s3.SetFail(http.StatusServiceUnavailable)
	for i := 1; i <= maxAttempts; i++ {
		worked, err := e.svc.ProcessNext(ctx)
		if err != nil || !worked {
			t.Fatalf("deneme %d: %v %v", i, worked, err)
		}
		if worked, _ := e.svc.ProcessNext(ctx); worked {
			t.Fatalf("deneme %d: ertelenen iş hemen alınmamalı", i)
		}
		e.clock.add(11 * time.Minute)
	}
	u, err := e.svc.GetUpload(ctx, e.owner, c.Upload.ID)
	must(t, err)
	if u.Status != StatusRejected || u.RejectReason != "failed" {
		t.Fatalf("5 denemeden sonra: %+v", u)
	}
}

func TestCleanup(t *testing.T) {
	e := setup(t)
	body := gpsJPEG()
	pending := e.create(t, e.owner, PurposeAvatar, id.ID{}, body)
	if code := objstoretest.Do(t, http.MethodPut, pending.Link.URL, pending.Link.Headers, body); code != http.StatusOK {
		t.Fatal(code)
	}
	ready := e.upload(t, e.owner, PurposeAvatar, id.ID{}, body)
	kept := e.upload(t, e.owner, PurposeAvatar, id.ID{}, body)
	must(t, e.svc.AttachAvatar(ctx, e.owner, kept.ID))
	if n, err := e.svc.Cleanup(ctx); err != nil || n != 0 {
		t.Fatalf("erken temizlik: %d %v", n, err)
	}
	e.clock.add(25 * time.Hour)
	if n, err := e.svc.Cleanup(ctx); err != nil || n != 1 {
		t.Fatalf("24 saat: %d %v", n, err)
	}
	if _, err := e.svc.GetUpload(ctx, e.owner, pending.Upload.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("bekleyen silinmeli: %v", err)
	}
	e.clock.add(7 * 24 * time.Hour)
	if n, err := e.svc.Cleanup(ctx); err != nil || n != 1 {
		t.Fatalf("7 gün: %d %v", n, err)
	}
	if _, err := e.svc.GetUpload(ctx, e.owner, ready.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("bağlanmamış silinmeli: %v", err)
	}
	if got, err := e.svc.GetUpload(ctx, e.owner, kept.ID); err != nil || got.Status != StatusReady {
		t.Fatalf("bağlı avatar kalmalı: %v", err)
	}
	if len(e.s3.Keys()) != 2 {
		t.Fatalf("yalnızca bağlı avatarın iki dosyası kalmalı: %v", e.s3.Keys())
	}
	// The avatar changes (user.profile_updated without it): it goes too.
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return UserProfileUpdated(ctx, store.New(tx), e.owner, id.ID{}) }))
	if n, err := e.svc.Cleanup(ctx); err != nil || n != 1 || len(e.s3.Keys()) != 0 {
		t.Fatalf("eski avatar: %d %v %v", n, err, e.s3.Keys())
	}
}

// Keys, links, credentials and the file's content never reach the logs,
// on any path: success, rejection, outage, cleanup.
func TestLogsCarryNoSecrets(t *testing.T) {
	e := setup(t)
	body := gpsJPEG()
	ok := e.upload(t, e.owner, PurposeAvatar, id.ID{}, body)
	bad := e.create(t, e.owner, PurposeAvatar, id.ID{}, []byte("resim değil"))
	objstoretest.Do(t, http.MethodPut, bad.Link.URL, bad.Link.Headers, []byte("resim değil"))
	_, err := e.svc.CompleteUpload(ctx, e.owner, bad.Upload.ID)
	must(t, err)
	_, err = e.svc.ProcessNext(ctx)
	must(t, err)
	down := e.create(t, e.owner, PurposeAvatar, id.ID{}, body)
	objstoretest.Do(t, http.MethodPut, down.Link.URL, down.Link.Headers, body)
	_, err = e.svc.CompleteUpload(ctx, e.owner, down.Upload.ID)
	must(t, err)
	e.s3.SetFail(http.StatusInternalServerError)
	_, _ = e.svc.ProcessNext(ctx)
	_, _ = e.svc.CompleteUpload(ctx, e.owner, e.create(t, e.owner, PurposeAvatar, id.ID{}, body).Upload.ID)
	var keys []string
	rows, err := e.pool.Query(ctx, "SELECT r2_key FROM uploads")
	must(t, err)
	for rows.Next() {
		var k string
		must(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	if len(keys) != 4 {
		t.Fatalf("anahtarlar: %d", len(keys))
	}
	e.clock.add(8 * 24 * time.Hour)
	_, _ = e.svc.Cleanup(ctx)
	e.s3.SetFail(0)
	_, _ = e.svc.Cleanup(ctx)

	logs := e.logs.String()
	if !strings.Contains(logs, ok.ID.String()) || !strings.Contains(logs, "medya silindi") {
		t.Fatal("log'lar yakalanmadı")
	}
	for _, needle := range append(keys,
		"X-Amz", "Signature", "incoming/", "media/", objstoretest.AccessKeyID, objstoretest.SecretAccessKey,
		e.s3.URL, imagingtest.Marker, "Exif", ok.URLs.Full) {
		if strings.Contains(logs, needle) {
			t.Errorf("log %q içeriyor", needle)
		}
	}
}
