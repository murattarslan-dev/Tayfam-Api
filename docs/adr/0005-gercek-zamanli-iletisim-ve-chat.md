# ADR-0005: Gerçek zamanlı iletişim ve chat

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Revizyon:** 2026-11-07 (uygulamaya geçmeden önce):
  - DM geçmişi cihazlarda değil, sunucuda şifreli arşivde tutulacak şekilde
    değiştirildi.
  - Notion'daki proje planı ve veri modeli v13 ile hizalandı: kanal mesajları
    şifreli saklanır, moderatör silmesi 30 gün geri alınabilir, E2E protokolü
    Signal'dir, birebir mesaj izin kuralları ve bildirim merkezi eklendi.
- **Revizyon:** 2026-11-09: "Bağlantı" bölümü
  [ADR-0014](0014-websocket-erisim-tokeniyla.md) ile değişti (bilet yerine
  erişim token'ı).
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md)

## Bağlam

Tayfam'da iki tür mesajlaşma var:

- **Kanallar:** Tayfa yöneticilerinin ve moderatörlerinin açtığı, üyelerin
  yazdığı grup sohbetleri. Moderasyon gerektirir. `staff` görünürlüklü
  kanalları yalnızca yönetici ve moderatörler görür.
- **Birebir mesajlar (DM):** İki kullanıcı arasında, uçtan uca şifreli, yalnızca
  metin.

Ayrıca yeni etkinlik veya duyuru gibi olayların açık istemcilere anında
ulaşması, kapalı istemcilere ise push bildirimi ile gitmesi gerekir. Uygulama
içi bildirim listesi de bu kapsamdadır. Bu sorumlulukların tamamı chat
servisindedir.

Mesajlar silinebilir ama düzenlenemez. Silinen mesaj "Bu mesaj silindi" olarak
görünür.

## Karar

### Bağlantı

> 2026-11-09: bilet kaldırıldı, bağlantı kullanıcının erişim token'ıyla
> açılır ([ADR-0014](0014-websocket-erisim-tokeniyla.md)).

- WebSocket bağlantısı `GET /v1/realtime` ile, kullanıcının erişim token'ıyla
  açılır: mobilde `Authorization: Bearer` başlığı, web'de `tayfam_at` çerezi;
  token URL'de taşınmaz. gateway Origin'i ve token'ı doğrular, isteği chat'e
  aktarır (reverse proxy) ve token'ı `Authorization` başlığıyla iletir (çerezi
  değil); chat token'ı kendisi yeniden doğrular.
- Token yalnızca bağlantının belleğinde tutulur. Abonelikte, gönderimde ve
  üyelik değişince yeniden denetimde core'a bu token'la sorulur.
- Bağlantı en geç token'ın süresi dolunca kapanır (4003); istemci token'ı
  yenileyip yeniden bağlanır.
- Bağlantı mantığı tamamen chat'tedir. WebSocket kütüphanesi olarak
  `github.com/coder/websocket` kullanılır.
- Sunucu 30 saniyede bir ping gönderir; yanıt vermeyen bağlantı kapatılır.
- Her bağlantının sınırlı bir gönderim tamponu vardır. Tamponu dolan (çok yavaş)
  istemcinin bağlantısı kapatılır; istemci yeniden bağlanıp kaçırdıklarını
  çeker.
- identity'den gelen "oturum iptal edildi" olayı (outbox, ADR-0002) o oturumun
  açık bağlantılarını kapatır.

### Mesaj protokolü

- WebSocket çerçeveleri JSON'dur: `{ "type": ..., "id": ..., "data": ... }`.
- WebSocket yalnızca **canlı iletim** içindir. Geçmiş mesajlar ve kaçırılan
  mesajlar REST ile, son görülen mesaj ID'sinden itibaren çekilir. Yeniden
  bağlanan istemci önce eksikleri REST ile tamamlar.
- Mesaj gönderme REST üzerinden de yapılabilir; böylece gönderim, bağlantı
  koptuğunda da çalışır ve istemci tarafında tekrar denenebilir.
