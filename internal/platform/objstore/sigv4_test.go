package objstore

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

// AWS's documented Signature Version 4 examples for S3
// (docs.aws.amazon.com/AmazonS3/latest/API/sig-v4-header-based-auth.html and
// sigv4-query-string-auth.html). They pin the canonical request, the
// string to sign and the key derivation against an independent source.
var awsExample = signer{
	accessKeyID: "AKIAIOSFODNN7EXAMPLE",
	secret:      "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	region:      "us-east-1",
}

var awsExampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestSigV4PresignedExample(t *testing.T) {
	q := awsExample.presign(http.MethodGet, "/test.txt", []header{{"host", "examplebucket.s3.amazonaws.com"}}, awsExampleTime, 24*time.Hour)
	const want = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got := q.Get("X-Amz-Signature"); got != want {
		t.Fatalf("imza = %s, AWS örneği %s", got, want)
	}
	if q.Get("X-Amz-Credential") != "AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request" || q.Get("X-Amz-Expires") != "86400" {
		t.Fatalf("parametreler: %v", q)
	}
}

func TestSigV4HeaderExample(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	awsExample.authorize(r, []header{
		{"host", "examplebucket.s3.amazonaws.com"},
		{"range", "bytes=0-9"},
		{"x-amz-content-sha256", emptyPayload},
		{"x-amz-date", "20130524T000000Z"},
	}, emptyPayload, awsExampleTime)
	const want = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := r.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization =\n%s\nAWS örneği\n%s", got, want)
	}
}

func TestURIEncode(t *testing.T) {
	for in, want := range map[string]string{
		"a b/c~d": "a%20b%2Fc~d",
		"x=y&z":   "x%3Dy%26z",
		"ç":       "%C3%A7",
	} {
		if got := uriEncode(in, false); got != want {
			t.Errorf("%q: %s, %s bekleniyordu", in, got, want)
		}
	}
	if got := uriEncode("media/a b", true); got != "media/a%20b" {
		t.Errorf("yol: %s", got)
	}
	if got := canonicalQuery(url.Values{"b": {"2"}, "a": {"1 2"}}); got != "a=1%202&b=2" {
		t.Errorf("sorgu: %s", got)
	}
}

func TestSignerNeverFormatsSecret(t *testing.T) {
	for _, s := range []string{awsExample.String(), awsExample.GoString()} {
		if s != "objstore.signer" {
			t.Fatalf("biçim: %s", s)
		}
	}
}
