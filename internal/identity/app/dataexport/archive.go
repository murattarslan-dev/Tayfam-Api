package dataexport

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Archive and file format (docs/data-export-format.md, ADR-0021 item 2).
const (
	// FormatVersion is manifest.json's format_version.
	FormatVersion = 1
	// Format names the encrypted file's layout for the client.
	Format = "aes-256-gcm-v1"
	// MaxArchiveBytes bounds the zip (ADR-0021 item 4): beyond it the
	// request fails as too_large and the platform answers it by hand.
	MaxArchiveBytes = 16 << 20
	// fileVersion is the first byte of the encrypted file.
	fileVersion = 0x01
	nonceSize   = 12
	tagSize     = 16
	// headerSize is the version byte and the nonce.
	headerSize = 1 + nonceSize
	// MaxFileBytes is the largest encrypted file.
	MaxFileBytes = MaxArchiveBytes + headerSize + tagSize
)

// ErrTooLarge: the archive would pass MaxArchiveBytes.
var ErrTooLarge = errors.New("dışa aktarma arşivi çok büyük")

// errSectionOrder: a service sent a section again after another one.
var errSectionOrder = errors.New("dışa aktarma bölümü sırasız geldi")

// fileAAD is the additional data of the encrypted file.
func fileAAD(request id.ID) []byte {
	return append([]byte("tayfam/data-export/v1/"), request[:]...)
}

// limited is the archive's buffer: the first headerSize bytes are kept for
// the version and the nonce, so the zip can be sealed in place.
type limited struct {
	buf bytes.Buffer
}

func (l *limited) Write(p []byte) (int, error) {
	if l.buf.Len()-headerSize+len(p) > MaxArchiveBytes {
		return 0, ErrTooLarge
	}
	return l.buf.Write(p)
}

// archive builds the zip of one export. Every file is a JSON array of
// records; files of sections that sent nothing are written as [] at the
// end, so the client always finds every file of the format.
type archive struct {
	out     *limited
	zw      *zip.Writer
	cur     io.Writer // the open file's writer
	open    string    // file being written, "" when none
	first   bool      // no record written to open yet
	done    []string
	counts  map[string]int
	ordered []string
}

func newArchive() *archive {
	l := &limited{}
	l.buf.Write(make([]byte, headerSize))
	return &archive{out: l, zw: zip.NewWriter(l), counts: map[string]int{}}
}

func (a *archive) file(name string) error {
	if a.open == name {
		return nil
	}
	if slices.Contains(a.done, name) {
		return errSectionOrder
	}
	if err := a.close(); err != nil {
		return err
	}
	w, err := a.zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()})
	if err != nil {
		return err
	}
	a.cur, a.open, a.first = w, name, true
	if err := a.write([]byte("[")); err != nil {
		return err
	}
	a.ordered = append(a.ordered, name)
	return nil
}

func (a *archive) close() error {
	if a.open == "" {
		return nil
	}
	if err := a.write([]byte("]\n")); err != nil {
		return err
	}
	a.done = append(a.done, a.open)
	a.open = ""
	return nil
}

// write writes to the open file (zip.Writer keeps one file open at a
// time).
func (a *archive) write(p []byte) error {
	_, err := a.cur.Write(p)
	return err
}

// add appends records to file name. A record must be one JSON value.
func (a *archive) add(name string, records [][]byte) error {
	if err := a.file(name); err != nil {
		return err
	}
	for _, r := range records {
		if !json.Valid(r) {
			return fmt.Errorf("dışa aktarma: %s geçersiz kayıt", name)
		}
		if !a.first {
			if err := a.write([]byte(",\n")); err != nil {
				return err
			}
		}
		a.first = false
		if err := a.write(r); err != nil {
			return err
		}
		a.counts[name]++
	}
	return nil
}

// manifest is manifest.json.
type manifest struct {
	FormatVersion int            `json:"format_version"`
	DataExportID  string         `json:"data_export_id"`
	UserID        string         `json:"user_id"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Sections      []manifestFile `json:"sections"`
}

type manifestFile struct {
	File    string `json:"file"`
	Records int    `json:"records"`
}

// finish writes the empty files of expected that sent nothing and the
// manifest, and returns the zip with headerSize free bytes in front.
func (a *archive) finish(request, user id.ID, now time.Time, expected []string) ([]byte, error) {
	for _, name := range expected {
		if !slices.Contains(a.ordered, name) {
			if err := a.add(name, nil); err != nil {
				return nil, err
			}
		}
	}
	if err := a.close(); err != nil {
		return nil, err
	}
	m := manifest{FormatVersion: FormatVersion, DataExportID: request.String(), UserID: user.String(), GeneratedAt: now.UTC()}
	for _, name := range a.ordered {
		m.Sections = append(m.Sections, manifestFile{File: name, Records: a.counts[name]})
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	w, err := a.zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := a.zw.Close(); err != nil {
		return nil, err
	}
	a.out.buf.Grow(tagSize)
	return a.out.buf.Bytes(), nil
}

// seal encrypts the zip in buf[headerSize:] in place with key and returns
// the file: version || nonce || ciphertext || tag. No copy of the plain
// archive is left in buf.
func seal(buf, key []byte, request id.ID) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	buf[0] = fileVersion
	nonce := buf[1:headerSize]
	_, _ = rand.Read(nonce) // crypto/rand.Read never returns an error
	plain := buf[headerSize:]
	sealed := aead.Seal(plain[:0], nonce, plain, fileAAD(request))
	if &sealed[0] != &plain[0] {
		// Seal reallocated (no room for the tag): never expected after
		// Grow; keep the file whole anyway.
		out := make([]byte, 0, headerSize+len(sealed))
		out = append(out, buf[:headerSize]...)
		return append(out, sealed...), nil
	}
	return buf[:headerSize+len(sealed)], nil
}

// Open decrypts an export file with its key, as the client does
// (docs/data-export-format.md). It returns the zip.
func Open(file, key []byte, request id.ID) ([]byte, error) {
	if len(file) < headerSize+tagSize || file[0] != fileVersion {
		return nil, errors.New("dışa aktarma dosyası tanınmadı")
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, file[1:headerSize], file[headerSize:], fileAAD(request))
}
