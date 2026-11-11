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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/inspect/inspecttest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
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

// hookStore runs beforeCopy before every copy (a client replacing the file
// between the check and the copy).
type hookStore struct {
	Store
	beforeCopy func()
}

func (h *hookStore) Copy(ctx context.Context, src, dst, etag string) error {
	if h.beforeCopy != nil {
		h.beforeCopy()
	}
	return h.Store.Copy(ctx, src, dst, etag)
}

type env struct {
	svc   *Service
	pool  *pgxpool.Pool
	s3    *objstoretest.Server
	store *hookStore
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
	hs := &hookStore{Store: s3.Client(t)}
	cfg := Config{
		Pool: pool, Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Store: hs, Now: c.now,
	}
	for _, m := range mut {
		m(&cfg)
	}
	svc, err := New(cfg)
	must(t, err)
	e := &env{svc: svc, pool: pool, s3: s3, store: hs, logs: logs, clock: c, owner: id.New(), member: id.New(), stranger: id.New(), tayfa: id.New()}
	// The tayfa as the tayfa service writes it (its package imports this
	// one): the row, the creator's admin membership and the modules.
	must(t, txn.WithActor(ctx, pool, e.owner, func(_ pgx.Tx, q *store.Queries) error {
		if err := q.CreateTayfa(ctx, store.CreateTayfaParams{
			ID: e.tayfa.PG(), Name: "Olta Tayfası", NameNormalized: "olta tayfası", JoinCode: "MEDYA234",
			JoinPolicy: "open", CreatedBy: e.owner.PG(), ClientRequestID: id.New().PG(),
		}); err != nil {
			return err
		}
		if err := q.SetTayfaModule(ctx, store.SetTayfaModuleParams{TayfaID: e.tayfa.PG(), Module: "events", Enabled: true}); err != nil {
			return err
		}
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: e.tayfa.PG(), UserID: e.owner.PG(), Role: "admin", Status: "active", JoinedVia: "creator", JoinedAt: ts(time.Now()),
		})
	}))
	must(t, txn.WithActor(ctx, pool, e.member, func(_ pgx.Tx, q *store.Queries) error {
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: e.tayfa.PG(), UserID: e.member.PG(), Role: "member", Status: "active", JoinedVia: "code", JoinedAt: ts(time.Now()),
		})
	}))
	return e
}

// files are the variants a client sends: a 120×80 WebP preview and a
// 300×200 JPEG full image, both without metadata.
type files struct{ preview, full []byte }

func clean() files {
	return files{preview: inspecttest.WebP(120, 80, inspecttest.WebPOptions{}), full: inspecttest.JPEG(300, 200)}
}

func (f files) variants() []VariantInput {
	return []VariantInput{
		{Kind: KindPreview, ContentType: "image/webp", SizeBytes: int64(len(f.preview))},
		{Kind: KindFull, ContentType: "image/jpeg", SizeBytes: int64(len(f.full))},
	}
}

func (e *env) create(t *testing.T, user id.ID, purpose string, tayfa id.ID, f files) Created {
	t.Helper()
	c, err := e.svc.CreateUpload(ctx, user, CreateInput{Purpose: purpose, Variants: f.variants(), Tayfa: tayfa, ClientRequestID: id.New().String()})
	must(t, err)
	return c
}

// put sends each variant to its link as the client would.
func (e *env) put(t *testing.T, c Created, f files) {
	t.Helper()
	for _, l := range c.Links {
		body := f.full
		if l.Kind == KindPreview {
			body = f.preview
		}
		if code := objstoretest.Do(t, http.MethodPut, l.URL, l.Headers, body); code != http.StatusOK {
			t.Fatalf("PUT %s: %d", l.Kind, code)
		}
	}
}

