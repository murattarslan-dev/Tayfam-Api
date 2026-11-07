package service

import (
	"errors"

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
