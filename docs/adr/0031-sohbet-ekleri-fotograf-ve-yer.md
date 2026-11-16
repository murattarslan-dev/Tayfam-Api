# ADR-0031: Sohbet ekleri — fotoğraf ve yer (kanal ve uçtan uca birebir)

- **Durum:** Önerildi
- **Tarih:** 2026-11-15
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §1; issue #205). Bu ADR onları mimariye
  taşır ve açık kalan ayrıntıları gerekçesiyle seçer; seçilenler
  "Platform sahibine sorular" bölümündedir.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md),
  [ADR-0007](0007-dosyalar-ve-medya.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0013](0013-servis-kimligi.md),
  [ADR-0014](0014-websocket-erisim-tokeniyla.md),
  [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0018](0018-birebir-mesaj-e2e-sunucu-tarafi.md),
  [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md),
  [ADR-0024](0024-kanal-kitlesi.md); issue #205

## Bağlam

Faz 2'de kanal ve birebir mesajlarda fotoğraf ve haritadan seçilmiş yer
gönderilebilecek (Notion §1). Kararlar:

- Birebirde fotoğraf uçtan uca şifrelidir: istemci rastgele anahtarla
  AES-256-GCM ile şifreler, şifreli dosyayı R2'ye yükler; anahtar ve özet
  Signal mesajının içinde gider (Signal'in ek modeli). Sunucu fotoğrafı
  görmez; EXIF/GPS temizliği istemcidedir. Şikâyette fotoğrafı şikâyet eden
  kendi cihazından iletir.
- Kanalda fotoğraf mevcut medya hattıyla (karantina, başlık denetimi,
  imzalı link) gider; yalnızca kanalın kitlesi görür.
- Yer: OpenStreetMap seçicisiyle işaretlenen nokta ve isteğe bağlı ad.
  Kullanıcının bilerek paylaştığı içeriktir (ADR-0008), cihazın anlık
  konumu değildir; canlı konum yoktur.

Mimari gerilim: dosyaların, R2 anahtarlarının ve imzalı linklerin sahibi
**core**'dur (ADR-0007, ADR-0020); kanalların, kanal kitlesinin
(`channel_members`) ve birebir konuşmaların sahibi **chat**'tir
(ADR-0009, ADR-0024). Bir sohbet fotoğrafının linkini core imzalar, ama
kimin görebileceğini chat bilir.

## Karar

### 1. Mesaj türleri

- **Kanal mesajı** `kind` taşır: `text` (bugünkü), `photo`, `place`.
  Mevcut mesajlar `text`'tir. Tür sonradan değişmez (mesaj zaten
  düzenlenemez).
  - `photo`: `photo_upload_id` (core'daki dosyanın kimliği). Fotoğrafla
    birlikte metin (alt yazı) **yoktur**; metin ayrı mesajla gider (soru 1).
  - `place`: `latitude` (−90..90), `longitude` (−180..180), isteğe bağlı
    `name` (1–120 karakter; v15'teki `events.meeting_point_name` ile aynı
    sınır). Değerler sonlu olmalıdır (NaN/∞ reddedilir).
- **Birebir mesajın** türü sunucuda **yoktur**: metin, fotoğraf ya da yer
  zarfın içindedir (ADR-0018). Sunucu yalnızca, varsa, şifreli ekin dosya
  kimliğini (`attachment_upload_id`, mesaj başına en çok bir) bilir. Böylece
  sunucunun birebir mesaj hakkında bildiği meta veri yalnızca "bir eki var"
  kadar büyür (ADR-0005'te kabul edilen meta veriye ek; soru 2).

### 2. Kanal fotoğrafı: mevcut görüntü hattı

- `uploads.purpose = 'chat_photo'`. Akış ADR-0020'nin aynısıdır: istemci
  metadata'yı siler, `preview` ve `full` varyantlarını üretir;
  `CreateUpload` (tayfa zorunlu, çağıran tayfanın etkin üyesi, tayfa salt
  okunur değil) → imzalı PUT → `CompleteUpload` başlık denetimi →
  `media/`'ya kopya. Sınırlar ve kota aynıdır (saatte 30 yükleme, 500 MiB,
  varyant 10 MiB).
- **Bağlama:** `SendMessage{kind: photo, photo_upload_id}` geldiğinde chat,
  mesaj satırını transaction içinde yazdıktan sonra ve commit'ten (ve
  outbox yazımından) önce core'un `MediaService.AttachChannelPhoto`'sunu
  **gönderenin kendi token'ıyla** çağırır. core reddederse mesaj geri
  alınır; chat'in kendi denetimleri (izin, yavaş mod, birebirde cihaz
  kümesi) core'a gitmeden önce biter, böylece "cihazlar değişti" gibi
  olağan bir tekrar denemede dosya boşuna "kullanımda" kalmaz. Yalnızca
  commit'in kendisi başarısız olursa dosya var olmayan bir mesaja bağlı
  kalır (kabul edilen risk; kullanıcı fotoğrafı yeniden yükler): dosya gönderenin, `ready`,
  `chat_photo`, aynı tayfanın ve bağlanmamış (ya da zaten bu mesaja bağlı)
  olmalıdır. core `attached_type = 'channel_message'`,
  `attached_id = <mesaj kimliği>` yazar. Bir dosya yalnızca bir mesaja
  bağlanır.
