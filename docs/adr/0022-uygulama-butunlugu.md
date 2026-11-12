# ADR-0022: Uygulama bütünlüğü (Play Integrity / App Attest) oturuma bağlanır

- **Durum:** Kabul edildi (platform sahibi, 2026-11-12; PR #162'yi birleştirme yetkisiyle)
- **Tarih:** 2026-11-12
- **Karar veren:** Murat Tarslan (üç temel tercih sohbette verildi,
  2026-11-12: önce yalnızca kayıt, Android'de `MEETS_STRONG_INTEGRITY`,
  giriş + 24 saatte bir yeniden doğrulama)
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0016](0016-desteklenen-istemci-surumu.md),
  [ADR-0017](0017-yalnizca-mobil-istemci.md),
  [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md) (§3 kabul edilen risk);
  issue #142

## Bağlam

ADR-0020 görüntü işlemeyi istemciye taşıdı; sunucu yalnızca dosya başlığını
denetliyor. Değiştirilmiş bir istemci ya da doğrudan API'ye konuşan bir
betik bu denetimi, hız sınırlarını ve istemcideki diğer korumaları
atlayabilir. ADR-0017 ile Tayfam yalnızca Android ve iOS uygulamasıdır; API'yi
yalnızca resmi uygulamanın kullanması hedeflenebilir.

Platformların sunduğu iki mekanizma var:

- **Android: Play Integrity API (standard request).** Uygulama Google
  Play'den bir bütünlük token'ı alır; sunucu token'ı Google'a çözdürür
  (`decodeIntegrityToken`) ve kararları okur: uygulama Play'in tanıdığı sürüm
  mü (`appRecognitionVerdict`), cihaz hangi seviyede
  (`deviceRecognitionVerdict`), istek hangisi (`requestHash`). Standard
  request tekrar kullanıma karşı korumalıdır: aynı token ikinci kez
  çözüldüğünde kararlar boş döner. Varsayılan kota günde 10.000 istektir.
- **iOS: App Attest.** Uygulama cihazda bir anahtar üretir, Apple'a
  onaylatır (attestation); sunucu sertifika zincirini Apple'ın App Attest
  kök sertifikasıyla **çevrimdışı** doğrular ve açık anahtarı saklar.
  Sonraki isteklerde uygulama bu anahtarla imza (assertion) üretir; sayaç her
  imzada artar.

Google ve Apple ikisini de tek başına bir güvenlik sınırı olarak değil,
kötüye kullanıma karşı bir sinyal olarak önerir; Google ayrıca önce
zorlamadan gözlemlemeyi (telemetri) önerir.

## Karar

### 1. Doğrulama oturum düzeyindedir, identity'de yapılır

- Doğrulama her istekte değil, **oturum açılırken (giriş) ve token
  yenilemede** yapılır; sonuç oturuma (`sessions`) yazılır. Kararın sahibi
  oturumun sahibi olan identity'dir; Google ve Apple ile konuşan da
  identity'dir.
- Yeniden doğrulama: oturumun son başarılı doğrulaması **24 saatten
  eskiyse** token yenileme isteği yeni bir bütünlük kanıtı taşımalıdır.
  Token cevabı istemciye bir sonraki doğrulamanın zamanını
  (`integrity_check_after`) söyler; istemci o zamandan sonraki ilk
  yenilemeye kanıt ekler. Ara yenilemelerde Play'e gidilmez (kota ve
  gecikme).
- Her istekte doğrulama yapılmaz: 15 dakikalık erişim token'ı oturumun son
  durumunu taşır (aşağıda §5).

### 2. Kanıt yeni bir uca değil, var olan tek kullanımlık değerlere bağlanır

Bütünlük kanıtı belirli bir isteğe bağlanmazsa yalnızca "bu cihazda bir
yerde resmi uygulama çalışıyor" der ve başka bir isteğe taşınabilir. Bağlama
için sunucunun ürettiği ayrı bir challenge ucu yerine, zaten tek kullanımlık
ve sunucuda doğrulanan iki değer kullanılır:

