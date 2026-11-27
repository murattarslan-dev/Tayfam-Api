package integrity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// App Attest (ADR-0022 §4), verified offline with the standard library:
// the certificate chain with crypto/x509, the key with crypto/ecdsa. The
// receipt (Apple's fraud metric) is not used and not kept.
//
// The client data of a proof is its binding (SignInBinding, RefreshBinding):
// the app passes clientDataHash = SHA-256(binding), the binding being the
// 43-character base64url text as UTF-8 bytes.

// appAttestRootPEM is Apple App Attestation Root CA
// (https://www.apple.com/certificateauthority/Apple_App_Attestation_Root_CA.pem),
// valid 2020-03-18 – 2045-03-15, SHA-256 fingerprint
// 1C:B9:82:3B:A2:8B:A6:AD:2D:33:A0:06:94:1D:E2:AE:4F:51:3E:F1:D4:E8:31:B9:F7:E0:FA:7B:62:42:C9:32.
// A certificate is public; when Apple replaces it, this changes with the
// code (ADR-0022 §4).
const appAttestRootPEM = `-----BEGIN CERTIFICATE-----
MIICITCCAaegAwIBAgIQC/O+DvHN0uD7jG5yH2IXmDAKBggqhkjOPQQDAzBSMSYw
JAYDVQQDDB1BcHBsZSBBcHAgQXR0ZXN0YXRpb24gUm9vdCBDQTETMBEGA1UECgwK
QXBwbGUgSW5jLjETMBEGA1UECAwKQ2FsaWZvcm5pYTAeFw0yMDAzMTgxODMyNTNa
Fw00NTAzMTUwMDAwMDBaMFIxJjAkBgNVBAMMHUFwcGxlIEFwcCBBdHRlc3RhdGlv
biBSb290IENBMRMwEQYDVQQKDApBcHBsZSBJbmMuMRMwEQYDVQQIDApDYWxpZm9y
bmlhMHYwEAYHKoZIzj0CAQYFK4EEACIDYgAERTHhmLW07ATaFQIEVwTtT4dyctdh
NbJhFs/Ii2FdCgAHGbpphY3+d8qjuDngIN3WVhQUBHAoMeQ/cLiP1sOUtgjqK9au
Yen1mMEvRq9Sk3Jm5X8U62H+xTD3FE9TgS41o0IwQDAPBgNVHRMBAf8EBTADAQH/
MB0GA1UdDgQWBBSskRBTM72+aEH/pwyp5frq5eWKoTAOBgNVHQ8BAf8EBAMCAQYw
CgYIKoZIzj0EAwMDaAAwZQIwQgFGnByvsiVbpTKwSga0kP0e8EeDS4+sQmTvb7vn
53O5+FRXgeLhpJ06ysC5PrOyAjEAp5U4xDgEgllF7En3VcE3iexZZtKeYnpqtijV
oyFraWVIyd/dganmrduC1bmTBGwD
-----END CERTIFICATE-----`

const (
	// MaxAppAttestation bounds an attestation object before any work; real
	// ones are about 5–6 KB, most of it the receipt.
	MaxAppAttestation = 16 << 10
	// MaxAppAssertion bounds an assertion object; real ones are under
	// 250 bytes.
	MaxAppAssertion = 1 << 10
	// AppAttestKeyIDSize is the length of a key identifier: SHA-256 of the
	// public key.
	AppAttestKeyIDSize = sha256.Size
	// AppAttestPublicKeySize is a P-256 public key, uncompressed (X9.62).
	AppAttestPublicKeySize = 65

	authDataHeader = 32 + 1 + 4 // rpIdHash, flags, counter
	flagAttested   = 0x40       // AT: attested credential data follows
)

// AAGUIDs of App Attest keys: production and development environment.
const (
	aaguidProduction  = "appattest\x00\x00\x00\x00\x00\x00\x00"
	aaguidDevelopment = "appattestdevelop"
)