- **Görme:** link ucu tektir (`POST /v1/media/urls`, ADR-0020 §6). core,
  `channel_message`'a bağlı dosyalar için chat'in
  `MessageMediaService.FilterVisibleMessageMedia`'sını **çağıranın kendi
  token'ıyla**, tek toplu çağrıda sorar. chat, kanal mesajlarını
  listelerken uyguladığı kuralın aynısını uygular: çağıran kanalın
  kitlesinde (ADR-0024: `members`, `staff`, `event`, `selected`), mesaj o
  kanalda, silinmemiş ve `photo_upload_id` istenen dosya. Cevap yalnızca
  görülebilen dosya kimlikleridir. Kitle kararı verinin sahibindedir (chat);
  core yalnızca kendi kuralını (dosya hazır, bağlı) ekler. Sahip kendi
  dosyasını her zaman görür (ADR-0020).
- Her iki tarafın birbirini doğrulaması bağlamayı güvenli kılar: core
  dosyayı yalnızca bağlandığı mesaj için sorar, chat yalnızca o mesajın
  `photo_upload_id`'si o dosyaysa "görür" der. Ele geçirilmiş bir gateway
  `AttachChannelPhoto`'yu doğrudan çağırıp bir dosyayı başka bir mesaja
  bağlasa bile o mesaj dosyayı göstermediği için link üretilmez; gateway
  bu metodu yönlendirmez.

### 3. Birebir fotoğraf (E2E): opak blob

- `uploads.purpose = 'dm_attachment'`. Görüntü değildir: tek nesne
  (`blob`), `application/octet-stream`, **1 B – 10 MiB** (sunucu ham bir
  fotoğrafı değil, istemcinin `full` varyantının şifreli halini taşır;
  önizleme küçük olduğu için zarfın içinde gider). Tayfası yoktur.
- `CompleteUpload` blob için yalnızca **boyutu** denetler (R2 `HEAD`) ve
  nesneyi ETag'e bağlı kopyayla karantinadan `media/`'ya taşır; `Range`
  okuması, tür ve metadata denetimi **yapılmaz**, ölçü alınmaz. Sunucu
  blob'u hiçbir zaman görüntü olarak işlemeye çalışmaz: içerik şifrelidir,
  denetlenecek bir başlığı yoktur. EXIF/GPS temizliği istemcinin işidir
  (Notion §1).
- İstemci blob'u rastgele bir anahtarla AES-256-GCM ile şifreler; anahtar,
  özet (SHA-256) ve dosya kimliği Signal zarfının içinde karşı tarafa gider.
  **Anahtar ve özet sunucuya hiç gelmez**; sunucu kriptografi yapmaz
  (ADR-0018). Alıcı indirdiği blob'un özetini zarftaki özetle karşılaştırır.
- **Bağlama:** `SendDirectMessage{attachment_upload_id}` geldiğinde chat,
  kanaldaki gibi transaction içinde core'un
  `MediaService.AttachDirectMessageAttachment`'ını gönderenin token'ıyla
  çağırır (dosya gönderenin, `ready`,
  `dm_attachment`, bağlanmamış ya da zaten bu mesaja bağlı).
  `attached_type = 'dm_message'`.
