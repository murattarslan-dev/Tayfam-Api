package integrity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func at(t *testing.T, s string) func() time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return func() time.Time { return tm }
}

func newAppAttest(t *testing.T, appID string, dev bool, now func() time.Time) *AppAttest {
	t.Helper()
	a, err := NewAppAttest(AppAttestConfig{AppID: appID, Development: dev, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// TestAppAttestAppleSample verifies Apple's published sample attestation
// and that each documented check refuses it when its input is wrong.
func TestAppAttestAppleSample(t *testing.T) {
	keyID, obj := b64(t, appleSampleKeyID), b64(t, appleSampleAttestation)
	cdh := []byte(appleSampleClientDataHash)
	a := newAppAttest(t, appleSampleAppID, false, at(t, appleSampleTime))

	r, key := a.verifyAttestation(keyID, obj, cdh)
	if r.Status != StatusPassed || key == nil {
		t.Fatalf("örnek: %+v", r)
	}
	if !bytes.Equal(key.KeyID, keyID) || len(key.PublicKey) != AppAttestPublicKeySize || key.Counter != 0 {
		t.Fatalf("anahtar: %+v", key)
	}
	if s := sha256.Sum256(key.PublicKey); !bytes.Equal(s[:], keyID) {
		t.Fatal("anahtar kimliği açık anahtarın özeti değil")
	}

	otherKey := bytes.Clone(keyID)
	otherKey[0] ^= 1
	truncated := obj[:len(obj)/2]
	for name, tc := range map[string]struct {
		a     *AppAttest
		keyID []byte
		obj   []byte
		cdh   []byte
		want  Reason
	}{
		"başka istek":        {a, keyID, obj, []byte("another_challenge"), ReasonBinding},
		"başka anahtar":      {a, otherKey, obj, cdh, ReasonBinding},
		"başka uygulama":     {newAppAttest(t, "1234567890.com.example.other", false, at(t, appleSampleTime)), keyID, obj, cdh, ReasonAppUnrecognized},
		"geliştirme ortamı":  {newAppAttest(t, appleSampleAppID, true, at(t, appleSampleTime)), keyID, obj, cdh, ReasonAppUnrecognized},
		"süresi geçmiş":      {newAppAttest(t, appleSampleAppID, false, at(t, "2026-05-01T00:00:00Z")), keyID, obj, cdh, ReasonAppUnrecognized},
		"kısa anahtar":       {a, keyID[:16], obj, cdh, ReasonMalformed},
		"kesik nesne":        {a, keyID, truncated, cdh, ReasonMalformed},
		"boş nesne":          {a, keyID, nil, cdh, ReasonMalformed},
		"CBOR olmayan nesne": {a, keyID, []byte("not cbor at all"), cdh, ReasonMalformed},
	} {
		r, key := tc.a.verifyAttestation(tc.keyID, tc.obj, tc.cdh)
		if r.Status != StatusFailed || r.Reason != tc.want || key != nil {
			t.Errorf("%s: %+v, anahtar %v; beklenen %s", name, r, key != nil, tc.want)
		}
	}
}

// TestAppAttestDevice verifies a real device's attestation and assertions:
// the counter must grow, a replay and another request's assertion fail.
func TestAppAttestDevice(t *testing.T) {
	keyID := b64(t, termakanKeyID)
	a := newAppAttest(t, termakanAppID, false, at(t, termakanTime))

	r, key := a.verifyAttestation(keyID, b64(t, termakanAttestation), sum(termakanChallenge))
	if r.Status != StatusPassed || key == nil {
		t.Fatalf("attestation: %+v", r)
	}

	as1, as2 := b64(t, termakanAssertion1), b64(t, termakanAssertion2)
	r, next := a.verifyAssertion(*key, keyID, as1, sum(termakanClientData1))
	if r.Status != StatusPassed || next.Counter != 1 {
		t.Fatalf("assertion 1: %+v %+v", r, next)
	}
	r, next2 := a.verifyAssertion(*next, keyID, as2, sum(termakanClientData2))
	if r.Status != StatusPassed || next2.Counter != 2 {
		t.Fatalf("assertion 2: %+v %+v", r, next2)
	}

	other := *key
	other.KeyID = bytes.Clone(keyID)
	other.KeyID[0] ^= 1
	for name, tc := range map[string]struct {
		a      *AppAttest
		stored AttestedKey
		keyID  []byte
		obj    []byte
		cdh    []byte
		want   Reason
	}{
		"tekrar":             {a, *next2, keyID, as2, sum(termakanClientData2), ReasonBinding},
		"eski sayaç":         {a, *next2, keyID, as1, sum(termakanClientData1), ReasonBinding},
		"başka istek":        {a, *key, keyID, as1, sum(termakanClientData2), ReasonBinding},
		"başka cihaz":        {a, other, keyID, as1, sum(termakanClientData1), ReasonBinding},
		"başka uygulama":     {newAppAttest(t, "4L4SS26L9J.com.example.other", false, at(t, termakanTime)), *key, keyID, as1, sum(termakanClientData1), ReasonAppUnrecognized},
		"bozuk açık anahtar": {a, AttestedKey{KeyID: keyID, PublicKey: []byte{4, 1, 2}}, keyID, as1, sum(termakanClientData1), ReasonMalformed},
		"kesik nesne":        {a, *key, keyID, as1[:len(as1)-3], sum(termakanClientData1), ReasonMalformed},
	} {
		r, k := tc.a.verifyAssertion(tc.stored, tc.keyID, tc.obj, tc.cdh)
		if r.Status != StatusFailed || r.Reason != tc.want || k != nil {
			t.Errorf("%s: %+v; beklenen %s", name, r, tc.want)
		}
	}

	// An assertion is not an attestation, and the other way round.
	if r, _ := a.verifyAttestation(keyID, as1, sum(termakanChallenge)); r.Reason != ReasonMalformed {
		t.Errorf("assertion attestation sayıldı: %+v", r)
	}
	if r, _ := a.verifyAssertion(*key, keyID, b64(t, termakanAttestation)[:MaxAppAssertion], sum(termakanClientData1)); r.Reason != ReasonMalformed {
		t.Errorf("attestation assertion sayıldı: %+v", r)
	}
}

// TestAppAttestBinding fixes how a binding becomes clientDataHash: SHA-256
// of its text. The iOS app computes the same.
func TestAppAttestBinding(t *testing.T) {
	b := SignInBinding("n")
	h := sha256.Sum256([]byte(b))
	if base64.StdEncoding.EncodeToString(h[:]) != base64.StdEncoding.EncodeToString(sum("RQikIoUgcNuXXdqIefpw7eiSKtDfUXbWostlt13jJbM")) {
		t.Fatal("bağlama değişti")
	}
	a := newAppAttest(t, termakanAppID, false, at(t, termakanTime))
	// The device signed SHA-256(challenge), not a binding: refused as
	// another request's proof.
	if r, _ := a.VerifyAttestation(b64(t, termakanKeyID), b64(t, termakanAttestation), b); r.Reason != ReasonBinding {
		t.Fatalf("%+v", r)
	}
	if r, _ := a.VerifyAttestation(b64(t, termakanKeyID), b64(t, termakanAttestation), termakanChallenge); r.Status != StatusPassed {
		t.Fatalf("%+v", r)
	}
}

func TestNewAppAttest(t *testing.T) {
	for _, bad := range []string{"", "com.example.app", "ABCDE.com.example.app", "abcdefghij.com.example.app", "ABCDEFGHIJ.app", "ABCDEFGHIJ.com.example app"} {
		if _, err := NewAppAttest(AppAttestConfig{AppID: bad}); err == nil {
			t.Errorf("%q kabul edildi", bad)
		}
	}
	if _, err := NewAppAttest(AppAttestConfig{AppID: "ABCDEFGHIJ.com.example.app"}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeCBOR(t *testing.T) {
	ok := map[string][]byte{
		"uint":           {0x18, 0x64},
		"negint":         {0x38, 0x63},
		"bytes":          {0x43, 1, 2, 3},
		"text":           {0x62, 'h', 'i'},
		"array":          {0x82, 0x01, 0xf5},
		"map":            {0xa1, 0x61, 'a', 0xf6},
		"64-bit argüman": {0x1b, 0, 0, 0, 0, 0, 0, 0, 1},
	}
	for name, in := range ok {
		if _, err := decodeCBOR(in); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	deep := bytes.Repeat([]byte{0x81}, maxCBORDepth+2)
	deep = append(deep, 0x00)
	bad := map[string][]byte{
		"boş":                   {},
		"fazla bayt":            {0x01, 0x02},
		"belirsiz uzunluk":      {0x5f, 0x41, 0x00, 0xff},
		"etiket":                {0xc0, 0x60},
		"float":                 {0xf9, 0x3c, 0x00},
		"metin olmayan anahtar": {0xa1, 0x01, 0x02},
		"yinelenen anahtar":     {0xa2, 0x61, 'a', 0x01, 0x61, 'a', 0x02},
		"kısa bayt dizisi":      {0x45, 1, 2},
		"dev uzunluk":           {0x5b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"çok eleman":            {0x99, 0xff, 0xff},
		"çok derin":             deep,
		"ayrılmış argüman":      {0x1c},
	}
	for name, in := range bad {
		if _, err := decodeCBOR(in); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
}
