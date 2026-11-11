package dataexport

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func b64(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, KeySize)) }

func TestKeysWrapAndRotate(t *testing.T) {
	old, err := ParseKeys("1:" + b64(1))
	must(t, err)
	req, user := id.New(), id.New()
	data := bytes.Repeat([]byte{42}, KeySize)
	w := old.Wrap(req, user, data)
	if len(w) != WrappedKeySize || w[0] != 1 {
		t.Fatalf("sarılı anahtar: %d bayt, sürüm %d", len(w), w[0])
	}
	// After a rotation the old version still opens; new keys use the new one.
	rotated, err := ParseKeys("1:" + b64(1) + ", 2:" + b64(2))
	must(t, err)
	got, err := rotated.Unwrap(req, user, w)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("eski sürüm açılmalı: %v", err)
	}
	if w2 := rotated.Wrap(req, user, data); w2[0] != 2 {
		t.Fatalf("yeni sürüm kullanılmalı: %d", w2[0])
	}
	for name, tc := range map[string]struct {
		req, user id.ID
		w         []byte
	}{
		"başka talep":      {id.New(), user, w},
		"başka kullanıcı":  {req, id.New(), w},
		"bozuk":            {req, user, append(append([]byte{}, w[:len(w)-1]...), w[len(w)-1]^1)},
		"kısa":             {req, user, w[:10]},
		"bilinmeyen sürüm": {req, user, append([]byte{9}, w[1:]...)},
	} {
		if _, err := rotated.Unwrap(tc.req, tc.user, tc.w); err == nil {
			t.Errorf("%s açılmamalı", name)
		}
	}
}

func TestParseKeysRefusesAndHidesValue(t *testing.T) {
	secret := b64(3)
	for _, s := range []string{"", "x", "0:" + secret, "256:" + secret, "1:" + secret + ",1:" + secret, "1:" + base64.StdEncoding.EncodeToString([]byte("kısa")), "1:" + secret[:10] + "!!"} {
		_, err := ParseKeys(s)
		if err == nil {
			t.Errorf("%q kabul edilmemeli", s)
			continue
		}
		if strings.Contains(err.Error(), secret[:8]) {
			t.Errorf("hata anahtarı içermemeli: %v", err)
		}
	}
}

func TestArchiveRules(t *testing.T) {
	a := newArchive()
	must(t, a.add("core/a.json", [][]byte{[]byte(`{"x":1}`)}))
	must(t, a.add("core/a.json", [][]byte{[]byte(`{"x":2}`)}))
	must(t, a.add("core/b.json", nil))
	if err := a.add("core/a.json", [][]byte{[]byte(`{}`)}); !errors.Is(err, errSectionOrder) {
		t.Fatalf("kapanmış bölüme yazılmamalı: %v", err)
	}
	if err := a.add("core/c.json", [][]byte{[]byte(`{bozuk`)}); err == nil {
		t.Fatal("geçersiz JSON reddedilmeli")
	}
	b := newArchive()
	must(t, b.add("core/a.json", [][]byte{[]byte(`{"x":1}`), []byte(`{"x":2}`)}))
	req, user := id.New(), id.New()
	buf, err := b.finish(req, user, time.Now(), []string{"core/a.json", "core/empty.json"})
	must(t, err)
	key := bytes.Repeat([]byte{5}, KeySize)
	file, err := seal(buf, key, req)
	must(t, err)
	if file[0] != fileVersion || len(file) > MaxFileBytes {
		t.Fatalf("dosya: %d bayt", len(file))
	}
	plain, err := Open(file, key, req)
	must(t, err)
	files := readZip(t, plain)
	if string(files["core/a.json"]) != "[{\"x\":1},\n{\"x\":2}]\n" || string(files["core/empty.json"]) != "[]\n" {
		t.Fatalf("içerik: %q %q", files["core/a.json"], files["core/empty.json"])
	}
}
