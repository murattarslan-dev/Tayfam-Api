package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore/objstoretest"
)

// Tests for chat photos, encrypted DM attachments and report evidence
// (issue #205, ADR-0031).

// rangeCounter counts ranged reads: a DM attachment is never read.
type rangeCounter struct {
	Store
	mu    sync.Mutex
	reads int
}

func (r *rangeCounter) GetRange(ctx context.Context, key string, n int64, etag string) ([]byte, error) {
	r.mu.Lock()
	r.reads++
	r.mu.Unlock()
	return r.Store.GetRange(ctx, key, n, etag)
}

// fakeChat answers MessageMedia: visible holds the uploads chat says the
// caller may see.
type fakeChat struct {
	mu      sync.Mutex
	visible map[id.ID]bool
	err     error
	asked   []MessageMediaItem
}

func (f *fakeChat) Visible(_ context.Context, items []MessageMediaItem) (map[id.ID]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, items...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[id.ID]bool{}
	for _, it := range items {
		if f.visible[it.Upload] {
			out[it.Upload] = true
		}
	}
	return out, nil
}

// blob is an encrypted attachment as the client would send it; its bytes
// mean nothing to the server (they even start like a JPEG with EXIF).
var blob = append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 'E', 'x', 'i', 'f'}, bytes.Repeat([]byte{0x5A}, 500)...)

func (e *env) blobUpload(t *testing.T, user id.ID) Created {
	t.Helper()
	c, err := e.svc.CreateUpload(ctx, user, CreateInput{
		Purpose: PurposeDMAttachment, ClientRequestID: id.New().String(),
		Variants: []VariantInput{{Kind: KindBlob, ContentType: BlobType, SizeBytes: int64(len(blob))}},
	})
	must(t, err)
	return c
}

func TestDMAttachmentIsNeverRead(t *testing.T) {
	var rc *rangeCounter
	e := setup(t, func(c *Config) { rc = &rangeCounter{Store: c.Store}; c.Store = rc })
	c := e.blobUpload(t, e.member)
	if len(c.Links) != 1 || c.Links[0].Kind != KindBlob || c.Links[0].Headers["Content-Type"] != BlobType {
		t.Fatalf("tek blob linki: %+v", c.Links)
	}
	if c.Upload.SizeBytes != int64(len(blob)) || c.Upload.TayfaID != (id.ID{}) {
		t.Fatalf("kayıt: %+v", c.Upload)
	}
	// An image's dimensions are refused.
	var v *apperr.ValidationError
	if _, err := e.svc.CompleteUpload(ctx, e.member, c.Upload.ID, 300, 200); !errors.As(err, &v) || v.Field != "width" {
		t.Fatalf("ölçü verilmemeli: %v", err)
	}
	if code := objstoretest.Do(t, http.MethodPut, c.Links[0].URL, c.Links[0].Headers, blob); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	u, err := e.svc.CompleteUpload(ctx, e.member, c.Upload.ID, 0, 0)
	must(t, err)
	if u.Status != StatusReady || u.URLs == nil || u.URLs.Preview != "" || u.URLs.FullContentType != BlobType || u.URLs.Width != 0 {
		t.Fatalf("hazır: %+v %+v", u, u.URLs)
	}
	if rc.reads != 0 {
		t.Fatalf("blob okunmamalı: %d okuma", rc.reads)
	}
	resp, err := http.Get(u.URLs.Full)
	must(t, err)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !bytes.Equal(got, blob) {
		t.Fatal("blob aynen sunulmalı")
	}
	if strings.Contains(strings.Join(e.s3.Keys(), ","), "incoming/") {
		t.Fatalf("karantina boşalmalı: %v", e.s3.Keys())
	}
}

func TestDMAttachmentSizeMismatch(t *testing.T) {
	e := setup(t)
	c := e.blobUpload(t, e.member)
	// The storage enforces the signed size; a smaller object put directly
	// stands for a storage that did not.
	var key string
	must(t, e.pool.QueryRow(ctx, "SELECT r2_key FROM uploads WHERE id = $1", c.Upload.ID.PG()).Scan(&key))
	e.s3.Put("incoming/"+key+"/full", objstoretest.Object{Body: blob[:10], ContentType: BlobType})
	u, err := e.svc.CompleteUpload(ctx, e.member, c.Upload.ID, 0, 0)
	must(t, err)
	if u.Status != StatusRejected || u.RejectReason != "size_mismatch" {
		t.Fatalf("boyut: %+v", u)
	}
}

