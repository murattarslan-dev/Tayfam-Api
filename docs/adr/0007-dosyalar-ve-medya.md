# ADR-0007: Dosyalar ve medya

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Arslan
- **Revizyon:** Kısmen [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md) ile
  değişti (kabul edildi, platform sahibi, 2026-11-11): görüntüyü sunucu
  değil mobil istemci işler (iki varyant, metadata'sız); sunucu yalnızca
  başlıkları denetler ve libvips kullanılmaz. Yükleme akışı, durumlar,
  sunma (toplu imzalı link ucu) ve Faz 2'ye kadar tek bucket ADR-0020'dedir.
  "İşleme", "Yükleme akışı", "Sunma" ve "Bucket'lar" bölümlerinin geri
  kalanı geçerlidir.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0006](0006-dagitim-ve-ortamlar.md)

## Bağlam

MVP'de dosyalar profil fotoğrafı, av fotoğrafı, kapak görselleri ve KVKK veri
dışa aktarma dosyalarıdır; ilan fotoğrafları, etkinlik albümü ve sohbette
fotoğraf Faz 2'dedir (Notion özellik haritası). Dosyalar Cloudflare R2'de
saklanır; R2'nin yurt dışında olması Notion'daki planda bilinçli bir tercih
olarak kabul edilmiştir. Fotoğraflar çoğunlukla telefondan, bazen zayıf mobil
bağlantıyla yüklenir ve konum bilgisi (EXIF GPS) içerebilir; gizli bir balık
merasının fotoğraftan sızmaması gerekir.

Notion'daki planda "dosyalar herkese açık bucket'ta durmaz" ilkesi vardı. Bu
ADR ile ilkeye tek bir istisna getirilir: paylaşılan linklerin önizlemesi
(link preview) için açıkça işaretlenen kapak görselleri herkese açık olabilir.

## Karar

### Sahiplik

- Medya kayıtlarının (veri modelindeki `uploads` tablosu) ve işlenmesinin
  sahibi **core**'dur (`media` domain paketi). Diğer servisler medyaya ID ile
  referans verir.
- `uploads.purpose` değerleri: `avatar`, `catch_photo`, `cover`,
  `data_export`; Faz 2'de `event_photo` ve `listing_photo` eklenir. Kapak
  görselleri için veri modeline gereken alanlar ADR-0009'dadır.
- Veri dışa aktarma dosyaları da private bucket'ta, şifrelenmiş zip olarak
  tutulur ve 7 gün sonra silinir (ADR-0008).

### Bucket'lar

Her ortam için iki bucket vardır:

| Bucket | İçerik | Erişim |
|--------|--------|--------|
| `tayfam-<ortam>-private` | `incoming/` (ham yüklemeler), `media/` (işlenmiş dosyalar) | Yalnızca imzalı linklerle |
| `tayfam-<ortam>-public` | Herkese açık işaretlenmiş görsellerin kopyaları | `cdn.tayfam.app` (dev: `cdn-dev.tayfam.app`) üzerinden, Cloudflare önbelleği ile |

### Yükleme akışı

1. İstemci core'dan yükleme ister: dosya türü, boyut ve hangi içeriğe ait
   olduğu. core yetkiyi, türü ve boyutu kontrol eder.
2. core bir medya kaydı oluşturur ve **5 dakika geçerli, yalnızca o dosya,
   o tür ve o boyut için** imzalı bir R2 yükleme linki döner. Dosya
   `incoming/` altına yazılır.
3. İstemci dosyayı doğrudan R2'ye yükler; dosya sunucularımızdan geçmez.
4. İstemci yüklemenin bittiğini bildirir. core dosyanın varlığını ve boyutunu
   kontrol eder ve işleme kuyruğuna ekler.

Medya kaydının durumları: `bekliyor` → `yüklendi` → `işleniyor` → `hazır`
veya `reddedildi`.

### İşleme

