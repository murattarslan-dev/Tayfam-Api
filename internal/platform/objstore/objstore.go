// Package objstore is a small S3-compatible object storage client for
// Cloudflare R2 (ADR-0007, ADR-0020 item 5): presigned PUT and GET links,
// and HEAD, GET, PUT and DELETE of single objects, signed with AWS
// Signature Version 4.
//
// Only the standard library is used: the signature is HMAC-SHA256
// (crypto/hmac, crypto/sha256); this package writes the canonical request
// text only. An SDK would bring dozens of modules for these requests.
//
// Nothing here logs. Errors carry the operation and the HTTP status only:
// never the object key, a URL, a signature or the credentials. Presigned
// links are bearer secrets for their lifetime; callers hand them to the
// client and never log them.
package objstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Errors. StatusError carries the HTTP status of any other refusal.
var (
	ErrNotFound = errors.New("objstore: nesne yok")
	ErrTooLarge = errors.New("objstore: nesne sınırdan büyük")
)

// StatusError is an unexpected HTTP status from the storage. The response
// body is not kept.
type StatusError struct {
	Op   string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("objstore: %s: HTTP %d", e.Op, e.Code) }

// Config configures a Client.
type Config struct {
	// Endpoint is the storage's base URL (scheme and host only), for R2
	// https://<account>.r2.cloudflarestorage.com. Requests are path-style:
	// <endpoint>/<bucket>/<key>.
	Endpoint *url.URL
	Bucket   string
	// Region is "auto" for R2.
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	// HTTP is the client for HEAD, GET, copy and DELETE; nil means one with
	// a 30-second timeout. Redirects are never followed either way.
	HTTP *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Client talks to one bucket.
type Client struct {
	base   *url.URL
	bucket string
	sig    signer
	http   *http.Client
	now    func() time.Time
}

// New checks c and returns a Client.
func New(c Config) (*Client, error) {
	if c.Endpoint == nil || (c.Endpoint.Scheme != "https" && c.Endpoint.Scheme != "http") || c.Endpoint.Host == "" ||
		(c.Endpoint.Path != "" && c.Endpoint.Path != "/") || c.Endpoint.RawQuery != "" || c.Endpoint.User != nil {
		return nil, errors.New("objstore: uç adresi geçersiz")
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/?#") {
		return nil, errors.New("objstore: bucket geçersiz")
	}
	if c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, errors.New("objstore: bölge ve erişim anahtarı gerekli")
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		hc = &cp
	}
	// A storage that redirects is not followed: the credentials and the
	// object never go to another address.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := c.Now
	if now == nil {
		now = time.Now
	}
	base := &url.URL{Scheme: c.Endpoint.Scheme, Host: c.Endpoint.Host}
	return &Client{
		base: base, bucket: c.Bucket, http: hc, now: now,
		sig: signer{accessKeyID: c.AccessKeyID, secret: c.SecretAccessKey, region: c.Region},
	}, nil
}

