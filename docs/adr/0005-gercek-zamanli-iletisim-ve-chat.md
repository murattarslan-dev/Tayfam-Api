# ADR-0005: Gerçek zamanlı iletişim ve chat

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md)

## Bağlam

Tayfam'da iki tür mesajlaşma var:

- **Kanallar:** Tayfa adminlerinin açtığı, üyelerin yazdığı grup sohbetleri.
  Moderasyon gerektirir.
- **Direkt mesajlar (DM):** İki kullanıcı arasında, uçtan uca şifreli, yalnızca
  metin.

Ayrıca yeni etkinlik veya ilan gibi olayların açık istemcilere anında
ulaşması, kapalı istemcilere ise push bildirimi ile gitmesi gerekir. Bu
sorumlulukların tamamı chat servisindedir.

## Karar

### Bağlantı

- İstemci, `POST /realtime/ticket` ile bilet alır (ADR-0004). Bilet **chat
  tarafından üretilir** ve Postgres'te kısa ömürlü, tek kullanımlık olarak
  saklanır.
- WebSocket bağlantısı `GET /realtime?ticket=...` ile açılır. gateway bu yolu
  olduğu gibi chat'e aktarır (reverse proxy); bileti chat doğrular.
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
  koptuğunda da çalışır ve istemci tarafında tekrar denenebilir. Her mesaj
  istemcinin ürettiği bir ID taşır, böylece tekrar gönderim çift mesaj
  oluşturmaz.
- Çerçeve tipleri ve REST uçları OpenAPI spec'inde tanımlanır.

### Ölçekleme

- Başlangıçta chat **tek kopya** çalışır; tüm bağlantılar aynı süreçtedir.
- İkinci kopya gerektiğinde kopyalar arası dağıtım **Postgres LISTEN/NOTIFY**
  ile yapılır: mesajı kaydeden kopya bildirim yayınlar, ilgili bağlantıyı tutan
  kopya iletir. Bu durumda da mesaj kuyruğu eklenmez.

### Kanal mesajları

- Postgres'te **düz metin** saklanır; moderatörler ve adminler mesaj silebilir.
- Silme işlemi mesajı geri getirilemez şekilde temizler, yerinde yalnızca
  "mesaj silindi" kaydı kalır.
- Sayfalama UUIDv7 mesaj ID'leri üzerinden imleçle (cursor) yapılır.
- Kanal erişimi için üyelik ve rol bilgisi core'dan alınır ve kısa süre
  önbellekte tutulur (ADR-0002).

### Direkt mesajlar (uçtan uca şifreli)

- Sunucu, şifreleme protokolünden bağımsız tasarlanır. Protokol (cihaz başına
  basit anahtar çiftleri veya Signal protokolü) istemci tarafı ADR'sinde
  seçilir.
- Sunucu yalnızca şunları saklar:
  - Kullanıcının **cihazları** ve her cihazın açık anahtar materyali.
  - Her mesaj için, alıcı ve gönderenin diğer cihazlarına ayrı ayrı şifrelenmiş
    **zarflar** (envelope).
- Sunucu mesaj içeriğini hiçbir koşulda göremez ve çözemez.
- Zarf, hedef cihaz teslim aldığını bildirince silinir. Teslim edilemeyen
  zarflar 30 gün sonra silinir. DM geçmişi yalnızca cihazlarda yaşar; yeni bir
  cihaz eski mesajları göremez.
- Bir kullanıcının anahtarları değiştiğinde karşı tarafa bildirim gider;
  bunun arayüzde nasıl gösterileceği istemci ADR'sinde belirlenir.

### Diğer canlı olaylar

- core'daki olaylar (yeni etkinlik, yeni ilan, yeni soru) outbox ile chat'e
  iletilir (ADR-0002). chat bunları ilgili tayfanın bağlı üyelerine WebSocket
  üzerinden gönderir.

### Push bildirimleri

- Android ve iOS bildirimleri **FCM** (HTTP v1 API) ile gönderilir. iOS için
  APNs anahtarı Firebase'e tanımlanır.
- Cihazların push token'ları chat'te saklanır.
- Bildirim, kullanıcının o anda açık bir bağlantısı yoksa gönderilir.
- **DM bildirimleri içerik taşımaz:** yalnızca "yeni mesaj" ve gönderen bilgisi
  içerir.

## Gerekçe

- Canlı iletimi WebSocket'e, geçmişi ve senkronizasyonu REST'e bırakmak, kopan
  bağlantılarda mesaj kaybını önler ve WebSocket katmanını basit tutar.
- Biletin chat'te olması gateway'i durumsuz bırakır.
- Postgres LISTEN/NOTIFY, birkaç kopyaya kadar ek bileşen olmadan ölçeklenmeyi
  sağlar.
- DM zarflarının teslimden sonra silinmesi, sunucuda tutulan kişisel veriyi
  en aza indirir.

## Sonuçlar

- chat servisi bağlantı yönetimi, bilet, kanal mesajları, DM zarfları, cihaz
  anahtarları ve push bildirimlerini içerir; servislerin en karmaşığıdır.
- Kullanıcılar DM geçmişini yeni bir cihaza taşıyamaz.
- Firebase projesi ve APNs anahtarı gerekir.

## Değerlendirilen alternatifler

- **gateway'in WebSocket'i sonlandırıp chat ile gRPC stream kurması:** Daha
  fazla gRPC deneyimi, ama iki katman bağlantı yönetimi ve daha karmaşık hata
  durumları getirir.
- **`gorilla/websocket`:** Yaygın ve olgun, ama `coder/websocket` context
  desteği ve daha sade API'si nedeniyle tercih edildi.
- **DM'leri sunucuda şifreli olarak kalıcı saklamak:** Cihazlar arası geçmiş
  sağlar, ama anahtar yedekleme gibi karmaşık sorunlar getirir ve sunucuda veri
  biriktirir.
