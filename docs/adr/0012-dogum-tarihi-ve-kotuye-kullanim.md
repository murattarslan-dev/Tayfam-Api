# ADR-0012: Doğum tarihi, genç kullanıcı güvenliği ve kötüye kullanım tespiti

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07 (güncelleme: 2026-11-09, şikâyet önceliğinin tayfa
  kuyruğunda görünmesi)
- **Karar veren:** Murat Arslan
- **İlgili:** [ADR-0008](0008-kvkk-ve-platform-yonetimi.md), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)
- **Kaynak:** Notion: Veri Modeli v14 ("Doğum Tarihi ve Şikâyet Önceliği"),
  "Kötüye Kullanım ve Genç Kullanıcı Güvenliği (taslak)"

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
  ayrı bir anahtar). Şifreli kayıt anahtar sürümünü taşır. Şifreleme
  `users.id`'yi ek doğrulanmış veri (AAD) olarak kullanır; bir satırın şifreli
  değeri başka bir satıra kopyalanırsa çözülmez.
- **Notion v14'ten sapma:** v14'teki `birth_date date` kolonu
  `birth_date_enc bytea` olur. Veritabanı şifreli değeri okuyamadığı için
  v14'teki 16 yaş trigger'ı ve `birth_date >= '1900-01-01'` CHECK'i
  uygulanamaz; bu kontroller yalnızca Go'da (`app` katmanı, tablo testleriyle)
  yapılır. Veritabanında yalnızca "aktif hesapta `birth_date_enc` NULL
  olamaz" CHECK'i kalır. Veritabanı yedeği veya bir SQL enjeksiyonu doğum
  tarihlerini açık metin olarak sızdırmasın diye bu takas bilinçli olarak
  yapılır.
- Yaş saklanmaz, her ihtiyaçta Europe/Istanbul tarihine göre hesaplanır (v14).
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

