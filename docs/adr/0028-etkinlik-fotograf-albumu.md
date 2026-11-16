# ADR-0028: Etkinlik fotoğraf albümü

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15)
- **Tarih:** 2026-11-15
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 2 (v2) — Medya ve Sosyal" §2; issue #206). Bu ADR onların mimariye
  taşınmasıdır; açık kalan ayrıntılar platform sahibiyle sohbette
  kararlaştırıldı ("Açık ayrıntılarda kararlar").
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0007](0007-dosyalar-ve-medya.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md)

## Bağlam

Etkinliğe gerçekten katılanlar fotoğraf paylaşabilmeli, tayfanın üyeleri
albümü görebilmelidir. ADR-0007 `uploads.purpose = event_photo`'yu Faz 2
için öngörmüştü; Veri Modeli v15 "Planlanan, şemada yok" altında
`event_photos` tablosunu tanımlar (`id, event_id, uploader_id, upload_id,
caption, created_at, deleted_at, deleted_by`; "yalnızca attended kişiler
yükler").

Platform sahibinin kararları (2026-11-15):

- **Yükleme:** yalnızca o etkinlikte check-in'i doğrulanmış katılımcı;
  etkinliğin başlangıcından bitişin 7 gün sonrasına kadar. İptal edilen
  etkinlikte yükleme yok.
- **Görüntüleme:** tayfanın tüm (aktif) üyeleri.
- **Sınırlar:** kişi başı etkinlik başına 30, albüm başına 500 fotoğraf.
- **Silme:** yükleyen kendi fotoğrafını; yönetici, moderatör ve etkinliğin
  organizatörü her fotoğrafı.
- Fotoğraf **şikâyet edilebilir**; albüm fotoğrafı **herkese açık kapak
  olarak işaretlenemez**.
- Bu turda bildirim yok.

## Karar

### 1. Veri: `event_photos` (core) ve `uploads.purpose = event_photo`

Albüm core'dadır: etkinlik, katılım, izin tablosu ve `uploads` core'un
verisidir (ADR-0002, ADR-0009). Albüm ayrı bir varlık değildir; bir
etkinliğin silinmemiş `event_photos` satırları onun albümüdür.

| Alan | Tür | Kısıt |
|------|-----|-------|
| `id` | uuid PK | UUIDv7, sunucuda |
| `event_id` | uuid FK → events | CASCADE (etkinlik kalıcı silinince fotoğraflar da gider) |
| `uploader_id` | uuid | NOT NULL (hesap silmede satır silinir, NULL kalmaz) |
| `upload_id` | uuid FK → uploads | NOT NULL, UNIQUE, CASCADE |
| `caption` | text | NULL ya da 1–200 karakter |
| `client_request_id` | uuid | UNIQUE(uploader_id, client_request_id) |
| `created_at` | timestamptz | |
| `deleted_at` / `deleted_by` | timestamptz / uuid | ikisi birlikte |

v15'e göre fark: `client_request_id` eklendi (her oluşturma ucunda olduğu
gibi tekrar gönderim koruması; CLAUDE.md "ID'ler").

`uploads`: `purpose` listesine `event_photo`, bağlanma kuralına
`event_photo → attached_type = event_photo` eklenir. Fotoğraf yükleme
akışı ADR-0020'nin aynısıdır (istemci metadata'yı siler, iki varyant,
karantina, başlık denetimi). Mevcut iki trigger albüme de kurulur:
`uploads_check_ref` (satır yalnızca kendisine bağlı, hazır `event_photo`
dosyasını gösterebilir) ve `uploads_release_ref` (satır kalıcı silinince
dosya silme kuyruğuna girer; soft delete dosyayı bırakmaz).

**Herkese açık kapak yok:** `events.cover_upload_id` ve
`tayfas.cover_upload_id` trigger'ları yalnızca `purpose = cover`
dosyasını kabul eder; bir `event_photo` dosyası kapak kolonuna yazılamaz
(TF080) ve bir albüm satırına bağlı dosya başka içeriğe bağlanamaz
(`ERROR_REASON_UPLOAD_IN_USE`). Herkese açık kopya (`cover_public_key`)
bugün yoktur; geldiğinde yalnızca `purpose = cover` için açılır. Bu kural
testle sabitlenir.

