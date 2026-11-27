package integritytest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"math/big"
	"sort"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
)

// AppID is the fake iOS app's App ID (TEAMID.bundle).
const AppID = "ABCDE12345.app.tayfam.test"

// Apple is a fake App Attest CA: a root, an intermediate and the devices'
// keys it attests. Nothing here is Apple's; the real objects are tested in
// package integrity against Apple's root.
type Apple struct {
	t        testing.TB
	now      func() time.Time
	root     *x509.Certificate
	inter    *x509.Certificate
	interKey *ecdsa.PrivateKey
	serial   int64
}

// NewApple returns a fake CA and an App Attest verifier that trusts only
// it, for AppID in the production environment.
func NewApple(t testing.TB, now func() time.Time) (*Apple, *integrity.AppAttest) {
	t.Helper()
	a := &Apple{t: t, now: now}
	rootKey := newKey(t)
	a.root = a.cert(&x509.Certificate{Subject: pkix.Name{CommonName: "Fake App Attestation Root CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, nil, rootKey, &rootKey.PublicKey)
	a.interKey = newKey(t)
	a.inter = a.cert(&x509.Certificate{Subject: pkix.Name{CommonName: "Fake App Attestation CA 1"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}, a.root, rootKey, &a.interKey.PublicKey)
	roots := x509.NewCertPool()
	roots.AddCert(a.root)
	v, err := integrity.NewAppAttest(integrity.AppAttestConfig{AppID: AppID, Now: now, TestRoots: roots})
	if err != nil {
		t.Fatal(err)
	}
	return a, v
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (a *Apple) cert(tmpl, parent *x509.Certificate, signer *ecdsa.PrivateKey, pub *ecdsa.PublicKey) *x509.Certificate {
	a.t.Helper()
	a.serial++
	tmpl.SerialNumber = big.NewInt(a.serial)
	tmpl.NotBefore = a.now().Add(-48 * time.Hour)
	tmpl.NotAfter = a.now().Add(60 * 24 * time.Hour)
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		a.t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		a.t.Fatal(err)
	}
	return c
}

// Device is one App Attest key on a fake device.
type Device struct {
	apple   *Apple
	key     *ecdsa.PrivateKey
	KeyID   []byte
	counter uint32
}

// NewDevice makes a new key.
func (a *Apple) NewDevice() *Device {
	k := newKey(a.t)
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		a.t.Fatal(err)
	}
	id := sha256.Sum256(pub)
	return &Device{apple: a, key: k, KeyID: id[:]}
}

// Attest returns the key's attestation for binding (clientDataHash =
// SHA-256(binding)), as DCAppAttestService.attestKey would.
func (d *Device) Attest(binding string) []byte { return d.AttestWith(binding, AttestOptions{}) }

// AttestOptions bend an attestation for negative tests. The zero value is a
// well-formed attestation.
type AttestOptions struct {
	OnlyLeaf     bool   // x5c without the intermediate
	NoNonce      bool   // leaf without the nonce extension
	NoAttested   bool   // AT flag cleared
	Counter      uint32 // authenticator data counter
	AAGUID       string // default production ("appattest" + 7 NULs)
	CredentialID []byte // default the key ID
}

// AttestWith is Attest bent by o.
func (d *Device) AttestWith(binding string, o AttestOptions) []byte {
	a := d.apple
	a.t.Helper()
	aaguid := o.AAGUID
	if aaguid == "" {
		aaguid = "appattest\x00\x00\x00\x00\x00\x00\x00"
	}
	cred := o.CredentialID
	if cred == nil {
		cred = d.KeyID
	}
	flags := byte(0x40)
	if o.NoAttested {
		flags = 0
	}
	rp := sha256.Sum256([]byte(AppID))
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, flags)
	authData = binary.BigEndian.AppendUint32(authData, o.Counter)
	authData = append(authData, aaguid...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(cred))) //nolint:gosec // test IDs are short
	authData = append(authData, cred...)
	cdh := sha256.Sum256([]byte(binding))
	nonce := sha256.Sum256(append(append([]byte{}, authData...), cdh[:]...))
	ext, err := asn1.Marshal(struct {
		Nonce []byte `asn1:"tag:1,explicit"`
	}{nonce[:]})
	if err != nil {
		a.t.Fatal(err)
	}
	tmpl := &x509.Certificate{Subject: pkix.Name{CommonName: "fake device key"}, KeyUsage: x509.KeyUsageDigitalSignature}
	if !o.NoNonce {
		tmpl.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}, Value: ext}}
	}
	leaf := a.cert(tmpl, a.inter, a.interKey, &d.key.PublicKey)
	x5c := []any{leaf.Raw, a.inter.Raw}
	if o.OnlyLeaf {
		x5c = x5c[:1]
	}
	return cborMap(map[string]any{
		"fmt":      "apple-appattest",
		"attStmt":  map[string]any{"x5c": x5c, "receipt": []byte("fake receipt")},
		"authData": authData,
	})
}

// Assert returns the next assertion for binding; the counter grows by one.
func (d *Device) Assert(binding string) []byte {
	d.counter++
	return d.AssertWith(binding, d.counter)
}

// AssertWith returns an assertion with the given counter.
func (d *Device) AssertWith(binding string, counter uint32) []byte {
	a := d.apple
	a.t.Helper()
	rp := sha256.Sum256([]byte(AppID))
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, 0)
	authData = binary.BigEndian.AppendUint32(authData, counter)
	cdh := sha256.Sum256([]byte(binding))
	nonce := sha256.Sum256(append(append([]byte{}, authData...), cdh[:]...))
	digest := sha256.Sum256(nonce[:])
	sig, err := ecdsa.SignASN1(rand.Reader, d.key, digest[:])
	if err != nil {
		a.t.Fatal(err)
	}
	return cborMap(map[string]any{"signature": sig, "authenticatorData": authData})
}

// A minimal CBOR encoder for the values above.
func cborMap(m map[string]any) []byte {
	var b bytes.Buffer
	encode(&b, m)
	return b.Bytes()
}

func encode(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case string:
		head(b, 3, len(x))
		b.WriteString(x)
	case []byte:
		head(b, 2, len(x))
		b.Write(x)
	case []any:
		head(b, 4, len(x))
		for _, e := range x {
			encode(b, e)
		}
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		head(b, 5, len(keys))
		for _, k := range keys {
			encode(b, k)
			encode(b, x[k])
		}
	default:
		panic("integritytest: CBOR türü yok")
	}
}

func head(b *bytes.Buffer, major byte, n int) {
	switch {
	case n < 24:
		b.WriteByte(major<<5 | byte(n)) //nolint:gosec // n < 24
	case n < 1<<8:
		b.WriteByte(major<<5 | 24)
		b.WriteByte(byte(n)) //nolint:gosec // n < 256
	default:
		b.WriteByte(major<<5 | 25)
		_ = binary.Write(b, binary.BigEndian, uint16(n)) //nolint:gosec // test objects are small
	}
}
