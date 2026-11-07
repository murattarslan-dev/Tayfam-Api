// Package grpc is identity's gRPC transport: it converts between protobuf
// messages and the app layer and holds no business logic (CLAUDE.md).
package grpc

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
)

// PublicMethods are the identity methods callable without a user token.
var PublicMethods = []string{identityv1.KeyService_ListPublicKeys_FullMethodName}

// keysCacheTTL tells callers how long to cache the key list. A new key is
// announced before it signs anything (docs/runbooks/anahtar-rotasyonu.md),
// so this only delays how soon callers see it.
const keysCacheTTL = 5 * time.Minute

// KeyServer implements identityv1.KeyServiceServer.
type KeyServer struct {
	identityv1.UnimplementedKeyServiceServer
	ring *keys.Ring
}

// NewKeyServer returns a KeyServer serving ring's public keys.
func NewKeyServer(ring *keys.Ring) *KeyServer { return &KeyServer{ring: ring} }

// ListPublicKeys returns public keys only; private keys never leave identity.
func (s *KeyServer) ListPublicKeys(context.Context, *identityv1.ListPublicKeysRequest) (*identityv1.ListPublicKeysResponse, error) {
	resp := &identityv1.ListPublicKeysResponse{CacheTtl: durationpb.New(keysCacheTTL)}
	for _, k := range s.ring.PublicKeys() {
		resp.Keys = append(resp.Keys, &identityv1.PublicKey{
			Kid:       k.ID,
			Algorithm: identityv1.KeyAlgorithm_KEY_ALGORITHM_ED25519,
			PublicKey: k.Key,
		})
	}
	return resp, nil
}
