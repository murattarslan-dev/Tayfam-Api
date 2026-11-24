# ADR-0045: Apple ile giriş (yalnızca iOS) ve hesap silmede token iptali

- **Durum:** Kabul edildi (platform sahibi, 2026-11-24, sohbette; iki ürün
  kararı ve PR #348'deki ayrıntılar. İzleme: #347)
- **Tarih:** 2026-11-24
- **Karar veren:** Murat Arslan (2026-11-24, sohbette: "Apple ile kayıt olan
  kullanıcı Android'de şimdilik giremesin"; "token hesap silme anında iptal
  edilsin, sunucuda saklanmasın")
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0017](0017-yalnizca-mobil-istemci.md),
  [ADR-0022](0022-uygulama-butunlugu.md); Tayfam-App ADR-0012, ADR-0034

## Bağlam

ADR-0004 girişin Google ve Apple ile olacağını, hesabın `provider` + `sub`
ile bulunacağını, farklı sağlayıcı hesaplarının otomatik birleştirilmeyeceğini
ve Apple'ın ad ile e-postayı yalnızca ilk girişte verdiğini karara bağladı.
Apple Developer üyeliği olmadığı için yalnızca Google yazıldı. Bugünkü durum:

- `auth_identities.provider` `apple` değerini kabul eder; proto'da
  `PROVIDER_APPLE` vardır.
- REST sözleşmesinde `SignInRequest.provider` yalnızca `google`'dır; identity'de
  Apple doğrulayıcısı yoktur (`Verifiers` haritasında yalnızca `google`).
- Hesap silme, son 10 dakikada sağlayıcıyla açılmış bir oturum ister
  (`reauthentication-required`); sağlayıcıya herhangi bir çağrı yapmaz.

Platform sahibi 2026-11-24'te Apple Developer hesabını etkinleştirdi ve iOS
sürümüne başladı (Tayfam-App ADR-0034). Apple, üçüncü taraf girişi sunan
uygulamadan eşdeğer bir seçenek, Apple ile giriş sunan uygulamadan da hesap
silinirken kullanıcının Apple token'larının iptalini ister.

## Karar

### 1. Kapsam

- Apple ile giriş **yalnızca iOS uygulamasından** kabul edilir
  (`provider: apple` + `platform: ios`). Başka platformla gelen Apple girişi
  `invalid-request` alır.
- Android'de Apple ile giriş yoktur. Apple ile kayıt olan kullanıcı
  Android'de o hesaba giremez (platform sahibi, 2026-11-24). Android'de
  Google ile girerse ADR-0004 gereği ayrı, boş bir hesap açılır; hesaplar
  birleştirilmez.
- Hesaba ikinci sağlayıcı bağlama (ADR-0004'te öngörülen açık işlem) bu
  ADR'nin kapsamı dışındadır; ayrı bir kararla gelir.
- Platform yönetim paneli girişi Google'da kalır (ADR-0008).

### 2. Kimlik token'ının doğrulanması

`oidc` paketine Google'ınkiyle aynı arayüzde bir Apple doğrulayıcısı eklenir.
Yeni bağımlılık yoktur; imza denetimi mevcut `verifyRS256` ile yapılır (Apple
kimlik token'ları RS256 ile imzalıdır).

- Anahtarlar: `https://appleid.apple.com/auth/keys` (mevcut JWKS önbelleği).
- `iss` = `https://appleid.apple.com`.
- `aud` = iOS bundle kimliği; kabul listesi `APPLE_CLIENT_IDS` ortam
  değişkeninden (gizli değil, virgülle ayrılmış).
- `exp`, `iat`, `sub` ve boyut sınırları `checkStandard` ile, Google'dakiyle
  aynı.
- `nonce`: istemci Apple'a, giriş isteğinde gönderdiği `nonce` değerinin
  aynısını verir; token'daki `nonce` bununla sabit zamanlı karşılaştırılır
  (Google ile aynı kural). `nonce_supported` yanlışsa token reddedilir.
- E-posta yalnızca `email_verified` doğruysa alınır; Apple bu alanı metin
  (`"true"`) olarak da gönderebildiği için iki biçim de okunur. Gizli aktarma
  adresi (private relay) normal e-posta gibi saklanır (ADR-0004). E-posta
  hesap bulmak ya da bağlamak için kullanılmaz.
- Ad kimlik token'ında yoktur. Apple adı yalnızca ilk yetkilendirmede ve
  yalnızca uygulamaya verir; istemci onu kayıt formunu önceden doldurmak için
  kullanır ve kullanıcı onaylayınca mevcut `RegistrationRequest.display_name`
  ile gönderir. Sözleşmeye ad için yeni alan eklenmez.

### 3. Sözleşme

- `SignInRequest.provider`: `[google, apple]`.
- Giriş hataları değişmez: geçersiz token `invalid-credentials`, yapılandırılmamış
  sağlayıcı `unsupported-provider`, Apple'a ulaşılamaması `503`.
- `DELETE /v1/me`: isteğe bağlı JSON gövdesi
  `{ "apple_authorization_code": "…" }` (en çok 1024 karakter). Apple
  kimliğiyle açılmış hesapta zorunludur; Google hesabında gönderilirse yok
  sayılır.
- Uygulama bütünlüğü: iOS'ta kanıt alanı (`app_attest`) bu ADR'yle gelmez;
  ADR-0022 §4 ve #195. Apple girişleri o zamana kadar `report` kipinde
  `missing` sayılır, Google'ın iOS girişleriyle aynı.

### 4. Hesap silmede token iptali

Karar (platform sahibi, 2026-11-24): Apple token'ı sunucuda saklanmaz; iptal
silme anında yapılır.

1. İstemci hesap silmeden önce zaten Apple ile yeniden giriş yaptırır
   (ADR-0004, `reauthentication-required`). Bu yetkilendirmede Apple, kimlik
   token'ının yanında tek kullanımlık bir yetkilendirme kodu verir (yaklaşık
   5 dakika geçerli).
2. İstemci yeni oturumun token'ıyla `DELETE /v1/me` çağırır ve kodu gövdede
   gönderir.
3. identity, mevcut yeniden giriş denetimlerinden sonra ve veritabanında
   hiçbir şey silmeden önce: kodu Apple'ın token ucunda takas eder
   (`/auth/token`), dönen yenileme token'ını iptal eder (`/auth/revoke`) ve
   iki değeri de bellekten bırakır. Değerler veritabanına, log'a ve olaylara
   yazılmaz.
4. Ardından mevcut silme transaction'ı çalışır.

Hata durumları:

| Durum | Cevap | Hesap |
|-------|-------|-------|
| Apple hesabı, kod yok ya da boş | `403 reauthentication-required` | Silinmez |
| Kod geçersiz, kullanılmış ya da süresi dolmuş | `403 reauthentication-required` | Silinmez |
| Kodun ait olduğu Apple kullanıcısı hesabın `sub`'ı değil | `403 reauthentication-required` | Silinmez |
| Apple'a ulaşılamıyor ya da 5xx | `503` | Silinmez; kullanıcı yeniden dener |
| İptal başarılı | `204` | Silinir |

- Takas cevabındaki kimlik token'ı doğrulanır ve `sub`'ı hesabın Apple
  kimliğiyle karşılaştırılır; başka bir Apple hesabının koduyla silme
  yapılamaz.
- İptal ile silme arasında süreç ölürse token iptal edilmiş, hesap silinmemiş
  olur. Kullanıcı yeniden giriş yapıp tekrar siler; hesap ve verisi bozulmaz.
  Tersi (hesap silinmiş, token iptal edilmemiş) oluşamaz; sıra bu yüzden
  böyledir.
- Platform sahibinin ya da saklama işinin başlattığı silmelerde (panelden
  silme, 30 günlük kalıcı silme, demo temizliği) kullanıcı ortada olmadığı
  için kod yoktur ve iptal yapılamaz. Bu yollar değişmez; durum KVKK
  aydınlatma metni ve hukukçu listesi için not edilir (Sonuçlar).

### 5. Yapılandırma ve gizli değerler

| Değişken | Gizli mi | Not |
|----------|----------|-----|
| `APPLE_CLIENT_IDS` | Hayır | iOS bundle kimliği. Boşsa Apple girişi kapalıdır (`unsupported-provider`) |
| `APPLE_TEAM_ID` | Hayır | İstemci sırrının `iss`'i |
| `APPLE_KEY_ID` | Hayır | Sign in with Apple anahtarının kimliği |
| `APPLE_PRIVATE_KEY` | **Evet** | Sign in with Apple anahtarı (`.p8`, ES256). SOPS ile, yalnızca identity'de |

- `APPLE_CLIENT_IDS` doluyken diğer üçünden biri eksikse identity başlamaz:
  silerken iptal edemeyeceğimiz Apple hesabı kabul edilmez.
- İstemci sırrı (Apple'ın istediği kısa ömürlü ES256 JWT) her çağrıda
  standart kütüphaneyle (`crypto/ecdsa`) üretilir, en çok 5 dakika geçerlidir,
  saklanmaz ve loglanmaz.
- Anahtar platform sahibinin bilgisayarında, yeni bir betikle
  (`scripts/new-apple-secret.sh`, mevcut betiklerle aynı kalıp) doğrudan
  sops'a verilir; açık hali diske, terminale ya da sohbete yazılmaz (kesin
  kural 6). Yenileme adımları `docs/runbooks/sops.md`'ye eklenir.
- **Uygulama notu (2026-11-24):** dört değer tek secret'ta
  (`identity-apple`) durur; gizli olmayan üçü de anahtarla aynı şifreli
  dosyadadır. Böylece Apple girişi bir ortamda tek dosyayla açılır ve yarım
  yapılandırma (kimlikler yazılmış, anahtar yok) oluşamaz. Secret
  Deployment'ta isteğe bağlıdır; yoksa Apple girişi kapalıdır.
  `APPLE_CLIENT_IDS` tek bir bundle kimliği taşır: yetkilendirme kodu tek
  bir uygulamaya verilir ve takas isteği onu adıyla bildirir. Tayfam Ops'ta
  "Apple ile giriş" kartı aynı betiği çalıştırır.
- Ağ: identity'nin mevcut dış 443 çıkışı yeterlidir; yeni NetworkPolicy
  gerekip gerekmediği uygulama işinde manifestten doğrulanır.
- Önce dev'de açılır; prod'da `APPLE_CLIENT_IDS` iOS istemcisi hazır olunca
  doldurulur.

### 6. Loglama ve ölçüm

- Kimlik token'ı, yetkilendirme kodu, Apple'dan dönen token'lar, e-posta ve
  `sub` loglanmaz (kesin kural 3). Yalnızca sonuç sınıfı loglanır: giriş
  kabul/ret, takas ve iptal başarılı/başarısız, Apple'ın HTTP durum kodu.
- Giriş ve silme uçlarının mevcut hız sınırları geçerlidir; Apple'a giden
  çağrılar için 5 saniyelik zaman aşımı (Google JWKS istemcisiyle aynı).

## Gerekçe

- **Yalnızca iOS:** en küçük kapsam. Android'de Apple girişi web tabanlı bir
  akış, Services ID ve geri dönüş adresi ister; hesap bağlama yeni uçlar ve
  ekranlar ister. İkisi de sonradan, bu kararı bozmadan eklenebilir.
- **Silme anında iptal:** "tutmadığın veri çalınamaz" (ADR-0011). Yenileme
  token'ını saklamak, her Apple kullanıcısı için veritabanında uzun ömürlü
  bir gizli değer demektir; sızarsa kullanıcı adına Apple'a token isteği
  yapılabilir. Silme zaten yeniden giriş istediği için kod elde hazırdır.
- **Önce iptal, sonra silme:** yarıda kalan işlem kullanıcının yeniden
  deneyebileceği durumda kalır.
- **Yapılandırma eksikken başlamama:** yarım kurulumla Apple hesabı kabul
  edip sonra silememek, hiç kabul etmemekten kötüdür.

## Sonuçlar

- Hassas alan: `internal/identity/`, `proto/tayfam/identity/`, `deploy/`,
  yeni secret betiği. Uygulama PR'ları buna göre işaretlenir.
- İş paketleri (ayrı iş kayıtları, bu sırayla): (1) Apple doğrulayıcısı ve
  `SignInRequest.provider`; (2) `DELETE /v1/me` gövdesi, takas ve iptal;
  (3) secret betiği, runbook ve dev/prod yapılandırması. İstemci işleri
  Tayfam-App'te (ADR-0034 §5): Swift köprüsü, giriş düğmesi, silmede kodun
  gönderilmesi.
- İstemci üreticisi `DELETE` isteğinde gövdeyi desteklemelidir; desteklemiyorsa
  sözleşme değişmeden önce Tayfam-App'te düzeltilir.
- KVKK aydınlatma metni: giriş sağlayıcısı olarak Apple, işlenen veri (Apple
  kullanıcı kimliği, varsa e-posta ya da gizli aktarma adresi) ve silmede
  Apple'a yapılan iptal çağrısı metne eklenir; yeni sürüm ve hukukçu listesi
  (Notion).
- Platform sahibinin işleri (tek liste, uygulama işi başlamadan):
  1. Developer portalında Sign in with Apple için bir anahtar (Keys → Sign in
     with Apple, birincil uygulama kimliği `com.murattarslan.tayfam`) üretmek;
     `.p8` bir kez indirilir.
  2. Betik hazır olunca anahtarı dev ve prod için sops'a vermek.
  3. Key ID'yi iletmek (gizli değil). Team ID bilinmektedir.
- Apple ile kayıt olup Android'e geçen kullanıcı hesabına ulaşamaz. Hesap
  bağlama gelene kadar bilinen bir sınırdır; mağaza ve destek metinlerinde
  söylenmesi gerekip gerekmediği platform sahibinin kararıdır.

## Değerlendirilen alternatifler

- **Yenileme token'ını şifreli saklayıp silerken iptal etmek:** akış daha
  basit ve platform sahibinin başlattığı silmelerde de iptal yapılabilir;
  karşılığında kullanıcı başına uzun ömürlü gizli değer saklanır. Platform
  sahibi 2026-11-24'te silme anında iptali seçti.
- **Kodu giriş isteğiyle alıp oturumda tutmak:** `DELETE` gövdesine gerek
  kalmaz; ama kod ya da takas edilen token silmeye kadar sunucuda bekler.
- **Android'de Apple ile giriş / hesap bağlama:** §1; ertelendi.
- **Kimlik token'ında nonce'un SHA-256 özetini beklemek:** ham nonce Apple'a
  gitmez; ama Google ile kural farklılaşır ve bütünlük bağlaması (ADR-0022
  §2) iki biçimi ayırmak zorunda kalır. Nonce tek kullanımlık ve gizli
  olmadığı için aynı kural seçildi.
- **Hazır bir Apple/OIDC kütüphanesi:** gereken birkaç denetim standart
  kütüphaneyle yazılabiliyor; kimlik yoluna yeni bağımlılık girmiyor.
