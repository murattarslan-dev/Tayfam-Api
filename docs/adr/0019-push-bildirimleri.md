# ADR-0019: Push bildirimleri (FCM)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-11)
- **Tarih:** 2026-11-10
- **Revizyon:** 2026-11-11 (platform sahibi, sohbette): öneriler kabul
  edildi (60 gün token saklaması, `mentions` listesi, prod'da zorunlu FCM).
  İki değişiklik: `hike_overdue` alıcı bağlıyken de push alır (madde 2);
  chat'in internet çıkışı push açılana kadar eklenmez (madde 8, en az
  yetki).
- **Revizyon:** 2026-11-11 (platform sahibi, sohbette; issue #138): birebir
  konuşma tercihinde seviye yalnızca `all` ya da `none` olabilir
  (`mentions` uçta ve veritabanında reddedilir). Madde 3'teki `mentions`
  listesinde `dm_message` ve `dm_request` durur ama `dm_conversation`
  kapsamında artık ulaşılmaz.
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md) (bu ADR "Bildirimler" bölümünü uygular), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md) (`push_tokens`), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0015](0015-veritabani-rolleri.md), [ADR-0018](0018-birebir-mesaj-e2e-sunucu-tarafi.md) (cihaz = oturum); issue #134
- **Kaynak:** Notion: Veri Modeli (taslak v14) "Bildirimler", "Senaryo Kuralları → Bildirimler"

## Bağlam

