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
		ID: id.New().PG(), UserID: owner.PG(), TayfaID: tayfa, Purpose: o.purpose, R2Key: r2Key(t),
		PreviewType: "image/webp", PreviewBytes: 100, FullType: "image/jpeg", FullBytes: o.size, ClientRequestID: id.New().PG(),
	})
	if err != nil {
		t.Fatalf("yükleme açılamadı: %v", err)
	}
	return id.ID(got.Bytes)
}

// readyUpload opens an upload and marks it checked.
func readyUpload(t *testing.T, q *Queries, owner id.ID, o uploadOpts) id.ID {
	t.Helper()
	u := newUpload(t, q, owner, o)
	if n, err := q.MarkUploadReady(context.Background(), MarkUploadReadyParams{
		ID: u.PG(), Width: pgtype.Int4{Int32: 1600, Valid: true}, Height: pgtype.Int4{Int32: 1200, Valid: true}, Now: ts(time.Now()),
	}); err != nil || n != 1 {
		t.Fatalf("hazır: %d %v", n, err)
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

	base := func() CreateUploadParams {
		return CreateUploadParams{
			ID: id.New().PG(), UserID: owner.PG(), Purpose: "avatar", R2Key: r2Key(t),
			PreviewType: "image/webp", PreviewBytes: 1, FullType: "image/jpeg", FullBytes: 10 << 20, ClientRequestID: id.New().PG(),
		}
	}
	p := base()
	if _, err := q.CreateUpload(ctx, p); err != nil {
		t.Fatalf("10 MiB kabul edilmeli: %v", err)
	}
	// The same client_request_id opens nothing new.
	again := base()
	again.ClientRequestID = p.ClientRequestID
	if _, err := q.CreateUpload(ctx, again); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim yeni kayıt açmamalı: %v", err)
	}
	if got, err := q.GetUploadByClientRequest(ctx, GetUploadByClientRequestParams{UserID: owner.PG(), ClientRequestID: p.ClientRequestID}); err != nil || got.ID != p.ID {
		t.Fatalf("ilk kayıt dönmeli: %v", err)
	}

	for name, tc := range map[string]struct {
		mut  func(*CreateUploadParams)
		cons string
	}{
		"boş önizleme":           {func(p *CreateUploadParams) { p.PreviewBytes = 0 }, "uploads_variants"},
		"10 MiB üstü":            {func(p *CreateUploadParams) { p.FullBytes = 10<<20 + 1 }, "uploads_variants"},
		"png":                    {func(p *CreateUploadParams) { p.FullType = "image/png" }, "uploads_variants"},
		"heic önizleme":          {func(p *CreateUploadParams) { p.PreviewType = "image/heic" }, "uploads_variants"},
		"bilinmeyen amaç":        {func(p *CreateUploadParams) { p.Purpose = "banner" }, "uploads_purpose"},
		"varyantlı dışa aktarma": {func(p *CreateUploadParams) { p.Purpose = "data_export"; p.TayfaID = pgtype.UUID{} }, "uploads_variants"},
		"tahmin edilir ad":       {func(p *CreateUploadParams) { p.R2Key = "../media/x" }, "uploads_r2_key"},
		"avatarda tayfa":         {func(p *CreateUploadParams) { p.TayfaID = tayfa.PG() }, "uploads_tayfa"},
		"kapakta tayfa yok":      {func(p *CreateUploadParams) { p.Purpose = "cover" }, "uploads_tayfa"},
	} {
		t.Run(name, func(t *testing.T) {
			p := base()
			tc.mut(&p)
			_, err := q.CreateUpload(ctx, p)
			if got := constraintOf(err); got != tc.cons {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.cons)
			}
		})
	}
}

// A pending upload becomes ready or rejected once; ready needs the size.
func TestUploadReadyAndReject(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	owner := id.New()
	a, b := newUpload(t, q, owner, uploadOpts{}), newUpload(t, q, owner, uploadOpts{})
	size := func(w, h int32) MarkUploadReadyParams {
		return MarkUploadReadyParams{ID: a.PG(), Width: pgtype.Int4{Int32: w, Valid: w > 0}, Height: pgtype.Int4{Int32: h, Valid: h > 0}, Now: ts(time.Now())}
	}
	if _, err := q.MarkUploadReady(ctx, size(0, 0)); constraintOf(err) != "uploads_ready" {
		t.Fatalf("ölçüsüz hazır olmamalı: %v", err)
	}
	if _, err := q.MarkUploadReady(ctx, size(1601, 10)); constraintOf(err) != "uploads_size" {
		t.Fatalf("1600 px üstü: %v", err)
	}
	if n, err := q.MarkUploadReady(ctx, size(1600, 900)); err != nil || n != 1 {
		t.Fatalf("hazır: %d %v", n, err)
	}
	if n, _ := q.MarkUploadReady(ctx, size(1600, 900)); n != 0 {
		t.Fatal("ikinci kez hazır yapılmamalı")
	}
	if n, _ := q.RejectPendingUpload(ctx, RejectPendingUploadParams{ID: a.PG(), RejectReason: text("metadata_present")}); n != 0 {
		t.Fatal("hazır dosya reddedilmemeli")
	}
	if _, err := q.RejectPendingUpload(ctx, RejectPendingUploadParams{ID: b.PG(), RejectReason: text("bilinmez")}); constraintOf(err) != "uploads_reject_reason" {
		t.Fatalf("bilinmeyen sebep: %v", err)
	}
	if n, err := q.RejectPendingUpload(ctx, RejectPendingUploadParams{ID: b.PG(), RejectReason: text("metadata_present")}); err != nil || n != 1 {
		t.Fatalf("ret: %d %v", n, err)
	}
	if uploadStatus(t, pool, a) != "ready" || uploadStatus(t, pool, b) != "rejected" {
		t.Fatal("durumlar")
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
	if total, err := q.UserStoredBytes(ctx, owner.PG()); err != nil || total != 5000+100+1000+100 {
		t.Fatalf("kota: %d %v (iki varyantın bildirilen boyutları)", total, err)
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
		"boyut":    `UPDATE uploads SET full_bytes = 1 WHERE id = $1`,
		"tür":      `UPDATE uploads SET preview_type = 'image/jpeg' WHERE id = $1`,
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