| İstek | Bağlanan değer |
|-------|----------------|
| `POST /v1/auth/sign-in` | OIDC `nonce` (ID token'ın içinde; identity zaten doğruluyor) |
| `POST /v1/auth/refresh` | Refresh token (her kullanımda döner; ikinci kullanım oturumu iptal eder) |

Bağlama değeri:

```
sign-in:  SHA-256("tayfam-integrity/v1/sign-in\n"  || nonce)
refresh:  SHA-256("tayfam-integrity/v1/refresh\n"  || SHA-256(refresh_token))
```

base64url (dolgusuz) olarak Android'de `requestHash`, iOS'ta `clientData`
olur. Refresh token'ın kendisi hiçbir zaman Google'a ya da Apple'a gitmez;
yalnızca özetinin özeti gider. Böylece:

- token istemeyen yeni bir açık uç ve challenge tablosu gerekmez (CLAUDE.md
  kural 8: açık uç listesi büyümez);
- bir kanıt yalnızca kendi girişinde ya da kendi yenilemesinde geçerlidir;
  yakalanıp başka bir oturuma taşınamaz.

### 3. Android kararı

identity token'ı Google'a çözdürür ve aşağıdakilerin **hepsi** sağlanırsa
oturum `passed` olur:

- `requestDetails.requestPackageName` beklenen paket adı,
  `requestHash` §2'deki değer, `timestampMillis` en fazla **2 dakika** önce;
- `appIntegrity.appRecognitionVerdict = PLAY_RECOGNIZED` ve
  `certificateSha256Digest` yapılandırılmış imza sertifikası özetlerinden
  biri (Play App Signing anahtarı);
- `deviceIntegrity.deviceRecognitionVerdict` **`MEETS_STRONG_INTEGRITY`**
  içerir (platform sahibinin tercihi). Bu etiket Play Console'da isteğe
  bağlı olarak açılır.

`appLicensingVerdict` kararda kullanılmaz, yalnızca sayılır: uygulamanın
resmi sürüm olması `appRecognitionVerdict` ile zaten denetlenir; mağaza
dışı kurulumu ayrıca cezalandırmak istenmedi. İsteğe bağlı ortam sinyalleri
(`appAccessRiskVerdict`, `playProtectVerdict`) açılmaz: karar onlara
dayanmaz ve gereksiz veri toplanmaz.

### 4. iOS kararı (Apple Developer üyeliği gelince uygulanır)

- **Girişte attestation:** uygulama bir App Attest anahtarı üretir ve
  `clientDataHash = SHA-256(bağlama değeri)` ile onaylatır. identity Apple'ın
  belgelediği adımları çevrimdışı yapar: zincir App Attest kök sertifikasına,
  nonce, anahtar kimliği, `RP ID = SHA-256(TeamID.BundleID)`, sayaç `0`,
  `aaguid` (prod'da `appattest`, prod dışında `appattestdevelop`). Açık
  anahtar oturumun cihazına (`devices`) yazılır; aynı anahtar başka bir
  kullanıcıya bağlıysa reddedilir.
- **Yenilemede assertion:** imza saklanan anahtarla doğrulanır, sayaç bir
  öncekinden büyük olmalıdır, yeni sayaç saklanır.
- Apple'ın dolandırıcılık riski metriği (receipt) ilk sürümde kullanılmaz;
  receipt saklanmaz.
- Apple kök sertifikası koda gömülür (sertifika açık bir değerdir);
  değiştiğinde kod değişikliğiyle güncellenir.

Apple Developer üyeliği olmadığı için iOS kodu bu ADR kabul edildiğinde
yazılmaz; şema ve sözleşme iOS'a yer açar (aşağıda).

### 5. Sonuç oturumda tutulur, erişim token'ı taşır

- `sessions` tablosuna: `integrity_status` (`unverified`, `passed`,
  `failed`), `integrity_reason` (kapalı liste: `missing`, `binding`,
  `stale`, `app_unrecognized`, `device`, `unavailable`, `malformed`),
  `integrity_checked_at`, `integrity_passed_at`. Ham token, Google'ın ham
  cevabı, cihaz bilgisi ve Apple receipt'i **saklanmaz**.
- Erişim token'ına tek bir claim eklenir: `int` (`true` yalnızca oturum
  `passed` ise). Token 15 dakikalıktır; durum değişikliği en geç bir
  yenilemede görünür.
- Google'a ulaşılamazsa (`unavailable`) oturumun önceki durumu korunur:
  son 7 gün içinde `passed` olmuş bir oturum `passed` kalır. Google'ın
  kesintisi kullanıcıları dışarıda bırakmaz.

### 6. Zorlama kademelidir; başlangıç "yalnızca kayıt"

Ortam başına `INTEGRITY_MODE` (`internal/platform/config`):

| Mod | Davranış |
|-----|----------|
| `off` | Kanıt istenmez, çözülmez. |
| `report` | Kanıt çözülür, sonuç oturuma ve metriklere yazılır; **hiçbir istek reddedilmez.** |
| `enforce` | `int` claim'i olmayan erişim token'ı gateway'de `integrity-required` (403) alır. |

- **Başlangıç `report`** (platform sahibinin tercihi). Her denetim tek bir
  sayım satırı olarak log'a yazılır (`bütünlük denetimi`: mod, platform,
  durum, neden, lisans kararı); cluster'da metrik sistemi olmadığı için
  sayım log'dan yapılır. Kullanıcı, oturum, token ve cihaz bilgisi bu satıra
  girmez. `enforce`'a geçiş, telemetriye bakılarak pilottan
  önce ayrı bir kararla ve yalnızca configMap değişikliğiyle yapılır.
