// Package fcmtest is a fake FCM and Google OAuth server for tests. Its key
// is generated per test; nothing here is a real credential.
package fcmtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Project is the fake project ID.
const Project = "tayfam-test-1"

// Response is a canned answer for one push token.
type Response struct {
	Status     int
	Body       string
	RetryAfter string
}

// Server is the fake. Sent holds the decoded messages:send bodies.
type Server struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu       sync.Mutex
	sent     []map[string]any
	answers  map[string][]Response
	oauth    int
	hosts    map[string]int
	rejectAT bool
	issued   int
}

// New starts a fake server, stopped when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{key: k, answers: map[string][]Response{}, hosts: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// Account returns a service account JSON for the fake project.
func (s *Server) Account(t testing.TB) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(s.key)
	if err != nil {
		t.Fatal(err)
	}
	// A test key generated above, not a credential.
	b, err := json.Marshal(map[string]string{ //nolint:gosec // G101: field names, the key is generated per test
		"type": "service_account", "project_id": Project, "private_key_id": "test-key",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "push@" + Project + ".iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Transport sends every request to the fake, remembering the host it was
// meant for.
func (s *Server) Transport() http.RoundTripper {
	return roundTrip(func(r *http.Request) (*http.Response, error) {
		s.mu.Lock()
		s.hosts[r.URL.Host]++
		s.mu.Unlock()
		c := r.Clone(r.Context())
		c.URL.Scheme, c.URL.Host, c.Host = "http", strings.TrimPrefix(s.srv.URL, "http://"), ""
		c.RequestURI = ""
		return http.DefaultTransport.RoundTrip(c)
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Answer queues answers for a push token; once used up, sends succeed.
func (s *Server) Answer(token string, rs ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[token] = append(s.answers[token], rs...)
}

// RejectAccessTokens makes the next send refuse the access token (401).
func (s *Server) RejectAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectAT = true
}

// Sent returns the messages received so far.
func (s *Server) Sent() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.sent...)
}

// OAuthCalls is how many access tokens were requested.
func (s *Server) OAuthCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oauth
}

// Hosts is how many requests went to each intended host.
func (s *Server) Hosts() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.hosts {
		out[k] = v
	}
	return out
}

// AccessToken is the fake access token of the n-th grant (1-based).
func AccessToken(n int) string { return "fake-access-" + strconv.Itoa(n) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/token":
		s.grant(w, r)
	case "/v1/projects/" + Project + "/messages:send":
		s.send(w, r)
	default:
		http.NotFound(w, r)
	}
}

// grant checks the JWT bearer assertion and its RS256 signature.
func (s *Server) grant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	parts := strings.Split(r.PostForm.Get("assertion"), ".")
	if len(parts) != 3 {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err != nil || rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Scope, Aud string
		Iat, Exp   int64
	}
	if json.Unmarshal(raw, &c) != nil || c.Scope != "https://www.googleapis.com/auth/firebase.messaging" ||
		c.Aud != "https://oauth2.googleapis.com/token" || c.Exp-c.Iat > 3600 {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.oauth++
	s.issued++
	n := s.issued
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"`+AccessToken(n)+`","expires_in":3599,"token_type":"Bearer"}`)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	reject := s.rejectAT
	s.rejectAT = false
	issued := s.issued
	s.mu.Unlock()
	if reject || r.Header.Get("Authorization") != "Bearer "+AccessToken(issued) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"status":"UNAUTHENTICATED"}}`)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	msg, _ := body["message"].(map[string]any)
	token, _ := msg["token"].(string)
	s.mu.Lock()
	s.sent = append(s.sent, msg)
	var a *Response
	if q := s.answers[token]; len(q) > 0 {
		a, s.answers[token] = &q[0], q[1:]
	}
	s.mu.Unlock()
	if a != nil {
		if a.RetryAfter != "" {
			w.Header().Set("Retry-After", a.RetryAfter)
		}
		w.WriteHeader(a.Status)
		_, _ = io.WriteString(w, a.Body)
		return
	}
	_, _ = io.WriteString(w, `{"name":"projects/`+Project+`/messages/1"}`)
}

// Canned FCM errors.
var (
	Unregistered = Response{Status: 404, Body: `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`}
	BadToken     = Response{Status: 400, Body: `{"error":{"code":400,"message":"The registration token is not a valid FCM registration token","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"},{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.token","description":"Invalid registration token"}]}]}}`}
	BadPayload   = Response{Status: 400, Body: `{"error":{"code":400,"message":"Invalid value","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"INVALID_ARGUMENT"},{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.android.ttl"}]}]}}`}
	Mismatch     = Response{Status: 403, Body: `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"SENDER_ID_MISMATCH"}]}}`}
	Unavailable  = Response{Status: 503, Body: `{"error":{"code":503,"status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNAVAILABLE"}]}}`, RetryAfter: "120"}
	Quota        = Response{Status: 429, Body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"QUOTA_EXCEEDED"}]}}`}
	APNsAuth     = Response{Status: 401, Body: `{"error":{"code":401,"status":"UNAUTHENTICATED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"THIRD_PARTY_AUTH_ERROR"}]}}`}
)
