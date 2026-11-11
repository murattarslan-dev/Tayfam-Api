# ADR-0020: Görüntü işleme istemcide, sunucuda başlık denetimi; medya bağlama ve sunumu

- **Durum:** Kabul edildi (platform sahibi, 2026-11-11)
- **Tarih:** 2026-11-11
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0007](0007-dosyalar-ve-medya.md) (bu ADR onun "İşleme",
  "Yükleme akışı", "Sunma" ve "Bucket'lar" bölümlerini kısmen değiştirir),
  [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0019](0019-push-bildirimleri.md) (isteğe bağlı secret ve ağ çıkışı kalıbı); issue #135
- **Kaynak:** Notion: Veri Modeli (taslak v14) "Dosyalar", "Yüklenen dosya
  takibi", "Saklama süreleri"

## Bağlam

ADR-0007 dosyaların sahibini (core), R2'yi, doğrudan yüklemeyi, karantinayı
ve **sunucuda** libvips (govips, cgo) ile yeniden kodlamayı seçti. Uygulamaya
geçerken sunucu tarafı yeniden kodlamanın bedeli netleşti:

- Tüm servisler tek `Dockerfile`'la `CGO_ENABLED=0` derlenir ve
  `distroless/static` üzerinde çalışır. libvips, core image'ına onlarca
  paylaşımlı C kütüphanesi (libjpeg, libpng, libwebp, libheif, libde265,
  libexif, glib…) ve CI'ın test, lint ve govulncheck işlerine `libvips-dev`
  kurulumunu (`.github/` değişikliği) getirir.
- Bu C çözücüleri güvenilmeyen girdiyi ayrıştıran, bellek güvenliği
  açıklarıyla bilinen kodlardır (ör. libwebp CVE-2023-4863) ve core'un
  veritabanı kimliğini ve R2 anahtarını tutan süreçte çalışırdı: ADR-0011'in
  "hasarı sınırla" ilkesiyle çelişir.
- Go standart kütüphanesi HEIC ve WebP okuyamaz, WebP yazamaz. iPhone
  kamerası varsayılan olarak HEIC üretir.
- Telefon zaten resmi çözer ve gösterir; HEIC'i okuyabilen, küçültüp
  JPEG/WebP yazabilen platform kodlayıcıları cihazda vardır. Küçültmeyi
  cihazda yapmak, zayıf mobil bağlantıda 10 MB yerine birkaç yüz KB
  yüklemek demektir.

Sunucuda saf Go yeniden kodlama (yalnızca JPEG/PNG, JPEG çıktı) ve WASM
içinde libwebp/libheif de değerlendirildi (aşağıda).

## Karar

### 1. Görüntüyü mobil istemci işler; sunucu pikselleri hiç çözmez

- İstemci fotoğrafı okur (HEIC dahil), **EXIF (GPS dahil), XMP ve diğer
  metadata'yı siler**, yönü piksellere uygular ve iki varyant kodlar:
  `preview` (en uzun kenar ≤ 400 px) ve `full` (en uzun kenar ≤ 1600 px),
  her biri JPEG ya da WebP.
- Sunucu hiçbir zaman çözmez ve yeniden kodlamaz. ADR-0007'deki sunucu
  tarafı yeniden kodlama ve libvips **bırakıldı**: cgo, büyük image, CI
  değişikliği ve core sürecinde C çözücüsü olmadan aynı hedefe (metadata'sız,
  küçük, iki boyutlu dosya) ulaşılır.

### 2. Sunucu ucuz denetim yapar (yalnızca başlıklar)

`CompleteUpload` her varyant için, aynı çağrıda:

1. R2 `HEAD`: nesne var mı, boyutu bildirilen (ve imzayla zorlanan) boyutla
   aynı mı (`size_mismatch`).
