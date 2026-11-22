package material_test

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/material"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"  Türev   özeti ":      "Türev özeti",
		"Limit\n(çözümlü)\t":    "Limit (çözümlü)",
		strings.Repeat("ş", 80): strings.Repeat("ş", 80),
	} {
		if got, ok := material.CleanTitle(in); !ok || got != want {
			t.Errorf("CleanTitle(%q) = %q %v, %q bekleniyordu", in, got, ok, want)
		}
	}
	for name, in := range map[string]string{
		"boş":             "  \t ",
		"denetim kodu":    "Türev\x00",
		"görünmez işaret": "Tü​rev",
		"yön işareti":     "Türev‮",
		"geçersiz UTF-8":  "Tü\xffrev",
		"81 karakter":     strings.Repeat("ş", 81),
	} {
		if _, ok := material.CleanTitle(in); ok {
			t.Errorf("%s: kabul edildi", name)
		}
	}
}

// TestNoNetworkImports: the server never opens a link (ADR-0040 §3).
// Nothing in this package can make an outgoing request; package link's
// TestNoNetwork checks its dependencies all the way down.
func TestNoNetworkImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("dosyalar: %v", err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "net" || p == "os/exec" || p == "crypto/tls" || strings.HasPrefix(p, "net/http") {
				t.Errorf("%s: %s içe aktarılıyor; sunucu bağlantıyı açmaz", f, p)
			}
		}
	}
}

// studyTags is test data only: the real list is the platform owner's
// (issue #260). subject_old and topic_old are retired.
const studyTags = `INSERT INTO catalog_study_tags (key, parent_key, position, labels, status) VALUES
	('level_a', NULL, 1, '{"tr": "Düzey A"}', 'active'),
	('level_a_other', 'level_a', 99, '{"tr": "Diğer"}', 'active'),
	('subject_a', 'level_a', 1, '{"tr": "Ders A"}', 'active'),
	('subject_a_other', 'subject_a', 99, '{"tr": "Diğer"}', 'active'),
	('topic_a', 'subject_a', 1, '{"tr": "Konu A"}', 'active'),
	('topic_old', 'subject_a', 2, '{"tr": "Eski konu"}', 'retired'),
	('subject_b', 'level_a', 2, '{"tr": "Ders B"}', 'active'),
	('topic_b', 'subject_b', 1, '{"tr": "Konu B"}', 'active'),
	('subject_old', 'level_a', 3, '{"tr": "Eski ders"}', 'retired')`

type env struct {
	svc    *material.Service
	tayfas *tayfa.Service
	pool   *pgxpool.Pool
	admin  *pgxpool.Pool
	now    time.Time
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
	e := &env{pool: d.App, admin: d.Admin, now: time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)}
	fw, err := component.New(component.Config{Pool: d.App, Log: log, Now: func() time.Time { return e.now }})
	must(t, err)
	e.svc = material.New(fw)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: d.App, Log: log})
	must(t, err)
	_, err = d.Admin.Exec(ctx, studyTags)
	must(t, err)
	return e
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

