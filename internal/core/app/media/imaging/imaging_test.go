package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/imaging/imagingtest"
)

func TestSniff(t *testing.T) {
	for name, tc := range map[string]struct {
		b    []byte
		want Kind
	}{
		"jpeg":  {[]byte{0xFF, 0xD8, 0xFF, 0xE0}, JPEG},
		"png":   {[]byte("\x89PNG\r\n\x1a\nxxxx"), PNG},
		"webp":  {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), WebP},
		"heic":  {[]byte("\x00\x00\x00\x18ftypheic\x00\x00"), HEIC},
		"mif1":  {[]byte("\x00\x00\x00\x18ftypmif1\x00\x00"), HEIC},
		"mp4":   {[]byte("\x00\x00\x00\x18ftypisom\x00\x00"), Unknown},
		"metin": {[]byte("merhaba, bu bir resim değil"), Unknown},
		"html":  {[]byte("<html><script>"), Unknown},
		"boş":   {nil, Unknown},
	} {
		if got := Sniff(tc.b); got != tc.want {
			t.Errorf("%s: %v, %v bekleniyordu", name, got, tc.want)
		}
	}
}

// markers returns the JPEG markers before the scan data.
func markers(t *testing.T, b []byte) []byte {
	t.Helper()
	if Sniff(b) != JPEG {
		t.Fatal("çıktı JPEG değil")
	}
	var out []byte
	for i := 2; i+4 <= len(b); {
		m := b[i+1]
		out = append(out, m)
		if m == 0xDA {
			return out
		}
		i += 2 + (int(b[i+2])<<8 | int(b[i+3]))
	}
	t.Fatal("tarama bulunamadı")
	return nil
}

// The acceptance criterion of issue #135: a JPEG with GPS in its EXIF comes
// out without EXIF, GPS or any other metadata.
func TestMetadataIsStripped(t *testing.T) {
	in := imagingtest.JPEG(imagingtest.Quadrants(64, 48), 1)
	if !bytes.Contains(in, []byte(imagingtest.Marker)) || !bytes.Contains(in, []byte("Exif\x00\x00")) {
		t.Fatal("fikstür EXIF/GPS taşımıyor")
	}
	res, err := Process(in, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string][]byte{"full": res.Full, "preview": res.Preview} {
		for _, needle := range [][]byte{[]byte(imagingtest.Marker), []byte("Exif"), []byte("GPS"), []byte("ASCII")} {
			if bytes.Contains(out, needle) {
				t.Errorf("%s çıktısında %q var", name, needle)
			}
		}
		for _, m := range markers(t, out) {
			if (m >= 0xE0 && m <= 0xEF) || m == 0xFE {
				t.Errorf("%s çıktısında metadata segmenti var: FF%02X", name, m)
			}
		}
	}
	if res.ContentType != "image/jpeg" || res.Width != 64 || res.Height != 48 {
		t.Fatalf("sonuç: %+v", res)
	}
}

func near(c color.Color, want color.RGBA) bool {
	r, g, b, _ := c.RGBA()
	d := func(a uint32, w uint8) bool { x := int(a>>8) - int(w); return x > -40 && x < 40 }
	return d(r, want.R) && d(g, want.G) && d(b, want.B)
}

// The EXIF orientation is applied to the pixels (the tag itself is gone).
func TestOrientation(t *testing.T) {
	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	white := color.RGBA{255, 255, 255, 255}
	// Display corners (top-left, top-right, bottom-left, bottom-right) for
	// a stored image red|green over blue|white.
	for o, want := range map[int][4]color.RGBA{
		1: {red, green, blue, white},
		2: {green, red, white, blue},
		3: {white, blue, green, red},
		4: {blue, white, red, green},
		5: {red, blue, green, white},
		6: {blue, red, white, green},
		7: {white, green, blue, red},
		8: {green, white, red, blue},
	} {
		res, err := Process(imagingtest.JPEG(imagingtest.Quadrants(80, 40), o), Defaults())
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(res.Full))
		if err != nil {
			t.Fatal(err)
		}
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		if (o >= 5) != (w == 40 && h == 80) || res.Width != w || res.Height != h {
			t.Fatalf("yön %d: %dx%d", o, w, h)
		}
		corners := [4]color.Color{img.At(4, 4), img.At(w-5, 4), img.At(4, h-5), img.At(w-5, h-5)}
		for i := range corners {
			if !near(corners[i], want[i]) {
				t.Errorf("yön %d köşe %d: %v, %v bekleniyordu", o, i, corners[i], want[i])
			}
		}
	}
}

