# ADR-0004: Kimlik doğrulama ve yetkilendirme

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md)

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

- Hesaplar `users` ve `user_identities (provider, subject)` tablolarıyla
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
  edilir ve **o oturumun tamamı iptal edilir**.
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
- chat, kanal erişimi için core'a üyelik ve rol bilgisini sorar (ADR-0002).
- **Platform admin** yetkisi identity'de kullanıcı üzerinde bir bayraktır.
  Token'da taşınmaz; `/admin` altındaki her istekte gateway bu yetkiyi
  identity'ye canlı olarak sorar. Bu yetki API üzerinden verilemez, yalnızca
  sunucuda çalıştırılan bir komutla atanır.

### Kötüye kullanıma karşı

- gateway, giriş ve token yenileme uçlarında IP ve kullanıcı bazlı rate limit
  uygular.
- Giriş, token yenileme, oturum iptali ve admin işlemleri denetim kaydına
  (audit log) yazılır.

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
- Erişim token'ı iptal edilemez; oturum iptal edildiğinde en fazla 15 dakika
  daha geçerli kalabilir. Admin işlemleri canlı kontrol edildiği için bu
  durumdan etkilenmez.

## Değerlendirilen alternatifler

- **Yalnızca sunucu tarafı oturum (her istekte veritabanı kontrolü):** Anında
  iptal sağlar, ama her istekte identity'ye çağrı gerektirir.
- **Rolleri token'a koymak:** Daha az servis çağrısı, ama rol değişiklikleri
  token süresi dolana kadar gecikir ve token büyür.
- **Web'de token'ları localStorage'da tutmak:** Daha basit, ama XSS durumunda
  token'lar çalınabilir.
