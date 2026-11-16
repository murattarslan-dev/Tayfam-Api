# ADR-0031: Sohbet ekleri — fotoğraf ve yer (kanal ve uçtan uca birebir)

- **Durum:** Kabul edildi (2026-11-16)
- **Tarih:** 2026-11-15, güncelleme 2026-11-16
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §1; issue #205). Bu ADR onları mimariye
  taşır. Açık kalan ayrıntılar platform sahibine soruldu ve 2026-11-16'da
  cevaplandı ("Platform sahibinin kararları"); kanal mesajı şikâyeti bu
  cevapla eklendi (§6).
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
    birlikte metin (alt yazı) **yoktur**; metin ayrı mesajla gider (karar 2).
  - `place`: `latitude` (−90..90), `longitude` (−180..180), isteğe bağlı
    `name` (1–120 karakter; v15'teki `events.meeting_point_name` ile aynı
    sınır). Değerler sonlu olmalıdır (NaN/∞ reddedilir).
- **Birebir mesajın** türü sunucuda **yoktur**: metin, fotoğraf ya da yer
  zarfın içindedir (ADR-0018). Sunucu yalnızca, varsa, şifreli ekin dosya
  kimliğini (`attachment_upload_id`, mesaj başına en çok bir) bilir. Böylece
  sunucunun birebir mesaj hakkında bildiği meta veri yalnızca "bir eki var"
  kadar büyür (ADR-0005'te kabul edilen meta veriye ek; karar 3).

### 2. Kanal fotoğrafı: mevcut görüntü hattı

- `uploads.purpose = 'chat_photo'`. Akış ADR-0020'nin aynısıdır: istemci
  metadata'yı siler, `preview` ve `full` varyantlarını üretir;
  `CreateUpload` (tayfa zorunlu, çağıran tayfanın etkin üyesi, tayfa salt
  okunur değil) → imzalı PUT → `CompleteUpload` başlık denetimi →
  `media/`'ya kopya. Sınırlar ve kota aynıdır (saatte 30 yükleme, 500 MiB,
  varyant 10 MiB).
- **Bağlama:** `SendMessage{kind: photo, photo_upload_id}` geldiğinde chat
  önce dosyanın kendi mesajlarından birinde olup olmadığına bakar
  (varsa `upload-in-use`), sonra mesaj satırını yazmadan hemen önce,
  **transaction dışında**, core'un `MediaService.AttachChannelPhoto`'sunu
  **gönderenin kendi token'ıyla** çağırır: dosya gönderenin, `ready`,
  `chat_photo`, aynı tayfanın olmalı. core `attached_type =
  'channel_message'`, `attached_id = <mesaj kimliği>` yazar. Servisler
  arası çağrı sırasında chat'te kilit ve bağlantı tutulmaz.
