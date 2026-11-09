package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func newAnnouncement(t *testing.T, q *Queries, tayfa, author id.ID, title string) id.ID {
	t.Helper()
	aid := id.New()
	if err := q.CreateAnnouncement(context.Background(), CreateAnnouncementParams{
		ID: aid.PG(), TayfaID: tayfa.PG(), AuthorID: author.PG(), Title: title, Body: "Gövde",
		ClientRequestID: id.New().PG(),
	}); err != nil {
		t.Fatalf("duyuru: %v", err)
	}
	return aid
}

func TestAnnouncementConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	crid := id.New()

	for name, tc := range map[string]struct {
		title, body string
		constraint  string
	}{
		"boş başlık":  {"", "x", "announcements_title_len"},
		"uzun başlık": {strings.Repeat("ş", 121), "x", "announcements_title_len"},
		"uzun gövde":  {"Başlık", strings.Repeat("a", 5001), "announcements_body_len"},
	} {
		err := q.CreateAnnouncement(ctx, CreateAnnouncementParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: tc.title, Body: tc.body, ClientRequestID: id.New().PG(),
		})
		if constraintOf(err) != tc.constraint {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Sınırlar dahil.
	if err := q.CreateAnnouncement(ctx, CreateAnnouncementParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: strings.Repeat("ş", 120),
		Body: strings.Repeat("ğ", 5000), ClientRequestID: crid.PG(),
	}); err != nil {
		t.Fatal(err)
	}
	// Aynı yazar, aynı client_request_id: ikinci kayıt açılmaz.
	err := q.CreateAnnouncement(ctx, CreateAnnouncementParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), AuthorID: admin.PG(), Title: "Tekrar", ClientRequestID: crid.PG(),
	})
	if constraintOf(err) != "announcements_client_request_key" {
		t.Fatalf("tekrar gönderim: %v", err)
	}
	// Başka tayfaya ait olmayan tayfa_id reddedilir (FK).
	err = q.CreateAnnouncement(ctx, CreateAnnouncementParams{
		ID: id.New().PG(), TayfaID: id.New().PG(), AuthorID: admin.PG(), Title: "Yok", ClientRequestID: id.New().PG(),
	})
	if constraintOf(err) != "announcements_tayfa_id_fkey" {
		t.Fatalf("bilinmeyen tayfa: %v", err)
	}
	// deleted_by silinmemiş satırda olamaz.
	aid := newAnnouncement(t, q, tayfa, admin, "Duyuru")
	_, err = pool.Exec(ctx, "UPDATE announcements SET deleted_by = $1 WHERE id = $2", admin.PG(), aid.PG())
	if constraintOf(err) != "announcements_deleted_by" {
		t.Fatalf("deleted_by: %v", err)
	}
}

