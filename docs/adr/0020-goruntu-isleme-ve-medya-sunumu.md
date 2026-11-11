# ADR-0020: Görüntü işleme, medya bağlama ve sunumu

- **Durum:** Önerildi (platform sahibinin kararı bekleniyor: madde 1)
- **Tarih:** 2026-11-11
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0007](0007-dosyalar-ve-medya.md) (bu ADR onun "İşleme",
  "Sunma" ve "Bucket'lar" bölümlerini değiştirir), [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0019](0019-push-bildirimleri.md) (isteğe bağlı secret ve ağ çıkışı kalıbı); issue #135
- **Kaynak:** Notion: Veri Modeli (taslak v14) "Dosyalar", "Yüklenen dosya
  takibi", "Saklama süreleri"

## Bağlam

ADR-0007 dosyaların sahibini (core), R2'yi, doğrudan yüklemeyi, karantinayı
ve libvips (govips, cgo) ile yeniden kodlamayı seçti. Uygulamaya geçerken üç
şey netleşti:

- **libvips bu repoda bugün gerçekçi değil.** Tüm servisler tek
  `Dockerfile`'la `CGO_ENABLED=0` derlenir ve `distroless/static` üzerinde
  çalışır. govips için core image'ı libvips ve onun onlarca paylaşımlı
  kütüphanesini (libjpeg, libpng, libwebp, libheif, libde265, libexif,
  glib…) taşıyan bir tabana geçmeli; CI'ın test, lint ve govulncheck işleri
  `libvips-dev` kurmalıdır (`.github/` değişikliği, yalnızca platform
  sahibi). Ayrıca bu C kütüphaneleri güvenilmeyen girdiyi (kullanıcının
  dosyası) ayrıştıran, tarihçesi bellek güvenliği açıklarıyla dolu kodlardır
  (ör. libwebp CVE-2023-4863) ve core'un veritabanı kimliğini ve R2
  anahtarını tutan süreçte çalışır. ADR-0011'in "hasarı sınırla" ilkesiyle
  çelişir.
- Go standart kütüphanesi JPEG ve PNG'yi okur ve JPEG yazar; WebP yazamaz,
  WebP ve HEIC okuyamaz. Yeni bir modül (ör. `golang.org/x/image`) eklemek
  ise WebP'yi yalnızca okumayı getirir.
- ADR-0007 imzalı linklerin "API yanıtlarına eklenmesini" söyler. Avatar
  kimliği bugün onlarca uçta (üye listeleri, arkadaşlar, profil, yazarlar)
  döner; her birine link eklemek her listeye R2 imzası ve görme yetkisi
  kontrolü taşır.

## Karar

### 1. Görüntü işleme: seçenekler (platform sahibi seçer)

| | A. Standart kütüphane (uygulandı) | B. libvips / govips (ADR-0007) | C. WebAssembly içinde libwebp + libheif |
|---|---|---|---|
| Giriş | JPEG, PNG | JPEG, PNG, WebP, HEIC | JPEG, PNG, WebP, HEIC |
| Çıkış | JPEG (kalite 82) | WebP | WebP |
| Bağımlılık | yok | govips + sistem kütüphaneleri | `github.com/tetratelabs/wazero` (bağımlılıksız) + `github.com/gen2brain/webp`, `github.com/gen2brain/heic` |
| cgo / image | yok / değişmez | var / distroless static'ten çıkar, büyür | yok / değişmez, binary ~10 MB büyür |
| CI (`.github/`) | değişmez | `libvips-dev` kurulumu (platform sahibi) | değişmez |
| Çözücü açığında | Go bellek güvenli; panik süreçten kaçmaz (recover) | süreçte bellek bozulması, DB ve R2 kimliğine erişim | WASM kum havuzunda kalır; süreç belleğine ve sistem çağrılarına erişemez |
| Hız | yavaş ama bu ölçekte yeter (12 MP ~1 sn) | en hızlı | orta (~2–3x yerel) |
| Lisans | — | LGPL (dinamik) | libheif LGPL, WASM binary'ye gömülü; HEVC patent belirsizliği |