### 2. Yükleme kuralı

`POST /v1/tayfas/{tayfa_id}/events/{event_id}/photos` (gRPC
`EventService.AddEventPhoto`) çağıranın kendi, bu tayfa için açılmış,
hazır `event_photo` yüklemesini albüme ekler. Sırasıyla:

1. Aktif üyelik ve `event.photo_add` (izin tablosu; susturulmuş üye
   ekleyemez), etkinlik modülü açık, tayfa salt okunur değil.
2. Etkinlik satırı kilitlenir (`FOR UPDATE`): aynı etkinliğe eşzamanlı
   eklemeler sıraya girer.
3. İptal edilmiş etkinlik → `EVENT_CANCELLED`.
4. Pencere `[starts_at, ends_at + 7 gün)`; dışı → `ALBUM_CLOSED` (yeni
   hata nedeni; başlamadan önce de sonra da aynı neden: istemci yalnızca
   "albüm şu an kapalı" der).
5. Çağıranın katılımı `attended` değilse → `NOT_ATTENDED`. "Check-in'i
   doğrulanmış" = `event_rsvps.attendance = 'attended'` (yöntem fark
   etmez: konum, online, yürüyüş başlangıcı ya da organizatör düzeltmesi;
   organizatörün işaretlediği katılım da doğrulanmış sayılır — platform
   sahibi, 2026-11-15). Av günlüğünün kuralıyla aynıdır.
6. Sınırlar: çağıranın bu etkinlikteki silinmemiş fotoğrafı 30'a ya da
   albümün silinmemiş fotoğrafı 500'e ulaştıysa → `EVENT_LIMIT`. Silinen
   fotoğraf sayılmaz (kişi yanlış fotoğrafı silip yenisini koyabilir).
7. Dosya bağlanır (`media.Attach`), sonra satır yazılır.

**Veritabanı ikinci hat:** `event_photos_guard` BEFORE INSERT trigger'ı
etkinlik satırını aynı kilitle (`FOR UPDATE`) alır, iptali, yükleyenin
`attended` olmasını ve 30/500 sınırlarını yeniden denetler. Kilit
sayesinde iki eşzamanlı ekleme, uygulama katmanını atlayan bir yoldan
gelse bile sınırı aşamaz. Reddin nedeni ayrı SQLSTATE'lerdedir ve
uygulama onları API hatasına çevirir (500 değil): `TF090` sınır →
`EVENT_LIMIT`, `TF091` katılım doğrulanmamış → `NOT_ATTENDED`, `TF092`
iptal ya da silinmiş etkinlik → `EVENT_CANCELLED`. Pencere (saat) yalnızca Go'da
denetlenir: veritabanının saati test saatinden ayrıdır ve pencere
bir güvenlik sınırı değil, ürün kuralıdır.

Tekrar gönderim (aynı `client_request_id`) ilk fotoğrafı döndürür;
silinmiş ya da başka etkinliğe ait kayıtla eşleşirse `VALIDATION`.

### 3. Görüntüleme

`GET .../events/{event_id}/photos` (`ListEventPhotos`): `event.view`
(her aktif üye; susturulmuş üye de okur), etkinlik modülü açık, etkinlik
silinmemiş. İptal edilmiş etkinliğin mevcut albümü okunur. Sıra
`(created_at, id)` artan (albüm etkinliğin akışını anlatır), sayfalama
mevcut `page_size` (varsayılan 50, en çok 100) ve opak, isteğe bağlı
`page_token` ile.

