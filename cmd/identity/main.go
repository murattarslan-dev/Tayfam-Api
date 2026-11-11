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
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/coremedia"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/dataexport"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/keys"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/minor"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/platformadmin"
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
	// Avatar (issue #135): core attaches the upload with the caller's own
	// token (auth.ForwardToken); identity's service identity is not used.
	// Without CORE_GRPC_ADDR (a local run without core) SetAvatar is
	// UNAVAILABLE.
	var avatars account.Media
	if addr := rt.Config.String("CORE_GRPC_ADDR", ""); addr != "" {
		conn, err := grpcx.NewClient(addr, auth.ForwardToken()...)
		if err != nil {
			return err
		}
		avatars = coremedia.New(corev1.NewMediaServiceClient(conn))
	} else {
		rt.Log.Warn("CORE_GRPC_ADDR yok; avatar ayarlanamaz (UNAVAILABLE)")
	}
	accounts, err := account.New(account.Config{Pool: pool, Media: avatars})
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
	exports, err := dataExports(rt, pool, cipher)
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
	minors, err := minor.New(minor.Config{Pool: pool, Cipher: cipher})
	if err != nil {
		return err
	}
	// Kullanıcısız servis çağrıları (ADR-0013): MinorService'in tek metodu
	// IsMinor yalnızca core'un ServiceAccount'unun servis token'ını kabul
	// eder (ADR-0012: şikâyet önceliği için evet/hayır; doğum tarihi
	// identity'den çıkmaz). Yerelde (cluster yok) doğrulayıcı yoksa kapalıdır.
	si, err := rt.ServiceIdentity()
	if err != nil {
		return err
	}
	policy := auth.ServicePolicy{Verifier: si.Verifier, Services: []string{grpcapi.MinorServiceName, grpcapi.PlatformAuditServiceName}}
	if si.Verifier != nil {
		policy.Callers = map[string][]auth.ServiceAccount{
			identityv1.MinorService_IsMinor_FullMethodName: {si.Account("tayfam-core")},
			// core'un panel işlemlerinin denetim kaydı (issue #104).
			identityv1.PlatformAuditService_RecordPlatformAudit_FullMethodName: {si.Account("tayfam-core")},
		}
	}
	guard, err := auth.NewGuard(v, grpcapi.PublicMethods...).WithServices(policy)
	if err != nil {
		return err
	}
	// Platform yönetim paneli (ADR-0008, issue #104): ayrı tür ve audience'lı,
	// 8 saatlik admin token'ı. AdminAuthService'in metodları (açık
	// AdminSignIn dışında) yalnızca admin token'ını, diğer bütün metodlar
	// yalnızca uygulama token'ını kabul eder.
	adminSigner, err := auth.NewAdminSigner(auth.AdminSignerConfig{Key: ring.Active(), Issuer: issuer})
	if err != nil {
		return err
	}
	adminVerifier, err := auth.NewAdminVerifier(auth.AdminVerifierConfig{Keys: ring.Static(), Issuer: issuer})
	if err != nil {
		return err
	}
	admins, err := platformadmin.New(platformadmin.Config{Pool: pool, Log: rt.Log, Google: google, Signer: adminSigner})
	if err != nil {
		return err
	}
	guard, err = guard.WithAdmin(auth.AdminPolicy{Verifier: adminVerifier, Services: grpcapi.AdminServiceNames()})
	if err != nil {
		return err
	}
	srv := rt.NewGRPCServer(guard.ServerOptions()...)
	identityv1.RegisterMinorServiceServer(srv, grpcapi.NewMinorServer(minors, rt.Log))
	identityv1.RegisterKeyServiceServer(srv, grpcapi.NewKeyServer(ring))
	identityv1.RegisterAdminAuthServiceServer(srv, grpcapi.NewAdminAuthServer(admins, rt.Log))
	identityv1.RegisterAdminUserServiceServer(srv, grpcapi.NewAdminUserServer(admins, rt.Log))
	identityv1.RegisterPlatformAuditServiceServer(srv, grpcapi.NewPlatformAuditServer(admins, rt.Log))
	identityv1.RegisterAdminAuditServiceServer(srv, grpcapi.NewAdminAuditServer(admins, rt.Log))
	identityv1.RegisterAuthServiceServer(srv, grpcapi.NewAuthServer(sessions, rt.Log))
	identityv1.RegisterRegistrationServiceServer(srv, grpcapi.NewRegistrationServer(reg, rt.Log))
	// Hesap ayarları, oturumlar, yeniden onay ve hesap silme yalnızca tam
	// erişim token'ıyla (aud=AUTH_AUDIENCE); kayıt bekleyen hesabın token'ı
	// reddedilir.
	identityv1.RegisterAccountServiceServer(srv, grpcapi.NewAccountServer(grpcapi.AccountDeps{
		Accounts: accounts, Sessions: sessions, Consents: consents, Deletion: deleter,
		APIAudience: audience, Log: rt.Log,
	}))
	// KVKK veri dışa aktarma (issue #144, ADR-0021): anahtar ya da core/chat
	// adresi yoksa uçlar UNAVAILABLE döner, servis başlar.
	identityv1.RegisterDataExportServiceServer(srv, grpcapi.NewDataExportServer(exports, audience, rt.Log))
	rt.AddGRPCServer("grpc", c.String("GRPC_ADDR", ":9090"), srv)
	// Talep anında başlayan dışa aktarma işleri, birer birer.
	rt.AddWorker("data-export", exports.Run)
	// Saklama süreleri (refresh token 30 gün, oturum ve cihaz 90 gün,
	// silinmiş hesap 30 gün; issue #75). Birden fazla pod'da çalışabilir:
	// her DELETE koşulunu yeniden denetler.
	rt.AddWorker("retention", retain.Run)
	return addRelay(rt, pool)
}