var appIDPattern = regexp.MustCompile(`^[A-Z0-9]{10}\.[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

// oidAppAttestNonce is the leaf certificate's nonce extension.
func oidAppAttestNonce() asn1.ObjectIdentifier {
	return asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}
}

// AppAttestProof is what the iOS app sends: the key identifier and either an
// attestation (sign-in) or an assertion (refresh).
type AppAttestProof struct {
	KeyID       []byte
	Attestation []byte
	Assertion   []byte
}

// AttestedKey is an App Attest key bound to a device (devices.app_attest_*).
// A check that reads or advances a key returns it in Result.Key.
type AttestedKey struct {
	KeyID     []byte
	PublicKey []byte // uncompressed P-256 point
	Counter   int64  // 0 to 2³²−1
}

// AppAttestConfig wires an App Attest verifier.
type AppAttestConfig struct {
	// AppID is TEAMID.BundleID; its SHA-256 is the RP ID.
	AppID string
	// Development accepts development keys (aaguid appattestdevelop) instead
	// of production ones. The app's entitlement decides which it makes;
	// TestFlight and App Store builds make production keys.
	Development bool
	Now         func() time.Time
	// TestRoots replaces Apple's root, for tests that make their own
	// attestations (integritytest). cmd/identity never sets it.
	TestRoots *x509.CertPool
}

// AppAttest verifies App Attest attestations and assertions.
type AppAttest struct {
	rpIDHash [32]byte
	aaguid   string
	roots    *x509.CertPool
	now      func() time.Time
}

// NewAppAttest returns an App Attest verifier.
func NewAppAttest(cfg AppAttestConfig) (*AppAttest, error) {
	if !appIDPattern.MatchString(cfg.AppID) || len(cfg.AppID) > 255 {
		return nil, errors.New("integrity: geçersiz App ID (TEAMID.bundle bekleniyor)")
	}
	block, _ := pem.Decode([]byte(appAttestRootPEM))
	if block == nil {
		return nil, errors.New("integrity: App Attest kök sertifikası okunamadı")
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("integrity: App Attest kök sertifikası: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if cfg.TestRoots != nil {
		roots = cfg.TestRoots
	}
	a := &AppAttest{rpIDHash: sha256.Sum256([]byte(cfg.AppID)), aaguid: aaguidProduction, roots: roots, now: cfg.Now}
	if cfg.Development {
		a.aaguid = aaguidDevelopment
	}
	if a.now == nil {
		a.now = time.Now
	}
	return a, nil
}

// failed is a check that did not pass, for reason.
func failed(reason Reason) Result { return Result{Status: StatusFailed, Reason: reason} }

// VerifyAttestation checks a new key's attestation against binding, in the
// order Apple documents ("Validating apps that connect to your server").
// On a pass it returns the key to bind to the device, counter 0.
func (a *AppAttest) VerifyAttestation(keyID, attestation []byte, binding string) (Result, *AttestedKey) {
	cdh := sha256.Sum256([]byte(binding))
	return a.verifyAttestation(keyID, attestation, cdh[:])
}

func (a *AppAttest) verifyAttestation(keyID, attestation, clientDataHash []byte) (Result, *AttestedKey) {
	if len(keyID) != AppAttestKeyIDSize || len(attestation) == 0 || len(attestation) > MaxAppAttestation {
		return failed(ReasonMalformed), nil
	}
	v, err := decodeCBOR(attestation)
	obj, ok := v.(cborMap)
	if err != nil || !ok || obj["fmt"] != "apple-appattest" {
		return failed(ReasonMalformed), nil
	}
	stmt, ok := obj["attStmt"].(cborMap)
	authData, okA := obj.bytesOf("authData")
	if !ok || !okA {
		return failed(ReasonMalformed), nil
	}
	x5c, ok := stmt["x5c"].([]any)
	if !ok || len(x5c) < 2 {
		return failed(ReasonMalformed), nil
	}
	certs := make([]*x509.Certificate, 0, len(x5c))
	for _, c := range x5c {
		der, ok := c.([]byte)
		if !ok {
			return failed(ReasonMalformed), nil
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return failed(ReasonMalformed), nil
		}
		certs = append(certs, cert)
	}

	// 1. The chain ends at Apple's App Attest root, valid now.
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	leaf := certs[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: a.roots, Intermediates: inter, CurrentTime: a.now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return failed(ReasonAppUnrecognized), nil
	}

	// 2–4. nonce = SHA-256(authData ‖ clientDataHash) is the leaf's nonce
	// extension: the attestation is for this request.
	nonce := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash...))
	got, ok := leafNonce(leaf)
	if !ok {
		return failed(ReasonMalformed), nil
	}
	if !bytes.Equal(got, nonce[:]) {
		return failed(ReasonBinding), nil
	}

	// 5. The key identifier is SHA-256 of the leaf's public key.
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return failed(ReasonMalformed), nil
	}
	point, err := pub.ECDH()
	if err != nil {
		return failed(ReasonMalformed), nil
	}
	raw := point.Bytes()
	if sum := sha256.Sum256(raw); !bytes.Equal(sum[:], keyID) {
		return failed(ReasonBinding), nil
	}

	// 6–9. Authenticator data: our app, counter 0, the expected environment,
	// the credential is this key.
	ad, ok := parseAuthData(authData, true)
	if !ok {
		return failed(ReasonMalformed), nil
	}
	if ad.rpIDHash != a.rpIDHash || string(ad.aaguid) != a.aaguid {
		return failed(ReasonAppUnrecognized), nil
	}
	if ad.counter != 0 {
		return failed(ReasonMalformed), nil
	}
	if !bytes.Equal(ad.credentialID, keyID) {
		return failed(ReasonBinding), nil
	}
	return Result{Status: StatusPassed}, &AttestedKey{KeyID: bytes.Clone(keyID), PublicKey: raw, Counter: 0}
}

// VerifyAssertion checks an assertion by the device's key against binding.
// On a pass it returns the key with the assertion's counter, which the
// caller stores only if it is still above the stored one.
func (a *AppAttest) VerifyAssertion(stored AttestedKey, keyID, assertion []byte, binding string) (Result, *AttestedKey) {
	cdh := sha256.Sum256([]byte(binding))
	return a.verifyAssertion(stored, keyID, assertion, cdh[:])
}

func (a *AppAttest) verifyAssertion(stored AttestedKey, keyID, assertion, clientDataHash []byte) (Result, *AttestedKey) {
	if len(keyID) != AppAttestKeyIDSize || len(assertion) == 0 || len(assertion) > MaxAppAssertion {
		return failed(ReasonMalformed), nil
	}
	if !bytes.Equal(keyID, stored.KeyID) {
		// Not the key this device attested.
		return failed(ReasonBinding), nil
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), stored.PublicKey)
	if err != nil {
		return failed(ReasonMalformed), nil
	}
	v, err := decodeCBOR(assertion)
	obj, ok := v.(cborMap)
	if err != nil || !ok {
		return failed(ReasonMalformed), nil
	}
	sig, okS := obj.bytesOf("signature")
	authData, okA := obj.bytesOf("authenticatorData")
	if !okS || !okA {
		return failed(ReasonMalformed), nil
	}
	ad, ok := parseAuthData(authData, false)
	if !ok {
		return failed(ReasonMalformed), nil
	}
	// The key signs nonce = SHA-256(authenticatorData ‖ clientDataHash) with
	// ECDSA P-256/SHA-256, so the digest it signs is SHA-256(nonce).
	nonce := sha256.Sum256(append(append([]byte{}, authData...), clientDataHash...))
	digest := sha256.Sum256(nonce[:])
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return failed(ReasonBinding), nil
	}
	if ad.rpIDHash != a.rpIDHash {
		return failed(ReasonAppUnrecognized), nil
	}
	if int64(ad.counter) <= stored.Counter {
		// Replayed or out of order.
		return failed(ReasonBinding), nil
	}
	return Result{Status: StatusPassed}, &AttestedKey{KeyID: bytes.Clone(stored.KeyID), PublicKey: bytes.Clone(stored.PublicKey), Counter: int64(ad.counter)}
}

type authData struct {
	rpIDHash     [32]byte
	counter      uint32
	aaguid       []byte
	credentialID []byte
}

// parseAuthData reads the fixed header and, when attested, the AAGUID and
// credential ID. What follows (the COSE key, extensions) is not needed: the
// key comes from the certificate, and an assertion is signed over the bytes
// as they are.
func parseAuthData(b []byte, attested bool) (authData, bool) {
	var ad authData
	if len(b) < authDataHeader {
		return ad, false
	}
	copy(ad.rpIDHash[:], b[:32])
	flags := b[32]
	ad.counter = binary.BigEndian.Uint32(b[33:37])
	if !attested {
		return ad, true
	}
	rest := b[authDataHeader:]
	if flags&flagAttested == 0 || len(rest) < 16+2 {
		return ad, false
	}
	ad.aaguid = rest[:16]
	n := int(binary.BigEndian.Uint16(rest[16:18]))
	if len(rest) < 18+n {
		return ad, false
	}
	ad.credentialID = rest[18 : 18+n]
	return ad, true
}

// leafNonce reads the nonce extension: SEQUENCE { [1] EXPLICIT OCTET STRING }.
func leafNonce(c *x509.Certificate) ([]byte, bool) {
	for _, e := range c.Extensions {
		if !e.Id.Equal(oidAppAttestNonce()) {
			continue
		}
		var ext struct {
			Nonce []byte `asn1:"tag:1,explicit"`
		}
		rest, err := asn1.Unmarshal(e.Value, &ext)
		if err != nil || len(rest) != 0 || len(ext.Nonce) != sha256.Size {
			return nil, false
		}
		return ext.Nonce, true
	}
	return nil, false
}
