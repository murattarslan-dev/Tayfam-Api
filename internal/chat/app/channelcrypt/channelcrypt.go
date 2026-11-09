// Package channelcrypt encrypts channel message bodies for storage (ADR-0005
// "Kanal mesajları", ADR-0011 principle 4, issue #81).
//
// Bodies are encrypted in the application with AES-256-GCM from the standard
// library (CLAUDE.md rule 4); the database, its backups and anyone reading
// them without the key see only ciphertext. The server can decrypt, which
// moderation needs; direct messages are end-to-end encrypted elsewhere.
//
// Keys are versioned ("1:<base64>,2:<base64>", CHAT_MESSAGE_KEYS). New
// bodies use the highest version; the version is stored next to the
// ciphertext (channel_messages.key_version), so older bodies stay readable
// after a rotation as long as their key is kept. The key is separate from
// identity's birth date key (ADR-0012).
//
// The message ID and the channel ID are bound as additional authenticated
// data: a ciphertext copied to another row, or a row moved to another
// channel, does not decrypt.
package channelcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// ErrDecrypt hides why decryption failed (unknown key version, wrong row,
// modified data).
var ErrDecrypt = errors.New("kanal mesajı çözülemedi")

// Sealed is an encrypted body as stored: the key version and nonce ||
// ciphertext || tag.
type Sealed struct {
	Version int16
	Data    []byte
}

// Cipher encrypts and decrypts channel message bodies. It is safe for
// concurrent use and read-only after construction.
type Cipher struct {
	active int16
	aeads  map[int16]cipher.AEAD
}

// ParseKeys builds a Cipher from "<version>:<base64 32-byte key>,..." where
// version is 1–255. The value comes from a secret (SOPS, ADR-0006). Errors
// never echo the input.
func ParseKeys(s string) (*Cipher, error) {
	c := &Cipher{aeads: map[int16]cipher.AEAD{}}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		v, b64, ok := strings.Cut(entry, ":")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n < 1 || n > 255 {
			return nil, errors.New("kanal mesajı anahtarı \"<1-255>:<base64>\" biçiminde olmalı")
		}
		ver := int16(n)
		if _, dup := c.aeads[ver]; dup {
			return nil, fmt.Errorf("kanal mesajı anahtar sürümü tekrar ediyor: %d", n)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != KeySize {
			return nil, fmt.Errorf("kanal mesajı anahtarı %d: 32 baytlık base64 olmalı", n)
		}
		aead, err := newAEAD(key)
		if err != nil {
			return nil, fmt.Errorf("kanal mesajı anahtarı %d: %w", n, err)
		}
		c.aeads[ver] = aead
		c.active = max(c.active, ver)
	}
	if len(c.aeads) == 0 {
		return nil, errors.New("kanal mesajı anahtarı yok")
	}
	return c, nil
}

// Ephemeral returns a Cipher with a fresh random key (version 1) that exists
// only in this process. For tests and local runs: messages written with it
// cannot be read after a restart.
func Ephemeral() *Cipher {
	key := make([]byte, KeySize)
	_, _ = rand.Read(key) // crypto/rand.Read never returns an error
	aead, err := newAEAD(key)
	if err != nil {
		panic(err) // a 32-byte key is always valid
	}
	return &Cipher{active: 1, aeads: map[int16]cipher.AEAD{1: aead}}
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ActiveVersion is the key version new bodies are encrypted with.
func (c *Cipher) ActiveVersion() int16 { return c.active }

// aad binds a body to its message and channel.
func aad(message, channel id.ID) []byte {
	const prefix = "tayfam/chat/channel_messages.body/"
	out := make([]byte, 0, len(prefix)+2*len(message))
	out = append(out, prefix...)
	out = append(out, message[:]...)
	return append(out, channel[:]...)
}

// Encrypt seals body for the message in channel with the active key.
func (c *Cipher) Encrypt(message, channel id.ID, body string) Sealed {
	aead := c.aeads[c.active]
	nonce := make([]byte, aead.NonceSize())
	_, _ = rand.Read(nonce) // crypto/rand.Read never returns an error
	out := make([]byte, 0, len(nonce)+len(body)+aead.Overhead())
	out = append(out, nonce...)
	return Sealed{Version: c.active, Data: aead.Seal(out, nonce, []byte(body), aad(message, channel))}
}

// Decrypt reverses Encrypt. It fails with ErrDecrypt if s was made for
// another message or channel, with an unknown key, or was modified.
func (c *Cipher) Decrypt(message, channel id.ID, s Sealed) (string, error) {
	aead, ok := c.aeads[s.Version]
	if !ok || len(s.Data) < aead.NonceSize()+aead.Overhead() {
		return "", ErrDecrypt
	}
	n := aead.NonceSize()
	plain, err := aead.Open(nil, s.Data[:n], s.Data[n:], aad(message, channel))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// NeedsReencrypt reports whether s was encrypted with an older key.
func (c *Cipher) NeedsReencrypt(s Sealed) bool { return s.Version != c.active }
