# ADR-0013: Kullanıcısız servisler arası çağrılarda servis kimliği

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-08
- **Karar veren:** Murat Arslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)

## Bağlam

ADR-0004 ve ADR-0011, kullanıcısız arka plan çağrılarının (outbox relay,
ADR-0002) "ayrı, kısa ömürlü servis token'larıyla" yapılacağını söyler ama
token'ın nasıl üretilip doğrulanacağını bırakır. Örnek çağrılar: identity →
core/chat `user.profile_updated`, `user.deleted`; core → chat
`membership.changed`, `tayfa.deleted`.

Bu çağrılarda kullanıcı token'ı yoktur ya da süresi çoktan dolmuştur.
NetworkPolicy kimin bağlanabileceğini sınırlar, ama ihlal varsayımı gereği
hedef servis çağıranın kimliğini kendisi doğrulamalıdır.

Kullanıcının başlattığı isteklerde servis token'ı kullanılması da konuşuldu ve
reddedildi: kullanıcı token'ı zaten veritabanına gitmeden, bellekteki açık
anahtarla doğrulanır; servis token'ına geçmek "gateway'e güven" modeline döner
ve gateway'i ele geçiren herkes adına işlem yapabilir hale gelir.

## Karar

1. **Kaynak:** Kubernetes'in projeksiyonlu ServiceAccount token'ları. Her
   servis kendi ServiceAccount'uyla çalışır (`tayfam-identity`,
   `tayfam-core`, `tayfam-chat`). Çağrı yapacak pod'a, yalnızca hedef
   servisin audience'ı için geçerli, 10 dakikalık bir token projected volume
   ile bağlanır; kubelet token'ı süresi dolmadan yeniler. Genel
   `automountServiceAccountToken: false` korunur; Kubernetes API'sine erişim
   veren varsayılan token pod'a hiç girmez.
2. **Audience:** Hedef servis başına bir değer: `core.tayfam.internal`,
   `chat.tayfam.internal`, `identity.tayfam.internal`. Bir hedef için alınan
   token başka bir hedefte geçmez.
3. **Doğrulama:** Hedef servis token'ı API sunucusunun OIDC keşif uç
   noktasındaki açık anahtarlarla (`/openid/v1/jwks`) kendisi doğrular:
   imza, `iss` (cluster issuer'ı, config), `aud` (kendi audience'ı), `exp`
   ve en fazla 1 saatlik ömür, `sub`
   (`system:serviceaccount:<namespace>:<ad>`). JWKS önbelleği ve bilinmeyen
   anahtarda sınırlı yeniden çekme, `RemoteKeys` ile aynı kurallarla
   çalışır. TokenReview kullanılmaz: servislere Kubernetes API'sinde yetki
   (ClusterRoleBinding) verilmez ve her çağrı API sunucusuna gitmez.
4. **Yetki:** Kullanıcı token'ı ve servis token'ı ayrı yollardır. `auth.Guard`
   her servis metodu için izin verilen çağıran ServiceAccount'larını açıkça
   listeler (örneğin core'un `ApplyUserDeleted` metodu yalnızca
   `tayfam-identity`). Servis token'ı kullanıcı metodlarında, kullanıcı
   token'ı servis metodlarında geçmez. Listede olmayan servis metodu
   varsayılan olarak reddedilir.
5. **Kapsam:** Yalnızca outbox tüketici metodları ve ileride eklenecek
   kullanıcısız iç işler. Kullanıcının başlattığı istekler kullanıcı
   token'ını taşımaya devam eder (ADR-0004).
6. **Ağ:** Servislere API sunucusuna JWKS için çıkış izni verilir
   (NetworkPolicy; yalnızca API sunucusu adresi ve portu). JWKS uç noktası
   kimlik doğrulaması istemeyen `system:service-account-issuer-discovery`
   rolüyle okunur; erişim kısıtlıysa bu rol servis ServiceAccount'larına
   bağlanır ve bu tek istisna PR'da yazılır.

## Gerekçe

- Platform sahibine anahtar üretme, saklama ve rotasyon işi çıkmaz;
  anahtarları cluster yönetir ve token'lar kendiliğinden yenilenir.
- Token kısa ömürlü ve audience'a bağlıdır: çalınan bir token yalnızca tek
  bir hedefte, birkaç dakika işe yarar.
- Bir servis ele geçirilirse saldırgan yalnızca o servisin izinli olduğu
  tüketici metodlarını çağırabilir; kullanıcı adına işlem yapamaz.
- Doğrulama yerel ve kullanıcı token'ıyla aynı modeldedir (açık anahtar
  önbelleği), yeni bir bağımlılık getirmez.

## Sonuçlar

- `internal/platform/auth` servis token'ı doğrulayıcısı ve Guard'da metod
  başına izinli çağıran listesi kazanır; testlerle sabitlenir.
- `deploy/base`: servis başına ServiceAccount, çağıran pod'larda projected
  token volume'u, API sunucusuna JWKS çıkışı.
- Yerel geliştirmede (Docker Compose, `make dev`) Kubernetes yoktur; orada
  yalnızca testlerde kullanılan bir yerel imzalayıcı ile aynı doğrulama yolu
  denenir. Bu imzalayıcı üretim yapılandırmasında çalışamaz.
- Prod VPS'e geçişte cluster issuer'ı değişir; config ile verilir.

## Değerlendirilen alternatifler

- **Servis başına kendi Ed25519 anahtarı (SOPS):** Aynı güvenlik özellikleri,
  ama anahtar üretimi ve rotasyonu platform sahibine kalır.
- **TokenReview API'si:** Pod'un hâlâ var olduğunu da kontrol eder, ama
  servislere Kubernetes API'sinde yetki ve her çağrıda API sunucusuna istek
  gerektirir; ele geçirilen bir servisin API sunucusuna erişimini genişletir.
- **Servisler arası mTLS (service mesh):** Taşıma katmanını da korur, ama tek
  node'lu ev cluster'ı için ağır; ADR-0006'da ileriye bırakıldı.
- **Kullanıcılı isteklerde de servis token'ı:** Reddedildi (Bağlam).