func TestChatUploadValidation(t *testing.T) {
	e := setup(t)
	f := clean()
	var v *apperr.ValidationError
	for name, in := range map[string]CreateInput{
		"sohbet fotoğrafı tayfasız": {Purpose: PurposeChatPhoto, Variants: f.variants()},
		"blob tayfalı":              {Purpose: PurposeDMAttachment, Tayfa: e.tayfa, Variants: []VariantInput{{Kind: KindBlob, ContentType: BlobType, SizeBytes: 10}}},
		"kanıt tayfalı":             {Purpose: PurposeReportEvidence, Tayfa: e.tayfa, Variants: f.variants()},
		"blob iki varyant":          {Purpose: PurposeDMAttachment, Variants: f.variants()},
		"blob görüntü türü":         {Purpose: PurposeDMAttachment, Variants: []VariantInput{{Kind: KindBlob, ContentType: "image/jpeg", SizeBytes: 10}}},
		"blob 10 MiB üstü":          {Purpose: PurposeDMAttachment, Variants: []VariantInput{{Kind: KindBlob, ContentType: BlobType, SizeBytes: MaxVariantBytes + 1}}},
		"görüntüde blob":            {Purpose: PurposeChatPhoto, Tayfa: e.tayfa, Variants: []VariantInput{{Kind: KindBlob, ContentType: BlobType, SizeBytes: 10}}},
	} {
		in.ClientRequestID = id.New().String()
		if _, err := e.svc.CreateUpload(ctx, e.member, in); !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A chat photo needs membership of its tayfa.
	if _, err := e.svc.CreateUpload(ctx, e.stranger, CreateInput{Purpose: PurposeChatPhoto, Tayfa: e.tayfa, Variants: f.variants(), ClientRequestID: id.New().String()}); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olmayan: %v", err)
	}
}

func TestAttachChannelPhoto(t *testing.T) {
	e := setup(t)
	u := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	msg := id.New()
	for name, tc := range map[string]struct {
		user, tayfa id.ID
		want        error
	}{
		"başkasının":     {e.owner, e.tayfa, apperr.ErrUploadNotFound},
		"başka tayfanın": {e.member, id.New(), apperr.ErrUploadNotFound},
	} {
		if err := e.svc.AttachChannelPhoto(ctx, tc.user, u.ID, tc.tayfa, msg); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, u.ID, e.tayfa, msg))
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, u.ID, e.tayfa, msg)) // a repeat
	// Unconfirmed, it moves to the owner's next message (ADR-0031); once
	// chat confirmed it, a second message is refused.
	next := id.New()
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, u.ID, e.tayfa, next))
	if _, err := e.pool.Exec(ctx, "UPDATE uploads SET attach_confirmed_at = now() WHERE id = $1", u.ID.PG()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AttachChannelPhoto(ctx, e.member, u.ID, e.tayfa, id.New()); !errors.Is(err, apperr.ErrUploadInUse) {
		t.Fatalf("ikinci mesaj: %v", err)
	}
	// Another purpose cannot be attached to a message.
	avatar := e.upload(t, e.member, PurposeAvatar, id.ID{}, clean(), 300, 200)
	if err := e.svc.AttachChannelPhoto(ctx, e.member, avatar.ID, e.tayfa, id.New()); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("avatar: %v", err)
	}
	if err := e.svc.AttachDirectMessageAttachment(ctx, e.member, u.ID, id.New()); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("sohbet fotoğrafı DM eki değildir: %v", err)
	}
	// Not ready yet.
	c := e.create(t, e.member, PurposeChatPhoto, e.tayfa, clean())
	if err := e.svc.AttachChannelPhoto(ctx, e.member, c.Upload.ID, e.tayfa, id.New()); !errors.Is(err, apperr.ErrUploadNotReady) {
		t.Fatalf("hazır değil: %v", err)
	}
}

