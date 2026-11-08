package grpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"testing"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
)

func TestListPublicKeysReturnsOnlyPublicHalves(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	copy(seed, "test")
	oldPub, _ := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	ring, err := keys.Parse("k2:"+base64.StdEncoding.EncodeToString(seed), "k1:"+base64.StdEncoding.EncodeToString(oldPub))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := NewKeyServer(ring).ListPublicKeys(context.Background(), &identityv1.ListPublicKeysRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetKeys()) != 2 || resp.GetKeys()[0].GetKid() != "k2" || resp.GetKeys()[1].GetKid() != "k1" {
		t.Fatalf("anahtarlar = %v", resp.GetKeys())
	}
	wantPub, _ := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	for _, k := range resp.GetKeys() {
		if len(k.GetPublicKey()) != ed25519.PublicKeySize || k.GetAlgorithm() != identityv1.KeyAlgorithm_KEY_ALGORITHM_ED25519 {
			t.Fatalf("anahtar biçimi: %v", k)
		}
	}
	if !slices.Equal(resp.GetKeys()[0].GetPublicKey(), wantPub) {
		t.Fatal("aktif anahtarın açık yarısı yanlış")
	}
	if resp.GetCacheTtl().AsDuration() <= 0 {
		t.Fatal("cache_ttl yok")
	}
}

func TestPublicMethodsAreFixed(t *testing.T) {
	want := []string{
		identityv1.KeyService_ListPublicKeys_FullMethodName,
		identityv1.AuthService_SignIn_FullMethodName,
		identityv1.AuthService_Refresh_FullMethodName,
	}
	if !slices.Equal(PublicMethods, want) {
		t.Fatalf("açık metodlar değişti: %v; her ekleme güvenlik incelemesi gerektirir", PublicMethods)
	}
	if slices.Contains(PublicMethods, identityv1.AuthService_SignOut_FullMethodName) {
		t.Fatal("SignOut token istemeli")
	}
}