- **Onaylanmamış bağlama ve uzlaştırma:** bağlama yapıldıktan sonra chat
  mesajı yazamazsa (doğrulama, birebirde değişen cihaz kümesi, geri alma,
  istemci kopması, core commit ettikten sonra zaman aşımı) dosya hiç var
  olmamış bir mesaja bağlı kalır. Bu yüzden sohbet bağlaması
  `uploads.attach_confirmed_at` boşken **onaylanmamıştır**:
  - aynı sahibin yeni mesajına taşınabilir (tekrar deneme
    `upload-in-use` almaz); onaylanmış bağlama taşınmaz;
  - core'un `media-chat-reconcile` işi 10 dakikadan eski onaylanmamış
    bağlamaları chat'in `MediaHolderService.FindMessageMediaHolders`'ına
    **core'un servis token'ıyla** (ADR-0013; yalnızca `tayfam-core`)
    toplu sorar. Dosyayı tutan mesaj varsa bağlama o mesajla onaylanır
    (arada taşındıysa doğru mesaja düzeltilir); yoksa bağlama kaldırılır ve
    dosya bağlanmamış hazır dosya gibi 7 günde silinir (kotadan düşer).
    chat cevap veremezse hiçbir şey değişmez; ağ çağrısı sırasında core'da
    kilit tutulmaz, her güncelleme koşulludur.
  Mesajın commit'i ile bağlama arasındaki tek gerçek tutarlılık
  noktası chat'in kendi tekil indeksidir (bir dosya tek mesajda).
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
  kanaldaki gibi transaction dışında core'un
  `MediaService.AttachDirectMessageAttachment`'ını gönderenin token'ıyla
  çağırır; onay ve uzlaştırma kanaldakiyle aynıdır (dosya gönderenin, `ready`,
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
  rapor okunurken yalnızca "kanıt fotoğrafı var" bilgisi döner
  (`ReportedItem.has_evidence_photo`), link imzalanmaz. "Kanıt fotoğrafını
  aç" panelin kendi ucuna (`/sikayetler/{id}/kanit-fotografi`) gider; panel
  her tıklamada core'un `AdminService.GetReportEvidencePhotoUrl`'ünden
  yeni, 5 dakikalık bir link alır. Açma ayrı bir denetim işlemi olarak
  yazılır (`report.view_evidence_photo`, dosya kimliğiyle; link yazılmaz)
  ve panel referrer'sız yönlendirir. Sayfa depolama adresi taşımaz; panelin CSP'si
  (`img-src 'self'`) değişmez. Kullanıcı uçları bu dosya için sahibi
  dışında link üretmez. Şikâyet edenin hesabı silinse de kanıt rapor
  durdukça kalır (kapaktaki gibi sahipliği kaldırılır); rapor 1 yıl sonra
  silinince dosya da gider (bağlı kolon trigger'ı).
- **Kanal** (platform sahibi, 2026-11-16; karar 1): `reports.target_type`'a
  `channel_message` eklenir; metin, fotoğraf ve yer mesajları şikâyet
  edilir. Mevcut şikâyet modeli aynen geçerlidir: kapalı gerekçe listesi,
  aynı hedefe tekrar şikâyet yok (ilk şikâyet döner), günlük sınır, 18 yaş
  altı önceliği, tayfanın kuyruğu ve 7 günde platforma yükselme, şikâyet
  edenin kimliği kimseye gösterilmez. "Yönetici ve moderatör başkasının
  içeriğini şikâyet etmez, gizler" kuralı diğer modüllerdeki gibi
  istemcidedir (Notion, 2026-11-14: tüm modüllerde tek kural); sunucu
  diğer içeriklerde olduğu gibi bunu zorlamaz.
  - **Kimin şikâyet edebileceği chat'in kararıdır.** core,
    `ChannelService.VerifyChannelMessageForReport`'u **şikâyet edenin kendi
    token'ıyla** çağırır (birebirdeki `VerifyDirectMessageForReport` gibi).
    chat, mesajları listelerken uyguladığı kuralın aynısını uygular:
    şikâyet eden tayfanın etkin üyesi ve kanalın kitlesinde (`members`,
    `staff`, `event`, `selected`), mesaj bu tayfanın, silinmemiş, yazarın
    hesabı silinmemiş. Aksi her durumda `report-target-not-found` (neden
    söylenmez): kitle dışındaki biri mesajı şikâyet edemez ve varlığını da
    öğrenemez (IDOR). Kendi mesajı `invalid-request`. Çağrı yazma
    transaction'ından önce, kilit tutulmadan yapılır; önce aynı hedefe
    önceki şikâyete bakılır (varsa chat'e sorulmaz).
  - **Şikâyet edilen mesaj şikâyetle saklanır.** chat'in cevabı (kanal,
    tür, metin ya da yer ya da fotoğrafın dosya kimliği) `reports`'a yazılır
    (`channel_id`, `message_kind`, `message_body`,
    `message_photo_upload_id`, `place_*`) ve değişmez (`reports_guard`).
    Gerekçe: yazarın kendi silmesi kanal mesajının metnini hemen siler
    (§5); şikâyet konusunu kaybetmemelidir. Saklanan, şikâyet edenin zaten
    okuyabildiği mesajın kendisidir; core başka hiçbir mesajı okumaz ve
    chat'e "her mesajı oku" yetkisi verilmez. Bu kopyayı yalnızca platform
    paneli okur; log'a, olaya, şikâyet edene ve tayfa kuyruğuna girmez;
    şikâyet 1 yıl sonra silinince gider. Fotoğrafın kendisi kopyalanmaz:
    yazar mesajı silerse fotoğraf da gider (§5).
  - **Tayfa kuyruğu:** şikâyet edilen kişi mesajın yazarıdır; kuyruk
    mesajı açmak için `channel_id`'yi de gösterir. Kanalı okuyamayan
    moderatör (içinde olmadığı etkinlik ya da seçili kişi kanalı) mesajı
    açamaz ve gizleyemez; şikâyeti reddedebilir, üyeyi susturabilir ya da
    7 gün sonra platforma gider.
  - **`content_removed`:** moderatörün mesajı kendi yoluyla gizlemesi
    (ya da yazarın silmesi) gerekir. core bunu chat'in
    `ReportedMessageService.GetChannelMessageStatus`'una **core'un servis
    token'ıyla** (yalnızca `tayfam-core`) sorar; cevap tek bayraktır
    (silinmiş ya da yok), içerik dönmez. Çağrı çözüm transaction'ından önce
    yapılır; chat cevap veremezse `503`.
  - **Platform paneli:** metin ve yer "şikâyet edilmiş içerik moderasyon
    için görüntülenebilir" kuralıyla (Notion "Panelin göremedikleri")
    şikâyetteki kopyadan gösterilir; chat'e sorulmaz. Paylaşılan yer
    içeriktir (ADR-0008), kullanıcı konumu değildir; panel sözleşmesinin
    koordinat yasağına yalnızca `ReportedPlace` için dar bir istisna
    eklendi (test). Fotoğraf kanıt fotoğrafıyla aynı desende açılır:
    okumada yalnızca `has_message_photo`, link "Mesajın fotoğrafını aç"
    tıklamasında `AdminService.GetReportedMessagePhotoUrl` ile imzalanır
    (5 dakika; dosya hâlâ o mesaja bağlı, hazır `chat_photo` olmalı) ve
    ayrı denetim işlemi olarak yazılır (`report.view_message_photo`).
  - **Platformun `content_removed` kararı:** core kanal mesajını
    kaldıramaz (verinin sahibi chat). Karar transaction'ında
    `report.channel_message_removed` olayı yazılır (yalnızca kimlikler,
    aktör platform yöneticisi); core'un chat relay'i bunu
    `ApplyCoreEvent{channel_message_removed}` olarak iletir ve chat mesajı
    **moderatör gizlemesi gibi** gizler (tayfa yöneticisi 30 gün içinde
    geri alabilir; aktör platform yöneticisi olarak kaydedilir; bağlı
    istemcilere silindi bilgisi gider). Tekrar gelen olay bir şey
    değiştirmez.
  - **Ele geçirilirse:** core'un servis token'ı chat'te yalnızca "bu mesaj
    kaldırılmış mı" sorusunu ve geri alınabilir gizlemeyi açar; hiçbir
    mesajın içeriğini okutmaz. Şikâyet edenin token'ıyla yapılan doğrulama
    kitleyi aşamaz. Ele geçirilmiş bir gateway şikâyete kitle dışı bir
    mesaj ekleyemez (core chat'e şikâyet edenin token'ıyla sorar).

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

- core `00026_chat_media.sql` (main'deki `00023_event_photos`'un
  `event_photo` değerlerini korur, Down da onları geri almaz):
  `uploads.attach_confirmed_at` (yalnızca sohbet bağlamasında) ve
  uzlaştırma indeksi; `uploads.purpose`'a `chat_photo`,
  `dm_attachment`, `report_evidence`; blob tek nesnedir: `full_type =
  'application/octet-stream'`, `full_bytes` 1–10 485 760, önizleme ve
  ölçü yok (R2'de görüntünün `full` anahtarında; temizlik işi aynı
  kalır); bağlanma türleri
  `channel_message`, `dm_message`, `report`; `reports.evidence_upload_id`
  (bağlı dosya trigger'ları ADR-0020 düzeninde); `evidence_text`'in
  `dm_message`'da metin ya da dosyadan en az birine gevşemesi;
  `reports.target_type`'a `channel_message` ve şikâyet edilen mesajın
  kolonları (`channel_id`, `message_kind`, `message_body` 1–4000,
  `message_photo_upload_id`, `place_latitude`/`place_longitude` aralıkta,
  `place_name` 1–120; türe göre tam olarak gerekenler `CHECK` ile, değişmez
  `reports_guard` ile; diğer servisin kimlikleri FK'siz).
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

- chat iki yeni gRPC servisi kazanır: `MessageMediaService` (kullanıcı
  token'ı) ve `MediaHolderService` (yalnızca core'un servis token'ı); ilk
  outbox relay'ini de kazanır; chat pod'una core audience'lı projeksiyonlu servis token'ı
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
  Kanal mesajı şikâyetinin sakladığı mesaj, şikâyet edenin dışa
  aktarımına girmez (başkasının içeriğidir); şikâyetin kendisi girer.
- Kanal mesajı şikâyeti: chat `ChannelService.VerifyChannelMessageForReport`
  (kullanıcı token'ı) ve `ReportedMessageService` (yalnızca core'un servis
  token'ı) kazanır; core'un chat'e giden olay listesine
  `report.channel_message_removed` eklenir. core'un servis token'ıyla chat
  bağlantısı tek yerde kurulur ve uzlaştırma işiyle paylaşılır.
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
- **Kanal fotoğrafına alt yazı:** yaygın, ama ürün kararında yok
  (karar 2).
- **Şikâyet edilen kanal mesajını panelde chat'ten canlı okumak:** core'a
  servis token'ıyla "herhangi bir mesajı oku" yetkisi gerekirdi (chat
  şikâyetin varlığını doğrulayamaz) ve yazar mesajı silince metni de
  giderdi. Şikâyet anındaki kopya, şikâyet edenin zaten okuyabildiğinden
  fazlasını içermez.
- **Kanal mesajında kanıtı şikâyet edene yükletmek (birebirdeki gibi):**
  sunucu kanal mesajını zaten okuyabilir; şikâyet edenin değiştirebileceği
  bir kopyaya gerek yok.
- **Bağlamayı chat'in transaction'ı içinde yapmak (ilk taslak):** geri
  almada core'daki bağlama yine kalırdı (servisler arası transaction yok)
  ve 5 sn'ye kadar süren bir çağrı chat'te kilit ve bağlantı tutardı.
- **Bağlamayı commit'ten sonra outbox'la yapmak:** core sahipliği
  kullanıcının token'ıyla doğrular; relay'in servis token'ı gönderenin
  dosyası olduğunu kanıtlayamaz. Reddedilen bir dosya için mesaj da
  yayınlanmış olurdu.
- **Serbest bırakmayı senkron çağrıyla yapmak (chat → core, kullanıcı
  token'ıyla):** yazarın silmesinde yeterdi, ama 30 günlük kalıcı silmede
  kullanıcı token'ı yoktur.

## Platform sahibinin kararları (2026-11-16)

1. **Kanal mesajı şikâyeti bu işle eklenir** (§6): `channel_message`
   (metin, fotoğraf, yer); tayfa kuyruğu ve platform paneli; mevcut
   şikâyet modeli; çözüm `content_removed` = moderatör gizlemesi; panelde
   metin/yer "şikâyet edilince içerik görünür" kuralıyla, fotoğraf
   tıklamada imzalanan kısa link ve ayrı denetim kaydıyla; kitle dışındaki
   biri şikâyet edemez.
2. **Alt yazı yok:** fotoğraf ve metin ayrı mesajlardır.
3. **Diğer seçimler uygun:** birebir ekte sunucu yalnızca "eki var" bilir
   (tür bilinmez); `dm_attachment` 10 MiB; hesap silinince kanal
   fotoğrafları silinir; kanıt fotoğrafı şikâyet durdukça kalır; chat'in
   core audience'lı servis token'ı (`deploy/base/chat.yaml`).

Bu ADR'nin kararlar dışında seçtiği ayrıntılar (gerekçeleri §6'da):
şikâyet edilen kanal mesajının şikâyetle saklanması (fotoğraf hariç),
panel sözleşmesinde `ReportedPlace` için koordinat istisnası, kanalı
okuyamayan moderatörün mesajı gizleyememesi.