func TestSizes(t *testing.T) {
	res, err := Process(imagingtest.JPEG(imagingtest.Quadrants(4000, 3000), 6), Defaults())
	if err != nil {
		t.Fatal(err)
	}
	full, _ := jpeg.DecodeConfig(bytes.NewReader(res.Full))
	prev, _ := jpeg.DecodeConfig(bytes.NewReader(res.Preview))
	if full.Width != 1200 || full.Height != 1600 || prev.Width != 300 || prev.Height != 400 {
		t.Fatalf("boyutlar: full %dx%d, preview %dx%d", full.Width, full.Height, prev.Width, prev.Height)
	}
	// A small image is not enlarged.
	res, err = Process(imagingtest.PNG(imagingtest.Quadrants(120, 90)), Defaults())
	if err != nil || res.Width != 120 || res.Height != 90 {
		t.Fatalf("küçük resim: %+v %v", res, err)
	}
}

func TestTransparencyOnWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 10, 10)) // fully transparent
	res, err := Process(imagingtest.PNG(img), Defaults())
	if err != nil {
		t.Fatal(err)
	}
	out, _ := jpeg.Decode(bytes.NewReader(res.Full))
	if !near(out.At(5, 5), color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("saydam alan beyaz olmalı: %v", out.At(5, 5))
	}
}

func TestRejections(t *testing.T) {
	good := imagingtest.JPEG(imagingtest.Quadrants(64, 48), 1)
	small := Defaults()
	small.MaxPixels = 1000
	for name, tc := range map[string]struct {
		b    []byte
		o    Options
		want Reason
	}{
		// "foto.jpg" whose content is not an image.
		"jpg uzantılı metin": {[]byte("Bu bir resim değil ama adı foto.jpg"), Defaults(), ReasonNotImage},
		"html":               {[]byte("<html><body>x</body></html>"), Defaults(), ReasonNotImage},
		"webp":               {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 xxxxxxxx"), Defaults(), ReasonUnsupported},
		"heic":               {[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), Defaults(), ReasonUnsupported},
		"yarım jpeg":         {good[:len(good)/2], Defaults(), ReasonCorrupt},
		"yalnızca imza":      {[]byte{0xFF, 0xD8, 0xFF}, Defaults(), ReasonCorrupt},
		"çok piksel":         {good, small, ReasonTooLarge},
		"çok geniş":          {imagingtest.PNG(image.NewGray(image.Rect(0, 0, 10001, 1))), Defaults(), ReasonTooLarge},
	} {
		_, err := Process(tc.b, tc.o)
		var re *RejectError
		if !errors.As(err, &re) || re.Reason != tc.want {
			t.Errorf("%s: %v, %s bekleniyordu", name, err, tc.want)
		}
	}
}

// Malformed input never panics out of Process and never succeeds with
// metadata.
func FuzzProcess(f *testing.F) {
	f.Add(imagingtest.JPEG(imagingtest.Quadrants(16, 16), 6))
	f.Add(imagingtest.PNG(imagingtest.Quadrants(16, 16)))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10, 'E', 'x', 'i', 'f', 0, 0, 'M', 'M', 0, 42, 0xFF, 0xFF, 0xFF, 0xFF})
	o := Defaults()
	o.MaxPixels = 1 << 16
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := Process(b, o)
		if err != nil {
			var re *RejectError
			if !errors.As(err, &re) {
				t.Fatalf("beklenmeyen hata: %v", err)
			}
			return
		}
		if bytes.Contains(res.Full, []byte("Exif")) {
			t.Fatal("çıktıda EXIF var")
		}
	})
}

func BenchmarkProcess12MP(b *testing.B) {
	in := imagingtest.JPEG(imagingtest.Quadrants(4000, 3000), 6)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Process(in, Defaults()); err != nil {
			b.Fatal(err)
		}
	}
}