// dataExports returns the KVKK data export service (issue #144, ADR-0021).
// The wrapping key comes from the optional identity-export-key secret
// (IDENTITY_DATA_EXPORT_KEYS); a malformed value stops the start. core and
// chat are called with the requesting user's own token (auth.ForwardToken);
// identity gets no storage credentials: core signs the links. Without the
// key or the addresses export is off and its methods are UNAVAILABLE.
func dataExports(rt *service.Runtime, pool *pgxpool.Pool, birth *birthdate.Cipher) (*dataexport.Service, error) {
	c := rt.Config
	raw := c.String("IDENTITY_DATA_EXPORT_KEYS", "")
	coreAddr := c.String("CORE_GRPC_ADDR", "")
	chatAddr := c.String("CHAT_GRPC_ADDR", "")
	cfg := dataexport.Config{Pool: pool, Log: rt.Log, Birth: birth, AnyStorageHost: rt.Env == "local"}
	switch {
	case raw == "":
		rt.Log.Info("IDENTITY_DATA_EXPORT_KEYS yok; veri dışa aktarma kapalı")
	case coreAddr == "" || chatAddr == "":
		rt.Log.Warn("CORE_GRPC_ADDR ya da CHAT_GRPC_ADDR yok; veri dışa aktarma kapalı")
	default:
		keys, err := dataexport.ParseKeys(raw)
		if err != nil {
			return nil, err
		}
		coreConn, err := grpcx.NewClient(coreAddr, auth.ForwardToken()...)
		if err != nil {
			return nil, err
		}
		chatConn, err := grpcx.NewClient(chatAddr, auth.ForwardToken()...)
		if err != nil {
			return nil, err
		}
		cfg.Keys = keys
		cfg.Storage = dataexport.CoreStorage{C: corev1.NewMediaServiceClient(coreConn)}
		cfg.Core = dataexport.CoreSource{C: corev1.NewExportServiceClient(coreConn)}
		cfg.Chat = dataexport.ChatSource{C: chatv1.NewExportServiceClient(chatConn)}
		rt.Log.Info("veri dışa aktarma açık")
	}
	return dataexport.New(cfg)
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
