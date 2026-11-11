package objstore_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore/objstoretest"
)

func TestPresignedPutOnlyForThatKeyTypeAndSize(t *testing.T) {
	s := objstoretest.New(t)
	c := s.Client(t)
	body := []byte("0123456789")
	p, err := c.PresignPut("incoming/aaaa", "image/jpeg", int64(len(body)), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if p.Headers["Content-Type"] != "image/jpeg" || p.Headers["Content-Length"] != "10" {
		t.Fatalf("başlıklar: %v", p.Headers)
	}
	other := strings.Replace(p.URL, "incoming/aaaa", "media/aaaa", 1)
	for name, tc := range map[string]struct {
		url  string
		hdr  map[string]string
		body []byte
	}{
		"başka boyut":   {p.URL, p.Headers, []byte("01234567890")},
		"başka tür":     {p.URL, map[string]string{"Content-Type": "image/png"}, body},
		"başka anahtar": {other, p.Headers, body},
	} {
		if code := objstoretest.Do(t, http.MethodPut, tc.url, tc.hdr, tc.body); code != http.StatusForbidden {
			t.Errorf("%s: %d, 403 bekleniyordu", name, code)
		}
	}
	if code := objstoretest.Do(t, http.MethodPut, p.URL, p.Headers, body); code != http.StatusOK {
		t.Fatalf("doğru yükleme: %d", code)
	}
	if o, ok := s.Get("incoming/aaaa"); !ok || string(o.Body) != string(body) {
		t.Fatal("nesne yazılmadı")
	}
	// After five minutes the link is dead.
	s.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	if code := objstoretest.Do(t, http.MethodPut, p.URL, p.Headers, body); code != http.StatusForbidden {
		t.Fatalf("süresi dolan link: %d", code)
	}
}

func TestClientRoundTrip(t *testing.T) {
	s := objstoretest.New(t)
	c := s.Client(t)
	ctx := context.Background()
	if _, err := c.Head(ctx, "incoming/x/full"); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("yok: %v", err)
	}
	s.Put("incoming/x/full", objstoretest.Object{Body: []byte("abcdef"), ContentType: "image/jpeg"})
	o, err := c.Head(ctx, "incoming/x/full")
	if err != nil || o.Size != 6 || o.ETag != objstoretest.ETag([]byte("abcdef")) {
		t.Fatalf("head: %+v %v", o, err)
	}
	if b, err := c.GetRange(ctx, "incoming/x/full", 3, o.ETag); err != nil || string(b) != "abc" {
		t.Fatalf("aralık: %q %v", b, err)
	}
	if b, err := c.GetRange(ctx, "incoming/x/full", 100, o.ETag); err != nil || string(b) != "abcdef" {
		t.Fatalf("küçük nesne: %q %v", b, err)
	}
	if err := c.Copy(ctx, "incoming/x/full", "media/x/full", o.ETag); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get("media/x/full"); !ok || string(got.Body) != "abcdef" || got.ContentType != "image/jpeg" {
		t.Fatal("kopya")
	}
	// Replaced in between: neither the read nor the copy goes through.
	s.Put("incoming/x/full", objstoretest.Object{Body: []byte("XXXXXX")})
	if _, err := c.GetRange(ctx, "incoming/x/full", 3, o.ETag); !errors.Is(err, objstore.ErrChanged) {
		t.Fatalf("değişen nesne okunmamalı: %v", err)
	}
	if err := c.Copy(ctx, "incoming/x/full", "media/x/full", o.ETag); !errors.Is(err, objstore.ErrChanged) {
		t.Fatalf("değişen nesne kopyalanmamalı: %v", err)
	}
	if got, _ := s.Get("media/x/full"); string(got.Body) != "abcdef" {
		t.Fatal("kopya değişmemeli")
	}
	g, err := c.PresignGet("media/x/full", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if code := objstoretest.Do(t, http.MethodGet, g.URL, nil, nil); code != http.StatusOK {
		t.Fatalf("imzalı indirme: %d", code)
	}
	if code := objstoretest.Do(t, http.MethodGet, strings.Replace(g.URL, "media/x/full", "media/x/preview", 1), nil, nil); code != http.StatusForbidden {
		t.Fatalf("indirme linki başka nesnede çalışmamalı: %d", code)
	}
	if err := c.Delete(ctx, "media/x/full"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "media/x/full"); err != nil {
		t.Fatalf("olmayanı silmek hata değil: %v", err)
	}

	// Wrong credentials are refused by the storage.
	cfg := s.Config()
	cfg.SecretAccessKey = "wrong"
	bad, _ := objstore.New(cfg)
	var se *objstore.StatusError
	if _, err := bad.Head(ctx, "incoming/x/full"); !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Fatalf("yanlış anahtar: %v", err)
	}
}

// Errors never carry the key, the URL or the credentials.
func TestErrorsCarryNoSecrets(t *testing.T) {
	cfg := objstore.Config{
		Endpoint: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, Bucket: "b", Region: "auto",
		AccessKeyID: "AKIDSECRETLOOKING", SecretAccessKey: "s3cr3t",
	}
	c, err := objstore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, err := range []error{
		c.Copy(ctx, "incoming/0123456789abcdef/full", "media/0123456789abcdef/full", `"e"`),
		c.Delete(ctx, "media/0123456789abcdef/full"),
		func() error { _, err := c.Head(ctx, "incoming/0123456789abcdef"); return err }(),
		func() error { _, err := c.GetRange(ctx, "incoming/0123456789abcdef", 10, `"e"`); return err }(),
	} {
		if err == nil {
			t.Fatal("hata bekleniyordu")
		}
		for _, secret := range []string{"0123456789abcdef", "AKIDSECRETLOOKING", "s3cr3t", "127.0.0.1"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("hata mesajı %q içeriyor: %v", secret, err)
			}
		}
	}
}

func TestKeysCannotEscape(t *testing.T) {
	c, err := objstore.New(objstore.Config{
		Endpoint: &url.URL{Scheme: "https", Host: "example.invalid"}, Bucket: "b", Region: "auto", AccessKeyID: "a", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"", "/abs", "../b2/x", "a/../../x", "a?x=1", "a#b", "a b", "ä"} {
		if _, err := c.PresignGet(k, time.Hour); err == nil {
			t.Errorf("%q kabul edilmemeli", k)
		}
	}
	for _, e := range []*url.URL{
		nil, {Scheme: "ftp", Host: "x"}, {Scheme: "https"}, {Scheme: "https", Host: "x", Path: "/p"},
		{Scheme: "https", Host: "x", User: url.User("u")},
	} {
		if _, err := objstore.New(objstore.Config{Endpoint: e, Bucket: "b", Region: "auto", AccessKeyID: "a", SecretAccessKey: "s"}); err == nil {
			t.Errorf("uç adresi %v kabul edilmemeli", e)
		}
	}
}
