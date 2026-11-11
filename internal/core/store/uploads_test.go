package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Tests for 00017_uploads.sql and db/core/queries/uploads.sql (issue #135).

func r2Key(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

type uploadOpts struct {
	purpose string
	tayfa   id.ID
	size    int64
}

func newUpload(t *testing.T, q *Queries, owner id.ID, o uploadOpts) id.ID {
	t.Helper()
	if o.purpose == "" {
		o.purpose = "avatar"
	}
	if o.size == 0 {
		o.size = 1000
	}
	var tayfa pgtype.UUID
	if o.tayfa != (id.ID{}) {
		tayfa = o.tayfa.PG()
	}
	got, err := q.CreateUpload(context.Background(), CreateUploadParams{
		ID: id.New().PG(), UserID: owner.PG(), TayfaID: tayfa, Purpose: o.purpose,
		ContentType: "image/jpeg", SizeBytes: o.size, R2Key: r2Key(t), ClientRequestID: id.New().PG(),
	})
	if err != nil {
		t.Fatalf("yükleme açılamadı: %v", err)
	}
	return id.ID(got.Bytes)
}

// readyUpload takes an upload through the whole queue.
func readyUpload(t *testing.T, q *Queries, owner id.ID, o uploadOpts) id.ID {
	t.Helper()
	ctx := context.Background()
	u := newUpload(t, q, owner, o)
	now := time.Now()
	if n, err := q.MarkUploadReceived(ctx, MarkUploadReceivedParams{ID: u.PG(), Now: ts(now.Add(-time.Hour))}); err != nil || n != 1 {
		t.Fatalf("alındı: %d %v", n, err)
	}
	l, err := q.LeaseUpload(ctx, LeaseUploadParams{Now: ts(now), LeaseUntil: ts(now.Add(time.Minute))})
	if err != nil || id.ID(l.ID.Bytes) != u {
		t.Fatalf("kiralama: %v", err)
	}
	if n, err := q.FinishUpload(ctx, FinishUploadParams{
		ID: u.PG(), Attempts: l.Attempts, OutputType: text("image/jpeg"), StoredBytes: pgtype.Int8{Int64: 300, Valid: true},
		Width: pgtype.Int4{Int32: 1600, Valid: true}, Height: pgtype.Int4{Int32: 1200, Valid: true}, Now: ts(now),
	}); err != nil || n != 1 {
		t.Fatalf("bitirme: %d %v", n, err)
	}
	return u
}

func uploadStatus(t *testing.T, pool *pgxpool.Pool, u id.ID) string {
	t.Helper()
	var s string
	if err := adminOf(t, pool).QueryRow(context.Background(), "SELECT status FROM uploads WHERE id = $1", u.PG()).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUploadCreateAndConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})

	crid := id.New()
	p := CreateUploadParams{
		ID: id.New().PG(), UserID: owner.PG(), Purpose: "avatar", ContentType: "image/png",
		SizeBytes: 10 << 20, R2Key: r2Key(t), ClientRequestID: crid.PG(),
	}
	if _, err := q.CreateUpload(ctx, p); err != nil {
		t.Fatalf("10 MiB kabul edilmeli: %v", err)
	}
	// The same client_request_id opens nothing new.
	again := p
	again.ID, again.R2Key = id.New().PG(), r2Key(t)
	if _, err := q.CreateUpload(ctx, again); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim yeni kayıt açmamalı: %v", err)
	}
	if got, err := q.GetUploadByClientRequest(ctx, GetUploadByClientRequestParams{UserID: owner.PG(), ClientRequestID: crid.PG()}); err != nil || got.ID != p.ID {
		t.Fatalf("ilk kayıt dönmeli: %v", err)
	}

	for name, tc := range map[string]struct {
		mut  func(*CreateUploadParams)
		cons string
	}{
		"boş dosya":         {func(p *CreateUploadParams) { p.SizeBytes = 0 }, "uploads_size_bytes"},
		"10 MiB üstü":       {func(p *CreateUploadParams) { p.SizeBytes = 10<<20 + 1 }, "uploads_size_bytes"},
		"bilinmeyen tür":    {func(p *CreateUploadParams) { p.ContentType = "image/gif" }, "uploads_content_type"},
		"bilinmeyen amaç":   {func(p *CreateUploadParams) { p.Purpose = "data_export" }, "uploads_purpose"},
		"tahmin edilir ad":  {func(p *CreateUploadParams) { p.R2Key = "../media/x" }, "uploads_r2_key"},
		"avatarda tayfa":    {func(p *CreateUploadParams) { p.TayfaID = tayfa.PG() }, "uploads_tayfa"},
		"kapakta tayfa yok": {func(p *CreateUploadParams) { p.Purpose = "cover" }, "uploads_tayfa"},
	} {
		t.Run(name, func(t *testing.T) {
			p := CreateUploadParams{
				ID: id.New().PG(), UserID: owner.PG(), Purpose: "avatar", ContentType: "image/jpeg",
				SizeBytes: 100, R2Key: r2Key(t), ClientRequestID: id.New().PG(),
			}
			tc.mut(&p)
			_, err := q.CreateUpload(ctx, p)
			if got := constraintOf(err); got != tc.cons {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.cons)
			}
		})
	}
}

