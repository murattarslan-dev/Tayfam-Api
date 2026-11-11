package media

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore/objstoretest"
)

// Tests of the KVKK data export storage (issue #144, ADR-0021).

func TestExportStorageFlow(t *testing.T) {
	e := setup(t)
	request := id.New()
	up, err := e.svc.ReserveExport(ctx, e.owner, request)
	must(t, err)
	again, err := e.svc.ReserveExport(ctx, e.owner, request)
	if err != nil || again != up {
		t.Fatalf("tekrar gönderim aynı yuvayı dönmeli: %v %v", again, err)
	}

	body := bytes.Repeat([]byte{0xab}, 100)
	// Not completable before a size and an object exist.
	if _, err := e.svc.CompleteExport(ctx, e.owner, up); !errors.Is(err, apperr.ErrUploadNotReceived) {
		t.Fatalf("boyutsuz tamamlama: %v", err)
	}
	for _, size := range []int64{0, MinExportBytes - 1, MaxExportBytes + 1} {
		var ve *apperr.ValidationError
		if _, err := e.svc.ExportUploadURL(ctx, e.owner, up, size); !errors.As(err, &ve) {
			t.Errorf("%d bayt reddedilmeli: %v", size, err)
		}
	}
	link, err := e.svc.ExportUploadURL(ctx, e.owner, up, int64(len(body)))
	must(t, err)
	if !strings.Contains(link.URL, "/exports/") || link.Headers["Content-Type"] != "application/octet-stream" {
		t.Fatalf("link: %+v", link)
	}
	// The size is fixed by the first link.
	var ve *apperr.ValidationError
	if _, err := e.svc.ExportUploadURL(ctx, e.owner, up, int64(len(body))+1); !errors.As(err, &ve) {
		t.Fatalf("başka boyut reddedilmeli: %v", err)
	}
	if _, err := e.svc.CompleteExport(ctx, e.owner, up); !errors.Is(err, apperr.ErrUploadNotReceived) {
		t.Fatalf("nesnesiz tamamlama: %v", err)
	}
	// The link refuses another length.
	if code := objstoretest.Do(t, http.MethodPut, link.URL, link.Headers, append(body, 1)); code == http.StatusOK {
		t.Fatal("başka boyutta yükleme reddedilmeli")
	}
	if code := objstoretest.Do(t, http.MethodPut, link.URL, link.Headers, body); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	expires, err := e.svc.CompleteExport(ctx, e.owner, up)
	must(t, err)
	if d := expires.Sub(e.clock.now().Truncate(time.Microsecond)); d != ExportLifetime {
		t.Fatalf("süre %v", d)
	}
	if again, err := e.svc.CompleteExport(ctx, e.owner, up); err != nil || !again.Equal(expires) {
		t.Fatalf("tekrar tamamlama: %v %v", again, err)
	}

	// IDOR: nobody else finds it, in any method.
	if _, err := e.svc.ExportDownloadURL(ctx, e.member, up); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("başkası indirme linki almamalı: %v", err)
	}
	if _, err := e.svc.ExportUploadURL(ctx, e.member, up, int64(len(body))); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("başkası yükleme linki almamalı: %v", err)
	}
	if _, err := e.svc.CompleteExport(ctx, e.member, up); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("başkası tamamlamamalı: %v", err)
	}
	// Image methods never see it, its owner included.
	if _, err := e.svc.GetUpload(ctx, e.owner, up); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("görüntü yolu dışa aktarmayı görmemeli: %v", err)
	}
	if urls, err := e.svc.MediaURLs(ctx, e.owner, []id.ID{up}); err != nil || len(urls) != 0 {
		t.Fatalf("medya linki üretilmemeli: %v %v", urls, err)
	}
	if err := e.svc.AttachAvatar(ctx, e.owner, up); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("avatar olarak bağlanmamalı: %v", err)
	}

	get, err := e.svc.ExportDownloadURL(ctx, e.owner, up)
	must(t, err)
	if d := time.Until(get.Expires); !strings.Contains(get.URL, "/exports/") || d > ExportLinkTTL || d < ExportLinkTTL-time.Minute {
		t.Fatalf("indirme linki: %+v", get)
	}
	if code := objstoretest.Do(t, http.MethodGet, get.URL, nil, nil); code != http.StatusOK {
		t.Fatalf("GET: %d", code)
	}

	// Past its 7 days it is no longer served and cleanup deletes it.
	e.clock.add(ExportLifetime + time.Minute)
	if _, err := e.svc.ExportDownloadURL(ctx, e.owner, up); !errors.Is(err, apperr.ErrUploadNotReady) {
		t.Fatalf("süresi dolan indirilmemeli: %v", err)
	}
	n, err := e.svc.Cleanup(ctx)
	must(t, err)
	if n != 1 || slices.ContainsFunc(e.s3.Keys(), func(k string) bool { return strings.Contains(k, "exports/") }) {
		t.Fatalf("temizlik: %d %v", n, e.s3.Keys())
	}

	// Links, keys and the content never reach the log.
	logs := e.logs.String()
	for _, secret := range []string{link.URL, get.URL, "exports/", "X-Amz-Signature"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log %q içermemeli", secret)
		}
	}
}

func TestExportStorageReplacesOldSlotAndNeedsStorage(t *testing.T) {
	e := setup(t)
	first, err := e.svc.ReserveExport(ctx, e.owner, id.New())
	must(t, err)
	second, err := e.svc.ReserveExport(ctx, e.owner, id.New())
	must(t, err)
	if first == second {
		t.Fatal("yeni talep yeni yuva açmalı")
	}
	if _, err := e.svc.ExportUploadURL(ctx, e.owner, first, 100); !errors.Is(err, apperr.ErrUploadNotFound) {
		t.Fatalf("eski yuva bırakılmalı: %v", err)
	}
	if _, err := e.svc.ExportUploadURL(ctx, e.owner, second, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReserveExport(ctx, e.owner, id.ID{}); err == nil {
		t.Fatal("boş talep kimliği reddedilmeli")
	}

	off := setup(t, func(c *Config) { c.Store = nil })
	if _, err := off.svc.ReserveExport(ctx, off.owner, id.New()); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("depolama yokken: %v", err)
	}
	if _, err := off.svc.ExportDownloadURL(ctx, off.owner, id.New()); !errors.Is(err, apperr.ErrUnavailable) {
		t.Fatalf("depolama yokken: %v", err)
	}
}