- Her mesaj istemcinin ürettiği bir `client_request_id` taşır;
  `UNIQUE(yazar, client_request_id)` sayesinde tekrar gönderim çift mesaj
  oluşturmaz, mevcut kayıt döner. Birincil anahtar (UUIDv7) her zaman sunucuda
  üretilir; istemci saatine dayalı ID kabul edilmez.
- REST ve WebSocket gönderimleri aynı fonksiyondan geçer: yetki, susturma
  (`muted_until`), yavaş mod ve hız sınırları tek yerde kontrol edilir.
- Mesaj boyutu ve gönderim hızı sınırlıdır: kanal mesajı en fazla 4000
  karakter, DM zarfı en fazla 64 KB.
- Çerçeve tipleri ve REST uçları OpenAPI spec'inde tanımlanır.

### Ölçekleme

- Başlangıçta chat **tek kopya** çalışır; tüm bağlantılar aynı süreçtedir.
- İkinci kopya gerektiğinde kopyalar arası dağıtım **Postgres LISTEN/NOTIFY**
  ile yapılır: mesajı kaydeden kopya bildirim yayınlar, ilgili bağlantıyı tutan
  kopya iletir. Bu durumda da mesaj kuyruğu eklenmez.

### Kanal mesajları

- TLS ile taşınır ve veritabanında **uygulama seviyesinde şifreli** saklanır
  (`body_encrypted`): AES-256-GCM, anahtar Kubernetes secret'ından okunur.
  Şifreli kayıt anahtar sürümünü taşır; böylece anahtar yenilenebilir. Sunucu
  içeriği çözebilir, bu moderasyon için gereklidir.
- **Silme:**
  - Yazar kendi mesajını silerse içerik hemen boşaltılır ve geri alınamaz.
  - Moderatör veya yönetici silerse mesaj gizlenir ve 30 gün boyunca geri
    alınabilir; geri alma denetim kaydına yazılır. 30 gün sonra kalıcı silinir.
- **Yavaş mod:** `slow_mode_seconds` (0–3600); yönetici ve moderatörler muaf.
  Kullanıcının son mesaj zamanı bellek içinde tutulur.
- Sayfalama UUIDv7 mesaj ID'leri üzerinden imleçle (cursor) yapılır.
- Kanal erişimi için üyelik ve rol bilgisi core'dan alınır ve kısa süre
  önbellekte tutulur (ADR-0002). `staff` kanallarında rol kontrolü hem REST hem
  WebSocket aboneliğinde yapılır; moderatörlükten düşen kişinin açık aboneliği
  core'dan gelen olayla anında kesilir.

### Direkt mesajlar (uçtan uca şifreli)

- Protokol: **Signal** (X3DH/PQXDH + Double Ratchet). Kendi kripto şemamızı
  yazmıyoruz. Oturum durumu (ratchet) cihazlarda kalır. Dart/Flutter
  kütüphanesinin seçimi istemci tarafı ADR'sinde yapılır.
- Sunucu yalnızca açık anahtarları (kimlik anahtarı, imzalı ön anahtar, tek
  kullanımlık ön anahtarlar), şifreli zarfları, şifreli arşivi ve meta veriyi
  (konuşma, gönderen, zaman, okunma) tutar. Okunma bilgisi bilinçli kabul edilen
  bir meta veridir.
- Sunucu mesaj içeriğini hiçbir koşulda göremez ve çözemez. İstemcinin yerel
  veritabanı yalnızca önbellektir; DM geçmişinin kaynağı sunucudaki şifreli
  arşivdir.
- **Kim kime yazabilir:** Notion veri modeli v7'deki mesaj isteği modeli
  geçerlidir: ortak aktif tayfa veya arkadaşlık gerekir, engelleme her şeyi
  kapatır, ortak etkinlik geçmişi olmayanlar tek mesajlık istek atabilir. Bu
  kontrol için chat, core'a tek bir gRPC çağrısıyla sorar (ADR-0009).
