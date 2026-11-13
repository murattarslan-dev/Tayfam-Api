# ADR-0002: Servis sınırları ve veri sahipliği

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Arslan
- **İlgili:** [ADR-0001](0001-mikroservis-mimarisi.md)

## Bağlam

ADR-0001 ile mikroservis mimarisine karar verildi. Servis sayısını düşük tutma
ilkesi gereği, sınırlar net ayrışan sorumluluklara göre çizilmelidir. Ayrıca
servisler arası yan etkilerin (örneğin bir grup silinince sohbet kanallarının
kapanması) bir mesaj kuyruğu olmadan güvenilir biçimde nasıl taşınacağı
belirlenmelidir.

## Karar

### Servisler

| Servis | Sorumluluk | Dışa açık mı? |
|--------|------------|---------------|
| **gateway** | Tek giriş noktası. REST API, erişim token'ı doğrulama, rate limit, istekleri iç servislere yönlendirme. WebSocket bağlantısını kimlik doğrulamasından sonra chat'e aktarır. | Evet (tek açık servis) |
| **identity** | Google/Apple ile giriş, kullanıcı hesapları, oturumlar ve refresh token'lar, token imzalama, KVKK talepleri, platform admin ve yaptırımlar. | Hayır |
| **core** | Tayfalar (gruplar), üyelikler ve roller, duyurular, etkinlikler, soru-cevap, anketler, arkadaşlık ve engelleme, medya, şikâyetler; Faz 2'de ilanlar. | Hayır |
| **chat** | Kanallar, mesajlar, gerçek zamanlı iletim, E2E DM için açık anahtarlar, bildirimler. | Hayır |

core içindeki alanlar birbirine sıkı bağlı olduğu için şimdilik ayrılmaz. core'un
içi yine domain paketlerine bölünür ve ileride ayrılabilecek şekilde yazılır.

### Veri sahipliği

- Tek bir PostgreSQL instance kullanılır. **Her servisin kendi veritabanı ve
  kendi DB kullanıcısı** olur: `identity`, `core`, `chat`. gateway'in
  veritabanı yoktur.
- Bir servis başka bir servisin veritabanına bağlanamaz. Başka servisin
  verisine yalnızca o servisin gRPC API'si üzerinden erişilir.
- Kullanıcıların sahibi identity'dir. Diğer servisler kullanıcıyı yalnızca
  `user_id` (UUID) ile referanslar; veritabanları arasında foreign key yoktur.
  Veri modeli v13'teki tabloların servislere dağılımı ve servisler arası
  foreign key davranışlarının yerine geçen olaylar
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md)'dadır.
- Kimlikler UUIDv7'dir. Böylece zamana göre sıralanabilir ve servisler
  birbirine sormadan ID üretebilir.

### İletişim

- **gateway → identity, core, chat:** senkron gRPC.
- **chat → core:** senkron gRPC (örneğin "bu kullanıcı bu tayfanın üyesi mi,
  rolü ne?"). chat bu yanıtı kısa süreli önbellekte tutabilir.
- **Yan etkiler** (core → chat, identity → core/chat) **outbox deseni** ile
  taşınır. Outbox olarak Notion veri modeli v10'daki `domain_events` ve
  `event_consumers` tabloları kullanılır; her servisin kendi veritabanında bu
  iki tablo bulunur:
  1. Servis, kendi işlemiyle **aynı transaction içinde** `domain_events`'e bir
     olay kaydı yazar (örneğin `tayfa.deleted`). Kayda kişisel içerik (mesaj
     metni, konum) yazılmaz; yalnızca kimlikler ve sınıflandırma bilgisi.
  2. Aynı servisteki bir arka plan işçisi (relay), olayları UUIDv7 sırasıyla
     okur ve hedef servisin ilgili gRPC metodunu çağırır. Her hedef ayrı bir
     tüketicidir ve kaldığı yeri `event_consumers`'ta tutar.
  3. Başarılı çağrıdan sonra tüketicinin konumu ilerletilir. Hata olursa artan
     bekleme süresiyle tekrar denenir.
  4. Hedef metodlar **idempotent** olmalıdır: aynı olay iki kez gelirse sonuç
     değişmez. Bunun için her olayın benzersiz bir ID'si bulunur.
- Döngüsel senkron çağrı yasaktır. Bir istek zinciri aynı servise geri
  dönmemelidir.

## Gerekçe

- Dört servis, öğrenme hedefini (gateway, kimlik ayrımı, gerçek zamanlı servis,
  servisler arası tutarlılık) karşılayan en küçük kümedir.
- Servis başına ayrı veritabanı, sınırları fiziksel olarak zorunlu kılar; ama
  tek Postgres instance işletme yükünü düşük tutar.
- Outbox deseni, mesaj kuyruğu eklemeden "en az bir kez teslim" garantisi
  sağlar. İş verisi ve olay aynı transaction'da yazıldığı için olay kaybolmaz.

## Sonuçlar

- Her servis kendi migration'larını yönetir.
- Bir grubun silinmesi, kullanıcının hesabını silmesi (KVKK) gibi işlemler
  servisler arasında **nihai tutarlılık** ile tamamlanır: kısa bir süre için
  servisler farklı durum görebilir.
- Relay işçileri ve idempotent metodlar ek kod demektir; bu ADR-0001'de kabul
  edilen bedelin parçasıdır.
- Olay sayısı veya alıcı sayısı arttığında (bir olayı üç servis dinlemeye
  başladığında) mesaj kuyruğuna geçiş yeniden değerlendirilir.

## Değerlendirilen alternatifler

- **Üç servis (identity, gateway içinde):** Daha az parça, ama kimlik
  yönetimini dışa açık serviste tutar ve kimlik ayrımını öğrenme fırsatını
  kaybettirir.
- **NATS JetStream ile olay yayını:** Asenkron mesajlaşma için uygun, ama
  şu anki az sayıda olay için ek bir bileşen getirir. Şimdilik ertelendi.
- **Servisler arası paylaşılan tek veritabanı:** En kolayı, ama sınırları
  yalnızca kurala bırakır ve servisleri veri düzeyinde birbirine bağlar.