// validKey keeps keys to the characters this service generates, so a key
// can never escape its bucket or prefix.
func validKey(key string) bool {
	if key == "" || len(key) > 512 || strings.HasPrefix(key, "/") || strings.Contains(key, "..") {
		return false
	}
	for _, c := range key {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '/', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func (c *Client) objectURL(key string) *url.URL {
	u := *c.base
	u.Path = "/" + c.bucket + "/" + key
	u.RawPath = "/" + uriEncode(c.bucket, false) + "/" + uriEncode(key, true)
	return &u
}

// Presigned is a link the client uses directly.
type Presigned struct {
	URL string
	// Headers the request must carry exactly (they are signed).
	Headers map[string]string
	Expires time.Time
}

// PresignPut returns a PUT link valid for ttl for key only, with this
// content type and exactly size bytes: the storage refuses another key,
// type or length.
func (c *Client) PresignPut(key, contentType string, size int64, ttl time.Duration) (Presigned, error) {
	if !validKey(key) || contentType == "" || size <= 0 || ttl <= 0 {
		return Presigned{}, errors.New("objstore: geçersiz yükleme linki isteği")
	}
	u := c.objectURL(key)
	length := strconv.FormatInt(size, 10)
	now := c.now()
	q := c.sig.presign(http.MethodPut, u.EscapedPath(), []header{
		{"content-length", length}, {"content-type", contentType}, {"host", u.Host},
	}, now, ttl)
	u.RawQuery = canonicalQuery(q)
	return Presigned{
		URL:     u.String(),
		Headers: map[string]string{"Content-Type": contentType, "Content-Length": length},
		Expires: now.Add(ttl),
	}, nil
}

// PresignGet returns a GET link for key valid for ttl.
func (c *Client) PresignGet(key string, ttl time.Duration) (Presigned, error) {
	if !validKey(key) || ttl <= 0 {
		return Presigned{}, errors.New("objstore: geçersiz indirme linki isteği")
	}
	u := c.objectURL(key)
	now := c.now()
	q := c.sig.presign(http.MethodGet, u.EscapedPath(), []header{{"host", u.Host}}, now, ttl)
	u.RawQuery = canonicalQuery(q)
	return Presigned{URL: u.String(), Expires: now.Add(ttl)}, nil
}

// do signs and sends a request without a body. hs are extra headers
// (lower-case names), all signed. The transport error never reaches the
// message: it names the address and the URL with the key.
func (c *Client) do(ctx context.Context, op, method, key string, hs ...header) (*http.Response, error) {
	if !validKey(key) {
		return nil, fmt.Errorf("objstore: %s: geçersiz anahtar", op)
	}
	u := c.objectURL(key)
	r, err := http.NewRequestWithContext(ctx, method, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("objstore: %s: istek kurulamadı", op)
	}
	now := c.now().UTC()
	signed := []header{{"host", u.Host}, {"x-amz-content-sha256", emptyPayload}, {"x-amz-date", now.Format(amzDateFormat)}}
	r.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	r.Header.Set("X-Amz-Content-Sha256", emptyPayload)
	for _, h := range hs {
		r.Header.Set(h.name, h.value)
		signed = append(signed, h)
	}
	c.sig.authorize(r, signed, emptyPayload, now)
	resp, err := c.http.Do(r)
	if err != nil {
		return nil, fmt.Errorf("objstore: %s: %w", op, transportError(err))
	}
	return resp, nil
}

// ErrTransport is a network failure reaching the storage. The underlying
// error is dropped: it names the address and the URL with the key.
var ErrTransport = errors.New("objstore: depoya ulaşılamadı")

func transportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Errorf("%w (zaman aşımı)", ErrTransport)
	}
	return ErrTransport
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// Object is what HEAD says about an object.
type Object struct {
	Size int64
	// ETag identifies this content; GetRange and Copy are bound to it.
	ETag string
}

// ErrChanged means the object no longer has the ETag the request was bound
// to: it was replaced in between.
var ErrChanged = errors.New("objstore: nesne değişti")

// Head returns the size and ETag of key, or ErrNotFound.
func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	resp, err := c.do(ctx, "head", http.MethodHead, key)
	if err != nil {
		return Object{}, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		etag := resp.Header.Get("ETag")
		if resp.ContentLength < 0 || etag == "" {
			return Object{}, errors.New("objstore: head: boyut ya da etag yok")
		}
		return Object{Size: resp.ContentLength, ETag: etag}, nil
	case http.StatusNotFound:
		return Object{}, ErrNotFound
	}
	return Object{}, &StatusError{Op: "head", Code: resp.StatusCode}
}

// GetRange reads the first n bytes of key (fewer when the object is
// smaller), only while its ETag is etag (ErrChanged otherwise).
func (c *Client) GetRange(ctx context.Context, key string, n int64, etag string) ([]byte, error) {
	if n <= 0 || etag == "" {
		return nil, errors.New("objstore: get: geçersiz aralık")
	}
	resp, err := c.do(ctx, "get", http.MethodGet, key,
		header{"if-match", etag}, header{"range", "bytes=0-" + strconv.FormatInt(n-1, 10)})
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusPreconditionFailed:
		return nil, ErrChanged
	default:
		return nil, &StatusError{Op: "get", Code: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err != nil {
		return nil, fmt.Errorf("objstore: get: okunamadı: %w", err)
	}
	return b, nil
}

// Copy copies src to dst inside the bucket, unchanged, only while src's
// ETag is etag (ErrChanged otherwise). The service never writes object
// content itself.
func (c *Client) Copy(ctx context.Context, src, dst, etag string) error {
	if !validKey(src) || etag == "" {
		return errors.New("objstore: copy: geçersiz istek")
	}
	resp, err := c.do(ctx, "copy", http.MethodPut, dst,
		header{"x-amz-copy-source", "/" + uriEncode(c.bucket, false) + "/" + uriEncode(src, true)},
		header{"x-amz-copy-source-if-match", etag})
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusPreconditionFailed:
		return ErrChanged
	default:
		return &StatusError{Op: "copy", Code: resp.StatusCode}
	}
	// S3 may answer 200 with an error document; only a result counts.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || !bytes.Contains(b, []byte("CopyObjectResult")) {
		return &StatusError{Op: "copy", Code: resp.StatusCode}
	}
	return nil
}

// Delete removes key; a missing object is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, "delete", http.MethodDelete, key)
	if err != nil {
		return err
	}
	defer drain(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return &StatusError{Op: "delete", Code: resp.StatusCode}
}