func (e *env) newTayfa(t *testing.T) (tf, admin id.ID) {
	t.Helper()
	admin = id.New()
	v, err := e.tayfas.Create(ctx, admin, tayfa.CreateInput{Name: "Etüt Tayfası", ClientRequestID: id.New().String()})
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

func (e *env) photo(t *testing.T, owner, tf id.ID) id.ID {
	t.Helper()
	return mediatest.Ready(t, e.pool, owner, media.PurposeMaterialNote, tf)
}

func linkInput(title, url, subject, topic string) material.Input {
	return material.Input{Kind: material.KindLink, Title: title, URL: url, SubjectKey: subject, TopicKey: topic}
}

func (e *env) add(t *testing.T, author, tf id.ID, in material.Input) material.Material {
	t.Helper()
	m, err := e.svc.Create(ctx, author, tf, in, id.New().String())
	must(t, err)
	return m
}

func ids(ms []material.Material) []id.ID {
	out := make([]id.ID, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// A link and a note photo: stored as normalized, listed newest first,
// narrowed by kind, subject, topic and author, paged; the caller's flags.
func TestCreateAndList(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	b := e.member(t, tf, authz.Member)

	l := e.add(t, a, tf, linkInput("  Türev   özeti ", " HTTPS://TR.Wikipedia.org:443/wiki/Türev ", "subject_a", "topic_a"))
	if l.Kind != material.KindLink || l.Title != "Türev özeti" || l.URL != "https://tr.wikipedia.org/wiki/T%C3%BCrev" ||
		l.Domain != "tr.wikipedia.org" || l.Photos != nil || l.SubjectKey != "subject_a" || l.TopicKey != "topic_a" ||
		l.Author != a || !l.CallerCanEdit || !l.CallerCanDelete {
		t.Fatalf("bağlantı: %+v", l)
	}
	e.now = e.now.Add(time.Minute)
	p1, p2 := e.photo(t, b, tf), e.photo(t, b, tf)
	n := e.add(t, b, tf, material.Input{Kind: material.KindNotePhoto, Title: "Notlarım", Photos: []id.ID{p2, p1}, SubjectKey: "subject_b"})
	if n.URL != "" || n.Domain != "" || !slices.Equal(n.Photos, []id.ID{p2, p1}) || n.TopicKey != "" {
		t.Fatalf("not fotoğrafı: %+v", n)
	}
	if mediatest.Status(t, e.pool, p1) != media.StatusReady {
		t.Fatal("fotoğraf hazır değil")
	}

	// What a member, the author and a moderator may do.
	got, err := e.svc.Get(ctx, a, tf, n.ID)
	must(t, err)
	if got.CallerCanEdit || got.CallerCanDelete {
		t.Fatalf("başkasının materyali: %+v", got)
	}
	got, err = e.svc.Get(ctx, admin, tf, n.ID)
	must(t, err)
	if got.CallerCanEdit || !got.CallerCanDelete {
		t.Fatalf("yöneticinin görünümü: %+v", got)
	}

	page, err := e.svc.List(ctx, a, tf, material.Filter{})
	must(t, err)
	if !slices.Equal(ids(page.Materials), []id.ID{n.ID, l.ID}) || page.NextPageToken != "" || !page.CallerCanAdd {
		t.Fatalf("liste: %+v", page)
	}
	for name, tc := range map[string]struct {
		f    material.Filter
		want []id.ID
	}{
		"bağlantılar": {material.Filter{Kind: material.KindLink}, []id.ID{l.ID}},
		"ders":        {material.Filter{SubjectKey: "subject_b"}, []id.ID{n.ID}},
		"konu":        {material.Filter{SubjectKey: "subject_a", TopicKey: "topic_a"}, []id.ID{l.ID}},
		"başka konu":  {material.Filter{TopicKey: "topic_b"}, []id.ID{}},
		"benimkiler":  {material.Filter{Mine: true}, []id.ID{l.ID}},
	} {
		p, err := e.svc.List(ctx, a, tf, tc.f)
		must(t, err)
		if !slices.Equal(ids(p.Materials), tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", name, ids(p.Materials), tc.want)
		}
	}

	// Pages of one; a token is good only for its own list.
	p, err := e.svc.List(ctx, a, tf, material.Filter{PageSize: 1})
	must(t, err)
	if !slices.Equal(ids(p.Materials), []id.ID{n.ID}) || p.NextPageToken == "" {
		t.Fatalf("ilk sayfa: %+v", p)
	}
	p2nd, err := e.svc.List(ctx, a, tf, material.Filter{PageSize: 1, PageToken: p.NextPageToken})
	must(t, err)
	if !slices.Equal(ids(p2nd.Materials), []id.ID{l.ID}) || p2nd.NextPageToken != "" {
		t.Fatalf("ikinci sayfa: %+v", p2nd)
	}
	_, err = e.svc.List(ctx, a, tf, material.Filter{PageSize: 1, PageToken: p.NextPageToken, Kind: material.KindLink})
	wantField(t, "başka listenin sayfası", err, material.FieldPageToken)
	_, err = e.svc.List(ctx, a, tf, material.Filter{PageToken: "bozuk"})
	wantField(t, "bozuk sayfa", err, material.FieldPageToken)
	_, err = e.svc.List(ctx, a, tf, material.Filter{PageSize: material.MaxPageSize + 1})
	wantField(t, "sayfa boyu", err, material.FieldPageSize)
	_, err = e.svc.List(ctx, a, tf, material.Filter{Kind: "pdf"})
	wantField(t, "tür", err, material.FieldKind)

	// A retry returns the same material.
	crid := id.New().String()
	first, err := e.svc.Create(ctx, a, tf, linkInput("Tekrar", "https://okul.example.com/a", "subject_a", ""), crid)
	must(t, err)
	again, err := e.svc.Create(ctx, a, tf, linkInput("Tekrar", "https://okul.example.com/a", "subject_a", ""), crid)
	must(t, err)
	if again.ID != first.ID {
		t.Fatalf("tekrar gönderim yeni materyal açtı")
	}
}

// Bad input names its field; a photo must be the caller's own
// material_note upload of the tayfa.
func TestCreateRefuses(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	b := e.member(t, tf, authz.Member)
	other, _ := e.newTayfa(t)
	create := func(in material.Input) error {
		_, err := e.svc.Create(ctx, a, tf, in, id.New().String())
		return err
	}
	ok := linkInput("Özet", "https://example.com/x", "subject_a", "")
	with := func(f func(*material.Input)) material.Input {
		in := ok
		f(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in    material.Input
		field string
	}{
		"tür yok":             {with(func(x *material.Input) { x.Kind = "" }), material.FieldKind},
		"PDF":                 {with(func(x *material.Input) { x.Kind = "pdf" }), material.FieldKind},
		"başlık yok":          {with(func(x *material.Input) { x.Title = " " }), material.FieldTitle},
		"81 karakter":         {with(func(x *material.Input) { x.Title = strings.Repeat("a", 81) }), material.FieldTitle},
		"bağlantı yok":        {with(func(x *material.Input) { x.URL = "" }), material.FieldURL},
		"http":                {with(func(x *material.Input) { x.URL = "http://example.com/" }), material.FieldURL},
		"IP adresi":           {with(func(x *material.Input) { x.URL = "https://169.254.169.254/latest" }), material.FieldURL},
		"kullanıcı bilgisi":   {with(func(x *material.Input) { x.URL = "https://a@example.com/" }), material.FieldURL},
		"localhost":           {with(func(x *material.Input) { x.URL = "https://localhost/" }), material.FieldURL},
		"501 karakter":        {with(func(x *material.Input) { x.URL = "https://example.com/" + strings.Repeat("a", 481) }), material.FieldURL},
		"bağlantıda fotoğraf": {with(func(x *material.Input) { x.Photos = []id.ID{id.New()} }), material.FieldPhotoIDs},
		"ders yok":            {with(func(x *material.Input) { x.SubjectKey = "" }), material.FieldSubjectKey},
		"düzey ders değil":    {with(func(x *material.Input) { x.SubjectKey = "level_a" }), material.FieldSubjectKey},
		"emekli ders":         {with(func(x *material.Input) { x.SubjectKey = "subject_old" }), material.FieldSubjectKey},
		"bilinmeyen ders":     {with(func(x *material.Input) { x.SubjectKey = "subject_x" }), material.FieldSubjectKey},
		"başka dersin konusu": {with(func(x *material.Input) { x.TopicKey = "topic_b" }), material.FieldTopicKey},
		"emekli konu":         {with(func(x *material.Input) { x.TopicKey = "topic_old" }), material.FieldTopicKey},
		"fotoğrafsız not":     {material.Input{Kind: material.KindNotePhoto, Title: "Not", SubjectKey: "subject_a"}, material.FieldPhotoIDs},
		"notta bağlantı":      {material.Input{Kind: material.KindNotePhoto, Title: "Not", URL: "https://example.com/", Photos: []id.ID{id.New()}, SubjectKey: "subject_a"}, material.FieldURL},
		"dört fotoğraf":       {material.Input{Kind: material.KindNotePhoto, Title: "Not", Photos: []id.ID{id.New(), id.New(), id.New(), id.New()}, SubjectKey: "subject_a"}, material.FieldPhotoIDs},
		"aynı fotoğraf iki kez": {func() material.Input {
			p := id.New()
			return material.Input{Kind: material.KindNotePhoto, Title: "Not", Photos: []id.ID{p, p}, SubjectKey: "subject_a"}
		}(), material.FieldPhotoIDs},
	} {
		wantField(t, name, create(tc.in), tc.field)
	}
	_, err := e.svc.Create(ctx, a, tf, ok, "x")
	wantField(t, "istek kimliği", err, material.FieldClientRequestID)

	note := func(p id.ID) material.Input {
		return material.Input{Kind: material.KindNotePhoto, Title: "Not", Photos: []id.ID{p}, SubjectKey: "subject_a"}
	}
	wantErr(t, "başkasının fotoğrafı", create(note(e.photo(t, b, tf))), apperr.ErrUploadNotFound)
	wantErr(t, "başka tayfanın fotoğrafı", create(note(mediatest.Ready(t, e.pool, a, media.PurposeMaterialNote, other))), apperr.ErrUploadNotFound)
	wantErr(t, "başka amaçlı yükleme", create(note(mediatest.Ready(t, e.pool, a, media.PurposeQuestionPool, tf))), apperr.ErrUploadNotFound)
	used := e.photo(t, a, tf)
	must(t, create(note(used)))
	wantErr(t, "kullanılmış fotoğraf", create(note(used)), apperr.ErrUploadInUse)

	// Anyone but an active member; the module off.
	_, err = e.svc.Create(ctx, id.New(), tf, ok, id.New().String())
	wantErr(t, "üye değil", err, apperr.ErrTayfaNotFound)
	_, err = e.svc.List(ctx, id.New(), tf, material.Filter{})
	wantErr(t, "üye olmayan okur", err, apperr.ErrTayfaNotFound)
	e.exec(t, `UPDATE tayfa_modules SET enabled = false WHERE tayfa_id = $1 AND module = 'materials'`, tf.PG())
	_, err = e.svc.List(ctx, a, tf, material.Filter{})
	wantErr(t, "modül kapalı", err, apperr.ErrModuleDisabled)
	wantErr(t, "modül kapalıyken ekleme", create(ok), apperr.ErrModuleDisabled)
}

// The blocked domain list: the domain and every domain under it are
// refused; a domain added later hides the materials linking to it (not
// deleted), and they come back when it leaves the list.
func TestBlockedDomains(t *testing.T) {
	e := setup(t)
	tf, admin := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	for _, u := range []string{"https://bit.ly/3abc", "https://WWW.Bit.ly./x", "https://chat.whatsapp.com/davet", "https://t.me/grup"} {
		_, err := e.svc.Create(ctx, a, tf, linkInput("Kısa", u, "subject_a", ""), id.New().String())
		wantErr(t, u, err, apperr.ErrBlockedLink)
	}
	// Only whole labels match: "notbit.ly" is not under "bit.ly".
	e.add(t, a, tf, linkInput("Benzer ad", "https://notbit.ly/x", "subject_a", ""))

	m := e.add(t, a, tf, linkInput("Konu anlatımı", "https://ders.example.org/turev", "subject_a", ""))
	e.exec(t, `INSERT INTO blocked_domains (domain, created_by) VALUES ('example.org', $1)`, admin.PG())
	_, err := e.svc.Get(ctx, a, tf, m.ID)
	wantErr(t, "engellenen alan adı", err, apperr.ErrMaterialNotFound)
	page, err := e.svc.List(ctx, a, tf, material.Filter{})
	must(t, err)
	if slices.Contains(ids(page.Materials), m.ID) {
		t.Fatal("engellenen bağlantı listede")
	}
	_, err = e.svc.Update(ctx, a, tf, m.ID, linkInput("Konu", "https://okul.example.net/", "subject_a", ""))
	wantErr(t, "engellenen bağlantıyı düzenleme", err, apperr.ErrMaterialNotFound)
	_, err = e.svc.Create(ctx, a, tf, linkInput("Yeni", "https://example.org/", "subject_a", ""), id.New().String())
	wantErr(t, "engellenen alan adına yeni bağlantı", err, apperr.ErrBlockedLink)
	var deleted bool
	must(t, e.admin.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM materials WHERE id = $1`, m.ID.PG()).Scan(&deleted))
	if deleted {
		t.Fatal("engelleme materyali sildi")
	}
	e.exec(t, `DELETE FROM blocked_domains WHERE domain = 'example.org'`)
	if _, err := e.svc.Get(ctx, a, tf, m.ID); err != nil {
		t.Fatalf("listeden çıkan alan adı: %v", err)
	}
}

// Editing is the author's; deleting the author's or a moderator's. The
// author's delete drops the photos at once; a moderator's only hides.
func TestEditAndDelete(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	b := e.member(t, tf, authz.Member)
	mod := e.member(t, tf, authz.Moderator)

	l := e.add(t, a, tf, linkInput("Özet", "https://example.com/1", "subject_a", "topic_a"))
	got, err := e.svc.Update(ctx, a, tf, l.ID, linkInput("Yeni özet", "https://example.com/2", "subject_b", "topic_b"))
	must(t, err)
	if got.Title != "Yeni özet" || got.URL != "https://example.com/2" || got.SubjectKey != "subject_b" || got.TopicKey != "topic_b" || got.Kind != material.KindLink {
		t.Fatalf("düzenleme: %+v", got)
	}
	_, err = e.svc.Update(ctx, b, tf, l.ID, linkInput("Benim", "https://example.com/3", "subject_b", ""))
	wantErr(t, "başkasının materyali", err, apperr.ErrMaterialNotFound)
	_, err = e.svc.Update(ctx, mod, tf, l.ID, linkInput("Benim", "https://example.com/3", "subject_b", ""))
	wantErr(t, "moderatör düzenlemez", err, apperr.ErrMaterialNotFound)
	_, err = e.svc.Update(ctx, a, tf, l.ID, material.Input{Title: "Not", Photos: []id.ID{e.photo(t, a, tf)}, SubjectKey: "subject_b"})
	wantField(t, "tür değişmez", err, material.FieldPhotoIDs)

	// A retired tag stays when left as it was; it cannot be chosen anew.
	e.exec(t, `UPDATE catalog_study_tags SET status = 'retired' WHERE key = 'topic_b'`)
	_, err = e.svc.Update(ctx, a, tf, l.ID, linkInput("Aynı etiket", "https://example.com/2", "subject_b", "topic_b"))
	must(t, err)
	_, err = e.svc.Update(ctx, a, tf, l.ID, linkInput("Başka", "https://example.com/2", "subject_a", "topic_old"))
	wantField(t, "emekli konu seçilemez", err, material.FieldTopicKey)

	// Photos: replacing one releases it; the kept ones stay.
	p1, p2, p3 := e.photo(t, a, tf), e.photo(t, a, tf), e.photo(t, a, tf)
	n := e.add(t, a, tf, material.Input{Kind: material.KindNotePhoto, Title: "Defter", Photos: []id.ID{p1, p2}, SubjectKey: "subject_a"})
	got, err = e.svc.Update(ctx, a, tf, n.ID, material.Input{Title: "Defter", Photos: []id.ID{p2, p3}, SubjectKey: "subject_a"})
	must(t, err)
	if !slices.Equal(got.Photos, []id.ID{p2, p3}) {
		t.Fatalf("fotoğraflar: %v", got.Photos)
	}
	if s := mediatest.Status(t, e.pool, p1); s != "deleting" && s != "" {
		t.Fatalf("çıkarılan fotoğraf %q", s)
	}
	for _, p := range []id.ID{p2, p3} {
		if s := mediatest.Status(t, e.pool, p); s != media.StatusReady {
			t.Fatalf("yeri değişen ya da eklenen fotoğraf %q", s)
		}
	}

	// Deleting: not someone else's for a member; a moderator removes and
	// the photos stay until the retention job; the author's drops them.
	err = e.svc.Delete(ctx, b, tf, n.ID)
	wantErr(t, "başkasının materyalini silme", err, apperr.ErrPermissionDenied)
	m2 := e.add(t, b, tf, material.Input{Kind: material.KindNotePhoto, Title: "Defter 2", Photos: []id.ID{e.photo(t, b, tf)}, SubjectKey: "subject_a"})
	must(t, e.svc.Delete(ctx, mod, tf, m2.ID))
	if s := mediatest.Status(t, e.pool, m2.Photos[0]); s != media.StatusReady {
		t.Fatalf("moderatör kaldırması fotoğrafı sildi: %q", s)
	}
	_, err = e.svc.Get(ctx, a, tf, m2.ID)
	wantErr(t, "kaldırılan materyal", err, apperr.ErrMaterialNotFound)
	must(t, e.svc.Delete(ctx, a, tf, n.ID))
	for _, p := range []id.ID{p2, p3} {
		if s := mediatest.Status(t, e.pool, p); s != "deleting" && s != "" {
			t.Fatalf("yazarın silmesinden sonra fotoğraf %q", s)
		}
	}
	wantErr(t, "iki kez silme", e.svc.Delete(ctx, a, tf, n.ID), apperr.ErrMaterialNotFound)
	var by pgtype.UUID
	must(t, e.admin.QueryRow(ctx, `SELECT deleted_by FROM materials WHERE id = $1`, n.ID.PG()).Scan(&by))
	if !by.Valid || id.ID(by.Bytes) != a {
		t.Fatal("silen yazılmadı")
	}

	// A muted member deletes their own, but adds and edits nothing.
	e.exec(t, `UPDATE memberships SET muted_until = $3 WHERE tayfa_id = $1 AND user_id = $2`, tf.PG(), a.PG(),
		pgtype.Timestamptz{Time: e.now.Add(24 * time.Hour), Valid: true})
	_, err = e.svc.Create(ctx, a, tf, linkInput("Susturulmuş", "https://example.com/m", "subject_a", ""), id.New().String())
	wantErr(t, "susturulmuş ekleme", err, apperr.ErrMuted)
	_, err = e.svc.Update(ctx, a, tf, l.ID, linkInput("Susturulmuş", "https://example.com/m", "subject_a", ""))
	wantErr(t, "susturulmuş düzenleme", err, apperr.ErrMuted)
	must(t, e.svc.Delete(ctx, a, tf, l.ID))
}

// A member has at most MaxPerAuthor live materials in a tayfa.
func TestLimit(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	e.exec(t, `INSERT INTO materials (id, tayfa_id, author_id, kind, title, url, domain, subject_key, client_request_id, created_at, updated_at)
		SELECT uuidv7(), $1, $2, 'link', 'M' || i, 'https://example.com/' || i, 'example.com', 'subject_a', uuidv7(), now(), now()
		FROM generate_series(1, $3::integer) i`, tf.PG(), a.PG(), material.MaxPerAuthor)
	_, err := e.svc.Create(ctx, a, tf, linkInput("Bir fazla", "https://example.com/x", "subject_a", ""), id.New().String())
	wantErr(t, "sınır", err, apperr.ErrMaterialLimit)
	e.exec(t, `UPDATE materials SET deleted_at = now() WHERE id = (SELECT id FROM materials WHERE author_id = $1 LIMIT 1)`, a.PG())
	e.add(t, a, tf, linkInput("Silinen yer açar", "https://example.com/x", "subject_a", ""))
}

// The database refuses what the app layer never writes (second line of
// defence).
func TestChecks(t *testing.T) {
	e := setup(t)
	tf, _ := e.newTayfa(t)
	a := e.member(t, tf, authz.Member)
	insert := func(kind, title, url, domain string) error {
		_, err := e.admin.Exec(ctx, `INSERT INTO materials (id, tayfa_id, author_id, kind, title, url, domain, subject_key, client_request_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), 'subject_a', $8, now(), now())`,
			id.New().PG(), tf.PG(), a.PG(), kind, title, url, domain, id.New().PG())
		return err
	}
	must(t, insert("link", "Özet", "https://example.com/", "example.com"))
	for name, row := range map[string][4]string{
		"pdf":               {"pdf", "Özet", "", ""},
		"http":              {"link", "Özet", "http://example.com/", "example.com"},
		"bağlantısız link":  {"link", "Özet", "", ""},
		"alan adısız link":  {"link", "Özet", "https://example.com/", ""},
		"IP alan adı":       {"link", "Özet", "https://1.2.3.4/", "1.2.3.4"},
		"büyük harf":        {"link", "Özet", "https://Example.com/", "Example.com"},
		"boşluklu bağlantı": {"link", "Özet", "https://example.com/a b", "example.com"},
		"notta bağlantı":    {"note_photo", "Özet", "https://example.com/", "example.com"},
		"başlık boşluğu":    {"link", " Özet", "https://example.com/", "example.com"},
		"501 karakter":      {"link", "Özet", "https://example.com/" + strings.Repeat("a", 481), "example.com"},
	} {
		if err := insert(row[0], row[1], row[2], row[3]); err == nil {
			t.Errorf("%s: kabul edildi", name)
		}
	}
	if _, err := e.admin.Exec(ctx, `INSERT INTO blocked_domains (domain) VALUES ('Bit.LY')`); err == nil {
		t.Error("büyük harfli engelli alan adı kabul edildi")
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO blocked_domains (domain) VALUES ('example.net')`); err == nil {
		t.Error("servis engelli alan adı ekleyebildi")
	}
}