- **Şikâyet:** Sunucu DM içeriğini göremediği için şikâyet eden kişi mesajı
  kendi cihazından açık metin olarak iletir.

**İki katman: canlı zarflar ve geçmiş arşivi**

1. **Zarflar (canlı iletim):** Her cihazın kendi anahtar çifti vardır. Gönderen
   cihaz mesajı alıcının ve kendi kullanıcısının **tüm kayıtlı cihazları** için
   ayrı ayrı şifreler. Zarf, hedef cihaz teslim aldığını bildirince silinir;
   teslim edilemeyen zarflar 30 gün sonra silinir. Böylece kayıtlı her cihaz,
   eşleştirilmiş olsun olmasın, yeni mesajları alır.
2. **Geçmiş arşivi:** Her kullanıcının yalnızca kendi cihazlarının bildiği
   simetrik bir **geçmiş anahtarı** vardır. Kullanıcının eşleştirilmiş cihazları,
   gönderdikleri ve aldıkları her mesajı bu anahtarla şifreleyip kullanıcının
   kendi arşivine yazar. Sunucu arşivi saklar ama çözemez. Arşiv, kullanıcı
   silene veya hesap silinene kadar tutulur.

Her kullanıcının arşivi kendine aittir: gönderen ve alıcı aynı mesajı kendi
arşivlerine kendi anahtarlarıyla yazar.

**Yeni cihaz ve geçmiş senkronizasyonu**

- Yeni cihaz girişte kendi anahtar çiftini üretir ve kaydolur; bu andan itibaren
  yeni mesajları zarflarla alır. **QR okutmak zorunlu değildir.**
- Kullanıcının başka eşleştirilmiş cihazı varsa, yeni cihazda bir banner
  gösterilir: "Eski mesajlarını görmek için diğer cihazından QR'ı okut."
- QR'ı yeni cihaz gösterir; eşleştirilmiş cihaz okutur. QR, yeni cihazın açık
  anahtarını ve tek kullanımlık bir doğrulama kodunu içerir. Eşleştirilmiş cihaz
  geçmiş anahtarını yeni cihazın açık anahtarıyla şifreleyip sunucu üzerinden
  iletir. Bundan sonra yeni cihaz arşivi indirebilir ve arşive yazabilir.
- Eşleştirilmemiş cihaz arşive yazamaz; aldığı mesajlar eşleştirilmiş cihazlar
  tarafından arşive eklenir.
- Kullanıcının hiç eşleştirilmiş cihazı kalmamışsa (tüm cihazlar kaybolmuş veya
  kapatılmış), yeni cihaz yeni bir geçmiş anahtarı üretir. Eski arşiv
  okunamaz hale gelir; kullanıcıya bu açıkça bildirilir. İleride bir kurtarma
  kodu ile bu durum çözülebilir.

**Anahtar yenileme**

- Bir cihaz kaldırıldığında veya kaybolduğunda geçmiş anahtarı yenilenir: yeni
  kayıtlar yeni anahtarla şifrelenir, yeni anahtar kalan eşleştirilmiş cihazlara
  dağıtılır. Anahtarlar sürüm numarası taşır; eski kayıtlar eski sürümle
  okunmaya devam eder.
- Cihaz eklenince, çıkarılınca veya kimlik anahtarı değişince
  `user_key_events`'e kayıt düşer; karşı tarafın istemcisi DM'de "Güvenlik kodu
  değişti" uyarısı gösterir. Güvenilen anahtarlar ve "doğrulandı" bilgisi
  yalnızca istemcide tutulur.
- Web istemcisinde özel anahtarlar WebCrypto ile dışa aktarılamaz
  (non-extractable) şekilde saklanır.

### Diğer canlı olaylar

- core'daki olaylar (yeni etkinlik, duyuru, soru, katılım onayı) outbox ile
  chat'e iletilir (ADR-0002). chat bunları ilgili tayfanın bağlı üyelerine
  WebSocket üzerinden gönderir.

### Bildirimler