ADR-0005 chat'i bildirim merkezi yapar: uygulama içi liste
(`notifications`) olayla aynı transaction'da yazılır; push, alıcının açık
bağlantısı yoksa FCM HTTP v1 ile gider; tercihler push'tan önce okunur ve
`hike_overdue` sessize alınamaz; DM push'ları içerik taşımaz. Liste
(issue #64) ve tercihler var; push yok. Açık kalanlar: push token'ının
kimliğe nasıl bağlanacağı (ADR-0009 `push_tokens.device_id` der, ama
token'da cihaz kimliği yoktur), gönderimin yeniden başlatmaya nasıl
dayanacağı, metin taşımayan bir API'nin push'u nasıl göstereceği, yeniden
deneme, saklama ve FCM kimlik bilgisinin nasıl tutulacağı. Firebase projesi
henüz yoktur.

## Karar

### 1. Token = oturum

- Push token'ı, onu kaydeden **oturuma** (erişim token'ının `sid`'i)
  bağlıdır; oturum başına bir token vardır (`push_tokens.session_id`
  birincil anahtar). ADR-0018'deki "cihaz = oturum" ile aynı gerekçe:
  identity her girişte yeni cihaz ve oturum açar; ADR-0009'daki `device_id`
  yerine `session_id` tutulur, identity'ye değişiklik gerekmez.
- Oturum biterse token silinir: `session.revoked` (çıkış, başka cihazdan
  kapatma, token yeniden kullanımı), `user.sanctioned` (kullanıcının tüm
  token'ları) ve `user.deleted` (hesap silme temizliği). Olay yolları E2E
  cihazlarınınkiyle aynıdır (`userevents`).
- Biten oturum, elindeki erişim token'ı (≤ 15 dk) geçerli kalsa da token
  kaydedemez: chat'in biten oturum kayıtları (`e2e_ended_sessions`, bir gün)
  ve yaptırım anı (`user_e2e_state.sessions_ended_at`, token'ın `iat`'ıyla)
  E2E ile **paylaşılır**. Tablo adları korunur (yeniden adlandırma yalnızca
  isim kazandırır). Kayıt ile oturum bitişi/yaptırım arasındaki yarış
  Postgres advisory kilidiyle kapanır (kayıt kullanıcıyı ve oturumu,
  oturum bitişi oturumu, yaptırım kullanıcıyı kilitler).
- Aynı FCM token'ı yalnızca bir oturumda durur (UNIQUE): cihaz başka
  hesapla giriş yaparsa eski oturumun kaydı silinir, eski hesabın
  bildirimleri o cihaza gitmez.
- Kullanıcı başına en çok **10** token; fazlası en eski yenilenenden
  silinir. Platform `android | ios` (ADR-0017: yalnızca mobil).
- REST: `PUT /v1/me/push-token` (kaydet/yenile), `DELETE /v1/me/push-token`
  (çıkışta). İstemci her açılışta ve FCM token'ı yenilediğinde `PUT` eder.

### 2. Kuyruk ve gönderim

- Gönderim işi **bildirim kaydıyla aynı transaction'da** açılır:
  `notifications` üzerindeki trigger her yeni satır için `push_queue`'ya
  bir iş yazar. Birebir mesaj bildirimi tek satırda sayaçla güncellendiği
  için (issue #101) okunmamış sayacın güncellenmesi de iş açar; bekleyen iş
  varsa birleşir. Kod yolu ne olursa olsun yazılan her bildirim bir iş
  doğurur; tekrar gelen olay (`ON CONFLICT DO NOTHING`) iş doğurmaz.
- Kaydı yazan transaction push'u beklemez. chat'in `push` işçisi kuyruğu
  2 saniyede bir yoklar, zamanı gelen işleri **kiralar** (deneme sayısı
  artar, iş 2 dakika başkasına görünmez; `FOR UPDATE SKIP LOCKED`) ve en
  çok 8 işi aynı anda işler. İşçi düşerse iş kira bitince yeniden görünür:
  yeniden başlatma iş kaybettirmez. Bildirim silinince işi de gider
  (`ON DELETE CASCADE`).
- Gönderim anında sırayla: bildirim okunduysa iş biter; alıcının **açık
  WebSocket bağlantısı varsa** push gitmez (bildirim zaten canlı iletildi);
  tercih susturuyorsa push gitmez (madde 3); token yoksa iş biter. Aksi
  halde kullanıcının token'larına (en çok 10) birer FCM mesajı gider.
- **İstisna: `hike_overdue`** (yürüyüşten dönmeme, güvenlik uyarısı) alıcı
  bağlıyken de push olarak gider: uygulama açık ama ekranda olmayabilir,
  kullanıcı uyarıyı kaçırmamalıdır (platform sahibi, 2026-11-11). Tercih
  de onu engellemez (madde 3).
- Açık bağlantı bilgisi chat sürecinin belleğindedir (realtime hub); chat
  tek kopya çalışır (ADR-0005 "Ölçekleme"). İkinci kopyada bu bilgi
  LISTEN/NOTIFY ya da kısa ömürlü bir varlık tablosuyla paylaşılmalıdır.
- 24 saatten eski iş gönderilmez ve saklama işiyle silinir (push kapalıyken
  biriken işler dahil).

### 3. Tercihler

- `notification_preferences` gönderimden önce okunur; liste etkilenmez.
  Kapsam: DM bildirimlerinde `dm_conversation` (hedef konuşma), tayfaya ait
  diğer bildirimlerde `tayfa`. Bugün hiçbir bildirim kanala bağlı olmadığı
  için `channel` kapsamı okunmaz (kanal bildirimi eklenince eklenir).
- Etkin seviye: tercih yoksa ya da `muted_until` geçtiyse `all`; aksi halde
  satırdaki seviye. `none` push'u durdurur. `mentions` yalnızca alıcıya
  kişisel bildirimleri geçirir: `mention`, `answer_received` (kendi
  sorusu), `rsvp_promoted`, `join_approved`, `tayfa_invitation`,
  `report_resolved`, `dm_message`, `dm_request` (platform sahibi kabul
  etti, 2026-11-11).
- `hike_overdue` hiçbir tercihle engellenemez (v14).

### 4. İçerik: çeviri anahtarı ve kimlikler

API Türkçe metin döndürmez (CLAUDE.md). Push görünür bildirimdir ama metni
istemcinin çeviri dosyasından gelir:

- Android `notification.title_loc_key` / `body_loc_key`, iOS APNs
  `alert.title-loc-key` / `alert.loc-key`: `push_<type>_title`,
  `push_<type>_body` (ör. `push_event_created_title`). Çeviri argümanı
  (`loc-args`) **yoktur**: push'ta ad, başlık, mesaj metni, konum asla
  bulunmaz. İstemci ayrıntıyı uygulama açılınca kimliklerle çeker.
- `data` (string → string, yalnızca kimlik ve sınıflandırma):
  `notification_id`, `type`, `target_type`, `target_id`, varsa `tayfa_id`,
  `question_id` (`answer_received`), `subject_user_id` (`hike_overdue`),
  `reason` (`tayfa_warned`).
- **DM push'ları** (`dm_message` "Yeni mesaj", `dm_request` "Yeni mesaj
  isteği") içerik, gönderen ve okunmamış sayısı taşımaz; yalnızca
  konuşmanın kimliği.
- Gruplama: Android `tag` ve iOS `thread-id` / `apns-collapse-id` hedef
  kimliğidir (aynı konuşmanın mesajları tek bildirimde birleşir). Android
  kanalı: `tayfam_messages` (DM), `tayfam_alerts` (`hike_overdue`),
  `tayfam_general` (diğerleri); kanalları istemci açar. Öncelik: DM ve
  `hike_overdue` yüksek (iOS `hike_overdue` için `time-sensitive`), diğerleri
  normal. Geçerlilik (TTL) 24 saat.
- Platform yalnızca kendi bloğunu alır (`android` ya da `apns`).

### 5. Yeniden deneme ve geçersiz token

- Geçici hata (ağ, 429, 5xx, FCM `UNAVAILABLE`/`INTERNAL`/`QUOTA_EXCEEDED`,
  erişim token'ının reddi): en çok **5 deneme**, üstel geri çekilme
  (30 sn, 1, 2, 4 dk; üst sınır 30 dk), FCM `Retry-After` verirse ondan az
  beklenmez. Yalnızca başarısız olan oturumların token'larına yeniden
  denenir (`push_queue.session_ids`); başarılı olanlara tekrar gitmez.
- Token'a özgü kalıcı hata: `UNREGISTERED` (404), `SENDER_ID_MISMATCH`
  (403) ve alan ihlali `message.token` olan `INVALID_ARGUMENT` (400) →
  token silinir (oturum hâlâ aynı token'la kayıtlıysa). Diğer
  `INVALID_ARGUMENT` ve 4xx'ler mesajın kalıcı hatasıdır: o token için
  denenmez, token silinmez, yalnızca hata kodu log'a yazılır.

### 6. FCM istemcisi: yalnızca standart kütüphane

- Servis hesabı anahtarıyla OAuth 2.0 JWT bearer akışı: RS256 imzalı
  (`crypto/rsa.SignPKCS1v15`, SHA-256) kısa ömürlü bir JWT
  `https://oauth2.googleapis.com/token`'a verilir, dönen erişim token'ı
  (1 saat) süresi dolmadan 5 dakika önceye kadar bellekte tutulur ve
  `https://fcm.googleapis.com/v1/projects/<proje>/messages:send`'e
  gönderilir. Kapsam yalnızca `firebase.messaging`.
- **Bu, "kendi kriptografini yazma" (kural 4) ihlali değildir:** imza
  standart kütüphanenin RSA PKCS#1 v1.5 uygulamasıdır; yazılan yalnızca
  JWT'nin (RFC 7519) JSON çerçevesidir. `firebase.google.com/go` ve
  `golang.org/x/oauth2/google` onlarca dolaylı bağımlılık getirir; bu iş
  için iki HTTP isteği yeter.
- Uç adresleri koddadır, yapılandırmadan gelmez (servis hesabı JSON'undaki
  `token_uri` farklıysa yapılandırma reddedilir); yönlendirme izlenmez;
  istek süresi 10 sn. Böylece ele geçirilmiş bir yapılandırma anahtarı
  başka bir adrese gönderemez.

### 7. Yapılandırma

- `CHAT_FCM_SERVICE_ACCOUNT`: servis hesabı JSON'u, `chat-fcm` secret'ından
  (`deploy/apps/<ortam>/secrets/`, SOPS; ADR-0006). Secret referansı
  `optional: true`'dur; `scripts/check-secret-refs.sh` isteğe bağlı
  referansları eksik saymaz.
- Değer yoksa push **kapalıdır**: servis başlar, bilgi log'u yazılır, token
  kaydı yine çalışır (istemci bilmek zorunda değildir), kuyruk 24 saatte
  temizlenir. Bozuk değer servisin başlamasını engeller.
- **Karar (platform sahibi, 2026-11-11):** Firebase projesi kurulunca
  prod'da FCM zorunludur; dev'de isteğe bağlı kalır. Uygulama, prod
  secret'ı eklendiği PR'da yapılır (bugün prod'da secret yok, prod
  kırılmaz): prod overlay'de `chat-fcm` referansından `optional`
  kaldırılır ve `CHAT_FCM_REQUIRED=true` verilir. chat,
  `CHAT_FCM_REQUIRED=true` iken değer yoksa başlamaz; ayrıca
  `TAYFAM_ENV=prod` ve `CHAT_FCM_REQUIRED=true` olmadan açılırsa uyarı
  log'u yazar. Adımlar `docs/runbooks/sops.md` "Push (FCM) servis
  hesabı"ndadır.

### 8. Ağ

- **Push kapalıyken chat'in internete çıkışı yoktur** (en az yetki;
  platform sahibi, 2026-11-11). `deploy/base`'de chat için internet kuralı
  bulunmaz; FCM ve Google OAuth uçlarına çıkış, ortamın `chat-fcm`
  secret'ının eklendiği PR'da **o ortamın** overlay'ine eklenen
  `chat-egress-internet` NetworkPolicy'siyle açılır. Kopyalanabilir YAML ve
  eklenecek dosyalar `docs/runbooks/sops.md` "Push (FCM) servis
  hesabı"ndadır.
- Kural: chat'ten internete yalnızca TCP 443; cluster içi ve özel adresler
  hariç (identity ve gateway kurallarıyla aynı daraltma). NetworkPolicy
  alan adıyla sınırlayamaz (Google uçları anycast); alan adı sınırı
  istemcidedir (madde 6).
- `scripts/netpol-test.sh` chat'in çıkışını politikanın varlığına göre
  dener: politika yoksa internet kapalı, varsa yalnızca 443 açık
  beklenir.

### 9. Saklama

| Kayıt | Ne zaman silinir |
|-------|------------------|
| Push token'ı | Oturum bitince; çıkışta; FCM geçersiz deyince; **60 gün** yenilenmezse; hesap silinince |
| Gönderim işi | Gönderilince ya da vazgeçilince; 5 denemeden sonra; 24 saat sonra; bildirim silinince |

Saatlik `push-retention` işi.

### 10. Gizlilik ve log

Push token'ı, FCM erişim token'ı, servis hesabı anahtarı, imzalı JWT ve
FCM cevap gövdesi log'a, metriğe, olaya ve hata mesajına girmez. Log
yalnızca sayılar, HTTP durum kodu ve FCM hata kodunu (`UNREGISTERED` gibi)
taşır. Bu, sahte FCM sunucusuyla tüm yolları log yakalayarak geçen bir
testle sabittir.

## Gerekçe

- Token'ı oturuma bağlamak, mevcut olaylarla her çıkışta token'ın
  silinmesini sağlar; çalınan bir erişim token'ı başka oturumun token'ını
  değiştiremez.
- Kuyruğu trigger'la kayıtla aynı transaction'a bağlamak "liste eksiksiz,
  push en az bir kez denenir" sözünü kod yollarından bağımsız yapar ve
  outbox'ın (ADR-0002) ayrı bir relay'ine gerek bırakmaz (iş aynı
  servisin içindedir).
- Push'ta metin yerine çeviri anahtarı, FCM'e (Google ve Apple
  altyapısına) kişisel içerik ve ad vermez; DM'in uçtan uca şifreli olma
  sözü push'ta da bozulmaz.

## Sonuçlar

- chat yeni bir uygulama paketi (`internal/chat/app/push`), FCM istemcisi
  (`internal/chat/app/push/fcm`), gRPC servisi (`PushService`) ve gateway'de
  `/v1/me/push-token` kazanır.
- ADR-0009'daki `push_tokens (device_id, push_token)` bu ADR'ye göre
  `session_id` taşır; Notion veri modeli v14'e not düşülmelidir (platform
  sahibi).
- Platform sahibinin işi: Firebase projesi, Android ve iOS uygulamaları,
  APNs anahtarı, yalnızca FCM gönderebilen bir servis hesabı, JSON'unun
  sops ile `chat-fcm` secret'ına şifrelenmesi ve aynı PR'da o ortamın
  `chat-egress-internet` kuralı (prod'da ayrıca zorunluluk).
- DM sohbetini sessize alma ucu ayrı bir iştir; push tercihi
  `dm_conversation` kapsamını şimdiden okur.
- İstemci: çeviri dosyalarına `push_<type>_title/_body` anahtarları ve
  Android bildirim kanalları.

## Değerlendirilen alternatifler

- **domain_events (outbox) üzerinden push:** Aynı dayanıklılık, ama olay
  servisler arası iletişim içindir ve relay'i başka servise çağrı yapar;
  push chat'in kendi işidir.
- **Bildirim satırında push durumu kolonları:** Ayrı tablo gerekmez, ama
  `notifications`'a yazma yetkisi ve 90 gün boyunca anlamsız kalan kolonlar
  getirir; DM sayacı güncellemesinde yeniden kurulması da zorlaşır.
- **Firebase Admin SDK / x/oauth2:** Hazır, ama bağımlılık ağacı büyük ve
  kullanılmayan yüzeyi geniş (madde 6).
- **Push token'ını şifreli saklamak:** Token tek başına push gönderemez
  (servis hesabı anahtarı gerekir); ek anahtar yönetimi kazancı aşar.
- **Push'ta yerelleştirilmiş metin:** API'nin metin döndürmeme kuralını
  bozar ve içerik sızdırır.