Yanıt link taşımaz, yalnızca `upload_id` (ADR-0020 "API yanıtları link
taşımaz"). İstemci linkleri `POST /v1/media/urls` ile toplu ister; bir
sayfa (≤ 100) tek çağrıya sığar. `GetMediaUrls`'in görme kuralına
`event_photo` eklenir: yükleyen kendisi her zaman; diğerleri aktif üye,
`event.view`, etkinlik modülü açık, fotoğraf ve etkinlik silinmemiş.
Böylece görme kararı tek yerde (izin tablosu) kalır ve 1 saatlik link
kuralı değişmez.

### 4. Silme ve izin tablosu

İzin tablosuna (ADR-0004) dört eylem eklenir:

| Eylem | En düşük rol | Susturulmuşken | Not |
|-------|--------------|----------------|-----|
| `event.photo_add` | üye | hayır | Ayrıca `attended`, pencere, sınırlar |
| `event.photo_delete_own` | üye | **evet** | Kendi fotoğrafı; susturma kişinin kendi görüntüsünü tayfada tutmaz (soru/cevap silmeyle aynı) |
| `event.photo_delete_own_event` | üye | hayır | Organizatör (`events.created_by`) kendi etkinliğinin her fotoğrafını; çağıran etkinliğin kendisinin olduğunu denetler |
| `event.photo_delete_any` | moderatör | hayır | Yönetici ve moderatör her etkinliğin her fotoğrafını |

"Her fotoğrafı" kararı gereği rol hiyerarşisi (`CanActOnContent`)
uygulanmaz: moderatör yöneticinin fotoğrafını, organizatör moderatörün
fotoğrafını kaldırabilir (av günlüğü silmeyle aynı). Silme soft delete'tir
(`deleted_at`, `deleted_by`) ve iptal edilmiş ya da bitmiş etkinlikte de
çalışır. Tayfa salt okunurken (askıda, arşivde, silme bekliyor) ya da
etkinlik modülü kapalıyken kendi fotoğrafını kaldırmak da dahil hiçbir
silme yapılamaz; bu, tayfadaki diğer içeriklerin davranışıyla aynıdır
(yazma yolları salt okunur tayfada ve kapalı modülde reddedilir). Başkasının fotoğrafının kaldırılması denetim kaydına
(`event.photo_removed`) yazılır; kendi fotoğrafını silmek ve eklemek
yazılmaz (500 kayıtlık albüm denetim kaydını boğmasın). Albüm için outbox
olayı ve bildirim yoktur (bu turda tüketicisi yok).

**Kalıcı silme:** saklama işi (`retention`) 30 günü geçen soft delete'leri
siler; satır silinince `uploads_release_ref` dosyayı silme kuyruğuna koyar
ve mevcut temizlik işçisi R2'deki iki varyantı, sonra `uploads` satırını
siler.

### 5. Şikâyet

`reports.target_type`'a `event_photo` eklenir (REST `event_photo`, proto
`REPORT_TARGET_TYPE_EVENT_PHOTO`). Mevcut akış aynen geçerlidir: kapalı
gerekçe listesi, tayfa kuyruğu, `content_removed` çözümü yalnızca fotoğraf
kaldırılmışsa, platform kuyruğunda platform yöneticisinin içerik kaldırma
kararı (`AdminDeleteEventPhoto`). Şikâyet edilen kişi yükleyendir; modül
etkinliklerdir. Panel öğe özetinde başlık olarak açıklama (caption) döner.

