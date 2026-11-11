// Package objstoretest is an in-memory S3-compatible server for tests
// (PUT, HEAD, ranged GET with If-Match, conditional copy, DELETE). It
// checks every request's SigV4 signature (objstore.VerifyRequest) as R2
// would, including the signed content type and length of presigned
// uploads, so tests prove that a link cannot be used for another key, type
// or size.
package objstoretest

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/objstore"
)

// Test credentials (not secrets; AWS's documented example key format).
const (
	Bucket          = "tayfam-test-private"
	Region          = "auto"
	AccessKeyID     = "AKIDTESTONLY0000"
	SecretAccessKey = "test-secret-key-0000000000000000000000"
)

// Object is a stored object.
type Object struct {
	Body        []byte
	ContentType string
}

// Server is the fake storage.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string]Object
	// Now is the server's clock for link expiry; nil means time.Now.
	Now func() time.Time
	// Fail, when set, answers every request with this status (outage).
	fail int
	// Rejected counts refused signatures.
	rejected int
}

// New starts a server that stops with the test.
func New(t testing.TB) *Server {
	s := &Server{objects: map[string]Object{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Config returns a client configuration for this server.
func (s *Server) Config() objstore.Config {
	u, _ := url.Parse(s.URL)
	return objstore.Config{Endpoint: u, Bucket: Bucket, Region: Region, AccessKeyID: AccessKeyID, SecretAccessKey: SecretAccessKey}
}

// Client returns a client for this server.
func (s *Server) Client(t testing.TB) *objstore.Client {
	t.Helper()
	c, err := objstore.New(s.Config())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// SetFail makes every request answer status (0 turns it off).
func (s *Server) SetFail(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = status
}

// Rejected returns how many requests failed signature checks.
func (s *Server) Rejected() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejected
}

// Put stores an object directly (as if a client had uploaded it).
func (s *Server) Put(key string, o Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = o
}

// Get returns a stored object.
func (s *Server) Get(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o, ok
}

// Keys returns the stored keys.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	return out
}

// Do sends a presigned request the way a mobile client would: the URL as
// given, the given headers and body.
func Do(t testing.TB, method, rawURL string, headers map[string]string, body []byte) int {
	t.Helper()
	r, err := http.NewRequestWithContext(context.Background(), method, rawURL, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Content-Length") {
			n, _ := strconv.ParseInt(v, 10, 64)
			r.ContentLength = n
			continue
		}
		r.Header.Set(k, v)
	}
	if len(body) > 0 && r.ContentLength != int64(len(body)) {
		// A client lying about the length: the server sees the real one.
		r.ContentLength = int64(len(body))
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// ETag is the ETag the server gives b.
func ETag(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // an ETag, as S3 computes it; not security
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != 0 {
		w.WriteHeader(s.fail)
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if err := objstore.VerifyRequest(r, body, AccessKeyID, SecretAccessKey, Region, now); err != nil {
		s.rejected++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, "/"+Bucket+"/")
	if !ok || key == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if src := r.Header.Get("X-Amz-Copy-Source"); src != "" {
			skey, ok := strings.CutPrefix(src, "/"+Bucket+"/")
			o, found := s.objects[skey]
			switch {
			case !ok || !found:
				w.WriteHeader(http.StatusNotFound)
			case r.Header.Get("X-Amz-Copy-Source-If-Match") != ETag(o.Body):
				w.WriteHeader(http.StatusPreconditionFailed)
			default:
				s.objects[key] = Object{Body: append([]byte(nil), o.Body...), ContentType: o.ContentType}
				_, _ = io.WriteString(w, "<CopyObjectResult><ETag>"+ETag(o.Body)+"</ETag></CopyObjectResult>")
			}
			return
		}
		s.objects[key] = Object{Body: body, ContentType: r.Header.Get("Content-Type")}
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		o, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if m := r.Header.Get("If-Match"); m != "" && m != ETag(o.Body) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		out, code := o.Body, http.StatusOK
		if rg := r.Header.Get("Range"); rg != "" {
			var start, end int
			if _, err := fmt.Sscanf(rg, "bytes=%d-%d", &start, &end); err != nil || start != 0 {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			if end+1 < len(out) {
				out = out[:end+1]
			}
			code = http.StatusPartialContent
		}
		w.Header().Set("Content-Type", o.ContentType)
		w.Header().Set("ETag", ETag(o.Body))
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(o.Body)))
		}
		w.WriteHeader(code)
		if r.Method == http.MethodGet {
			_, _ = w.Write(out)
		}
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
