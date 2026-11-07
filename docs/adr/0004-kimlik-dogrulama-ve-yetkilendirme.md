# ADR-0004: Kimlik doğrulama ve yetkilendirme

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Revizyon:** 2026-11-07, Notion'daki proje planı ve veri modeli v13 ile
  hizalandı: platform admin tasarımı ADR-0008'e taşındı, rol hiyerarşisi,
  rate limit ve token yeniden kullanım ayrıntıları eklendi.
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0008](0008-kvkk-ve-platform-yonetimi.md)

## Bağlam

Tayfam'a yalnızca Google ve Apple ile giriş yapılır; şifre yoktur. Hedef,
güvenlik önlemlerinin kullanıcıya görünmez olmasıdır: sık giriş isteme, davet
kodu veya ek adımlar olmamalı. Mobil uygulama ve web istemcisi aynı API'yi
kullanır. Kimlik bilgisinin sahibi identity servisidir; dış istekleri ise
gateway karşılar.

## Karar

### Giriş akışı

1. İstemci, Google veya Apple SDK'sı ile bir **ID token** alır. İstek, istemcinin
   ürettiği bir `nonce` ile yapılır.
2. İstemci bu token'ı `POST /auth/sign-in` ile gateway'e gönderir; gateway
   isteği identity'ye iletir.
3. identity ID token'ı doğrular: sağlayıcının JWKS anahtarlarıyla imza, `iss`,
   `aud` (bizim client ID'lerimiz), `exp` ve `nonce`.
4. Kullanıcı, sağlayıcının değişmez kimliği (`provider` + `sub`) ile bulunur
   veya oluşturulur. E-posta kimlik olarak kullanılmaz.
5. identity yeni bir **oturum** açar ve erişim token'ı ile refresh token
   döner.

Ek kurallar:

- Hesaplar `users` ve `auth_identities (provider, subject)` tablolarıyla
  modellenir; bir kullanıcıya ileride birden fazla sağlayıcı bağlanabilir.
- Aynı e-postaya sahip farklı sağlayıcı hesapları **otomatik birleştirilmez**.
  Hesap bağlama, oturum açmış kullanıcının açıkça yaptığı bir işlem olacak.
- Apple ad ve e-postayı yalnızca ilk girişte verir; bu bilgiler o anda
  kaydedilir. Apple'ın gizli e-posta adresleri (private relay) normal e-posta
  gibi kabul edilir.

### Token'lar

**Erişim token'ı**

- JWT, **Ed25519 (EdDSA)** ile imzalı, ömrü **15 dakika**.
- İçeriği: `sub` (user_id), `sid` (oturum ID), `iss`, `aud`, `iat`, `exp`;
  başlıkta `kid`.
- Rol veya yetki bilgisi taşımaz.

**Refresh token**

- Rastgele 256 bit, opak bir değer. Veritabanında yalnızca SHA-256 hash'i
  saklanır.
- **Her kullanımda yenilenir** (rotation): eski token geçersiz olur, yenisi
  verilir.
- Zaten kullanılmış bir refresh token tekrar gelirse token çalınmış kabul
  edilir ve **o oturumun tamamı iptal edilir** (`revoked_reason =
  'token_reuse'`). Kontrol ve yenileme tek transaction'da `SELECT … FOR UPDATE`
  ile yapılır. Aynı anda gelen meşru çift istekler için birkaç saniyelik
  tolerans penceresi uygulanır.
- Oturum 90 gün kullanılmazsa sona erer. Uygulama düzenli kullanıldıkça
  kullanıcıdan tekrar giriş istenmez.

**Oturumlar**

- Her cihaz ayrı bir oturumdur. Kullanıcı oturumlarını listeleyip
  kapatabilir; çıkış yapmak oturumu iptal eder.
- Hesap silindiğinde kullanıcının tüm oturumları iptal edilir.

**Anahtar yönetimi**

- İmzalama anahtarlarının sahibi identity'dir. Açık anahtarlar gRPC üzerinden
  JWKS olarak sunulur.
- Anahtar değiştirildiğinde eski açık anahtar, onunla imzalanmış son token'ın
  süresi dolana kadar yayında kalır.

### İstemci tarafında saklama

- **Mobil:** Token'lar işletim sisteminin güvenli deposunda tutulur
  (iOS Keychain, Android Keystore).
- **Web:** Token'lar JavaScript'in erişemeyeceği çerezlerde tutulur:
  `HttpOnly`, `Secure`, `SameSite=Lax`. Refresh token çerezinin yolu
  `/auth/refresh` ile sınırlıdır.
- Web'de CSRF'e karşı, durum değiştiren her istek özel bir başlık ister
  (`X-Tayfam-Client`). Tarayıcılar bu başlığı başka bir siteden gelen formlara
  ekleyemez.

### Doğrulama ve kimliğin servislere iletilmesi

- gateway, erişim token'ını identity'ye sormadan, önbelleğindeki açık
  anahtarlarla kendisi doğrular.
- Doğrulanan `user_id` ve `session_id`, gRPC metadata ile iç servislere iletilir.
  Servisler arası zincirleme çağrılarda (chat → core) bu bilgi aynen aktarılır.
- İç servisler bu metadata'ya güvenir; bu yüzden iç servislere yalnızca izin
  verilen servislerden erişilebilmelidir. Bu, Kubernetes NetworkPolicy ile
  sağlanır. Servisler arası mTLS dağıtım ADR'sinde değerlendirilecek.

### WebSocket kimlik doğrulaması

- İstemci önce `POST /realtime/ticket` ile **30 saniye geçerli, tek kullanımlık**
  bir bilet alır.
- WebSocket bağlantısı bu biletle açılır. Böylece erişim token'ı URL'de taşınmaz
  ve web ile mobil aynı yolu kullanır.
- Erişim token'ı süresi dolsa da açık bağlantı devam eder; oturum iptal
  edilirse sunucu bağlantıyı kapatır.

### Yetkilendirme

- **Tayfa rolleri** (admin, moderatör, üye) core'da tutulur ve core'un `app`
  katmanında, her işlem için kontrol edilir. Varsayılan cevap "hayır"dır: izin
  açıkça tanımlanmamışsa işlem reddedilir.
- Rol-işlem eşleşmesi kodda tek bir izin tablosunda tanımlanır ve testlerle
  korunur.
- **Tayfa kapsamı:** Tayfaya ait her sorgu `tayfa_id` ve üyelik kontrolüyle
  çalışır (IDOR'a karşı). Tayfada kapalı olan bir modülün (`tayfa_modules`)
  uçları isteği reddeder.
- **Rol hiyerarşisi:** admin (3) › moderatör (2) › üye (1). Bir kişi yalnızca
  kendinden düşük roldeki üyeyi susturabilir, banlayabilir, çıkarabilir veya
  rolünü değiştirebilir; kimse kendinden yüksek rol atayamaz. Kural uygulama
  katmanında uygulanır ve `memberships` trigger'ıyla veritabanında da
  doğrulanır (işlemi yapan kişi transaction'da `SET LOCAL app.actor_id` ile
  iletilir). Son yönetici koruması da aynı trigger'dadır.
- chat, kanal erişimi ve birebir mesaj izni için core'a sorar (ADR-0002,
  ADR-0009).
- **Platform admin** ayrı bir kimlik doğrulama katmanıyla korunur ve normal
  uygulama token'ı ile erişilemez. Ayrıntılar
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md)'dedir.
- **Platform yaptırımları:** Platform genelinde askıya alınan veya banlanan
  kullanıcının tüm oturumları kapatılır ve yeni giriş yapamaz; kontrol
  identity'de giriş ve token yenileme sırasında yapılır.