2. Nesnenin başı `Range` ile okunur (en çok 256 KiB, `If-Match` ile HEAD'deki
   ETag'e bağlı). Küçük bir standart kütüphane ayrıştırıcısı
   (`internal/core/app/media/inspect`):
   - **Gerçek tür** ilk baytlardan: JPEG `FF D8 FF`, WebP `RIFF….WEBP`;
     bildirilen türle aynı olmalı. PNG, HEIC, GIF ve animasyonlu WebP
     `unsupported_type`; tanınmayan içerik `not_image`.
   - **JPEG:** `SOS`'a kadar bütün segmentler taranır; `SOFn`'den boyut
     okunur. `APP1 Exif` (GPS EXIF'in içindedir), `APP1` XMP
     (`http://ns.adobe.com/xap/1.0/`, genişletilmiş XMP) ve `APP13`
     (Photoshop/IPTC) varsa `metadata_present`. JFIF, ICC ve Adobe
     segmentlerine izin verilir. Metadata segmentleri 256 KiB'a sığmalıdır.
   - **WebP:** RIFF chunk'ları okunur: `VP8 `/`VP8L` başlığından ya da
     `VP8X` tuvalinden boyut; `VP8X`'te EXIF/XMP bayrağı ya da `EXIF`/`XMP `
     chunk'ı varsa `metadata_present`; animasyon `unsupported_type`.
   - Ayrıştırıcı fuzz testiyle korunur (`go test -fuzz`, tohum korpusu).
3. **Boyutlar:** `full` başlıktaki ölçü istemcinin bildirdiği `width`,
   `height` ile aynı ve en uzun kenar ≤ 1600; `preview` en uzun kenar ≤ 400
   (`too_large`) ve en-boy oranı `full` ile en çok ±1 px yuvarlama farkıyla
   aynı (`dimension_mismatch`).
4. Geçen varyantlar karantinadan (`incoming/`) istemcinin yazamadığı
   `media/` altına R2 içinde **aynen kopyalanır** (`x-amz-copy-source-if-match`
   ile denetlenen ETag'e bağlı): 5 dakika geçerli PUT linki denetimden sonra
   kullanılıp dosya değiştirilemez. Ham varyantlar `incoming/`'dan silinir.

Sonuç `ready` ya da `rejected`'tır; işleme kuyruğu ve işçisi yoktur. Ret
sebepleri: `not_image`, `unsupported_type`, `metadata_present`,
`dimension_mismatch`, `size_mismatch`, `too_large`, `failed`. R2'ye
ulaşılamazsa yükleme `pending` kalır, istemci tekrar dener.

### 3. Kabul edilen risk (ADR-0011)

Sunucu pikselleri çözmediği için **başlığı geçerli ama gövdesi kötü niyetle
hazırlanmış** bir dosya (ör. bir çözücü açığını hedefleyen sıkıştırılmış
veri) denetimden geçip diğer kullanıcıların cihazlarındaki çözücülere
ulaşabilir. Sunucu tarafı yeniden kodlama bunu önlerdi, ama aynı riski kendi
sürecine (veritabanı ve R2 kimliğiyle) taşırdı. Bu risk bilerek kabul
edildi; mobil platform çözücüleri (Android/iOS) korumalı ve güncellenen
bileşenlerdir. Uygulamanın kendisinin yüklediğini doğrulayan imza (Play
Integrity / App Attest) **ayrı bir issue** olarak oturuma bağlanacak ve bu
riski azaltacaktır; bu ADR onun için kodda yer açmaz.

Metadata denetimi başlık düzeyindedir: piksellere gömülü bilgi (fotoğrafta
görünen bir tabela gibi) içeriktir ve denetlenmez.

### 4. Yükleme akışı ve kimlikler

1. `POST /v1/uploads {purpose, variants: [{kind: preview|full,
   content_type: image/jpeg|image/webp, size_bytes ≤ 10 MiB}], tayfa_id?,
   client_request_id}` → her varyant için 5 dakika geçerli, yalnızca o
   anahtar, o tür ve o boyut için imzalı PUT. **Anahtarı sunucu belirler**
   (`incoming/<rastgele>/preview|full`).
2. İstemci iki varyantı PUT eder.
3. `POST /v1/uploads/{id}/complete {width, height}` (full'ün ölçüleri) →
   denetim → `ready | rejected`.

Kimlik yalnızca `upload_id`'dir; istemcinin verdiği URL ya da anahtar
hiçbir uçta kabul edilmez. Durumlar `pending → ready | rejected` ve silme
kuyruğu için `deleting`. Notion v14'teki `attached` durumu yerine bağlanma
ayrı alanlardadır (`attached_type`, `attached_id`); **Notion'a not
düşülmeli.**

### 5. Bağlama

- Bir dosyayı yalnızca sahibi, yalnızca amacına uygun ve aynı tayfanın
  içeriğine, yalnızca bir kez bağlar. Veritabanı bunu ikinci kez denetler:
  içerik kolonu (`tayfas.cover_upload_id`, `events.cover_upload_id`,
  `fishing_catches.photo_upload_id`) yalnızca o içeriğe bağlanmış hazır
  dosyayı kabul eder (trigger, `TF080`).
- İçerik dosyayı bıraktığında (kolon değişti ya da satır kalıcı silindi,
  cascade dahil) eski dosya trigger'la silme kuyruğuna girer. Soft delete
  dosyayı bırakmaz (içerik 30 gün geri alınabilir).
- **Avatar:** identity medyanın sahibi değildir. `PUT /v1/me/avatar` →
  identity `SetAvatar` → core `MediaService.AttachAvatar` (kullanıcının
  kendi token'ıyla) → identity `users.avatar_upload_id`'yi yazar ve
  `user.profile_updated` yayınlar; core olayı alınca eski avatarları siler.
- Hesap silme (`user.deleted`): kullanıcının dosyaları silinir; tayfaya
  bağlı kapaklar sahipsiz kalır.
- Serinin ürettiği etkinlikler kapak taşımaz.

### 6. Sunma

- API yanıtları link taşımaz, yalnızca `*_upload_id`. İstemci linkleri
  `POST /v1/media/urls` ile toplu ister (en çok 100) ve 1 saat önbellekte
  tutar. Görülemeyen kimlik nedeni söylenmeden atlanır.
- Görme kuralları (izinler core'un izin tablosundan):

  | Dosya | Kim görür |
  |-------|-----------|
  | Kendi dosyası | Her zaman |
  | Avatar | Oturum açmış herkes; iki taraftan biri engellediyse ya da hesap silindiyse kimse |
  | Tayfa kapağı | Tayfanın aktif üyeleri (`TayfaView`) |
  | Etkinlik kapağı, av fotoğrafı | Etkinliği görebilen aktif üyeler (`EventView`, etkinlik modülü açık, içerik silinmemiş) |

- Herkese açık kopya (public bucket, `cdn.tayfam.app`) ve keşfette kapak
  Faz 2'dedir; o zamana kadar tek bucket: `tayfam-<ortam>-private`
  (`incoming/` karantina, `media/` denetlenmiş).

### 7. R2 istemcisi: yalnızca standart kütüphane

- AWS Signature V4 (HMAC-SHA256, `crypto/hmac`, `crypto/sha256`) ile imzalı
  PUT/GET linkleri ve HEAD, Range GET, kopyalama, DELETE. Bu, "kendi
  kriptografini yazma" (kural 4) ihlali değildir: yazılan yalnızca
  imzalanacak metnin kanonik biçimidir. `aws-sdk-go-v2` onlarca modül
  getirir. İmza AWS'nin belgelenmiş test vektörleriyle test edilir.
- Sunucu dosya içeriği yazmaz (PUT yalnızca istemcinin imzalı linkiyle).
- Uç adresi koddadır: `https://<hesap>.r2.cloudflarestorage.com`, hesap
  kimliği 32 hex. Yönlendirme izlenmez; istek süresi 30 sn.

### 8. Yapılandırma ve ağ (ADR-0019 kalıbı)

- `core-r2` secret'ı (`account-id`, `bucket`, `access-key-id`,
  `secret-access-key`) isteğe bağlıdır. Hiçbiri yoksa medya kapalıdır: core
  başlar, `MediaService` `UNAVAILABLE` (503) döner. Bir kısmı varsa ya da
  biçimi bozuksa core başlamaz.
- Medya kapalıyken core'un internete çıkışı yoktur. `core-egress-r2`
  (TCP 443, özel adresler hariç) ortamın `core-r2` secret'ıyla aynı PR'da o
  ortamın overlay'ine eklenir.
- R2 API token'ı yalnızca o ortamın bucket'ına "Object Read & Write"
  yetkilidir (ADR-0011 madde 11). `incoming/` için 1 günlük lifecycle kuralı
  platform sahibinin adımıdır (`docs/runbooks/sops.md` "Dosyalar (R2)").
- Görüntü çözülmediği için core'un bellek sınırı değişmez.

### 9. Sınırlar

Saatte 30 yükleme (`MEDIA_UPLOADS_PER_HOUR`), kullanıcı başına 500 MiB
(`MEDIA_QUOTA_MIB`, iki varyantın bildirilen boyutları), varyant başına
10 MiB, bağlanmamış hazır dosya 7 gün, `pending` ve `rejected` 24 saat.
Sayaçlar veritabanındadır.

### 10. Gizlilik ve log

R2 nesne anahtarı, imzalı linkler, erişim anahtarı ve imza, dosya içeriği
ve metadata'sı log'a, metriğe, olaya ve hata mesajına girmez; log yükleme
kimliği, durum, ret sebebi ve HTTP durum kodu taşır. Bu, log yakalayarak
tüm yolları geçen bir testle sabittir.

## Gerekçe

- İşleme cihazda: sunucuda C çözücüsü ve cgo yok, CI ve image değişmez,
  HEIC desteği bedava, yükleme küçük.
- Başlık denetimi ucuzdur (her varyant için birkaç küçük istek, piksel yok)
  ve istemcinin sözünü doğrular: metadata'lı, yanlış türde ya da büyük dosya
  sunulmaz. Karantinadan kopyalama, imzalı linkin denetimden sonra
  kullanılmasını engeller.
- Toplu link ucu görme yetkisini tek yerde toplar, liste uçlarını
  değiştirmez.

## Sonuçlar

- core: `media` paketi, başlık ayrıştırıcısı (`media/inspect`), R2 istemcisi
  (`internal/platform/objstore`), `MediaService`, `media-cleanup` işçisi;
  gateway `/v1/uploads`, `/v1/media/urls`, `/v1/me/avatar`.
- İstemci (Flutter) gereksinimleri: HEIC/JPEG/PNG okuma, yön uygulama,
  metadata'sız iki varyant (JPEG ya da WebP), boyutları ve bayt
  uzunluklarını bildirme, imzalı PUT, tamamlama.
- Platform sahibinin işi: bucket, lifecycle, en az yetkili token,
  `scripts/new-r2-secret.sh`, `core-egress-r2`; ilk açılışta R2'nin imzalı
  boyutu ve koşullu kopyalamayı uyguladığının denenmesi.
- Ayrı issue: uygulama imzası (Play Integrity / App Attest).

## Değerlendirilen alternatifler

- **Sunucuda libvips (ADR-0007):** her türü okur ve WebP yazar; ama cgo,
  büyük image, `.github/` değişikliği ve core sürecinde C çözücüsü.
- **Sunucuda saf Go yeniden kodlama:** cgo yok, çözücü bellek güvenli; ama
  HEIC ve WebP okunamaz, çıktı yalnızca JPEG, yükleme büyük kalır ve core'un
  bellek sınırı artar.
- **WASM içinde libwebp + libheif (wazero):** çözücü kum havuzunda; ama üç
  yeni modül, LGPL ve HEVC patent belirsizliği, işlem sunucuda.
- **İşlemeyi ayrı, ağsız bir pod'a taşımak:** en iyi yalıtım, ama yeni
  servis ve sözleşme. Cihazda işleme aynı kazancı servissiz verir.
- **İmzalı linkleri her yanıta eklemek (ADR-0007):** her liste ucuna imza ve
  yetki yükü, önbelleklenemeyen yanıtlar.
