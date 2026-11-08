// Command identity owns sign-in, sessions, tokens, KVKK requests and platform
// administration (ADR-0002, ADR-0004, ADR-0008).
//
//	identity           runs the service
//	identity migrate   applies database migrations and exits (init container)
package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/registration"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/identity/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/service"
)

// accessTokenTTL is the access token lifetime (ADR-0004).
const accessTokenTTL = 15 * time.Minute

func main() {
	service.MainWithMigrations("identity", identitydb.Migrations(), setup)
}

func setup(rt *service.Runtime) error {
	c := rt.Config
	// Secret'lar (deploy/apps/<ortam>/secrets, SOPS): imzalama anahtarı,
	// doğum tarihi anahtarı, veritabanı parolası. Emekli imzalama
	// anahtarlarının yalnızca açık yarıları yapılandırılır.
	signing := c.Required("AUTH_SIGNING_KEY")
	retired := c.String("AUTH_RETIRED_KEYS", "")
	birthdateKeys := c.Required("IDENTITY_BIRTHDATE_KEYS")
	dbCfg := db.LoadConfig(c)
	issuer := c.Required("AUTH_ISSUER")
	audience := c.Required("AUTH_AUDIENCE")
	// Google OAuth client ID'leri gizli değildir; virgülle ayrılmış liste.
	googleClients := splitList(c.Required("GOOGLE_CLIENT_IDS"))
	docs := registration.Documents{
		PrivacyNotice: c.Required("CONSENT_PRIVACY_NOTICE_VERSION"),
		Terms:         c.Required("CONSENT_TERMS_VERSION"),
	}
	if err := c.Err(); err != nil {
		return err
	}

	ring, err := keys.Parse(signing, retired)
	if err != nil {
		return err
	}
	cipher, err := birthdate.ParseKeys(birthdateKeys)
	if err != nil {
		return err
	}
	rt.Log.Info("anahtarlar yüklendi",
		slog.String("active_kid", ring.Active().ID),
		slog.Int("public_keys", len(ring.PublicKeys())))

	pool, err := db.Open(context.Background(), dbCfg.URL())
	if err != nil {
		return err
	}
	rt.Health.Add("postgres", db.Ping(pool))

	google, err := oidc.NewGoogle(googleClients, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	signer := func(aud string) (*auth.Signer, error) {
		return auth.NewSigner(auth.SignerConfig{Key: ring.Active(), Issuer: issuer, Audience: aud, TTL: accessTokenTTL})
	}
	apiSigner, err := signer(audience)
	if err != nil {
		return err
	}
	regSigner, err := signer(session.AudienceRegistration)
	if err != nil {
		return err
	}
	sessions, err := session.New(session.Config{
		Pool: pool, Log: rt.Log,
		Verifiers:    map[string]oidc.Verifier{"google": google},
		API:          apiSigner,
		Registration: regSigner,
	})
	if err != nil {
		return err
	}
	reg, err := registration.New(registration.Config{Pool: pool, Log: rt.Log, Cipher: cipher, Documents: docs})
	if err != nil {
		return err
	}

	// identity kendi token'larını kendi açık anahtarlarıyla doğrular. Kayıt
	// bekleyen kullanıcının token'ı (aud=tayfam-registration) yalnızca burada
	// geçerlidir; core ve chat onu kabul etmez (ADR-0012).
	v, err := auth.NewVerifier(auth.VerifierConfig{
		Keys: ring.Static(), Issuer: issuer, Audience: audience,
		AlsoAccept: []string{session.AudienceRegistration},
	})
	if err != nil {
		return err
	}
	srv := rt.NewGRPCServer(auth.NewGuard(v, grpcapi.PublicMethods...).ServerOptions()...)
	identityv1.RegisterKeyServiceServer(srv, grpcapi.NewKeyServer(ring))
	identityv1.RegisterAuthServiceServer(srv, grpcapi.NewAuthServer(sessions, rt.Log))
	identityv1.RegisterRegistrationServiceServer(srv, grpcapi.NewRegistrationServer(reg, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	return nil
}

func splitList(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