**Uygulanan: A.** İstemci WebP ve HEIC'i yüklemeden önce JPEG'e çevirir
(iOS kamera HEIC'i; Flutter'da `image_picker`'ın JPEG çıktısı ya da
`flutter_image_compress`). Sunucu `image/webp` ve `image/heic` beyanını
`media-type-unsupported` (415) ile reddeder; beyan yanlışsa gerçek tür
işlemede yakalanır (`unsupported_type`). Sözleşmede dört tür de vardır;
işleyici değişince istemci değişmeden açılır.

**Öneri:** A ile başlamak, WebP çıkışı veya sunucuda HEIC gerektiğinde C'ye
geçmek (ayrı PR: iki modül, go.sum, ADR güncellemesi). B'yi yalnızca
işleme ayrı, internete ve veritabanına çıkışı olmayan bir sürece/pod'a
taşınırsa düşünmek.

İşleyici `media.Processor` arayüzünün arkasındadır; seçenek değişikliği
uygulama ve taşıma katmanlarını etkilemez.

### 2. İşlemenin kuralları

- Gerçek tür ilk baytlardan okunur (JPEG `FF D8 FF`, PNG imzası; WebP
  `RIFF…WEBP`, HEIC `ftyp heic/heix/mif1…` tanınır ama A'da desteklenmez).
  Bildirilen tür ve dosya adı yok sayılır.
- Boyut başlıktan okunur, çözmeden önce sınırlanır: en uzun kenar 10000 px,
  en fazla 25 megapiksel (sıkıştırma bombasına karşı; core'un bellek
  sınırı 384 MiB'a çıkar, aynı anda tek dosya işlenir).
- Yön EXIF Orientation etiketinden okunur ve piksellere uygulanır; okunan
  tek metadata budur. Çıktı yalnızca piksellerden yeniden kodlanır: EXIF,
  GPS, XMP, ICC, yorumlar ve gömülü küçük resimler taşınmaz. Saydamlık
  beyaz zemine düzleştirilir.
- İki boyut: önizleme (en uzun kenar 400 px) ve tam (1600 px); küçük
  görsel büyütülmez. Ham dosya işlendikten (ya da reddedildikten) sonra
  `incoming/`'dan silinir.
- Geçici hatalar (R2'ye ulaşılamaması) en çok 5 denemeyle, artan aralıkla
  yeniden denenir; sonra `failed` ile reddedilir. Dosyaya bağlı hatalar
  (resim değil, bozuk, çok büyük) yeniden denenmez.

### 3. Durumlar ve bağlama

- Durumlar ADR-0007'deki gibidir: `pending → uploaded → processing → ready
  | rejected`; ek olarak `deleting` (R2 nesneleri silinecek, sonra satır).
  Notion v14'teki `attached` durumu yerine bağlanma ayrı alanlardadır
  (`attached_type`, `attached_id`): hazır dosya bağlansa da bağlanmasa da
  sunulabilir; 7 gün bağlanmayan silinir. **Notion'a not düşülmeli.**
- Bir dosyayı yalnızca sahibi, yalnızca amacına uygun ve aynı tayfanın
  içeriğine, yalnızca bir kez bağlar. Veritabanı bunu ikinci kez kontrol
  eder: içerik kolonu (`tayfas.cover_upload_id`, `events.cover_upload_id`,
  `fishing_catches.photo_upload_id`) yalnızca o içeriğe bağlanmış hazır
  dosyayı kabul eder (trigger, `TF080`).
- İçerik dosyayı bıraktığında (kolon değişti ya da satır kalıcı silindi,
  cascade dahil) eski dosya trigger'la silme kuyruğuna girer; kod yolundan
  bağımsızdır. Soft delete dosyayı bırakmaz (içerik 30 gün geri alınabilir).
- **Avatar:** identity medyanın sahibi değildir. `PUT /v1/me/avatar` →
  identity `SetAvatar` → core `MediaService.AttachAvatar` (kullanıcının
  kendi token'ıyla; identity'nin servis kimliği kullanılmaz) → identity
  `users.avatar_upload_id`'yi yazar ve `user.profile_updated` yayınlar.
  core olayı alınca kullanıcının güncel avatarı dışındaki bağlı avatarlarını
  siler; chat kopyasını günceller. Ağ izni (identity → core) zaten vardır.
- Hesap silme (`user.deleted`): kullanıcının dosyaları silinir; tayfaya
  bağlı kapak görselleri tayfanındır, yalnızca sahipliği kaldırılır.
- Serinin ürettiği etkinlikler kapak taşımaz (dosya tek içeriğe bağlanır).

### 4. Sunma

- İmzalı linkler içerik yanıtlarına gömülmez; yanıtlar `*_upload_id`
  taşır. İstemci linkleri `POST /v1/media/urls` ile toplu ister (en çok 100
  kimlik) ve 1 saat önbellekte tutar. Görülemeyen kimlik nedeni
  söylenmeden atlanır.
- Görme kuralları (izinler core'un izin tablosundan):

  | Dosya | Kim görür |
  |-------|-----------|
  | Kendi dosyası | Her zaman |
  | Avatar | Oturum açmış herkes; iki taraftan biri engellediyse ya da hesap silindiyse kimse |
  | Tayfa kapağı | Tayfanın aktif üyeleri (`TayfaView`) |
  | Etkinlik kapağı, av fotoğrafı | Etkinliği görebilen aktif üyeler (`EventView`, etkinlik modülü açık, içerik silinmemiş) |

- Herkese açık kopya (public bucket, `cdn.tayfam.app`) ve keşfette kapak
  Faz 2'dedir; o zamana kadar tek bucket vardır: `tayfam-<ortam>-private`
  (`incoming/`, `media/`).

### 5. R2 istemcisi: yalnızca standart kütüphane

- AWS Signature V4 (HMAC-SHA256, `crypto/hmac`, `crypto/sha256`) ile
  imzalı PUT/GET linkleri ve HEAD/GET/PUT/DELETE istekleri. Bu, "kendi
  kriptografini yazma" (kural 4) ihlali değildir: yazılan yalnızca
  imzalanacak metnin kanonik biçimidir; ilkel standart kütüphanededir.
  `aws-sdk-go-v2` onlarca modül getirir; altı istek için gerekmez. İmza
  AWS'nin belgelenmiş test vektörüyle test edilir.
- Yükleme linki 5 dakika geçerlidir ve `content-type` ile `content-length`
  imzaya dahildir: başka tür ya da boyutla, başka anahtara kullanılamaz.
  İndirme linkleri 1 saattir.
- Uç adresi koddadır: `https://<hesap>.r2.cloudflarestorage.com`, hesap
  kimliği 32 hex. Yapılandırma başka bir adres veremez; yönlendirme
  izlenmez; istek süresi 30 sn (indirme ve yükleme gövdesi en çok
  10 MiB + 1 bayt okunur).

### 6. Yapılandırma ve ağ (ADR-0019 kalıbı)

- `core-r2` secret'ı (`account-id`, `bucket`, `access-key-id`,
  `secret-access-key`) isteğe bağlıdır (`optional: true`). Hiçbiri yoksa
  medya **kapalıdır**: core başlar, bilgi log'u yazar, `MediaService`
  `UNAVAILABLE` döner (`503 unavailable`), içerikler dosyasız çalışır.
  Bir kısmı varsa ya da biçimi bozuksa core başlamaz.
- **Medya kapalıyken core'un internete çıkışı yoktur.** R2'ye çıkış
  (`core-egress-r2`, TCP 443, özel adresler hariç) ortamın `core-r2`
  secret'ının eklendiği PR'da **o ortamın** overlay'ine eklenir; `deploy/base`'de
  bulunmaz. YAML ve adımlar `docs/runbooks/sops.md` "Dosyalar (R2)"dadır.
- R2 API token'ı yalnızca o ortamın bucket'ına "Object Read & Write"
  yetkilidir (ADR-0011 madde 11: yedek anahtarı ayrıdır ve dosyaları
  okuyamaz). `incoming/` için 24 saatlik lifecycle kuralı platform
  sahibinin adımıdır.

### 7. Sınırlar

Yapılandırmadan (`MEDIA_UPLOADS_PER_HOUR`, varsayılan 30;
`MEDIA_QUOTA_BYTES`, varsayılan 500 MiB). Kota işlenmiş dosyaların
boyutunu, işlenmemişlerin bildirilen ham boyutunu sayar; silinen ve
reddedilenler sayılmaz. Sayaçlar veritabanındadır (birden çok pod'da
doğru).

### 8. Gizlilik ve log

R2 nesne anahtarı, imzalı linkler, erişim anahtarı ve imza, dosya
içeriği ve metadata'sı (EXIF, GPS) log'a, metriğe, olaya ve hata
mesajına girmez; log yükleme kimliği, durum, ret sebebi, HTTP durum kodu
ve süreler taşır. Bu, log yakalayarak tüm yolları geçen bir testle
sabittir.

## Gerekçe

- A bugün CI ve image değişikliği olmadan, yeni bağımlılık olmadan
  gerçekten çalışır; güvenilmeyen dosyayı bellek güvenli kodla çözer.
  Kaybedilen şey WebP çıktısının boyut kazancı ve sunucuda HEIC'tir; ikisi
  de istemcide ya da C ile telafi edilir.
- Toplu link ucu, görme yetkisini tek yerde (core, izin tablosu) toplar ve
  mevcut liste uçlarını değiştirmeden avatarları da kapsar.
- Bağlamanın veritabanında ikinci kez kontrol edilmesi ve içerikten
  bağımsız silme trigger'ı, uygulama hatalarında bile başkasının
  dosyasının gösterilmesini ya da sahipsiz dosya kalmasını önler.

## Sonuçlar

- core yeni bir `media` uygulama paketi, R2 istemcisi
  (`internal/platform/objstore`), `MediaService`, `media-process` ve
  `media-cleanup` işçileri kazanır; gateway `/v1/uploads`, `/v1/media/urls`,
  `/v1/me/avatar` uçlarını.
- core'un bellek sınırı 384 MiB olur.
- Platform sahibinin işi: ortam başına R2 bucket'ı, lifecycle kuralı, en
  az yetkili API token'ı, `scripts/new-r2-secret.sh` ve aynı PR'da
  `core-egress-r2` (`docs/runbooks/sops.md`).
- Notion veri modeli v14'e not: `uploads.status` değerleri ve bağlama
  alanları; `purpose`'a `cover` (ADR-0009'da zaten var).

## Değerlendirilen alternatifler

- **B (libvips) şimdi:** ADR-0007'nin seçimi; yukarıdaki nedenlerle
  bugün CI ve image değişikliği ister ve hasar alanını büyütür.
- **İşlemeyi ayrı bir pod'a taşımak (B veya C ile):** En iyi yalıtım
  (ağsız, veritabanısız işçi), ama yeni bir servis, iç sözleşme ve dağıtım
  getirir; trafik ve tehdit arttığında yeniden değerlendirilir.
- **İmzalı linkleri her yanıta eklemek (ADR-0007):** İstemci için tek
  istek, ama her liste ucuna imza ve yetki yükü, önbelleklenemeyen
  yanıtlar.
- **Yeni bir `golang.org/x/image` bağımlılığı:** WebP'yi yalnızca okur;
  yazamaz, HEIC'i okumaz. Kazancı küçük.