// Who gets a link to a chat file is chat's answer; core adds its own rule
// (ready, attached) and the owner always sees their own file.
func TestChatMediaVisibility(t *testing.T) {
	chat := &fakeChat{visible: map[id.ID]bool{}}
	e := setup(t, func(c *Config) { c.Messages = chat })
	photo := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	photoMsg := id.New()
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, photo.ID, e.tayfa, photoMsg))

	c := e.blobUpload(t, e.member)
	if code := objstoretest.Do(t, http.MethodPut, c.Links[0].URL, c.Links[0].Headers, blob); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	att, err := e.svc.CompleteUpload(ctx, e.member, c.Upload.ID, 0, 0)
	must(t, err)
	dmMsg := id.New()
	must(t, e.svc.AttachDirectMessageAttachment(ctx, e.member, att.ID, dmMsg))

	unattached := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	evidence := e.upload(t, e.member, PurposeReportEvidence, id.ID{}, clean(), 300, 200)

	all := []id.ID{photo.ID, att.ID, unattached.ID, evidence.ID}
	seen := func(viewer id.ID) map[id.ID]bool {
		t.Helper()
		got, err := e.svc.MediaURLs(ctx, viewer, all)
		must(t, err)
		out := map[id.ID]bool{}
		for _, u := range got {
			out[u.UploadID] = true
		}
		return out
	}
	// The owner sees all of their own files, without asking chat.
	chat.asked = nil
	if got := seen(e.member); len(got) != 4 || len(chat.asked) != 0 {
		t.Fatalf("sahibi: %v, chat'e soruldu: %v", got, chat.asked)
	}
	// Someone else: nothing until chat says so; core asks about attached
	// chat files only, each with its own message.
	if got := seen(e.owner); len(got) != 0 {
		t.Fatalf("chat izin vermeden: %v", got)
	}
	if len(chat.asked) != 2 {
		t.Fatalf("chat'e sorulanlar: %+v", chat.asked)
	}
	for _, it := range chat.asked {
		switch it.Upload {
		case photo.ID:
			if it.Message != photoMsg || it.Direct {
				t.Fatalf("kanal sorusu: %+v", it)
			}
		case att.ID:
			if it.Message != dmMsg || !it.Direct {
				t.Fatalf("birebir sorusu: %+v", it)
			}
		default:
			t.Fatalf("sorulmaması gereken: %+v", it)
		}
	}
	chat.visible[photo.ID], chat.visible[att.ID], chat.visible[evidence.ID], chat.visible[unattached.ID] = true, true, true, true
	got := seen(e.owner)
	if !got[photo.ID] || !got[att.ID] || got[unattached.ID] || got[evidence.ID] {
		t.Fatalf("chat izniyle: %v (bağlanmamış ve kanıt görünmemeli)", got)
	}
	// chat cannot answer: chat files are left out, others still served.
	chat.err = errors.New("chat kapalı")
	cover := e.upload(t, e.member, PurposeCover, e.tayfa, clean(), 300, 200)
	must(t, e.attach(t, e.member, cover.ID, PurposeCover, e.tayfa, Target{Type: TargetTayfa, ID: e.tayfa}))
	must(t, store.New(e.pool).SetTayfaCover(ctx, store.SetTayfaCoverParams{ID: e.tayfa.PG(), CoverUploadID: cover.ID.PG()}))
	res, err := e.svc.MediaURLs(ctx, e.owner, []id.ID{photo.ID, cover.ID})
	if err != nil || len(res) != 1 || res[0].UploadID != cover.ID {
		t.Fatalf("chat yokken: %+v %v", res, err)
	}
	// Without a chat client nobody but the owner sees chat files.
	e2 := setup(t)
	p2 := e2.upload(t, e2.member, PurposeChatPhoto, e2.tayfa, clean(), 300, 200)
	must(t, e2.svc.AttachChannelPhoto(ctx, e2.member, p2.ID, e2.tayfa, id.New()))
	if res, err := e2.svc.MediaURLs(ctx, e2.owner, []id.ID{p2.ID}); err != nil || len(res) != 0 {
		t.Fatalf("chat istemcisi yok: %+v %v", res, err)
	}
}

func TestMessageMediaReleased(t *testing.T) {
	e := setup(t)
	u := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	msg := id.New()
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, u.ID, e.tayfa, msg))
	q := store.New(e.pool)
	// Another message's event changes nothing.
	must(t, MessageMediaReleased(ctx, q, u.ID, id.New()))
	if s := statusOf(t, e, u.ID); s != StatusReady {
		t.Fatalf("başka mesaj: %s", s)
	}
	must(t, MessageMediaReleased(ctx, q, u.ID, msg))
	must(t, MessageMediaReleased(ctx, q, u.ID, msg)) // a repeat
	if s := statusOf(t, e, u.ID); s != statusDeleting {
		t.Fatalf("bırakılan fotoğraf silinmeli: %s", s)
	}
	// An avatar is never released by a chat event.
	a := e.upload(t, e.member, PurposeAvatar, id.ID{}, clean(), 300, 200)
	must(t, e.attach(t, e.member, a.ID, PurposeAvatar, id.ID{}, Target{Type: TargetUser, ID: e.member}))
	must(t, MessageMediaReleased(ctx, q, a.ID, e.member))
	if s := statusOf(t, e, a.ID); s != StatusReady {
		t.Fatalf("avatar: %s", s)
	}
	// A purged tayfa's chat photos go.
	p := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	must(t, TayfaPurged(ctx, q, e.tayfa))
	if s := statusOf(t, e, p.ID); s != statusDeleting {
		t.Fatalf("tayfa silinince: %s", s)
	}
}

