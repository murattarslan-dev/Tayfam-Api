package inspect

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/inspect/inspecttest"
)

func check(b []byte) (Info, error) { return Inspect(b, int64(len(b))) }

func wantReason(t *testing.T, name string, err error, want Reason) {
	t.Helper()
	var re *RejectError
	if !errors.As(err, &re) || re.Reason != want {
		t.Errorf("%s: %v, %s bekleniyordu", name, err, want)
	}
}

func TestCleanImages(t *testing.T) {
	for name, tc := range map[string]struct {
		b    []byte
		want Info
	}{
		"jpeg":            {inspecttest.JPEG(160, 90), Info{JPEG, 160, 90}},
		"jpeg ve yorum":   {inspecttest.WithSegment(inspecttest.JPEG(40, 30), 0xFE, []byte("yorum")), Info{JPEG, 40, 30}},
		"jpeg ve ICC":     {inspecttest.WithSegment(inspecttest.JPEG(40, 30), 0xE2, []byte("ICC_PROFILE\x00\x01\x01xxxx")), Info{JPEG, 40, 30}},
		"webp kayıpsız":   {inspecttest.WebP(1600, 1200, inspecttest.WebPOptions{}), Info{WebP, 1600, 1200}},
		"webp kayıplı":    {inspecttest.WebP(400, 225, inspecttest.WebPOptions{Lossy: true}), Info{WebP, 400, 225}},
		"webp genişletik": {inspecttest.WebP(300, 200, inspecttest.WebPOptions{Extended: true, Flags: 0x30, Before: [][2]string{{"ICCP", "icc"}, {"ALPH", "a"}}}), Info{WebP, 300, 200}},
	} {
		got, err := check(tc.b)
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
}

// The acceptance criterion of issue #135 after ADR-0020: a variant that
// still carries EXIF (with GPS), XMP or IPTC is rejected.
func TestMetadataIsRejected(t *testing.T) {
	j := inspecttest.JPEG(64, 48)
	gps := inspecttest.WithSegment(j, 0xE1, inspecttest.EXIF())
	if !bytes.Contains(gps, []byte(inspecttest.Marker)) {
		t.Fatal("fikstür GPS taşımıyor")
	}
	for name, b := range map[string][]byte{
		"jpeg EXIF/GPS":       gps,
		"jpeg XMP":            inspecttest.WithSegment(j, 0xE1, inspecttest.XMP()),
		"jpeg IPTC":           inspecttest.WithSegment(j, 0xED, []byte("Photoshop 3.0\x008BIM")),
		"jpeg MPF":            inspecttest.WithSegment(j, 0xE2, []byte("MPF\x00II*\x00")),
		"jpeg ICC sonra EXIF": inspecttest.WithSegment(inspecttest.WithSegment(j, 0xE1, inspecttest.EXIF()), 0xE2, []byte("ICC_PROFILE\x00")),
		"webp EXIF bayrağı":   inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, Flags: 0x08}),
		"webp XMP bayrağı":    inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, Flags: 0x04}),
		"webp EXIF parçası":   inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, Before: [][2]string{{"EXIF", string(inspecttest.EXIF())}}}),
		"webp sonda XMP":      inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, After: [][2]string{{"XMP ", "<x:xmpmeta/>"}}}),
		"webp sonda EXIF":     inspecttest.WebP(10, 10, inspecttest.WebPOptions{After: [][2]string{{"EXIF", "Exif"}}}),
	} {
		_, err := check(b)
		wantReason(t, name, err, Metadata)
	}
}

func TestOtherRejections(t *testing.T) {
	j := inspecttest.JPEG(64, 48)
	webp := inspecttest.WebP(10, 10, inspecttest.WebPOptions{})
	truncatedRIFF := append([]byte{}, webp...)
	truncatedRIFF[4]++ // RIFF size no longer the object's
	for name, tc := range map[string]struct {
		b    []byte
		want Reason
	}{
		// "foto.jpg" whose content is not an image.
		"jpg uzantılı metin":    {[]byte("Bu bir resim değil ama adı foto.jpg"), NotImage},
		"html":                  {[]byte("<html><body>x</body></html>"), NotImage},
		"png":                   {[]byte("\x89PNG\r\n\x1a\nxxxxxxxx"), Unsupported},
		"heic":                  {[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), Unsupported},
		"gif":                   {[]byte("GIF89a......"), Unsupported},
		"webp animasyon":        {inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, Flags: 0x02}), Unsupported},
		"yalnızca imza":         {[]byte{0xFF, 0xD8, 0xFF}, NotImage},
		"taramasız jpeg":        {j[:20], NotImage},
		"SOF'suz jpeg":          {[]byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02}, NotImage},
		"tuval ≠ çerçeve":       {inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, CanvasW: 20, CanvasH: 10}), NotImage},
		"RIFF boyutu yanlış":    {truncatedRIFF, NotImage},
		"bilinmeyen webp parça": {inspecttest.WebP(10, 10, inspecttest.WebPOptions{Extended: true, Before: [][2]string{{"ABCD", "x"}}}), NotImage},
	} {
		_, err := check(tc.b)
		wantReason(t, name, err, tc.want)
	}
	// The head must contain the whole header; total is the object's size.
	if _, err := Inspect(webp, int64(len(webp))+2); err == nil {
		t.Error("nesne boyutu RIFF ile uyuşmuyorsa reddedilmeli")
	}
	big := inspecttest.JPEG(640, 480)
	if _, err := Inspect(big[:len(big)/2], int64(len(big))); err != nil {
		t.Errorf("JPEG başlığı ilk baytlarda yeterli olmalı: %v", err)
	}
}

// Malformed input never panics and never passes with a nonsense result.
func FuzzInspect(f *testing.F) {
	j := inspecttest.JPEG(16, 16)
	f.Add(j)
	f.Add(inspecttest.WithSegment(j, 0xE1, inspecttest.EXIF()))
	f.Add(inspecttest.WebP(16, 16, inspecttest.WebPOptions{}))
	f.Add(inspecttest.WebP(16, 16, inspecttest.WebPOptions{Lossy: true}))
	f.Add(inspecttest.WebP(16, 16, inspecttest.WebPOptions{Extended: true, Flags: 0x10, Before: [][2]string{{"ALPH", "a"}}}))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42})
	f.Fuzz(func(t *testing.T, b []byte) {
		info, err := check(b)
		if err != nil {
			var re *RejectError
			if !errors.As(err, &re) {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			return
		}
		if (info.Type != JPEG && info.Type != WebP) || info.Width < 1 || info.Height < 1 ||
			info.Width > 1<<16 || info.Height > 1<<16 {
			t.Fatalf("geçersiz sonuç: %+v", info)
		}
		if image.Rect(0, 0, info.Width, info.Height).Empty() {
			t.Fatal("boş görüntü")
		}
	})
}
