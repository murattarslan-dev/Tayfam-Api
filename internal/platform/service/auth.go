package service

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
)

// NewVerifier returns an access token verifier using keys, with issuer and
// audience from AUTH_ISSUER and AUTH_AUDIENCE (both required).
func (rt *Runtime) NewVerifier(keys auth.KeySource) (*auth.Verifier, error) {
	issuer := rt.Config.Required("AUTH_ISSUER")
	audience := rt.Config.Required("AUTH_AUDIENCE")
	if issuer == "" || audience == "" {
		return nil, errors.New("AUTH_ISSUER ve AUTH_AUDIENCE zorunlu")
	}
	return auth.NewVerifier(auth.VerifierConfig{Keys: keys, Issuer: issuer, Audience: audience})
}

// IdentityVerifier returns a verifier that fetches public keys from identity
// at IDENTITY_GRPC_ADDR (required). Every service except identity uses it to
// check tokens itself instead of trusting the caller (ADR-0011, rule 8).
//
// Keys are fetched lazily on the first request, so the service starts and
// reports ready even while identity is down; requests then fail with
// Unavailable until identity is back.
func (rt *Runtime) IdentityVerifier() (*auth.Verifier, error) {
	addr := rt.Config.Required("IDENTITY_GRPC_ADDR")
	if addr == "" {
		return nil, errors.New("IDENTITY_GRPC_ADDR zorunlu")
	}
	conn, err := grpcx.NewClient(addr)
	if err != nil {
		return nil, err
	}
	return rt.NewVerifier(auth.NewRemoteKeys(auth.FetchFromIdentity(identityv1.NewKeyServiceClient(conn))))
}

// Service identity (ADR-0013) defaults: where the pod finds the API server's
// CA and its own short-lived token for the API server. Both come from
// projected volumes in deploy/base; the default ServiceAccount token is never
// mounted.
const (
	defaultJWKSURL      = "https://kubernetes.default.svc/openid/v1/jwks"
	defaultKubeCAFile   = "/var/run/secrets/tayfam/kube-api/ca.crt" // #nosec G101 -- a file path, not a credential
	defaultKubeAPIToken = "/var/run/secrets/tayfam/kube-api/token"  // #nosec G101 -- a file path, not a credential
)

// ServiceIdentity is what a service needs to accept service tokens.
type ServiceIdentity struct {
	// Verifier is nil only in a local environment without a cluster; every
	// service method is then refused.
	Verifier *auth.ServiceVerifier
	// Namespace is the namespace of this deployment. Allowed callers are
	// ServiceAccounts in it.
	Namespace string
}

// Account returns the ServiceAccount name in this deployment's namespace.
func (s ServiceIdentity) Account(name string) auth.ServiceAccount {
	return auth.ServiceAccount{Namespace: s.Namespace, Name: name}
}

// ServiceIdentity returns the verifier for service tokens addressed to this
// service (audience <name>.tayfam.internal, ADR-0013), configured from:
//
//	SERVICE_TOKEN_ISSUER         cluster issuer (required outside local)
//	SERVICE_NAMESPACE            this pod's namespace (required with issuer)
//	SERVICE_TOKEN_JWKS_URL       API server key endpoint
//	SERVICE_TOKEN_CA_FILE        API server CA
//	SERVICE_TOKEN_API_TOKEN_FILE this pod's token for the API server
//
// Only TAYFAM_ENV=local may leave the issuer unset, which closes service
// methods; dev and prod cannot start without it.
func (rt *Runtime) ServiceIdentity() (ServiceIdentity, error) {
	c := rt.Config
	var issuer string
	if rt.Env == "local" {
		issuer = c.String("SERVICE_TOKEN_ISSUER", "")
		if issuer == "" {
			rt.Log.Warn("servis token'ı yapılandırılmadı; servis metodları kapalı (yalnızca yerel ortam)")
			return ServiceIdentity{}, nil
		}
	} else {
		issuer = c.Required("SERVICE_TOKEN_ISSUER")
	}
	ns := c.Required("SERVICE_NAMESPACE")
	jwksURL := c.String("SERVICE_TOKEN_JWKS_URL", defaultJWKSURL)
	caFile := c.String("SERVICE_TOKEN_CA_FILE", defaultKubeCAFile)
	apiToken := c.String("SERVICE_TOKEN_API_TOKEN_FILE", defaultKubeAPIToken)
	if err := c.Err(); err != nil {
		return ServiceIdentity{}, err
	}
	if !(auth.ServiceAccount{Namespace: ns, Name: rt.Name}).Valid() {
		return ServiceIdentity{}, errors.New("SERVICE_NAMESPACE geçersiz")
	}
	client, err := caClient(caFile)
	if err != nil {
		return ServiceIdentity{}, err
	}
	v, err := auth.NewServiceVerifier(auth.ServiceVerifierConfig{
		Issuer:      issuer,
		Audience:    auth.ServiceAudience(rt.Name),
		JWKSURL:     jwksURL,
		Client:      client,
		BearerToken: auth.TokenFile(apiToken).Token,
	})
	if err != nil {
		return ServiceIdentity{}, err
	}
	return ServiceIdentity{Verifier: v, Namespace: ns}, nil
}

// caClient returns an HTTP client that trusts only the CA in file.
func caClient(file string) (*http.Client, error) {
	pem, err := os.ReadFile(file) // #nosec G304 -- path from configuration
	if err != nil {
		return nil, fmt.Errorf("API sunucusu CA dosyası okunamadı: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("API sunucusu CA dosyasında sertifika yok")
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout: 3 * time.Second,
			MaxIdleConns:        2,
		},
	}, nil
}

// ServiceToken returns the projected token file for calls to target
// (ADR-0013), from SERVICE_TOKEN_FILE_<TARGET>. Outside local it is
// required; locally ok is false when it is unset and the caller does not
// make service calls.
func (rt *Runtime) ServiceToken(target string) (f auth.TokenFile, ok bool) {
	key := "SERVICE_TOKEN_FILE_" + strings.ToUpper(target)
	if rt.Env == "local" {
		v := rt.Config.String(key, "")
		return auth.TokenFile(v), v != ""
	}
	v := rt.Config.Required(key)
	return auth.TokenFile(v), v != ""
}