- **Görme:** core `dm_message`'a bağlı dosyalar için yine
  `FilterVisibleMessageMedia`'ya sorar; chat yalnızca **konuşmanın iki
  tarafına** ve mesaj silinmemişken "görür" der. Link 1 saattir; link
  ele geçse bile içerik şifrelidir.
- Mesaj başına en çok bir ek (birden çok fotoğraf, birden çok mesajdır).

### 4. Yer paylaşımı

- **Kanalda** `place` mesajının içeriği (`{lat, lng, name}`) kanal
  mesajının metni gibi **sunucu anahtarıyla şifrelenir** (`body_encrypted`,
  AES-256-GCM, AAD = mesaj + kanal kimliği; `CHAT_MESSAGE_KEYS`). Veritabanı
  açık koordinatı görmez; bu yüzden aralık denetimi Go'dadır, veritabanı
  türü ve gövdenin varlığını `CHECK` ile denetler.
- **Birebirde** yer tamamen zarfın içindedir; sunucuya hiçbir şey eklenmez.
- Koordinat ve yer adı log'a, metriğe, trace'e, `domain_events`'e, bildirime
  ve hata mesajına girmez. Hata ayrıntısı yalnızca alan adını taşır.
  Bildirim ve push yeri taşımaz (ADR-0019).
- Cihaz konumu sunucuya hiç gelmez: "Bulunduğum yer" yalnızca istemcide
  haritayı taşır; gönderilen nokta kullanıcının onayladığı noktadır.

### 5. Silme ve temizlik

- **Kanal:** yazar kendi mesajını silince içerik hemen gider (mevcut
  kural): `place` için şifreli gövde, `photo` için `photo_upload_id`
  boşaltılır ve dosya serbest bırakılır. Moderatör gizlemesi dosyayı
  bırakmaz (30 gün geri alınabilir); kalıcı silmede (30 gün) bırakılır.
- **Birebir:** gönderen silince `attachment_upload_id` boşaltılır ve dosya
  serbest bırakılır; kalıcı silmede de.
- **Serbest bırakma outbox'la:** chat aynı transaction'da
  `chat.media_released` (`upload_id`, `message_id`; başka bir şey yok)
  yazar. chat'in outbox'ı ilk kez başka bir servise iletilir: chat'in
  relay'i core'un `ChatEventService.ApplyChatEvent`'ini **chat'in servis
  token'ıyla** (ADR-0013, audience `core.tayfam.internal`) çağırır; core
  bu metodu yalnızca `tayfam-chat`'ten kabul eder. core dosyayı, hâlâ o
  mesaja bağlıysa silme kuyruğuna alır (idempotent). Kullanıcı silme işi
  kullanıcı token'ı olmadan çalıştığı için senkron çağrı yetmezdi.
