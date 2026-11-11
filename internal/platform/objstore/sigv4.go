package objstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWS Signature Version 4 (https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html).
// The primitive is HMAC-SHA256 from the standard library; this file only
// builds the canonical text that is signed (ADR-0020 item 5).

const (
	algorithm       = "AWS4-HMAC-SHA256"
	service         = "s3"
	amzDateFormat   = "20060102T150405Z"
	dateFormat      = "20060102"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptyPayload is the SHA-256 of an empty body.
	emptyPayload = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// signer holds the credentials. It is never logged or formatted.
type signer struct {
	accessKeyID string
	secret      string
	region      string
}

// String keeps the secret out of any accidental formatting.
func (s signer) String() string { return "objstore.signer" }

// GoString is String for %#v.
func (s signer) GoString() string { return s.String() }

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// scope is "<date>/<region>/s3/aws4_request".
func (s signer) scope(t time.Time) string {
	return t.UTC().Format(dateFormat) + "/" + s.region + "/" + service + "/aws4_request"
}

// sign returns the hex signature of canonicalRequest made at t.
func (s signer) sign(t time.Time, canonicalRequest string) string {
	t = t.UTC()
	toSign := algorithm + "\n" + t.Format(amzDateFormat) + "\n" + s.scope(t) + "\n" + sha256Hex([]byte(canonicalRequest))
	k := hmacSHA256([]byte("AWS4"+s.secret), t.Format(dateFormat))
	k = hmacSHA256(k, s.region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	return hex.EncodeToString(hmacSHA256(k, toSign))
}

// uriEncode is SigV4's URI encoding: every byte except the unreserved
// characters is percent-encoded in upper case; '/' is kept when path is
// true.
func uriEncode(s string, path bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && path:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// canonicalQuery sorts and encodes the query parameters (one value each).
func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	return strings.Join(parts, "&")
}

// header is one signed header; name is lower case.
type header struct{ name, value string }

// canonicalHeaders returns the canonical header block and the signed
// header list. Values are trimmed and inner runs of spaces collapsed.
func canonicalHeaders(hs []header) (block, signed string) {
	sorted := append([]header(nil), hs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var b strings.Builder
	names := make([]string, 0, len(sorted))
	for _, h := range sorted {
		b.WriteString(h.name)
		b.WriteByte(':')
		b.WriteString(strings.Join(strings.Fields(h.value), " "))
		b.WriteByte('\n')
		names = append(names, h.name)
	}
	return b.String(), strings.Join(names, ";")
}

// canonicalRequest builds the text whose hash is signed.
func canonicalRequest(method, escapedPath, query string, hs []header, payloadHash string) (cr, signed string) {
	block, signed := canonicalHeaders(hs)
	return method + "\n" + escapedPath + "\n" + query + "\n" + block + "\n" + signed + "\n" + payloadHash, signed
}

// presign returns the signed query of a presigned request (X-Amz-*
// parameters plus X-Amz-Signature). hs must include host.
func (s signer) presign(method, escapedPath string, hs []header, t time.Time, ttl time.Duration) url.Values {
	t = t.UTC()
	_, signed := canonicalHeaders(hs)
	q := url.Values{}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", s.accessKeyID+"/"+s.scope(t))
	q.Set("X-Amz-Date", t.Format(amzDateFormat))
	q.Set("X-Amz-Expires", strconv.Itoa(int(ttl/time.Second)))
	q.Set("X-Amz-SignedHeaders", signed)
	cr, _ := canonicalRequest(method, escapedPath, canonicalQuery(q), hs, unsignedPayload)
	q.Set("X-Amz-Signature", s.sign(t, cr))
	return q
}

// authorize sets the SigV4 Authorization header on r (header-based
// signing). hs are the signed headers, host included; payloadHash is the
// body's SHA-256 in hex.
func (s signer) authorize(r *http.Request, hs []header, payloadHash string, t time.Time) {
	t = t.UTC()
	cr, signed := canonicalRequest(r.Method, r.URL.EscapedPath(), canonicalQuery(r.URL.Query()), hs, payloadHash)
	r.Header.Set("Authorization", algorithm+" Credential="+s.accessKeyID+"/"+s.scope(t)+
		", SignedHeaders="+signed+", Signature="+s.sign(t, cr))
}

// Verification errors of VerifyRequest. They carry no request values.
var (
	ErrBadSignature = errors.New("objstore: imza geçersiz")
	ErrExpired      = errors.New("objstore: imzalı linkin süresi dolmuş")
)

// VerifyRequest checks the SigV4 signature of a request as an S3-compatible
// server would: presigned (X-Amz-Signature in the query) or header-based
// (Authorization). Every signed header must match the request as sent,
// content-type and content-length included, and a presigned link must not
// be expired at now. body is the request body already read (for the
// payload hash of header-based requests). It exists for test servers
// (objstoretest); production code never verifies.
func VerifyRequest(r *http.Request, body []byte, accessKeyID, secret, region string, now time.Time) error {
	s := signer{accessKeyID: accessKeyID, secret: secret, region: region}
	get := func(name string) string {
		switch name {
		case "host":
			return r.Host
		case "content-length":
			if r.ContentLength < 0 {
				return r.Header.Get("Content-Length")
			}
			return strconv.FormatInt(r.ContentLength, 10)
		}
		return r.Header.Get(name)
	}
	q := r.URL.Query()
	if sig := q.Get("X-Amz-Signature"); sig != "" {
		t, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
		if err != nil {
			return ErrBadSignature
		}
		exp, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || exp <= 0 || exp > 7*24*3600 {
			return ErrBadSignature
		}
		if q.Get("X-Amz-Credential") != accessKeyID+"/"+s.scope(t) {
			return ErrBadSignature
		}
		var hs []header
		for _, n := range strings.Split(q.Get("X-Amz-SignedHeaders"), ";") {
			hs = append(hs, header{n, get(n)})
		}
		q.Del("X-Amz-Signature")
		cr, _ := canonicalRequest(r.Method, r.URL.EscapedPath(), canonicalQuery(q), hs, unsignedPayload)
		if !hmac.Equal([]byte(s.sign(t, cr)), []byte(sig)) {
			return ErrBadSignature
		}
		if now.After(t.Add(time.Duration(exp) * time.Second)) {
			return ErrExpired
		}
		return nil
	}
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, algorithm+" ")
	if !ok {
		return ErrBadSignature
	}
	fields := map[string]string{}
	for _, f := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(f), "=")
		fields[k] = v
	}
	t, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil || fields["Credential"] != accessKeyID+"/"+s.scope(t) {
		return ErrBadSignature
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload != unsignedPayload && payload != sha256Hex(body) {
		return ErrBadSignature
	}
	var hs []header
	for _, n := range strings.Split(fields["SignedHeaders"], ";") {
		hs = append(hs, header{n, get(n)})
	}
	cr, _ := canonicalRequest(r.Method, r.URL.EscapedPath(), canonicalQuery(r.URL.Query()), hs, payload)
	if !hmac.Equal([]byte(s.sign(t, cr)), []byte(fields["Signature"])) {
		return ErrBadSignature
	}
	if d := now.Sub(t); d > 15*time.Minute || d < -15*time.Minute {
		return ErrExpired
	}
	return nil
}
