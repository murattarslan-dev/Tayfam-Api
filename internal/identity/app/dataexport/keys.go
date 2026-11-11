package dataexport

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

// KeySize is the size of a data key and of a wrapping key (AES-256).
const KeySize = 32

// WrappedKeySize is version (1) || nonce (12) || key (32) || tag (16).
const WrappedKeySize = 1 + 12 + KeySize + 16

var errUnwrap = errors.New("dışa aktarma anahtarı açılamadı")

// Keys wraps data keys with identity's versioned wrapping keys
// (IDENTITY_DATA_EXPORT_KEYS, from the identity-export-key secret; ADR-0021
// item 2). New keys use the highest version; older versions stay readable
// so a rotation does not lose the exports of the last 7 days.
type Keys struct {
	active byte
	aeads  map[byte]cipher.AEAD
}

// ParseKeys builds Keys from "<version>:<base64 32-byte key>,..." with
// version 1–255. No part of the value is ever part of an error.
func ParseKeys(s string) (*Keys, error) {
	k := &Keys{aeads: map[byte]cipher.AEAD{}}
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		v, b64, ok := strings.Cut(entry, ":")
		n, err := strconv.Atoi(v)
		if !ok || err != nil || n < 1 || n > 255 {
			return nil, errors.New("dışa aktarma anahtarı \"<1-255>:<base64>\" biçiminde olmalı")
		}
		ver := byte(n)
		if _, dup := k.aeads[ver]; dup {
			return nil, fmt.Errorf("dışa aktarma anahtar sürümü tekrar ediyor: %d", n)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(key) != KeySize {
			return nil, fmt.Errorf("dışa aktarma anahtarı %d: 32 baytlık base64 olmalı", n)
		}
		aead, err := newGCM(key)
		if err != nil {
			return nil, fmt.Errorf("dışa aktarma anahtarı %d: %w", n, err)
		}
		k.aeads[ver] = aead
		k.active = max(k.active, ver)
	}
	if len(k.aeads) == 0 {
		return nil, errors.New("dışa aktarma anahtarı yok")
	}
	return k, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// wrapAAD binds a wrapped key to its request and user: a wrapped key copied
// to another row does not open.
func wrapAAD(request, user id.ID) []byte {
	out := []byte("tayfam/identity/data_export_requests.wrapped_key/")
	out = append(out, request[:]...)
	return append(out, user[:]...)
}

// Wrap seals dataKey for request of user.
func (k *Keys) Wrap(request, user id.ID, dataKey []byte) []byte {
	aead := k.aeads[k.active]
	out := make([]byte, 1+aead.NonceSize(), WrappedKeySize)
	out[0] = k.active
	_, _ = rand.Read(out[1:]) // crypto/rand.Read never returns an error
	return aead.Seal(out, out[1:], dataKey, wrapAAD(request, user))
}

// Unwrap reverses Wrap.
func (k *Keys) Unwrap(request, user id.ID, wrapped []byte) ([]byte, error) {
	if len(wrapped) != WrappedKeySize {
		return nil, errUnwrap
	}
	aead, ok := k.aeads[wrapped[0]]
	if !ok {
		return nil, errUnwrap
	}
	n := aead.NonceSize()
	key, err := aead.Open(nil, wrapped[1:1+n], wrapped[1+n:], wrapAAD(request, user))
	if err != nil || len(key) != KeySize {
		return nil, errUnwrap
	}
	return key, nil
}