- core içindeki bir işçi, Postgres'teki iş tablosundan (`FOR UPDATE SKIP
  LOCKED`) işleri alır.
- Dosya içeriği gerçekten bir resim mi diye kontrol edilir; uzantıya ve
  istemcinin bildirdiği türe güvenilmez.
- Resim yeniden kodlanır: **EXIF ve tüm metadata silinir** (GPS konumu dahil),
  yönü düzeltilir, WebP'ye çevrilir. İki boyut üretilir: önizleme (400 px) ve
  normal (1600 px).
- İşlenen dosyalar `media/` altına yazılır, ham dosya `incoming/`'dan silinir.
- Görüntü işleme için **libvips** kullanılır (Go bağlantısı `govips`). WebP
  kodlama ve HEIC okuma için gereklidir; core'un container image'ı libvips
  içerir.

### Sınırlar

- Başlangıçta yalnızca resim: JPEG, PNG, WebP, HEIC. Ham dosya en fazla
  10 MB.
- Kullanıcı başına yükleme sıklığı sınırı ve toplam kota uygulanır. Değerler
  yapılandırmadan okunur.

### Sunma

- **Üyelere özel (varsayılan):** core, medya ID'leri için **1 saat geçerli
  imzalı indirme linkleri** üretir ve API yanıtlarına ekler. Link yalnızca
  isteği yapan kişinin erişim yetkisi varsa üretilir.
- **Herkese açık:** Yalnızca paylaşılabilir olarak işaretlenen bir içeriğin
  (etkinlik, ilan, tayfa) **kapak görseli** herkese açılabilir. İşaretlenince
  işlenmiş kapak public bucket'a, tahmin edilemez rastgele bir adla
  kopyalanır. İşaret kaldırılınca dosya silinir ve Cloudflare önbelleği
  temizlenir.
- Paylaşılan linkin önizleme sayfası (başlık, tarih, kapak görseli) istemci
  tarafı ADR'sinde ele alınacak; bu ADR yalnızca görselin herkese açık sunumunu
  kapsar.

### Temizlik

- `incoming/` için R2 lifecycle kuralı: 24 saatten eski dosyalar silinir.
  Onaylanmayan veya işlenemeyen yüklemeler böylece temizlenir.
- Bir içerik silindiğinde ona ait medya da silinir; servisler arası durumlarda
  outbox kullanılır (ADR-0002).
- Hiçbir içeriğe bağlanmamış hazır medya 7 gün sonra silinir.

## Gerekçe

- Doğrudan yükleme, dosyanın gateway → core arasında gRPC'nin varsayılan
  4 MB mesaj sınırına takılmasını ve parçalı akış (streaming) RPC'si yazmayı
  önler. Yavaş yüklemeler sırasında servislerde bağlantı ve bellek meşgul
  edilmez. R2, sistemimiz kapalıyken bile yüklemeyi kabul eder.
- Ham dosyaların karantinada tutulması, işlenmemiş ve metadata'sı silinmemiş
  bir dosyanın hiçbir zaman sunulmamasını garanti eder.
- Herkese açık içeriğin yalnızca kapak görselleriyle sınırlı ve açıkça
  işaretlenmesi, link önizlemesi sağlarken tayfa içeriklerini varsayılan olarak
  özel tutar.

## Sonuçlar

- Her ortamda iki R2 bucket'ı, `incoming/` için lifecycle kuralı ve public
  bucket için özel alan adı tanımlanır.
- core'un image'ı libvips nedeniyle daha büyüktür ve cgo ile derlenir.
- İmzalı indirme linkleri Cloudflare önbelleğinden yararlanmaz; bu ölçekte
  sorun değildir.

## Değerlendirilen alternatifler

- **Yüklemeyi gateway ve core üzerinden yapmak:** Tek istekle daha basit akış,
  ama gRPC sınırı, uzun süre açık kalan bağlantılar ve ev bağlantısı üzerinden
  geçen ham veri nedeniyle seçilmedi.
- **Tüm medyayı tahmin edilemez adlarla herkese açık sunmak:** En basit ve en
  hızlı, ama bir link sızarsa üyelere özel fotoğraflar herkese açılır.
- **İmzalı token doğrulayan bir Cloudflare Worker ile sunmak:** Önbellekten
  yararlanır, ama ek bir bileşen getirir. Trafik arttığında yeniden
  değerlendirilir.
- **Saf Go ile görüntü işleme:** cgo gerektirmez, ama Go standart
  kütüphaneleri WebP kodlama ve HEIC okuma desteği sunmaz.
