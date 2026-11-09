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

	"github.com/jackc/pgx/v5/pgxpool"

	identitydb "github.com/murattarslan-dev/tayfam-api/db/identity"
	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/account"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/registration"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/retention"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/session"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	grpcapi "github.com/murattarslan-dev/tayfam-api/internal/identity/transport/grpc"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/grpcx"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
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
		Documents:    docs,
	})
	if err != nil {
		return err
	}
	reg, err := registration.New(registration.Config{Pool: pool, Log: rt.Log, Cipher: cipher, Documents: docs})
	if err != nil {
		return err
	}
	accounts, err := account.New(account.Config{Pool: pool})
	if err != nil {
		return err
	}
	consents, err := consent.NewService(pool, docs)
	if err != nil {
		return err
	}
	deleter, err := deletion.New(deletion.Config{Pool: pool, Log: rt.Log})
	if err != nil {
		return err
	}
	retain, err := retention.New(retention.Config{Pool: pool, Log: rt.Log})
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
	// Hesap ayarları, oturumlar, yeniden onay ve hesap silme yalnızca tam
	// erişim token'ıyla (aud=AUTH_AUDIENCE); kayıt bekleyen hesabın token'ı
	// reddedilir.
	identityv1.RegisterAccountServiceServer(srv, grpcapi.NewAccountServer(grpcapi.AccountDeps{
		Accounts: accounts, Sessions: sessions, Consents: consents, Deletion: deleter,
		APIAudience: audience, Log: rt.Log,
	}))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	// Saklama süreleri (refresh token 30 gün, oturum ve cihaz 90 gün,
	// silinmiş hesap 30 gün; issue #75). Birden fazla pod'da çalışabilir:
	// her DELETE koşulunu yeniden denetler.
	rt.AddWorker("retention", retain.Run)
	return addRelay(rt, pool)
}

// addRelay starts the outbox relay that delivers user events to core and
// chat, each with identity's service token for that target (ADR-0002,
// ADR-0013). Only a local environment may run without it; events then wait
// in the outbox.
func addRelay(rt *service.Runtime, pool *pgxpool.Pool) error {
	coreToken, coreOK := rt.ServiceToken("core")
	chatToken, chatOK := rt.ServiceToken("chat")
	var coreAddr, chatAddr string
	if coreOK {
		coreAddr = rt.Config.Required("CORE_GRPC_ADDR")
	}
	if chatOK {
		chatAddr = rt.Config.Required("CHAT_GRPC_ADDR")
	}
	if err := rt.Config.Err(); err != nil {
		return err
	}
	var consumers []outbox.Consumer
	if coreOK {
		conn, err := grpcx.NewClient(coreAddr, auth.ServiceTokenFrom(coreToken)...)
		if err != nil {
			return err
		}
		consumers = append(consumers, userevents.CoreConsumer(corev1.NewUserEventServiceClient(conn)))
	}
	if chatOK {
		conn, err := grpcx.NewClient(chatAddr, auth.ServiceTokenFrom(chatToken)...)
		if err != nil {
			return err
		}
		consumers = append(consumers, userevents.ChatConsumer(chatv1.NewUserEventServiceClient(conn)))
	}
	if len(consumers) == 0 {
		rt.Log.Warn("servis token'ı yok; outbox relay kapalı (yalnızca yerel ortam)")
		return nil
	}
	relay, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Log: rt.Log, Consumers: consumers})
	if err != nil {
		return err
	}
	rt.AddWorker("outbox", relay.Run)
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