**Panelde fotoğrafın kendisi** (platform sahibi, 2026-11-15): platform
paneli yalnızca platforma yükselmiş bir şikâyetin konusu olan albüm
fotoğrafını görebilir. `AdminService.GetReportedItem` şikâyet
`event_photo` ise, şikâyetin hedefinden okunan dosyanın full varyantı
için **5 dakikalık** imzalı GET linki üretir (`ReportedItem.media_url`).
Her link üretimi, şikâyeti görüntüleme kaydıyla birlikte platform denetim
kaydına yazılır (`report.view_item`, `media_link = issued`, `upload_id`;
link değil). Link başka hiçbir uçtan alınamaz: panelin dosya kimliğiyle
istediği bir uç yoktur, kimlik şikâyetten core içinde okunur; şikâyet
edilmemiş fotoğraf panelde görünmez. Panel CSP'si (`img-src 'self'`)
değişmez: görüntü sayfaya gömülmez, `rel="noopener noreferrer"` ile yeni
sekmede açılan bir bağlantıdır (panel adresi R2'ye gitmez). Link log'a
yazılmaz. Dosya silme kuyruğundaysa link üretilmez.

### 6. KVKK: dışa aktarma ve hesap silme (ADR-0021)

- **Dışa aktarma:** yeni bölüm `core/event_photos.json`: kullanıcının
  yüklediği her fotoğraf (`id`, `event_id`, `tayfa_id`, `upload_id`,
  `caption`, `created_at`, `deleted_at`). Dosyanın kendisi ADR-0021 §5
  gereği zip'e girmez; `core/uploads.json` zaten `event_photo`
  yüklemelerini listeler, istemci "Fotoğraflarımı da indir" ile
  `POST /v1/media/urls`'ten alır (kendi dosyası her zaman görünür).
- **Hesap silme (`user.deleted` tüketicisi):** kişinin albüm satırları
  silinir (fotoğraf kişiseldir, kapak gibi tayfaya kalmaz); dosyalar
  hem `DeleteUserUploads` hem `uploads_release_ref` ile silme kuyruğuna
  girer. Kişinin başkasının fotoğrafını silmiş olması (`deleted_by`) NULL
  yapılır. Tüketici idempotenttir.

### 7. Ele geçirilirse (ADR-0011)

- **gateway:** Albüm uçları core'a kullanıcının token'ıyla gider; core
  imzayı, üyeliği, izni, katılımı ve sınırları kendisi denetler. gateway
  başka tayfanın albümünü okuyamaz.
- **core uygulama katmanı atlanırsa** (`core_app` ile doğrudan SQL):
  `event_photos_guard` iptal, `attended` ve 30/500 sınırını; `uploads_check_ref`
  dosyanın bu satıra bağlı, hazır, doğru amaçlı olduğunu; `uploads`'ın kolon
  izinleri dosyanın anahtarının, amacının ve tayfasının değiştirilemediğini
  zorlar. `core_app` `event_photos`'ta yalnızca `deleted_at` ve
  `deleted_by`'ı güncelleyebilir: fotoğraf başka etkinliğe ya da başka
  dosyaya taşınamaz, yükleyeni değiştirilemez.
- **Link sızması:** linkler 1 saatliktir ve tek dosyaya bağlıdır; albümün
  listesi link taşımadığı için önbelleğe ya da log'a düşen bir liste
  yanıtı dosya açmaz. Panel linki 5 dakikalıktır ve yalnızca şikâyet
  edilmiş fotoğraf için, denetim kaydıyla üretilir.
- **Panel ele geçirilirse:** platform yöneticisi oturumuyla (her çağrıda
  identity'ye sorulur) yalnızca platforma yükselmiş şikâyetlerin
  fotoğraflarına ve her birini denetim kaydında iz bırakarak ulaşılır;
  albümler ya da şikâyetsiz fotoğraflar açılamaz.
- **Tutulmayan veri:** Fotoğrafın metadata'sı (GPS dahil) istemcide
  silinir ve sunucu başlıklarda metadata görürse reddeder (ADR-0020);
  açıklama ve dosya anahtarı log'a yazılmaz.

## Sonuçlar

- Albüm mevcut medya hattının, izin tablosunun ve şikâyet akışının üstüne
  oturur; yeni altyapı yoktur.
- Kişi başı 30 × 2 varyant × ≤ 10 MiB kişinin 500 MiB kotasına sayılır;
  saatlik 30 yükleme sınırı aynen geçerlidir (bir etkinliğin 30 fotoğrafı
  bir saatte yüklenebilir).
- Veri Modeli v15'te `event_photos` "Planlanan" bölümünden core
  tablolarına taşınır; `uploads.purpose` ve `reports.target_type`
  listelerine `event_photo` eklenir.

## Açık ayrıntılarda kararlar

Platform sahibi, 2026-11-15 (sohbette):

1. Organizatörün sonradan işaretlediği katılım (`attendance_method =
   organizer`) doğrulanmış sayılır (§2).
2. Açıklama isteğe bağlı, 1–200 karakter; kötüye kullanım filtresi yok.
3. Silmede rol hiyerarşisi yok ("her fotoğrafı").
4. Albüm sırası eskiden yeniye.
5. Platform paneli yalnızca şikâyet edilmiş fotoğrafı, kısa ömürlü imzalı
   linkle ve denetim kaydıyla görür (§5).
6. Albüm hatırlatma bildirimi ("albüme fotoğraf ekle") sonraki turdadır.