func statusOf(t *testing.T, e *env, u id.ID) string {
	t.Helper()
	var s string
	must(t, e.pool.QueryRow(ctx, "SELECT status FROM uploads WHERE id = $1", u.PG()).Scan(&s))
	return s
}

func TestReportEvidenceURL(t *testing.T) {
	e := setup(t)
	u := e.upload(t, e.member, PurposeReportEvidence, id.ID{}, clean(), 300, 200)
	report := id.New()
	// Not attached to the report: no link.
	if link, _, ok, err := e.svc.ReportEvidenceURL(ctx, report, u.ID); err != nil || ok || link != "" {
		t.Fatalf("bağlanmamış: %q %v", link, err)
	}
	must(t, e.attach(t, e.member, u.ID, PurposeReportEvidence, id.ID{}, Target{Type: TargetReport, ID: report}))
	link, exp, ok, err := e.svc.ReportEvidenceURL(ctx, report, u.ID)
	if err != nil || !ok || !strings.Contains(link, "/media/") || exp.Sub(e.clock.now()) > EvidenceLinkTTL+time.Second {
		t.Fatalf("link: %v", err)
	}
	if other, _, _, err := e.svc.ReportEvidenceURL(ctx, id.New(), u.ID); err != nil || other != "" {
		t.Fatalf("başka rapor: %q %v", other, err)
	}
	// The reporter's account goes; the evidence stays with the report.
	must(t, UserDeleted(ctx, store.New(e.pool), e.member))
	if s := statusOf(t, e, u.ID); s != StatusReady {
		t.Fatalf("hesap silinince kanıt kalmalı: %s", s)
	}
	// The link never reaches the logs.
	if strings.Contains(e.logs.String(), link) {
		t.Fatal("link log'a yazıldı")
	}
}

// fakeHolders answers MediaHolders: upload → the message chat says holds it.
type fakeHolders struct {
	mu    sync.Mutex
	held  map[id.ID]id.ID
	err   error
	asked []HolderItem
}

func (f *fakeHolders) Holders(_ context.Context, items []HolderItem) (map[id.ID]id.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, items...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[id.ID]id.ID{}
	for _, it := range items {
		if m, ok := f.held[it.Upload]; ok {
			out[it.Upload] = m
		}
	}
	return out, nil
}