func TestAnnouncementScopedByTayfa(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa, other := newTayfa(t, pool, admin, tayfaOpts{}), newTayfa(t, pool, id.New(), tayfaOpts{})
	aid := newAnnouncement(t, q, tayfa, admin, "Duyuru")

	if _, err := q.GetAnnouncement(ctx, GetAnnouncementParams{TayfaID: other.PG(), ID: aid.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("başka tayfadan okundu: %v", err)
	}
	for name, fn := range map[string]func() (int64, error){
		"güncelle": func() (int64, error) {
			return q.UpdateAnnouncementContent(ctx, UpdateAnnouncementContentParams{TayfaID: other.PG(), ID: aid.PG(), Title: "X"})
		},
		"sabitle": func() (int64, error) {
			return q.SetAnnouncementPinned(ctx, SetAnnouncementPinnedParams{TayfaID: other.PG(), ID: aid.PG(), Pinned: true})
		},
		"gizle": func() (int64, error) {
			return q.HideAnnouncement(ctx, HideAnnouncementParams{TayfaID: other.PG(), ID: aid.PG(), DeletedAt: ts(time.Now()), DeletedBy: admin.PG()})
		},
		"boşalt": func() (int64, error) {
			return q.EraseAnnouncement(ctx, EraseAnnouncementParams{TayfaID: other.PG(), ID: aid.PG(), DeletedAt: ts(time.Now()), DeletedBy: admin.PG()})
		},
	} {
		if n, err := fn(); err != nil || n != 0 {
			t.Errorf("%s başka tayfada çalıştı: %d %v", name, n, err)
		}
	}
	page, err := q.ListAnnouncementsPage(ctx, ListAnnouncementsPageParams{TayfaID: other.PG(), PageSize: 10})
	if err != nil || len(page) != 0 {
		t.Fatalf("başka tayfanın listesi: %v %v", page, err)
	}
}

func TestAnnouncementFeedOrderAndDeletion(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tayfa := newTayfa(t, pool, admin, tayfaOpts{})
	var ids []id.ID
	for _, title := range []string{"bir", "iki", "üç", "dört"} {
		ids = append(ids, newAnnouncement(t, q, tayfa, admin, title))
	}
	if n, err := q.SetAnnouncementPinned(ctx, SetAnnouncementPinnedParams{TayfaID: tayfa.PG(), ID: ids[0].PG(), Pinned: true}); err != nil || n != 1 {
		t.Fatal(err)
	}
	// Sabitlenen önce, sonra yeniden eskiye; iki satırlık sayfalarla.
	want := []id.ID{ids[0], ids[3], ids[2], ids[1]}
	var got []id.ID
	p := ListAnnouncementsPageParams{TayfaID: tayfa.PG(), PageSize: 2}
	for {
		rows, err := q.ListAnnouncementsPage(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			got = append(got, id.ID(r.ID.Bytes))
		}
		if len(rows) < 2 {
			break
		}
		last := rows[len(rows)-1]
		p.HasCursor, p.AfterPinned, p.AfterCreatedAt, p.AfterID = true, last.Pinned, last.CreatedAt, last.ID
	}
	if len(got) != len(want) {
		t.Fatalf("sıra = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sıra %d: %v, %v bekleniyordu", i, got, want)
		}
	}

	// Gizlenen duyuru listeden düşer, içeriği kalır, geri alınabilir.
	now := ts(time.Now())
	if n, err := q.HideAnnouncement(ctx, HideAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[1].PG(), DeletedAt: now, DeletedBy: admin.PG()}); err != nil || n != 1 {
		t.Fatal(err)
	}
	if _, err := q.GetAnnouncement(ctx, GetAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[1].PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("gizlenen okundu: %v", err)
	}
	if n, _ := q.HideAnnouncement(ctx, HideAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[1].PG(), DeletedAt: now, DeletedBy: admin.PG()}); n != 0 {
		t.Fatal("iki kez gizlendi")
	}
	if n, err := q.RestoreAnnouncement(ctx, RestoreAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[1].PG()}); err != nil || n != 1 {
		t.Fatalf("geri alma: %d %v", n, err)
	}
	r, err := q.GetAnnouncement(ctx, GetAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[1].PG()})
	if err != nil || r.Title != "iki" {
		t.Fatalf("geri alınan = %+v %v", r, err)
	}

	// Yazarın silmesi içeriği boşaltır ve geri alınamaz.
	if n, err := q.EraseAnnouncement(ctx, EraseAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[0].PG(), DeletedAt: now, DeletedBy: admin.PG()}); err != nil || n != 1 {
		t.Fatal(err)
	}
	var title, body string
	var pinned bool
	if err := pool.QueryRow(ctx, "SELECT title, body, pinned FROM announcements WHERE id = $1", ids[0].PG()).Scan(&title, &body, &pinned); err != nil || title != "" || body != "" || pinned {
		t.Fatalf("boşaltılmadı: %q %q %v %v", title, body, pinned, err)
	}
	if n, err := q.RestoreAnnouncement(ctx, RestoreAnnouncementParams{TayfaID: tayfa.PG(), ID: ids[0].PG()}); err != nil || n != 0 {
		t.Fatalf("boşaltılan geri alındı: %d %v", n, err)
	}
	// Silinmiş duyuru güncellenemez ve sabitlenemez.
	if n, _ := q.UpdateAnnouncementContent(ctx, UpdateAnnouncementContentParams{TayfaID: tayfa.PG(), ID: ids[0].PG(), Title: "geri"}); n != 0 {
		t.Fatal("silinen duyuru güncellendi")
	}
	if n, _ := q.SetAnnouncementPinned(ctx, SetAnnouncementPinnedParams{TayfaID: tayfa.PG(), ID: ids[0].PG(), Pinned: true}); n != 0 {
		t.Fatal("silinen duyuru sabitlendi")
	}
}

func TestQueriesTx(t *testing.T) {
	pool, q := setup(t)
	if _, ok := q.Tx(); ok {
		t.Fatal("havuz transaction sanıldı")
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if got, ok := New(tx).Tx(); !ok || got != tx {
		t.Fatal("transaction bulunamadı")
	}
}