// The queue hands each job to one worker; a worker whose lease ran out
// cannot write over the next one (attempts fences).
func TestUploadQueueLease(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	a, b := newUpload(t, q, owner, uploadOpts{}), newUpload(t, q, owner, uploadOpts{})
	now := time.Now()
	for i, u := range []id.ID{a, b} {
		if n, err := q.MarkUploadReceived(ctx, MarkUploadReceivedParams{ID: u.PG(), Now: ts(now.Add(time.Duration(i-10) * time.Second))}); err != nil || n != 1 {
			t.Fatalf("alındı: %d %v", n, err)
		}
	}
	// A pending upload cannot be received twice.
	if n, _ := q.MarkUploadReceived(ctx, MarkUploadReceivedParams{ID: a.PG(), Now: ts(now)}); n != 0 {
		t.Fatal("ikinci kez alındı yapılmamalı")
	}

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	l1, err := New(tx1).LeaseUpload(ctx, LeaseUploadParams{Now: ts(now), LeaseUntil: ts(now.Add(time.Minute))})
	if err != nil || id.ID(l1.ID.Bytes) != a {
		t.Fatalf("ilk iş a olmalı: %v", err)
	}
	// While tx1 holds a, another worker gets b.
	l2, err := q.LeaseUpload(ctx, LeaseUploadParams{Now: ts(now), LeaseUntil: ts(now.Add(time.Minute))})
	if err != nil || id.ID(l2.ID.Bytes) != b {
		t.Fatalf("kilitli iş atlanmalı: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Nothing else is due.
	if _, err := q.LeaseUpload(ctx, LeaseUploadParams{Now: ts(now), LeaseUntil: ts(now.Add(time.Minute))}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("kiralanmış iş yeniden verilmemeli: %v", err)
	}
	// The lease of a runs out; the next worker takes it again.
	l3, err := q.LeaseUpload(ctx, LeaseUploadParams{Now: ts(now.Add(2 * time.Minute)), LeaseUntil: ts(now.Add(3 * time.Minute))})
	if err != nil || id.ID(l3.ID.Bytes) != a || l3.Attempts != 2 {
		t.Fatalf("kira bitince iş yeniden görünmeli: %+v %v", l3, err)
	}
	finish := func(attempts int32) int64 {
		n, err := q.FinishUpload(ctx, FinishUploadParams{
			ID: a.PG(), Attempts: attempts, OutputType: text("image/jpeg"), StoredBytes: pgtype.Int8{Int64: 10, Valid: true},
			Width: pgtype.Int4{Int32: 10, Valid: true}, Height: pgtype.Int4{Int32: 10, Valid: true}, Now: ts(now),
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if finish(l1.Attempts) != 0 {
		t.Fatal("süresi geçen kira yazamamalı")
	}
	if finish(l3.Attempts) != 1 {
		t.Fatal("güncel kira yazabilmeli")
	}
	if n, _ := q.RejectProcessingUpload(ctx, RejectProcessingUploadParams{ID: b.PG(), Attempts: l2.Attempts, RejectReason: text("not_image")}); n != 1 {
		t.Fatal("reddedilmeli")
	}
	if s := uploadStatus(t, pool, b); s != "rejected" {
		t.Fatalf("durum = %s", s)
	}
}

// Only the owner's ready upload of the right purpose and tayfa attaches,
// and only to one target.
func TestAttachUpload(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner, other := id.New(), id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})
	otherTayfa := newTayfa(t, pool, owner, tayfaOpts{})
	cover := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	pending := newUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})

	attach := func(u, user id.ID, purpose, typ string, target, tayfa id.ID) int64 {
		var tp pgtype.UUID
		if tayfa != (id.ID{}) {
			tp = tayfa.PG()
		}
		n, err := q.AttachUpload(ctx, AttachUploadParams{
			ID: u.PG(), UserID: user.PG(), Purpose: purpose, AttachedType: typ, AttachedID: target.PG(),
			TayfaID: tp, Now: ts(time.Now()),
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, n := range map[string]int64{
		"başkasının dosyası": attach(cover, other, "cover", "tayfa", tayfa, tayfa),
		"başka amaç":         attach(cover, owner, "avatar", "user", owner, id.ID{}),
		"başka tayfa":        attach(cover, owner, "cover", "tayfa", otherTayfa, otherTayfa),
		"hazır değil":        attach(pending, owner, "cover", "tayfa", tayfa, tayfa),
	} {
		if n != 0 {
			t.Errorf("%s bağlanmamalı", name)
		}
	}
	if attach(cover, owner, "cover", "tayfa", tayfa, tayfa) != 1 {
		t.Fatal("sahibi bağlayabilmeli")
	}
	if attach(cover, owner, "cover", "tayfa", tayfa, tayfa) != 1 {
		t.Fatal("aynı hedefe yeniden bağlama başarılı olmalı")
	}
	if attach(cover, owner, "cover", "event", id.New(), tayfa) != 0 {
		t.Fatal("bağlı dosya başka içeriğe bağlanmamalı")
	}
}

// The content columns accept only an upload attached to that content, and
// release the old one (replaced or hard deleted) into the delete queue.
func TestContentMediaTriggers(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})
	attach := func(u id.ID, typ string, target id.ID) {
		purpose := map[string]string{"tayfa": "cover", "event": "cover", "fishing_catch": "catch_photo"}[typ]
		if n, err := q.AttachUpload(ctx, AttachUploadParams{
			ID: u.PG(), UserID: owner.PG(), Purpose: purpose, AttachedType: typ, AttachedID: target.PG(),
			TayfaID: tayfa.PG(), Now: ts(time.Now()),
		}); err != nil || n != 1 {
			t.Fatalf("bağlama: %d %v", n, err)
		}
	}

	first := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	// Not attached to the tayfa yet: refused by the trigger.
	err := q.SetTayfaCover(ctx, SetTayfaCoverParams{ID: tayfa.PG(), CoverUploadID: first.PG()})
	wantCode(t, err, "TF080")
	attach(first, "tayfa", tayfa)
	wantCode(t, q.SetTayfaCover(ctx, SetTayfaCoverParams{ID: tayfa.PG(), CoverUploadID: first.PG()}), "")

	second := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	attach(second, "tayfa", tayfa)
	wantCode(t, q.SetTayfaCover(ctx, SetTayfaCoverParams{ID: tayfa.PG(), CoverUploadID: second.PG()}), "")
	if s := uploadStatus(t, pool, first); s != "deleting" {
		t.Fatalf("değiştirilen kapak silinmeli, durum = %s", s)
	}
	// Removing the cover releases it too.
	wantCode(t, q.SetTayfaCover(ctx, SetTayfaCoverParams{ID: tayfa.PG()}), "")
	if s := uploadStatus(t, pool, second); s != "deleting" {
		t.Fatalf("kaldırılan kapak silinmeli, durum = %s", s)
	}

	// An event cover and a catch photo go when the event is purged
	// (the catch goes with it by cascade).
	ev, err := insertEvent(ctx, pool, tayfa, "fishing", "in_person", nil)
	if err != nil {
		t.Fatal(err)
	}
	evCover := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	attach(evCover, "event", ev)
	wantCode(t, q.SetEventCover(ctx, SetEventCoverParams{ID: ev.PG(), TayfaID: tayfa.PG(), CoverUploadID: evCover.PG()}), "")
	admin := adminOf(t, pool)
	if _, err := admin.Exec(ctx, "INSERT INTO event_fishing (event_id) VALUES ($1)", ev.PG()); err != nil {
		t.Fatal(err)
	}
	catch := id.New()
	if _, err := admin.Exec(ctx, `INSERT INTO fishing_catches (id, event_id, user_id, species, length_cm, client_request_id)
		VALUES ($1, $2, $3, 'Levrek', 42, $4)`, catch.PG(), ev.PG(), owner.PG(), id.New().PG()); err != nil {
		t.Fatal(err)
	}
	photo := readyUpload(t, q, owner, uploadOpts{purpose: "catch_photo", tayfa: tayfa})
	// A cover cannot be a catch photo.
	wantCode(t, q.SetCatchPhoto(ctx, SetCatchPhotoParams{ID: catch.PG(), PhotoUploadID: evCover.PG()}), "TF080")
	attach(photo, "fishing_catch", catch)
	wantCode(t, q.SetCatchPhoto(ctx, SetCatchPhotoParams{ID: catch.PG(), PhotoUploadID: photo.PG()}), "")

	// Soft delete keeps the media (the content can still be restored).
	if _, err := pool.Exec(ctx, "UPDATE events SET deleted_at = now() WHERE id = $1", ev.PG()); err != nil {
		t.Fatal(err)
	}
	if s := uploadStatus(t, pool, evCover); s != "ready" {
		t.Fatalf("soft delete dosyayı bırakmamalı, durum = %s", s)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM events WHERE id = $1", ev.PG()); err != nil {
		t.Fatal(err)
	}
	for name, u := range map[string]id.ID{"etkinlik kapağı": evCover, "av fotoğrafı": photo} {
		if s := uploadStatus(t, pool, u); s != "deleting" {
			t.Errorf("%s kalıcı silmede silinmeli, durum = %s", name, s)
		}
	}

	// The delete queue lists them; deleting the row clears nothing else.
	rows, err := q.ListDeletingUploads(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("silme kuyruğunda %d dosya, 4 bekleniyordu", len(rows))
	}
	for _, r := range rows {
		if n, err := q.DeleteUploadRow(ctx, r.ID); err != nil || n != 1 {
			t.Fatalf("satır silinemedi: %v", err)
		}
	}
}

// Avatars: the current one stays, the others go; account deletion deletes
// the user's files but keeps the tayfa's covers without an owner.
func TestUploadsOfUser(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})
	avatar := func() id.ID {
		u := readyUpload(t, q, owner, uploadOpts{})
		if n, err := q.AttachUpload(ctx, AttachUploadParams{
			ID: u.PG(), UserID: owner.PG(), Purpose: "avatar", AttachedType: "user", AttachedID: owner.PG(), Now: ts(time.Now()),
		}); err != nil || n != 1 {
			t.Fatalf("avatar: %d %v", n, err)
		}
		return u
	}
	old, current := avatar(), avatar()
	if n, err := q.ReleaseOtherAvatars(ctx, ReleaseOtherAvatarsParams{UserID: owner.PG(), Keep: current.PG()}); err != nil || n != 1 {
		t.Fatalf("eski avatar: %d %v", n, err)
	}
	if uploadStatus(t, pool, old) != "deleting" || uploadStatus(t, pool, current) != "ready" {
		t.Fatal("yalnızca eski avatar silinmeli")
	}

	cover := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	if n, _ := q.AttachUpload(ctx, AttachUploadParams{
		ID: cover.PG(), UserID: owner.PG(), Purpose: "cover", AttachedType: "tayfa", AttachedID: tayfa.PG(), TayfaID: tayfa.PG(), Now: ts(time.Now()),
	}); n != 1 {
		t.Fatal("kapak bağlanmalı")
	}
	loose := newUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	if n, err := q.DeleteUserUploads(ctx, owner.PG()); err != nil || n != 2 {
		t.Fatalf("hesap silme: %d %v", n, err)
	}
	if n, err := q.DisownUserCovers(ctx, owner.PG()); err != nil || n != 1 {
		t.Fatalf("kapak sahipliği: %d %v", n, err)
	}
	if uploadStatus(t, pool, cover) != "ready" || uploadStatus(t, pool, loose) != "deleting" || uploadStatus(t, pool, current) != "deleting" {
		t.Fatal("hesap silmede bağlı kapak kalmalı, diğerleri silinmeli")
	}
	if total, err := q.UserStoredBytes(ctx, owner.PG()); err != nil || total != 0 {
		t.Fatalf("silinen dosyalar kotaya sayılmamalı: %d %v", total, err)
	}
}

