# ADR-0012: Doğum tarihi, genç kullanıcı güvenliği ve kötüye kullanım tespiti

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0008](0008-kvkk-ve-platform-yonetimi.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)
- **Kaynak:** Notion: Proje Planı (Kullanıcı Akışı), "Kötüye Kullanım ve Genç
  Kullanıcı Güvenliği (taslak)"

## Bağlam

Notion'daki planda şu ürün kararları alındı:

- Kayıtta **doğum tarihi zorunludur**; 16 yaşından küçükler kayıt olamaz. Bu,
  daha önceki "16 yaşından büyüğüm" beyanının ve "doğum tarihi saklanmaz"
  kararının yerini alır (ADR-0008).
- Doğum tarihinin diğer kullanıcılara görünürlüğü üç seviyelidir: yaş ve doğum
  tarihi / yalnızca doğum günü (gün/ay) / kimse. Varsayılan: yalnızca
  arkadaşlar, yalnızca doğum günü. Doğum günü hatırlatmaları bu alandan
  beslenir.
- Yaş güvenlik amacıyla kullanılır: şikâyetin taraflarından biri 18 yaşından
  küçükse şikâyet öne geçer ve platform sahibine anında bildirim gider. Yaşa
  göre özellik kısıtlaması yapılmaz.
- Kötüye kullanım içerik okunmadan tespit edilir: tayfa ve etkinlik meta
  verisi bir kelime listesinden geçer, davranış sinyalleri izlenir.
- Çocuk güvenliği, gerçek şiddet tehdidi veya suç şüphesi içeren şikâyetlerde
  içerik silinmez; gizlenir ve dondurulur.

Doğum tarihi kişisel veridir ve bir kısmı reşit olmayanlara aittir. ADR-0011
gereği bu verinin ele geçirilen bir bileşenden sızma alanı en dar
tutulmalıdır.

## Karar

### Doğum tarihi yalnızca identity'de, şifreli

- Doğum tarihi **yalnızca identity'nin veritabanında** tutulur ve uygulama
  seviyesinde şifrelenir (AES-256-GCM, anahtar secret'tan; kanal mesajlarından
  ayrı bir anahtar). Şifreli kayıt anahtar sürümünü taşır.
- Diğer servislere doğum tarihi **hiçbir zaman** gönderilmez. Gerektiğinde
  yalnızca **türetilmiş bilgi** verilir:
  - `IsMinor(user_ids)`: 18 yaşından küçük mü? (evet/hayır)
  - Görünürlük kuralına uygun doğum günü gösterimi (aşağıda).
  - Doğum günü olayı (aşağıda).
- core veya chat ele geçirilse bile kimsenin doğum tarihi sızmaz; identity'nin
  veritabanı ele geçirilse bile anahtar olmadan okunamaz.
- Log, metrik, trace ve olaylara (`domain_events`) doğum tarihi veya yaş
  yazılmaz.

### Kayıt akışı