// upload runs the whole flow and returns the result.
func (e *env) upload(t *testing.T, user id.ID, purpose string, tayfa id.ID, f files, w, h int) Upload {
	t.Helper()
	c := e.create(t, user, purpose, tayfa, f)
	e.put(t, c, f)
	u, err := e.svc.CompleteUpload(ctx, user, c.Upload.ID, w, h)
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
	f := clean()
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	if c.Upload.Status != StatusPending || len(c.Links) != 2 || c.Links[0].Kind != KindPreview || c.Links[1].Kind != KindFull {
		t.Fatalf("oluşturma: %+v", c)
	}
	for _, l := range c.Links {
		if d := l.Expires.Sub(e.clock.now()); d <= 0 || d > UploadLinkTTL+time.Second || !strings.Contains(l.URL, "/incoming/") {
			t.Fatalf("link: %v", d)
		}
	}
	// Completing before the PUTs finds nothing; the upload stays pending.
	if _, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200); !errors.Is(err, apperr.ErrUploadNotReceived) {
		t.Fatalf("PUT'tan önce: %v", err)
	}
	e.put(t, c, f)
	u, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200)
	must(t, err)
	if u.Status != StatusReady || u.URLs == nil || u.URLs.Width != 300 || u.URLs.Height != 200 ||
		u.URLs.PreviewContentType != "image/webp" || u.URLs.FullContentType != "image/jpeg" {
		t.Fatalf("hazır: %+v %+v", u, u.URLs)
	}
	// The served files are the checked copies, byte for byte.
	for link, want := range map[string][]byte{u.URLs.Full: f.full, u.URLs.Preview: f.preview} {
		resp, err := http.Get(link)
		must(t, err)
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !bytes.Equal(got, want) {
			t.Fatalf("sunulan dosya: %d", resp.StatusCode)
		}
	}
	// Quarantine is empty; only the two checked copies remain.
	keys := e.s3.Keys()
	if len(keys) != 2 || strings.Contains(strings.Join(keys, ","), "incoming/") {
		t.Fatalf("depodaki nesneler: %v", keys)
	}
	// A repeat changes nothing.
	if again, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 1, 1); err != nil || again.Status != StatusReady {
		t.Fatalf("tekrar: %+v %v", again, err)
	}
	// The PUT link is still valid for a while, but it can only reach
	// quarantine: the served copy does not change.
	evil := append([]byte{}, f.full...)
	evil[len(evil)-3] ^= 0xFF // same size, other content
	for _, l := range c.Links {
		if l.Kind == KindFull {
			if code := objstoretest.Do(t, http.MethodPut, l.URL, l.Headers, evil); code != http.StatusOK {
				t.Fatalf("aynı boyutta PUT: %d", code)
			}
		}
	}
	for _, k := range e.s3.Keys() {
		if o, _ := e.s3.Get(k); strings.HasPrefix(k, "media/") && strings.HasSuffix(k, "/full") && !bytes.Equal(o.Body, f.full) {
			t.Fatal("denetlenmiş kopya değişmemeli")
		}
	}
}

// The client replaces a variant between the check and the copy: the copy
// is bound to the checked ETag, so nothing unchecked is served and the
// upload stays pending.
func TestReplacedDuringCheck(t *testing.T) {
	e := setup(t)
	f := clean()
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	e.put(t, c, f)
	e.store.beforeCopy = func() {
		for _, k := range e.s3.Keys() {
			if strings.HasPrefix(k, "incoming/") && strings.HasSuffix(k, "/full") {
				e.s3.Put(k, objstoretest.Object{Body: inspecttest.WithSegment(f.full, 0xE1, inspecttest.EXIF())})
			}
		}
	}
	if _, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("değişen dosya: %v", err)
	}
	for _, k := range e.s3.Keys() {
		if strings.HasPrefix(k, "media/") && strings.HasSuffix(k, "/full") {
			t.Fatal("değişen dosya kopyalanmamalı")
		}
	}
	e.store.beforeCopy = nil
	u, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200)
	must(t, err)
	// The replacement (larger, with EXIF) is checked again and refused.
	if u.Status != StatusRejected || u.RejectReason != "size_mismatch" {
		t.Fatalf("yeniden denetim: %+v", u)
	}
}