- **Tayfa kalıcı silinince** core tayfanın `chat_photo` dosyalarını kendi
  purge işinde siler (dosyanın tayfası core'da bellidir; olay gerekmez).
- **Hesap silinince** core kullanıcının dosyalarını siler (ADR-0020 §5):
  sohbet fotoğrafları ve ekler de gider; mesajlar "Silinmiş kullanıcı"
  olarak kalır, fotoğraf görünmez. Rapora bağlı şikâyet kanıtı istisnadır
  (§6).
- Bağlanmamış dosya 7 gün sonra silinir (mevcut kural): gönderilmeyen
  fotoğraf kalmaz.

### 6. Şikâyet

- **Birebir:** şikâyet eden fotoğrafı kendi cihazından iletir (Notion §1).
  İstemci zarftan çözdüğü fotoğrafı görüntü hattıyla yeniden yükler:
  `uploads.purpose = 'report_evidence'` (iki varyant, metadata yok, başlık
  denetimi; tayfasız). `CreateReport` `dm_message` için
  `evidence_upload_id` kabul eder; `evidence_text` ve
  `evidence_upload_id`'den **en az biri** gerekir (bugün metin zorunluydu).
  core dosyayı rapora bağlar (`attached_type = 'report'`,
  `reports.evidence_upload_id`). Dosyayı yalnızca platform paneli görür:
  rapor sayfasında "Kanıt fotoğrafını aç" panelin kendi ucuna
  (`/sikayetler/{id}/kanit-fotografi`) gider; panel core'dan her tıklamada
  yeni, 5 dakikalık bir link alır (görüntüleme denetim kaydına yazılır) ve
  referrer'sız yönlendirir. Sayfa depolama adresi taşımaz; panelin CSP'si
  (`img-src 'self'`) değişmez. Kullanıcı uçları bu dosya için sahibi
  dışında link üretmez. Şikâyet edenin hesabı silinse de kanıt rapor
  durdukça kalır (kapaktaki gibi sahipliği kaldırılır); rapor 1 yıl sonra
  silinince dosya da gider (bağlı kolon trigger'ı).
- **Kanal:** issue "mevcut kanal mesajı şikâyeti" diyor; **böyle bir şikâyet
  hedefi bugün yok** (`reports.target_type` listesinde `channel_message`
  yoktur). Bu ADR onu eklemez; kişiyi şikâyet etme ve moderatörün mesajı
  gizlemesi bugünkü yoldur (soru 3).

### 7. Sınırlar

| Konu | Sınır | Nerede |
|------|-------|--------|
| `chat_photo`, `report_evidence` | ADR-0020'nin görüntü sınırları | Go + `CHECK` |
| `dm_attachment` | tek nesne, 1 B – 10 MiB, `application/octet-stream` | Go + `CHECK` |
| Yükleme sıklığı ve kota | saatte 30, kullanıcı başına 500 MiB (tüm amaçlar birlikte, dışa aktarma hariç) | Go |
| Ek | birebir mesaj başına en çok 1 | Go + şema (tek kolon) |
| Yer adı | 1–120 karakter, kontrol karakteri yok | Go (şifreli) |
| Enlem / boylam | −90..90 / −180..180, sonlu | Go (şifreli) |
| Kanal mesajı türü | `text` / `photo` / `place` | Go + `CHECK` |

### 8. Veritabanı

- core `00026_chat_media.sql`: `uploads.purpose`'a `chat_photo`,
  `dm_attachment`, `report_evidence`; blob tek nesnedir: `full_type =
  'application/octet-stream'`, `full_bytes` 1–10 485 760, önizleme ve
  ölçü yok (R2'de görüntünün `full` anahtarında; temizlik işi aynı
  kalır); bağlanma türleri
  `channel_message`, `dm_message`, `report`; `reports.evidence_upload_id`
  (bağlı dosya trigger'ları ADR-0020 düzeninde); `evidence_text`'in
  `dm_message`'da metin ya da dosyadan en az birine gevşemesi.
- chat `00017_message_media.sql`: `channel_messages.kind`,
  `photo_upload_id`; `dm_messages.attachment_upload_id`; `chat_app`'e
  yalnızca gereken kolon düzeyinde `UPDATE`.
- Diğer servisin kimlikleri düz `uuid`'dir (FK yok, ADR-0002).

## Gerekçe

- **Tek dosya sahibi:** R2 anahtarı ve imzalama yalnızca core'da kalır.
  chat'e R2 kimliği vermek "ele geçirilirse neye ulaşır" cevabını tüm
  dosyalara genişletirdi (ADR-0011).
- **Kitle kararı verinin sahibinde:** kanal kitlesi ve konuşma tarafları
  chat'tedir; core onların kopyasını tutmaz (ADR-0024'teki gerekçeyle aynı).
  core → chat çağrısı kullanıcının token'ıyla yapılır; gateway kitle
  bilgisini iletemez (ADR-0011). Örnek: core'un şikâyette chat'e sorması
  (`VerifyDirectMessageForReport`).
- **Tek link ucu:** istemci her dosyayı aynı uçtan ister ve aynı önbelleği
  kullanır; liste uçları link taşımaz (ADR-0020).
- **Blob'u işlememek:** şifreli veride denetlenecek başlık yoktur; işlemeye
  çalışmak yanlış ret ya da bilgi sızıntısı (zamanlama) dışında bir şey
  kazandırmaz. Boyut ve kota denetimi kötüye kullanımı sınırlar.
- **Yer kanalda şifreli:** kanal metniyle aynı koruma; veritabanı dökümü
  koordinat vermez.
- **Serbest bırakma outbox'la:** kalıcı silme kullanıcısız bir iştir;
  ADR-0002'nin servisler arası yan etki kuralıdır.

## Sonuçlar

- chat yeni bir gRPC servisi (`MessageMediaService`) ve ilk outbox relay'ini
  kazanır; chat pod'una core audience'lı projeksiyonlu servis token'ı
  bağlanır (`deploy/base/chat.yaml`). Yerel ortamda token yoksa relay
  kapalıdır, olaylar outbox'ta bekler (core'daki gibi).
- core `MediaService` iki bağlama metodu, `ChatEventService` ve chat
  istemcisi (zaten şikâyet için var) kazanır; `GetMediaUrls` chat'e
  sorabilir. chat cevap veremezse sohbet dosyaları sessizce atlanır
  (diğerleri döner).
- gateway: `UploadPurpose` üç değer, `UploadVariant.kind` `blob`,
  `ChannelMessage.kind/photo_upload_id/place`,
  `DirectMessage.attachment_upload_id`, `ReportCreateRequest.evidence_upload_id`;
  panelde kanıt fotoğrafı linki. WebSocket `channel.message_created`
  çerçevesi aynı `ChannelMessage` gövdesini taşır.
- KVKK dışa aktarma: kullanıcının kendi kanal mesajlarının türü, yeri
  (açık) ve fotoğraf kimliği girer; dosyalar zip'e girmez (ADR-0021).
- Notion "Veri Modeli (v15)" bu tablolarla güncellenmelidir (PR
  açıklamasında listelendi).

## Değerlendirilen alternatifler

- **chat'e R2 kimliği verip linki chat'te imzalamak:** core'a çağrı
  gerekmez, ama dosya deposuna erişen ikinci bir servis ve tüm dosyalara
  ulaşan bir saldırı yüzeyi (ADR-0011).
- **Kanal kitlesini core'da hesaplamak:** `staff` ve `event` core'da
  çözülebilir ama `selected` (`channel_members`) chat'tedir; kopyası
  ADR-0024'te reddedildi.
- **chat'in onayladığı dosyalar için ayrı link ucu (gateway → chat →
  core):** core, chat'in onayını doğrulayamaz; ele geçirilmiş gateway
  doğrudan core'u çağırıp kitleyi atlayabilirdi. Kararın core'dan sorulması
  bunu kapatır.
