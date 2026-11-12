// Package googleauthtest is a fake Google OAuth token endpoint for tests,
// with room for one fake API next to it. Its key is generated per test;
// nothing here is a real credential.
package googleauthtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Project is the fake project ID.
const Project = "tayfam-test-1"

// Server is the fake. API, when set, answers every request that is not for
// the token endpoint.
type Server struct {
	srv *httptest.Server
	key *rsa.PrivateKey
	API http.HandlerFunc

	mu       sync.Mutex
	issued   int
	scopes   []string
	hosts    map[string]int
	tokenErr int // status to answer the token endpoint with; 0: 200
}

// New starts a fake server, stopped when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{key: k, hosts: map[string]int{}}
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
	b, err := json.Marshal(map[string]string{ //nolint:gosec // G101: field names, the key is generated per test
		"type": "service_account", "project_id": Project, "private_key_id": "test-key",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "test@" + Project + ".iam.gserviceaccount.com",
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

// FailTokens makes the token endpoint answer with status (0: succeed again).
func (s *Server) FailTokens(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenErr = status
}

// Issued is how many access tokens were granted.
func (s *Server) Issued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issued
}

// Scopes are the scopes of the granted tokens, in order.
func (s *Server) Scopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.scopes...)
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
	if r.URL.Path != "/token" {
		if s.API == nil {
			http.NotFound(w, r)
			return
		}
		s.API(w, r)
		return
	}
	s.mu.Lock()
	fail := s.tokenErr
	s.mu.Unlock()
	if fail != 0 {
		w.WriteHeader(fail)
		return
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	scope, ok := s.verify(r.PostForm.Get("assertion"))
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	s.issued++
	n := s.issued
	s.scopes = append(s.scopes, scope)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": AccessToken(n), "expires_in": 3600, "token_type": "Bearer"})
}

// verify checks the assertion's RS256 signature with the fake's key and
// returns its scope.
func (s *Server) verify(assertion string) (string, bool) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&s.key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var cl struct {
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
	}
	if json.Unmarshal(raw, &cl) != nil || cl.Aud != "https://oauth2.googleapis.com/token" {
		return "", false
	}
	return cl.Scope, true
}