// Every refusal of ADR-0020 item 2; a refused upload cannot be attached
// and its quarantined files are gone.
func TestRejections(t *testing.T) {
	e := setup(t)
	f := clean()
	big := inspecttest.JPEG(1700, 1000)
	for name, tc := range map[string]struct {
		f    files
		w, h int
		want string
	}{
		"full'de EXIF/GPS":     {files{f.preview, inspecttest.WithSegment(f.full, 0xE1, inspecttest.EXIF())}, 300, 200, "metadata_present"},
		"önizlemede XMP":       {files{inspecttest.WebP(120, 80, inspecttest.WebPOptions{Extended: true, Flags: 0x04}), f.full}, 300, 200, "metadata_present"},
		"jpg adlı metin":       {files{f.preview, []byte("Bu bir resim değil ama adı foto.jpg")}, 300, 200, "not_image"},
		"bildirilenden başka":  {files{f.preview, inspecttest.WebP(300, 200, inspecttest.WebPOptions{})}, 300, 200, "not_image"},
		"png":                  {files{f.preview, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)}, 300, 200, "unsupported_type"},
		"bildirilen ölçü":      {f, 301, 200, "dimension_mismatch"},
		"önizleme oranı":       {files{inspecttest.WebP(120, 60, inspecttest.WebPOptions{}), f.full}, 300, 200, "dimension_mismatch"},
		"1600 px üstü":         {files{inspecttest.WebP(340, 200, inspecttest.WebPOptions{}), big}, 1600, 1000, "too_large"},
		"önizleme 400 px üstü": {files{inspecttest.WebP(500, 333, inspecttest.WebPOptions{}), inspecttest.JPEG(600, 400)}, 600, 400, "too_large"},
	} {
		t.Run(name, func(t *testing.T) {
			u := e.upload(t, e.owner, PurposeAvatar, id.ID{}, tc.f, tc.w, tc.h)
			if u.Status != StatusRejected || u.RejectReason != tc.want || u.URLs != nil {
				t.Fatalf("%+v", u)
			}
			if err := e.attach(t, e.owner, u.ID, PurposeAvatar, id.ID{}, Target{TargetUser, e.owner}); !errors.Is(err, apperr.ErrUploadNotReady) {
				t.Fatalf("reddedilen bağlanmamalı: %v", err)
			}
		})
	}
	// A stored size that is not the declared one (the storage did not
	// enforce the signed length).
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	e.put(t, c, f)
	for _, k := range e.s3.Keys() {
		if strings.HasPrefix(k, "incoming/") && strings.HasSuffix(k, "/full") {
			e.s3.Put(k, objstoretest.Object{Body: append(append([]byte{}, f.full...), 0)})
		}
	}
	u, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200)
	if err != nil || u.RejectReason != "size_mismatch" {
		t.Fatalf("boyut: %+v %v", u, err)
	}
	for _, k := range e.s3.Keys() {
		if strings.HasPrefix(k, "incoming/") {
			t.Fatalf("reddedilen dosyalar silinmeli: %v", e.s3.Keys())
		}
	}
	var v *apperr.ValidationError
	if _, err := e.svc.CompleteUpload(ctx, e.owner, id.New(), 1601, 10); !errors.As(err, &v) || v.Field != "width" {
		t.Fatalf("ölçü doğrulaması: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t)
	f := clean()
	vs := func(mut func([]VariantInput)) []VariantInput {
		v := f.variants()
		mut(v)
		return v
	}
	for name, tc := range map[string]struct {
		in   CreateInput
		user id.ID
		want error
	}{
		"png":                   {CreateInput{Purpose: PurposeAvatar, Variants: vs(func(v []VariantInput) { v[1].ContentType = "image/png" })}, e.owner, apperr.Validation("variants")},
		"heic":                  {CreateInput{Purpose: PurposeAvatar, Variants: vs(func(v []VariantInput) { v[0].ContentType = "image/heic" })}, e.owner, apperr.Validation("variants")},
		"boş":                   {CreateInput{Purpose: PurposeAvatar, Variants: vs(func(v []VariantInput) { v[0].SizeBytes = 0 })}, e.owner, apperr.Validation("variants")},
		"10 MiB üstü":           {CreateInput{Purpose: PurposeAvatar, Variants: vs(func(v []VariantInput) { v[1].SizeBytes = MaxVariantBytes + 1 })}, e.owner, apperr.Validation("variants")},
		"iki full":              {CreateInput{Purpose: PurposeAvatar, Variants: vs(func(v []VariantInput) { v[0].Kind = KindFull })}, e.owner, apperr.Validation("variants")},
		"tek varyant":           {CreateInput{Purpose: PurposeAvatar, Variants: f.variants()[:1]}, e.owner, apperr.Validation("variants")},
		"avatarda tayfa":        {CreateInput{Purpose: PurposeAvatar, Variants: f.variants(), Tayfa: e.tayfa}, e.owner, apperr.Validation("tayfa_id")},
		"kapakta tayfa yok":     {CreateInput{Purpose: PurposeCover, Variants: f.variants()}, e.owner, apperr.Validation("tayfa_id")},
		"bilinmeyen amaç":       {CreateInput{Purpose: "data_export", Variants: f.variants()}, e.owner, apperr.Validation("purpose")},
		"üye olmadığı tayfa":    {CreateInput{Purpose: PurposeCatchPhoto, Variants: f.variants(), Tayfa: e.tayfa}, e.stranger, apperr.ErrTayfaNotFound},
		"olmayan tayfa":         {CreateInput{Purpose: PurposeCover, Variants: f.variants(), Tayfa: id.New()}, e.owner, apperr.ErrTayfaNotFound},
		"üyenin av fotoğrafı":   {CreateInput{Purpose: PurposeCatchPhoto, Variants: f.variants(), Tayfa: e.tayfa}, e.member, nil},
		"geçerli avatar isteği": {CreateInput{Purpose: PurposeAvatar, Variants: f.variants()}, e.stranger, nil},
	} {
		t.Run(name, func(t *testing.T) {
			in := tc.in
			in.ClientRequestID = id.New().String()
			c, err := e.svc.CreateUpload(ctx, tc.user, in)
			var want *apperr.ValidationError
			switch {
			case tc.want == nil:
				if err != nil || len(c.Links) != 2 {
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

	// A retry returns the same upload with fresh links; a different request
	// with the same ID is refused.
	in := CreateInput{Purpose: PurposeAvatar, Variants: f.variants(), ClientRequestID: id.New().String()}
	first, err := e.svc.CreateUpload(ctx, e.owner, in)
	must(t, err)
	again, err := e.svc.CreateUpload(ctx, e.owner, in)
	if err != nil || again.Upload.ID != first.Upload.ID || len(again.Links) != 2 {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	in.Variants = vs(func(v []VariantInput) { v[1].SizeBytes++ })
	var v *apperr.ValidationError
	if _, err := e.svc.CreateUpload(ctx, e.owner, in); !errors.As(err, &v) || v.Field != "client_request_id" {
		t.Fatalf("farklı istek aynı kimlikle: %v", err)
	}
}

// Another user's uploads cannot be completed, read or attached, and are
// not served to those who may not see them (IDOR).
func TestIDOR(t *testing.T) {
	e := setup(t)
	f := clean()
	avatar := e.upload(t, e.owner, PurposeAvatar, id.ID{}, f, 300, 200)
	if avatar.Status != StatusReady {
		t.Fatalf("avatar: %+v", avatar)
	}
	if _, err := e.svc.CompleteUpload(ctx, e.stranger, avatar.ID, 300, 200); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının tamamlaması: %v", err)
	}
	if _, err := e.svc.GetUpload(ctx, e.stranger, avatar.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının okuması: %v", err)
	}
	if err := e.svc.AttachAvatar(ctx, e.stranger, avatar.ID); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Errorf("başkasının avatarı bağlanmamalı: %v", err)
	}
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
	_, err := e.pool.Exec(ctx, "INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)", e.stranger.PG(), e.owner.PG())
	must(t, err)
	if got := urls(e.stranger, avatar.ID); len(got) != 0 {
		t.Fatal("engelleyen avatarı görmemeli")
	}

	cover := e.upload(t, e.owner, PurposeCover, e.tayfa, f, 300, 200)
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
	in := func(preview, full int64) CreateInput {
		return CreateInput{Purpose: PurposeAvatar, ClientRequestID: id.New().String(), Variants: []VariantInput{
			{Kind: KindPreview, ContentType: "image/webp", SizeBytes: preview}, {Kind: KindFull, ContentType: "image/jpeg", SizeBytes: full},
		}}
	}
	_, err := e.svc.CreateUpload(ctx, e.owner, in(100, 900))
	must(t, err)
	if _, err := e.svc.CreateUpload(ctx, e.owner, in(100, 500)); !errors.Is(err, apperr.ErrUploadQuotaExceeded) {
		t.Fatalf("kota (iki varyant birlikte): %v", err)
	}
	_, err = e.svc.CreateUpload(ctx, e.owner, in(100, 300))
	must(t, err)
	if _, err := e.svc.CreateUpload(ctx, e.owner, in(1, 1)); !errors.Is(err, apperr.ErrUploadRateLimited) {
		t.Fatalf("sıklık: %v", err)
	}
	_, err = e.svc.CreateUpload(ctx, e.member, in(1, 1))
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
	_, e1 := svc.CreateUpload(ctx, u, CreateInput{Purpose: PurposeAvatar, Variants: clean().variants(), ClientRequestID: id.New().String()})
	_, e2 := svc.CompleteUpload(ctx, u, id.New(), 10, 10)
	_, e3 := svc.GetUpload(ctx, u, id.New())
	_, e4 := svc.MediaURLs(ctx, u, []id.ID{id.New()})
	for _, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, apperr.ErrUnavailable) {
			t.Fatalf("kapalıyken UNAVAILABLE: %v", err)
		}
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.RunCleanup(c); err != nil {
		t.Fatal(err)
	}
}

// A storage outage leaves the upload pending; the next call finishes it.
func TestStorageOutage(t *testing.T) {
	e := setup(t)
	f := clean()
	c := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	e.put(t, c, f)
	e.s3.SetFail(http.StatusServiceUnavailable)
	if _, err := e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("kesinti: %v", err)
	}
	e.s3.SetFail(0)
	u, err := e.svc.GetUpload(ctx, e.owner, c.Upload.ID)
	if err != nil || u.Status != StatusPending {
		t.Fatalf("bekliyor kalmalı: %+v %v", u, err)
	}
	if u, err = e.svc.CompleteUpload(ctx, e.owner, c.Upload.ID, 300, 200); err != nil || u.Status != StatusReady {
		t.Fatalf("yeniden deneme: %+v %v", u, err)
	}
}

func TestCleanup(t *testing.T) {
	e := setup(t)
	f := clean()
	pending := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	e.put(t, pending, f)
	ready := e.upload(t, e.owner, PurposeAvatar, id.ID{}, f, 300, 200)
	kept := e.upload(t, e.owner, PurposeAvatar, id.ID{}, f, 300, 200)
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
	must(t, pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return UserProfileUpdated(ctx, store.New(tx), e.owner, id.ID{}) }))
	if n, err := e.svc.Cleanup(ctx); err != nil || n != 1 || len(e.s3.Keys()) != 0 {
		t.Fatalf("eski avatar: %d %v %v", n, err, e.s3.Keys())
	}
}

// Keys, links, credentials and the files' content never reach the logs,
// on any path: success, rejection, outage, cleanup.
func TestLogsCarryNoSecrets(t *testing.T) {
	e := setup(t)
	f := clean()
	ok := e.upload(t, e.owner, PurposeAvatar, id.ID{}, f, 300, 200)
	gps := files{f.preview, inspecttest.WithSegment(f.full, 0xE1, inspecttest.EXIF())}
	if bad := e.upload(t, e.owner, PurposeAvatar, id.ID{}, gps, 300, 200); bad.Status != StatusRejected {
		t.Fatal("GPS'li dosya reddedilmeli")
	}
	down := e.create(t, e.owner, PurposeAvatar, id.ID{}, f)
	e.put(t, down, f)
	e.s3.SetFail(http.StatusInternalServerError)
	_, _ = e.svc.CompleteUpload(ctx, e.owner, down.Upload.ID, 300, 200)
	var keys []string
	rows, err := e.pool.Query(ctx, "SELECT r2_key FROM uploads")
	must(t, err)
	for rows.Next() {
		var k string
		must(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	if len(keys) != 3 {
		t.Fatalf("anahtarlar: %d", len(keys))
	}
	e.clock.add(8 * 24 * time.Hour)
	_, _ = e.svc.Cleanup(ctx)
	e.s3.SetFail(0)
	_, _ = e.svc.Cleanup(ctx)

	logs := e.logs.String()
	if !strings.Contains(logs, ok.ID.String()) || !strings.Contains(logs, "medya silindi") || !strings.Contains(logs, "metadata_present") {
		t.Fatal("log'lar yakalanmadı")
	}
	for _, needle := range append(keys,
		"X-Amz", "Signature", "incoming/", "media/", objstoretest.AccessKeyID, objstoretest.SecretAccessKey,
		e.s3.URL, inspecttest.Marker, "Exif", ok.URLs.Full, objstoretest.ETag(f.full)) {
		if strings.Contains(logs, needle) {
			t.Errorf("log %q içeriyor", needle)
		}
	}
}

var _ Store = (*objstore.Client)(nil)