- chat, **bildirim merkezidir**: uygulama içi bildirim listesi
  (`notifications`), bildirim tercihleri ve push gönderimi buradadır. Diğer
  servisler bildirim isteğini outbox ile iletir.
- Uygulama içi bildirim kaydı, olayla aynı transaction'da yazılır; push
  gönderimi başarısız olsa bile liste eksiksiz kalır.
- Push öncesi bildirim tercihleri kontrol edilir; sessize alınmış olsa da
  bildirim listede görünür. `hike_overdue` (yürüyüşten dönmeme) sessize alma ile
  engellenemez.
- Android ve iOS bildirimleri **FCM** (HTTP v1 API) ile gönderilir. iOS için
  APNs anahtarı Firebase'e tanımlanır.
- Push, kullanıcının o anda açık bir bağlantısı yoksa gönderilir.
- **DM bildirimleri içerik taşımaz:** yalnızca "Yeni mesaj" veya "Yeni mesaj
  isteği" bilgisi gider; `payload`'a DM içeriği asla yazılmaz.
- Bildirim kayıtları 90 gün sonra silinir.

## Gerekçe

- Canlı iletimi WebSocket'e, geçmişi ve senkronizasyonu REST'e bırakmak, kopan
  bağlantılarda mesaj kaybını önler ve WebSocket katmanını basit tutar.
- Bağlantının chat'te olması gateway'i durumsuz bırakır.
- Postgres LISTEN/NOTIFY, birkaç kopyaya kadar ek bileşen olmadan ölçeklenmeyi
  sağlar.
- Canlı zarflar ile geçmiş arşivinin ayrılması, eşleştirme yapılmamış bir
  cihazın da yeni mesajları almasını; eşleştirilmiş cihazların ise web dahil
  aynı DM kutusunu görmesini sağlar. Kullanıcıdan şifre veya PIN istenmez.

## Sonuçlar

- chat servisi bağlantı yönetimi, kanal mesajları, DM zarfları ve
  arşivi, cihaz anahtarları ve bildirim merkezini içerir; servislerin en
  karmaşığıdır.
- Uygulama sırası (Notion faz planı): önce kanallar ve düz birebir mesaj
  çalışır hale gelir, E2E katmanı modülün son adımı olarak eklenir.
- DM şifreleme mantığının büyük kısmı istemcidedir (zarf üretimi, arşivleme,
  QR eşleştirme, anahtar yenileme). Sunucu tarafı cihaz kaydı, zarf kutusu,
  arşiv ve eşleştirme mesajlarının iletimi ile sınırlıdır.
- Tüm cihazlarını kaybeden kullanıcı, kurtarma kodu eklenene kadar DM geçmişini
  kaybeder.
- Firebase projesi ve APNs anahtarı gerekir.

## Değerlendirilen alternatifler

- **gateway'in WebSocket'i sonlandırıp chat ile gRPC stream kurması:** Daha
  fazla gRPC deneyimi, ama iki katman bağlantı yönetimi ve daha karmaşık hata
  durumları getirir.
- **`gorilla/websocket`:** Yaygın ve olgun, ama `coder/websocket` context
  desteği ve daha sade API'si nedeniyle tercih edildi.
- **DM geçmişini yalnızca cihazlarda tutmak (Signal modeli):** En basit ve en
  gizli seçenek, ama yeni cihaz boş başlar ve web istemcisinde tarayıcı verisi
  silinince geçmiş kaybolur.
- **E2E olmadan, sunucu anahtarıyla şifreleme:** Her cihazda anında tutarlı
  kutu sağlar, ama sunucu mesajları okuyabilir; uçtan uca şifreleme sözü
  karşılanmaz.
- **Geçmişi zorunlu QR eşleştirmesine bağlamak:** Daha basit durum yönetimi,
  ama yeni cihaz eklemeyi gereksiz yere zorlaştırır.
- **Geçmiş anahtarını PIN veya parola ile korumak:** Eşleştirme gerektirmez,
  ama kullanıcıya ek adım getirir ve düşük entropili PIN'ler özel donanım
  koruması olmadan kaba kuvvete açıktır.