// TestChatAttachmentReconcile (ADR-0031): chat has core attach a file
// before it writes the message. An attachment chat never confirmed moves to
// the owner's next message; after ReconcileAfter chat is asked which
// message holds the file: a holder confirms it (correcting the message ID),
// none undoes it so the file expires like any unattached upload.
func TestChatAttachmentReconcile(t *testing.T) {
	h := &fakeHolders{held: map[id.ID]id.ID{}}
	e := setup(t, func(c *Config) { c.Holders = h })
	attachedTo := func(u id.ID) (typ string, msg id.ID, confirmed bool) {
		t.Helper()
		var (
			ty  *string
			m   *id.ID
			cat *time.Time
		)
		must(t, e.pool.QueryRow(ctx, "SELECT attached_type, attached_id, attach_confirmed_at FROM uploads WHERE id = $1", u.PG()).Scan(&ty, &m, &cat))
		if ty != nil {
			typ = *ty
		}
		if m != nil {
			msg = *m
		}
		return typ, msg, cat != nil
	}

	// A send that failed after core attached: the retry moves the photo.
	photo := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	lost, kept := id.New(), id.New()
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, photo.ID, e.tayfa, lost))
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, photo.ID, e.tayfa, kept))
	if _, m, _ := attachedTo(photo.ID); m != kept {
		t.Fatalf("taşınmadı: %v", m)
	}
	// Not across kinds: a DM attachment never becomes a channel photo.
	att := e.blobReady(t, e.member)
	must(t, e.svc.AttachDirectMessageAttachment(ctx, e.member, att, id.New()))

	// Too young: chat is not asked yet.
	if c, d, err := e.svc.Reconcile(ctx); err != nil || c+d != 0 || len(h.asked) != 0 {
		t.Fatalf("erken: %d %d %v %v", c, d, err, h.asked)
	}
	e.clock.add(ReconcileAfter + time.Minute)
	// chat cannot answer: nothing changes.
	h.err = errors.New("chat kapalı")
	if _, _, err := e.svc.Reconcile(ctx); err == nil {
		t.Fatal("chat hatası bildirilmeli")
	}
	if _, m, conf := attachedTo(photo.ID); m != kept || conf {
		t.Fatal("chat cevap vermeden değişti")
	}
	h.err = nil
	// chat: the photo is held by kept; the attachment no message holds.
	h.held[photo.ID] = kept
	c, d, err := e.svc.Reconcile(ctx)
	if err != nil || c != 1 || d != 1 {
		t.Fatalf("uzlaştırma: %d onay, %d kaldırma, %v", c, d, err)
	}
	if typ, m, conf := attachedTo(photo.ID); typ != TargetChannelMessage || m != kept || !conf {
		t.Fatalf("onaylanan: %s %v %v", typ, m, conf)
	}
	if typ, _, _ := attachedTo(att); typ != "" {
		t.Fatalf("hayalet bağlama kalmamalı: %s", typ)
	}
	if s := statusOf(t, e, att); s != StatusReady {
		t.Fatalf("bağlaması kaldırılan dosya hazır kalmalı (7 gün sonra silinir): %s", s)
	}
	for _, it := range h.asked {
		if (it.Upload == att) != it.Direct {
			t.Fatalf("tür: %+v", it)
		}
	}
	// A confirmed attachment no longer moves.
	if err := e.svc.AttachChannelPhoto(ctx, e.member, photo.ID, e.tayfa, id.New()); !errors.Is(err, apperr.ErrUploadInUse) {
		t.Fatalf("onaylanan taşınmamalı: %v", err)
	}
	// chat names another message (a retry moved it between the question
	// and the answer, say): the holder wins.
	p2 := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	first, holder := id.New(), id.New()
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, p2.ID, e.tayfa, first))
	e.clock.add(ReconcileAfter + time.Minute)
	h.held[p2.ID] = holder
	if c, _, err := e.svc.Reconcile(ctx); err != nil || c != 1 {
		t.Fatalf("düzeltme: %d %v", c, err)
	}
	if _, m, conf := attachedTo(p2.ID); m != holder || !conf {
		t.Fatalf("sahip mesaj: %v %v", m, conf)
	}
	// Without a chat client the job does nothing.
	e2 := setup(t)
	if c, d, err := e2.svc.Reconcile(ctx); err != nil || c+d != 0 {
		t.Fatalf("istemcisiz: %d %d %v", c, d, err)
	}
}

// blobReady uploads and completes a DM attachment.
func (e *env) blobReady(t *testing.T, user id.ID) id.ID {
	t.Helper()
	c := e.blobUpload(t, user)
	if code := objstoretest.Do(t, http.MethodPut, c.Links[0].URL, c.Links[0].Headers, blob); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	u, err := e.svc.CompleteUpload(ctx, user, c.Upload.ID, 0, 0)
	must(t, err)
	return u.ID
}

// A reported channel message's photo (ADR-0031): the panel gets a link
// only to the upload the report recorded while that message still holds it.
func TestReportedMessagePhotoURL(t *testing.T) {
	e := setup(t)
	photo := e.upload(t, e.member, PurposeChatPhoto, e.tayfa, clean(), 300, 200)
	msg := id.New()
	if link, _, ok, err := e.svc.ReportedMessagePhotoURL(ctx, msg, photo.ID); err != nil || ok || link != "" {
		t.Fatalf("bağlanmamış: %q %v", link, err)
	}
	must(t, e.svc.AttachChannelPhoto(ctx, e.member, photo.ID, e.tayfa, msg))
	link, exp, ok, err := e.svc.ReportedMessagePhotoURL(ctx, msg, photo.ID)
	if err != nil || !ok || !strings.Contains(link, "/media/") || exp.Sub(e.clock.now()) > ReportedPhotoLinkTTL+time.Second {
		t.Fatalf("link: %v", err)
	}
	if other, _, _, err := e.svc.ReportedMessagePhotoURL(ctx, id.New(), photo.ID); err != nil || other != "" {
		t.Fatalf("başka mesaj: %q %v", other, err)
	}
	// The author deletes the message: chat lets go of the photo and the
	// link is gone with it.
	must(t, MessageMediaReleased(ctx, store.New(e.pool), photo.ID, msg))
	if link, _, ok, _ := e.svc.ReportedMessagePhotoURL(ctx, msg, photo.ID); ok || link != "" {
		t.Fatal("bırakılan fotoğrafa link verildi")
	}
	// Another purpose with the same message ID: no link.
	att := e.blobReady(t, e.member)
	must(t, e.svc.AttachDirectMessageAttachment(ctx, e.member, att, msg))
	if link, _, ok, _ := e.svc.ReportedMessagePhotoURL(ctx, msg, att); ok || link != "" {
		t.Fatal("birebir eke link verildi")
	}
}