func TestExpireUploads(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	pending := newUpload(t, q, owner, uploadOpts{size: 5000})
	loose := readyUpload(t, q, owner, uploadOpts{})
	if total, err := q.UserStoredBytes(ctx, owner.PG()); err != nil || total != 5000+300 {
		t.Fatalf("kota: %d %v (bekleyen ham boyutla, hazır işlenmiş boyutla sayılır)", total, err)
	}
	if n, err := q.CountUploadsSince(ctx, CountUploadsSinceParams{UserID: owner.PG(), Since: ts(time.Now().Add(-time.Hour))}); err != nil || n != 2 {
		t.Fatalf("saatlik sayı: %d %v", n, err)
	}
	now := time.Now()
	expire := func(at time.Time) []pgtype.UUID {
		got, err := q.ExpireUploads(ctx, ExpireUploadsParams{
			PendingBefore: ts(at.Add(-24 * time.Hour)), RejectedBefore: ts(at.Add(-24 * time.Hour)),
			UnattachedBefore: ts(at.Add(-7 * 24 * time.Hour)), N: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := expire(now); len(got) != 0 {
		t.Fatalf("henüz hiçbir şey silinmemeli: %d", len(got))
	}
	if got := expire(now.Add(25 * time.Hour)); len(got) != 1 || got[0] != pending.PG() {
		t.Fatalf("24 saat sonra yalnızca bekleyen silinmeli: %v", got)
	}
	if got := expire(now.Add(8 * 24 * time.Hour)); !slices.Equal(got, []pgtype.UUID{loose.PG()}) {
		t.Fatalf("7 gün sonra bağlanmamış hazır dosya silinmeli: %v", got)
	}
}

// The context ListMediaForViewer returns for the permission check.
func TestListMediaForViewer(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner, member, stranger, blocker := id.New(), id.New(), id.New(), id.New()
	tayfa := newTayfa(t, pool, owner, tayfaOpts{})
	seed(t, pool, tayfa, member, "member", "active")
	cover := readyUpload(t, q, owner, uploadOpts{purpose: "cover", tayfa: tayfa})
	if n, _ := q.AttachUpload(ctx, AttachUploadParams{
		ID: cover.PG(), UserID: owner.PG(), Purpose: "cover", AttachedType: "tayfa", AttachedID: tayfa.PG(), TayfaID: tayfa.PG(), Now: ts(time.Now()),
	}); n != 1 {
		t.Fatal("kapak bağlanmalı")
	}
	avatar := readyUpload(t, q, owner, uploadOpts{})
	if n, _ := q.AttachUpload(ctx, AttachUploadParams{
		ID: avatar.PG(), UserID: owner.PG(), Purpose: "avatar", AttachedType: "user", AttachedID: owner.PG(), Now: ts(time.Now()),
	}); n != 1 {
		t.Fatal("avatar bağlanmalı")
	}
	if _, err := adminOf(t, pool).Exec(ctx, "INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ($1, $2)", owner.PG(), blocker.PG()); err != nil {
		t.Fatal(err)
	}
	pending := newUpload(t, q, owner, uploadOpts{})

	view := func(viewer id.ID) map[id.ID]ListMediaForViewerRow {
		rows, err := q.ListMediaForViewer(ctx, ListMediaForViewerParams{ViewerID: viewer.PG(), Ids: []pgtype.UUID{cover.PG(), avatar.PG(), pending.PG()}})
		if err != nil {
			t.Fatal(err)
		}
		out := map[id.ID]ListMediaForViewerRow{}
		for _, r := range rows {
			out[id.ID(r.ID.Bytes)] = r
		}
		return out
	}
	m := view(member)
	if _, ok := m[pending]; ok {
		t.Fatal("hazır olmayan dosya listelenmemeli")
	}
	if r := m[cover]; id.ID(r.ContentTayfaID.Bytes) != tayfa || r.MemberStatus.String != "active" || r.MemberRole.String != "member" {
		t.Fatalf("üyenin kapak bağlamı: %+v", r)
	}
	if r := view(stranger)[cover]; !r.ContentTayfaID.Valid || r.MemberStatus.Valid {
		t.Fatalf("üye olmayanın üyeliği olmamalı: %+v", r)
	}
	if view(stranger)[avatar].Blocked || !view(blocker)[avatar].Blocked {
		t.Fatal("engel iki yönde de görülmeli")
	}

	// A deleted tayfa gives no context.
	if _, err := pool.Exec(ctx, "UPDATE tayfas SET deleted_at = now() WHERE id = $1", tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	if r := view(member)[cover]; r.ContentTayfaID.Valid {
		t.Fatal("silinmiş tayfanın kapağı bağlam vermemeli")
	}
}

// If the service is taken over, its database user still cannot point an
// upload record at another object or change what was declared.
func TestAppRoleCannotRepointUploads(t *testing.T) {
	pool, q := setup(t)
	u := newUpload(t, q, id.New(), uploadOpts{})
	for name, sql := range map[string]string{
		"anahtar":  `UPDATE uploads SET r2_key = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id = $1`,
		"amaç":     `UPDATE uploads SET purpose = 'cover' WHERE id = $1`,
		"boyut":    `UPDATE uploads SET size_bytes = 1 WHERE id = $1`,
		"tür":      `UPDATE uploads SET content_type = 'image/png' WHERE id = $1`,
		"tayfa":    `UPDATE uploads SET tayfa_id = id WHERE id = $1`,
		"kimlik":   `UPDATE uploads SET id = gen_random_uuid() WHERE id = $1`,
		"tekrarno": `UPDATE uploads SET client_request_id = id WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pool.Exec(context.Background(), sql, u.PG())
			if SQLState(err) != "42501" {
				t.Fatalf("yetki hatası bekleniyordu: %v", err)
			}
		})
	}
}