- **Birebir ekte sunucunun da mesaj türünü bilmesi:** liste ve bildirimde
  "fotoğraf" yazmak kolaylaşırdı, ama sunucuya içerik hakkında bilgi verir;
  istemci zarfı çözünce zaten bilir.
- **Kanal fotoğrafına alt yazı:** yaygın, ama ürün kararında yok; ayrı
  karar (soru 1).
- **Serbest bırakmayı senkron çağrıyla yapmak (chat → core, kullanıcı
  token'ıyla):** yazarın silmesinde yeterdi, ama 30 günlük kalıcı silmede
  kullanıcı token'ı yoktur.

## Platform sahibine sorular

1. Kanal fotoğrafında alt yazı yok (metin ayrı mesaj). Alt yazı istenirse
   `photo` mesajına şifreli `body` eklenir; şema buna hazırdır.
2. Birebirde sunucu "bu mesajın şifreli bir eki var" bilgisini görür (mesaj
   türünü değil). Kabul mü?
3. Kanal mesajı şikâyet hedefi (`channel_message`) bugün yok; fotoğraf için
   ayrı bir issue açılsın mı (chat'in mesajı doğrulaması, tayfa kuyruğu,
   panelde fotoğraf)?
4. `dm_attachment` üst sınırı 10 MiB (yalnızca fotoğraf). Video/dosya
   kapsam dışı olduğu için yeterli görüldü.
5. Hesap silinince kişinin kanal fotoğrafları da silinir (ADR-0020 §5'teki
   "kullanıcının dosyaları" kuralı); mesaj kalır, fotoğraf görünmez. Buna
   karşılık şikâyet kanıtı, şikâyet edenin hesabı silinse de rapor
   durdukça (1 yıl) platformda kalır. İkisi de kabul mü?
6. Kanal mesajı kalıcı silinirken ya da yazar silerken fotoğrafın core'da
   silinmesi chat'in ilk outbox relay'iyle olur; bunun için chat pod'una
   core audience'lı servis token'ı bağlandı (`deploy/base/chat.yaml`).
   Cluster tarafında ek adım yok; onay?