1. Google/Apple ile ilk girişte hesap **kayıt bekliyor** durumunda açılır
   (`birth_date_enc` NULL). Bu durumdaki token yalnızca kayıt tamamlama
   uçlarına erişebilir (ADR-0004'teki onay ekranıyla aynı desen).
2. Kullanıcı doğum tarihini girer; aydınlatma metni ve kullanım koşulları
   onaylanır.
3. Doğum tarihi 16 yaşından küçük bir kullanıcıyı gösteriyorsa hesap
   aktifleşmez (v14). Girilen tarih değiştirilemediği için aynı Google/Apple
   kimliğiyle farklı bir tarih denenemez.
4. Doğum tarihi beyana dayanır ve doğrulanmaz; yanlış beyan bilinçli kabul
   edilen bir risktir. Kullanıcı doğum tarihini uygulamadan değiştiremez;
   düzeltme talebi platform paneline düşer ve gerekçesiyle
   `platform_audit_logs`'a yazılır (v14).

### Görünürlük

- Ayar iki boyutludur ve identity'de tutulur (v14):
  - `birth_date_visibility`: `hidden`, `birthday` (varsayılan; yalnızca gün
    ve ay), `full` (yaş ve doğum tarihi)
  - `birth_date_audience`: `friends` (varsayılan), `tayfa` (ortak tayfa
    üyeleri), `public`; `hidden` iken etkisizdir
- Yanıt alanları baştan izleyiciye göre üretilir; tam nesne üretilip sonra
  filtrelenmez. Kullanıcının kendisi her zaman tam tarihi görür. Yaş ve doğum
  tarihi tayfa yöneticilerine ve moderatörlerine gösterilmez; yalnızca
  kullanıcının seçtiği görünürlük geçerlidir. Şikâyetin `minor_involved`
  önceliği ise tayfa moderasyon kuyruğunda görünür ve kuyrukta öne alınır
  (aşağıda, "Şikâyetlerde yaş önceliği").
- Görünürlük kararını **identity** verir. Arkadaşlık ve ortak tayfa bilgisi
  core'dadır (ADR-0009); identity bunu core'a sorar. Bu, core → identity
  (`IsMinor`) ile birlikte iki yönlü bir senkron bağımlılık yaratır; ADR-0002
  aynı istek zincirinde döngüyü yasaklar, iki ayrı akışta karşılıklı çağrıyı
  yasaklamaz. Hiçbir metod çağrıldığı servisi geri çağıran bir zincir
  başlatmaz.
- **Uygulama (2026-11-13, issue #185):** Gösterim
  `identity.v1.ProfileService.GetProfileDetails` ile yapılır; gateway onu
  `GET /v1/users/{user_id}/profile` cevabına `birthday` olarak ekler ve
  ilişki bilgisi iletmez. identity ilişkiyi core'un
  `ProfileService.GetUserProfile`'ına çağıranın kendi token'ıyla sorar
  (engel ve bilinmeyen kullanıcı `user-not-found`). Platform sahibinin
  kararıyla (Notion Proje Planı §4 madde 8) bugün yalnızca **arkadaşlar**
  görür: `birth_date_audience` her hesapta `friends`'tir, hiçbir uç onu
  değiştirmez ve okunmaz; `tayfa`/`public` kitlesi açılırsa ayrı karar
  gerekir. Üç seçenek 16–18 yaş arasına da açıktır. `birth_date_visibility`
  `PATCH /v1/me/account` ile değişir ve hiçbir profil kopyasına gitmez.

### Doğum günü hatırlatmaları

- identity günlük bir işle, o gün doğum günü olan ve görünürlüğü `hidden`
  olmayan kullanıcılar için `user.birthday` olayı yayınlar. Olay yalnızca
  `user_id` ve kitleyi taşır; tarih ve yaş taşımaz.
- core, kitleye göre alıcıları (arkadaşlar veya ortak tayfa üyeleri) belirler
  ve `friend_birthday` türünde bildirim isteği yayınlar; chat bildirimi
  yazar ve gönderir (ADR-0005). `public` kitlesi hatırlatma için arkadaşlar
  olarak ele alınır; herkese bildirim gönderilmez.

### Şikâyetlerde yaş önceliği

- core bir şikâyet oluştururken şikâyet eden ve hedef kullanıcı (hedef
  içerikse içeriğin yazarı) için identity'ye `IsMinor` sorar. Biri 18
  yaşından küçükse `reports.priority = minor_involved` ve
  `minor_involved_at` yazılır (v14). Değer oluşturma anında hesaplanır ve
  sonradan değişmez. Yaş veya doğum tarihi şikâyette ve olay payload'ında
  tutulmaz.
- identity'ye ulaşılamazsa şikâyet yine oluşturulur ve **güvenli tarafta
  kalınarak** `minor_involved` olarak işaretlenir; bir şikâyet, kimlik
  servisinin kesintisi yüzünden önceliğini kaybetmez.
- `minor_involved` veya "çocuk/genç güvenliği" kategorisindeki şikâyetler
  kuyrukta en öne geçer, platform sahibine anında bildirilir ve 7 günlük
  otomatik yükselmeyi beklemeden platforma düşer.
- **Tayfa kuyruğunda öncelik (platform sahibinin 2026-11-09 kararı):** Tayfa
  moderasyon kuyruğu şikâyetin önceliğini (`normal` / `minor_involved`)
  gösterir ve `minor_involved` şikâyetleri öne alır (Notion v14 "Şikâyet
  önceliği"). Tarafların yaşı ve doğum tarihi gösterilmez, hangi tarafın 18
  yaşından küçük olduğu söylenmez; core bunları hiç tutmaz. Bilinen bedel:
  öncelik, taraflardan birinin 18 yaşından küçük olduğunu dolaylı olarak
  gösterir. Tayfa moderatörlerinin daha hızlı müdahale edebilmesi için bu
  bedel kabul edildi. Şikâyet edenin kimliği ve platforma yükselme bilgisi
  kuyrukta gösterilmez.

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
- **Güncelleme (2026-11-13, platform sahibi; issue #186):** "Daha sonra
  doldur" artık **sunucuda** tutulur ve cihazlar arası tutarlıdır: erteleme
  7 gün sürer, forma yeni alan eklenince (`profilefields.FieldsVersion`
  artınca) geçersiz olur; istemci yalnızca
  `Account.profile_completion_pending`'e bakar. Alanlar (ilgi alanları,
  hakkımda, meslek, takım, diller, sosyal medya kullanıcı adı) identity'nin
  `profile_details` ve `user_social_accounts` tablolarındadır; core ve
  chat'in profil kopyalarına gitmez. Başka kullanıcılara yalnızca
  identity'nin `ProfileService`'i, kullanıcının `about_visibility` ayarına
  göre gösterir (ilişki core'a çağıranın token'ıyla sorulur, doğum günüyle
  aynı yol); sosyal medya hesabı ayar ne olursa olsun en fazla arkadaşlara
  görünür. Hangi alanın reddedildiği başkasına söylenmez. Alanlar KVKK veri
  dışa aktarmasında vardır, hesap silmede hemen silinir.

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

- identity: şifreli doğum tarihi, görünürlük ayarları, kayıt bekliyor
  durumu, `IsMinor` metodu, günlük doğum günü işi. `age_confirmed_at`
  kaldırılır.
- core: `reports.priority` ve şikâyet kategorisi, `moderation` paketi
  (kelime listesi, sinyaller, `moderation_flags`), yasal bekletme.
- chat: kanal mesajlarında yasal bekletme, mesaj isteği sayımlarının core'a
  iletilmesi.
- Notion'daki veri modeli v14'e `birth_date_enc` sapması işlenir.
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