### Kötüye kullanıma karşı

- Rate limit gateway'de, bellek içi token bucket ile uygulanır. Tek gateway
  kopyası olduğu için bu yeterlidir; yeniden başlatmada sayaçların sıfırlanması
  kabul edilmiştir. Gün bazlı limitler (DM isteği, arkadaşlık isteği gibi)
  veritabanından sayılır ve ilgili serviste uygulanır.
- Gerçek istemci IP'si `CF-Connecting-IP` başlığından okunur. Bu başlığa
  yalnızca cloudflared'den gelen isteklerde güvenilir.
- Mobil operatörlerin CGNAT kullanması nedeniyle IP bazlı limitler yalnızca
  düşük hacimli işlemlerde (giriş, tayfa açma) kullanılır; diğer limitler
  kullanıcı bazlıdır.
- Giriş, token yenileme ve oturum iptali denetim kaydına (audit log) yazılır.

## Gerekçe

- Kısa ömürlü erişim token'ı, gateway'in her istekte identity'ye gitmeden
  doğrulama yapmasını sağlar; çalınan bir token'ın etkisini en fazla 15 dakika
  ile sınırlar.
- Refresh token rotation ve tekrar kullanım tespiti, uzun süreli oturumları
  kullanıcıyı rahatsız etmeden güvenli tutar.
- Rollerin token'da değil core'da olması, bir rol değişikliğinin hemen etkili
  olmasını sağlar.
- Ed25519 küçük anahtarlı, hızlı ve yanlış kullanımı zor bir imza algoritmasıdır.

## Sonuçlar

- identity bir sağlayıcı doğrulama katmanı, oturum tablosu ve anahtar yönetimi
  içerir.
- Web istemcisi ile API'nin aynı site altında olması gerekir (örneğin
  `app.tayfam.app` ve `api.tayfam.app`); `SameSite` çerezleri buna dayanır.
- Erişim token'ı iptal edilemez; oturum iptal edildiğinde veya kullanıcı
  banlandığında en fazla 15 dakika daha geçerli kalabilir. Açık WebSocket
  bağlantıları ise hemen kapatılır (ADR-0005).

## Değerlendirilen alternatifler

- **Yalnızca sunucu tarafı oturum (her istekte veritabanı kontrolü):** Anında
  iptal sağlar, ama her istekte identity'ye çağrı gerektirir.
- **Rolleri token'a koymak:** Daha az servis çağrısı, ama rol değişiklikleri
  token süresi dolana kadar gecikir ve token büyür.
- **Web'de token'ları localStorage'da tutmak:** Daha basit, ama XSS durumunda
  token'lar çalınabilir.