1. Google/Apple ile ilk girişte hesap **kayıt bekliyor** durumunda açılır.
   Bu durumdaki token yalnızca kayıt tamamlama uçlarına erişebilir
   (ADR-0004'teki onay ekranıyla aynı desen).
2. Kullanıcı doğum tarihini girer; aydınlatma metni ve kullanım koşulları
   onaylanır.
3. 16 yaşından küçükse kayıt tamamlanmaz ve hesap, kişisel veri tutulmadan
   **hemen silinir**. Reddedilen bir başvurunun doğum tarihi veya kimliği
   saklanmaz.
4. Doğum tarihi beyana dayanır ve doğrulanmaz; yanlış beyan bilinçli kabul
   edilen bir risktir. Kayıttan sonra değiştirilmesi destek talebiyle olur
   (kendi başına 16 yaş sınırını aşmayı denemeyi zorlaştırmak için).

### Görünürlük

- Ayar iki boyutludur ve identity'de tutulur:
  - **Ayrıntı:** `full` (yaş ve doğum tarihi), `birthday` (yalnızca gün/ay),
    `none`
  - **Kitle:** `friends` (varsayılan) veya `tayfa` (ortak tayfa üyeleri)
- Görünürlük kararını **identity** verir. Arkadaşlık ve ortak tayfa bilgisi
  core'dadır (ADR-0009); identity bunu core'a sorar. Bu, core → identity
  (`IsMinor`) ile birlikte iki yönlü bir senkron bağımlılık yaratır; ADR-0002
  aynı istek zincirinde döngüyü yasaklar, iki ayrı akışta karşılıklı çağrıyı
  yasaklamaz. Hiçbir metod çağrıldığı servisi geri çağıran bir zincir
  başlatmaz.

### Doğum günü hatırlatmaları

- identity günlük bir işle, o gün doğum günü olan ve ayrıntı seviyesi `none`
  olmayan kullanıcılar için `user.birthday` olayı yayınlar. Olay yalnızca
  `user_id`, ayrıntı seviyesi ve kitleyi taşır; tarih taşımaz.
- core, kitleye göre alıcıları (arkadaşlar veya ortak tayfa üyeleri) belirler
  ve chat'e bildirim isteği gönderir (ADR-0005).

### Şikâyetlerde yaş önceliği

- core bir şikâyet oluştururken şikâyet eden ve şikâyet edilen kullanıcılar
  için identity'ye `IsMinor` sorar ve sonucu şikâyete `involves_minor`
  olarak yazar. Yaş veya doğum tarihi şikâyette tutulmaz.
- `involves_minor` veya "çocuk/genç güvenliği" kategorisindeki şikâyetler
  kuyrukta en öne geçer ve platform sahibine anında bildirilir.

### Kötüye kullanım tespiti (içerik okumadan)

- **Meta veri kontrolü (core, `moderation` paketi):** Tayfa adı ve açıklaması,
  etkinlik başlığı ve açıklaması oluşturma ve düzenleme anında kontrol edilir.
  Metin Türkçe kurallarla normalize edilir (İ/ı, rakamla yazım, boşluklu
  yazım). Eşleşme yayını engellemez; kayıt panelde inceleme kuyruğuna düşer.
- **Kelime listesi repoda tutulmaz.** Repo public olduğu için liste,
  filtreyi atlatma rehberi olur. Liste core'un veritabanında tutulur ve
  yönetim panelinden yönetilir; repoda yalnızca test için zararsız örnekler
  bulunur.
- **Davranış sinyalleri:** Kısa sürede çoklu şikâyet, ağırlıklı yeni
  hesaplarla hızlı büyüme, art arda çıkma veya yöneticiyi engelleme, arkadaş
  olmayanlara toplu mesaj isteği. Sinyaller sayılar üzerinden hesaplanır;
  chat kendi sayımlarını (mesaj isteği sayısı) olay olarak core'a iletir.
  Eşik aşılınca tayfa panelde işaretlenir (`moderation_flags`, core).

### Delil dondurma

- Çocuk güvenliği, gerçek şiddet tehdidi veya suç şüphesi kategorisindeki bir
  şikâyet, hedef içeriğe **yasal bekletme** (`legal_hold`) koyar. İçerik
  kullanıcılardan gizlenir, düzenlenemez ve silinemez.
- Bekletmedeki içerik, 30 günlük kalıcı silme (ADR-0008) ve moderatör geri
  alma süresinin dışındadır. Bekletmeyi yalnızca platform sahibi, gerekçe
  yazarak kaldırır; işlem `platform_audit_logs`'a girer.
- Bekletme core'da (duyuru, etkinlik, soru-cevap) ve chat'te (kanal mesajları)
  ayrı ayrı uygulanır; chat'e olayla iletilir. Birebir mesajlar sunucuda
  okunamadığı için şikâyetçinin ilettiği açık metin delil olarak şikâyette
  saklanır.

### Profil tamamlama

- Profil alanları için "cevaplanmadı / cevaplandı / paylaşmak istemiyorum"
  durumu tutulur; "paylaşmak istemiyorum" alanı cevaplanmış sayar. "Daha
  sonra doldur" seçimi ve tekrar gösterme zamanı istemci tarafında kalır.

### Hukuki açık noktalar

- **5651 sayılı Kanun:** Yer sağlayıcı yükümlülükleri, özellikle trafik ve
  erişim kayıtlarının saklanması, ADR-0008'deki "uygulama logları 30 gün, IP
  adresleri yalnızca güvenlik kayıtlarında" kararıyla çelişebilir. Hukukçu
  görüşü gelene kadar ADR-0008 geçerlidir; görüş geldiğinde gerekli kayıtlar
  ayrı, erişimi kısıtlı ve süresi tanımlı bir depoda tutulur.
- Yasal bekletmedeki içeriğin en uzun saklama süresi ve yetkili makama
  bildirim süreci hukukçuyla belirlenir.

## Gerekçe

- Doğum tarihini tek serviste, şifreli tutmak ve dışarıya yalnızca
  türetilmiş bilgi vermek, ADR-0011'in hasar sınırlama ilkesinin doğrudan
  uygulamasıdır; en hassas kişisel verilerden biri en dar alanda kalır.
- Kayıt bekliyor durumu, zorunlu bir alanın "sonra doldururum" ile
  atlanmasını sunucu tarafında engeller.
- Kelime listesinin repo dışında tutulması, public repoda filtreyi işlevsiz
  hale getirmemek için gereklidir.

## Sonuçlar

- identity: doğum tarihi, görünürlük ayarı, kayıt bekliyor durumu, `IsMinor`
  ve doğum günü görünürlüğü metodları, günlük doğum günü işi. ADR-0008'deki
  `age_confirmed_at` kaldırılır.
- core: `reports.involves_minor` ve şikâyet kategorisi, `moderation` paketi
  (kelime listesi, sinyaller, `moderation_flags`), yasal bekletme.
- chat: kanal mesajlarında yasal bekletme, mesaj isteği sayımlarının core'a
  iletilmesi.
- Notion'daki veri modeli bu ADR'ye göre güncellenir.
- Aydınlatma metni doğum tarihinin işlenmesi ve saklanmasıyla güncellenmiştir
  (Notion).

## Değerlendirilen alternatifler

- **Doğum tarihini profil kopyalarıyla (ADR-0009) core'a da göndermek:**
  Görünürlük ve hatırlatmalar daha basit olur, ama hassas veri iki
  veritabanına yayılır.
- **Yalnızca yaş bandı (16–17 / 18+) tutmak:** Daha az veri, ama doğum günü
  özellikleri mümkün olmaz; ürün kararı tam doğum tarihi yönünde.
- **Kelime listesini repoda tutmak:** Sürüm takibi kolay, ama public repoda
  listeyi atlatmayı öğretir.