- `enforce`'ta da şu uçlar `int` istemez: `getStatus`, `getClientConfig`,
  giriş, yenileme, kayıt ve onaylar, oturum listesi/kapatma, çıkış, hesap
  silme, KVKK veri dışa aktarma talebi. Kullanıcı bütünlük doğrulamasından
  geçemese de hesabını silebilmeli ve verisini isteyebilmelidir (KVKK).
- Zorlama gateway'dedir: bütünlük bir yetki kararı değil, istemcinin
  kaynağına dair bir kötüye kullanım sinyalidir. İç servisler token'ı
  kendileri doğrulamaya devam eder (ADR-0011); `int` claim'ine bakmazlar.
- prod dışı ortamlar (yerel `web` derlemesi, debug imzalı Android) Play
  doğrulamasından geçemez; bu ortamlarda mod `report` ya da `off` kalır ve
  `enforce` yalnızca prod configMap'inde açılır.

### 7. Sözleşme

- `SignInRequest` ve `RefreshRequest`'e isteğe bağlı alanlar:
  `integrity_token` (Android) ve `app_attest` (`key_id`, `attestation` ya da
  `assertion`; iOS). 2026-11-12 (issue #164): `integrity_token` eklendi;
  `app_attest` iOS doğrulayıcısıyla birlikte eklenir, kullanılmayan alan
  sözleşmeye önden girmez. Kanıtsız istek `report` modunda kabul edilir ve
  `missing` olarak sayılır.
- `Session` cevabına `integrity_check_after` (zaman) eklenir.
- Problem türü `integrity-required` (403) eklenir.
- Mevcut istemciler kırılmaz: alanların hepsi isteğe bağlıdır.

### 8. Gizli bilgi ve dış bağlantılar

- `decodeIntegrityToken` için **yalnızca Play Integrity yetkisi olan** ayrı
  bir Google Cloud servis hesabı (`tayfam-integrity-<ortam>`) kullanılır;
  push servis hesabı (ADR-0019) yeniden kullanılmaz. Anahtarı platform
  sahibinin bilgisayarında üretilip sops'a verilir; identity'nin secret'ına
  eklenir (`scripts/new-integrity-secret.sh`, ADR-0006 ve kural 6).
- identity'nin internete 443 çıkışı zaten vardır (`identity-egress-internet`);
  ağ kuralı değişmez.
- Play Console'da uygulama bu Cloud projesine bağlanır ve
  `MEETS_STRONG_INTEGRITY` etiketi açılır.

## Gerekçe

- Oturum düzeyi, her istekte Google'a gitmeden "API'yi resmi uygulama mı
  kullanıyor" sorusuna cevap verir; kota (günde 10.000) cihaz başına
  günde bir iki istekle yüzlerce kat yeterlidir.
- Var olan tek kullanımlık değerlere bağlamak yeni bir saldırı yüzeyi (açık
  challenge ucu) ve durum tablosu açmadan tekrar oynatmayı önler.
- `report` ile başlamak, `MEETS_STRONG_INTEGRITY` gibi sıkı bir eşiğin kaç
  gerçek kullanıcıyı dışarıda bırakacağını görmeden kimseyi kilitlemez.
- Ham kararları saklamamak "tutmadığın veri çalınamaz" ilkesine uyar
  (ADR-0011); karar için oturumdaki özet yeterlidir.

## Sonuçlar

- identity bir Play Integrity istemcisi (Google OAuth servis hesabıyla
  REST çağrısı) ve iOS için bir App Attest doğrulayıcısı (CBOR, X.509)
  içerir. Yeni bağımlılık gerekiyorsa uygulama PR'ında gerekçelendirilir;
  ilk tercih standart kütüphane ve go.mod'daki mevcut paketlerdir.
- tayfam-app Android'de uygulama açılışında token sağlayıcısını hazırlar
  (`prepareIntegrityToken`), girişte ve `integrity_check_after` geçtikten
  sonraki ilk yenilemede token alır.
- **`MEETS_STRONG_INTEGRITY` bazı eski ya da uzun süredir güncelleme
  almamış cihazları dışarıda bırakabilir.** `report` dönemindeki
  `failed/device` oranı yüksekse eşik `enforce`'tan önce yeniden
  değerlendirilir (örn. `MEETS_DEVICE_INTEGRITY`).
- Play dışından kurulan, Play'in tanımadığı derlemeler (debug, yerel test)
  `enforce`'ta API'yi kullanamaz; Play iç test kanalı Play'den kurulduğu
  için doğrulanabilir olmalıdır, `report` döneminde bu da gözlenir.
- Google'a cihaz bütünlüğü sinyali için istek gönderilmesi KVKK aydınlatma
  metnine eklenir ve hukukçu listesine yazılır (yurt dışına aktarım).
- Bütünlük bir sınır değil sinyaldir: köklü bir saldırgan gerçek bir
  cihazda resmi uygulamayı otomatikleştirebilir. Hız sınırları, sunucu
  tarafı yetki ve girdi denetimleri aynen kalır.

## Uygulama paketleri (kabulden sonra issue olur)

1. identity: şema (`sessions` alanları, `devices`'ta App Attest anahtarı ve
   sayacı), Play Integrity doğrulayıcısı, bağlama değeri, `report` modu,
   metrikler; giriş ve yenilemede çağrı.
2. Sözleşme ve gateway: istek alanları, `integrity_check_after`, `int`
   claim'i, `integrity-required`, `enforce` middleware'i ve muaf uç listesi
   (testle sabit).
3. Gizli bilgi: `scripts/new-integrity-secret.sh`, runbook, dev ve prod
   secret'ları (platform sahibinin adımı).
4. iOS App Attest doğrulayıcısı (Apple Developer üyeliğinden sonra).

## Değerlendirilen alternatifler

- **Her istekte bütünlük kanıtı:** En taze sinyal, ama her istekte Google'a
  gitmek kota ve gecikme demek; Play de bunu önermiyor.
- **Ayrı challenge ucu (`POST /v1/auth/integrity-challenge`):** Apple'ın
  örnek akışı budur; ama token istemeyen yeni bir açık uç ve challenge
  tablosu gerektirir. OIDC nonce ve refresh token aynı tek kullanımlık
  güvenceyi zaten verir.
- **Hemen zorlamak:** Eşiğin kaç kullanıcıyı dışarıda bırakacağı bilinmeden
  pilot kulübü kilitleme riski.
- **`MEETS_BASIC_INTEGRITY` / `MEETS_DEVICE_INTEGRITY`:** Daha çok cihaz
  geçer; platform sahibi daha sıkı eşiği seçti, telemetri sonrası yeniden
  bakılacak.
- **Kararı iç servislerde de denetlemek:** Bütünlük yetki değil; tek
  noktada (gateway) zorlamak yeterli ve iç servislerin token doğrulaması
  zaten ihlal varsayımını karşılıyor.
