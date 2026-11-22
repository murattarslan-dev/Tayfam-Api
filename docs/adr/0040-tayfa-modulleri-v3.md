# ADR-0040: v3 tayfa modülleri — ortak kütüphane, soru havuzu, materyal kütüphanesi, üye garajı ve ders-konu listesi

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Karar veren:** Murat Arslan (2026-11-18). Ürün kararları Notion "Faz 3 —
  Katalog ayrıntıları ve şema ekleri (2026-11-18)" §E.3–E.4 ve "Eğitim —
  Ders ve konu etiket listesi".
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0020](0020-goruntu-isleme-ve-medya-sunumu.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0027](0027-tayfa-ana-sayfa-bilesenleri.md) (§1'deki "her modül
  kendi ADR'siyle"),
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md)

## Bağlam

ADR-0027 dört modül bileşenini (ortak kütüphane, soru havuzu, materyal
kütüphanesi, üye garajı) adlandırıp her birinin veri modelini, yetkisini ve
KVKK'sını ayrı karara bıraktı. Platform sahibi dördünü ve Eğitim'in ders-konu
listesini 2026-11-18'de onayladı. Dört modül birbirinden bağımsızdır; tek
ADR'de, ayrı bölümlerde tutulur.

Ortak kurallar (dördü için):

- Her modül `tayfa_modules`'ta yeni bir anahtardır (`library`,
  `question_pool`, `materials`, `garage`); CHECK genişler. Modül kapalıysa
  uçları `module-disabled` döner (bugünkü kural).
- Bütün sorgular `tayfa_id` + üyelik kontrolüyle; izinler tek izin
  tablosunda, varsayılan "hayır" (ADR-0004).
- Serbest metin kötüye kullanım filtresinden geçer (ADR-0012); fotoğraflar
  ADR-0020 yolunu izler (istemcide işlenir, yalnızca mobil yükler, sunucuda
  başlık kontrolü). Her modül kendi yükleme amacını (`purpose`) alır.
- İçerik soft delete'tir; kalıcı silme 30 gün sonra (CLAUDE.md).
- Şikâyet edilebilir içerik bugünkü şikâyet akışına girer; 18 yaş altını
  ilgilendiren şikâyetler öne geçer.

## Karar

### 1. Ortak kütüphane (`library`) — ortak eşya

- `library_items` (core): `id`, `tayfa_id`, `owner_id`, `kind` (`book`,
  `camping_gear`, `board_game`, `tool`, `other`), `name` (1–80),
  `vocabulary_id` (yalnızca `book`; ad alanı `book`, ADR-0037 §4),
  `note` (≤ 300), en fazla 1 fotoğraf, `status` (`available`, `on_loan`,
  `withdrawn`).
- `library_loans`: `item_id`, `borrower_id`, `state` (`requested` →
  `approved` → `handed_over` → `returned`; ya da `declined` / `cancelled`),
  `due_at` (isteğe bağlı, en fazla 90 gün), zaman damgaları.
  Kalem başına aynı anda tek etkin ödünç; ödünçteyken gelen istekler
  `library_queue`'da sıra numarasıyla bekler.
- **Para, depozito, ceza alanı yoktur** (şemada ifade edilemez).
- `due_at` geçince borç alana tek bir uygulama içi hatırlatma (push yok).
- Yetki: kalem ekleme üye; düzenleme/geri çekme sahibi (ve moderatör içerik
  moderasyonu için); onay ve teslim adımları sahibi; iade alındı sahibi;
  istek ve iptal borç alan.
- **Üye ayrılınca** (çıkma, çıkarılma, hesap silme): kalemleri `withdrawn`
  olur; etkin ödünç varsa iki tarafa uygulama içi uyarı, kayıt
  "kapanmamış" olarak kalır ve 30 gün sonra kapanır. Sıradakiler düşer.
- KVKK: kişinin kalemleri ve ödünç geçmişi dışa aktarmaya girer; ödünç
  geçmişi 1 yıl tutulur.

**Uygulama notları (#270):**

- Uçlar `/v1/tayfas/{tayfa_id}/library/…` (core `TayfaLibraryService`);
  tablolar core 00061, bildirimler chat 00023. Hata kodları 2700–2706.
- **Görünürlük:** ödüncü yalnızca iki tarafı görür. Üyeler kalemin
  durumunu (`available` / `on_loan`) ve sırada kaç kişi beklediğini görür;
  sahibi etkin ödüncü ve sırayı (kimlerin beklediği), borç alan kendi
  ödüncünü ve sıradaki yerini görür. "Ödünçlerim" listesi kişinin bu
  tayfadaki ödünçleridir.
- **Durum makinesi:** `requested` → `approved` → `handed_over` →
  `returned`; sahibi teslimden önce reddeder (`declined`), borç alan
  teslimden önce geri alır (`cancelled`). Ek durum `closed`: teslimdeyken
  taraflardan biri ayrıldı, kayıt 30 gün içinde iade alınmadı. Kalem
  `closed` ile yeniden `available` olur (sahibi tayfadaysa); sahibi
  isterse geri çeker. Biten her ödünçte sıradaki kişiye otomatik
  `requested` ödünç açılır.
- **İade tarihi** teslimde verilir (isteğe bağlı, teslimden en fazla 90
  gün). Geçince borç alana tek hatırlatma: `library_due`. Ayrılmada iki
  tarafa `library_loan_orphaned`. İkisi de uygulama içidir (push yok),
  aktörsüzdür, hedefi ödüncün tayfasıdır, eşyayı ve kişiyi adlandırmaz.
  ADR'nin istemediği diğer adımlar (yeni istek, onay, teslim) bildirim
  üretmez.
- **Yetki:** ekleme ve istek üye (susturulmuş üye yapamaz); düzenleme
  sahibi; geri çekme sahibi, kaldırma moderatör (izin
  `library.item_remove_any`); onay, ret, teslim, iade sahibi. Geri çekme,
  isteği geri alma ve sahibin ödünç adımları susturulmuşken de açıktır
  (yalnızca daraltır ya da eşyanın sahibine dönmesini sağlar). Sahibi
  teslimdeki kalemi geri çekemez (`library-item-on-loan`); moderatörün
  kaldırması teslimdeki ödüncü açık bırakır.
- **Geri çekme** soft delete'tir; geri alma yoktur. Geri çekilen kalemin
  notu ve fotoğrafı hemen silinir (dosya silme kuyruğuna girer), adı ödünç
  geçmişi için kalır; ödünç kaydı kalmayınca 30 gün sonra kalıcı silinir.
  Biten ödünç 1 yıl sonra silinir.
- **Sınırlar:** üye başına tayfada 100 canlı kalem, kalem başına sırada 20
  kişi. Kitap adı tayfa sözlüğünün sınırıyla 60 karakterdir (diğerleri
  80). Kötüye kullanım filtresinin takılma noktası `library.checkTerms`.
- **Hesap silme:** kalemler geri çekilir ve sahibi boşalır, ödünçlerde
  borç alan boşalır, sıradaki yerler silinir; karşı tarafa uyarı gider.
- Dışa aktarma bölümleri 37 (`library_items`) ve 38 (`library_loans`,
  sıradaki yerler `queued` durumuyla); karşı taraf dışa aktarılmaz.

### 2. Soru havuzu (`question_pool`)

- Mevcut Soru-Cevap tablolarını (`questions`, `answers`, `answer_votes`)
  kullanır; `questions.pool` (`qa` / `question_pool`) ayrımıyla aynı
  altyapı. Soru metin ve/veya fotoğraf (en fazla 2); cevap metin ve/veya
  fotoğraf (en fazla 2).
- **Ders etiketi zorunlu**, konu isteğe bağlı (§5 listesi). "Çözüldü"
  işareti yoktur; cevaplar faydalı oyuyla sıralanır.
- `pool_link` bileşeni (ADR-0037) bu sorulara bağlanır.
- **İstatistik:** "çözülen soru" = en az 1 faydalı oy almış cevabı olan soru
  (ADR-0026).
- Topluluk Kuralları'na ve şikâyet nedenlerine **ücretli özel ders
  teklifi/reklamı** eklenir (`paid_tutoring`). Kopya için teknik önlem
  yoktur; kural ve şikâyetle yönetilir.

### 3. Materyal kütüphanesi (`materials`)

- `materials` (core): `id`, `tayfa_id`, `author_id`, `kind` (`link` /
  `note_photo`), `title` (1–80), `url` (yalnızca `link`), en fazla 3
  fotoğraf (yalnızca `note_photo`), ders (zorunlu) ve konu etiketi.
  **Dosya/PDF yüklenmez.**
- **URL kuralları:** yalnızca `https`, kullanıcı bilgisi yok, IP adresi
  host olamaz, en fazla 500 karakter, kötüye kullanım filtresi;
  `blocked_domains` listesindeki alan adları ve alt alan adları
  reddedilir (`blocked-link`).
- **Sunucu bağlantıyı hiçbir zaman açmaz:** önizleme, başlık ya da favicon
  çekilmez (SSRF yüzeyi ve gizlilik sızıntısı yok). Yanıt yalnızca URL'yi ve
  sunucunun ayrıştırdığı alan adını döner.
- `blocked_domains` (core): platform panelinden yönetilir, denetim kaydına
  yazılır; eklenen alan adı mevcut materyalleri de gizler (silmez).

### 4. Üye garajı (`garage`)

- Garaj **kişiye aittir** ve tektir: `garage_vehicles` (core): `id`,
  `owner_id`, `make` (1–40), `model` (≤ 40), `model_year`, `color`
  (≤ 30), `note` (≤ 200), en fazla 3 fotoğraf. **Plaka alanı yoktur.**
  Kişi başına en fazla 10 araç.
- Görünürlük tayfa bazındadır: `garage_tayfa_visibility` (`user_id`,
  `tayfa_id`, `visible`). Satır yoksa varsayılan: tayfanın ilgi alanlarında
  `automotive` varsa görünür, yoksa görünmez. Garaj bileşeni yalnızca o
  tayfada görünür araçları, yalnızca üyelere döner.
- `vehicles` bileşeni (konvoy, araç buluşması) aracı garajdan seçebilir:
  bileşen tablosundaki `vehicle_id` dolar (bugünkü `event_convoy_vehicles.vehicle_id`), marka/model/yıl/renk kopyalanır;
  **plaka yalnızca etkinlik kaydında** isteğe bağlı girilir ve 30 günde
  silinir (bugünkü kural). Oylamanın `vehicles` kaynağı bu kayıtları
  kullanır.
- Satılık/ilan alanı yoktur.
- KVKK: araçlar dışa aktarmaya girer; hesap silinince silinir.

### 5. Ders ve konu listesi (Eğitim)

- `catalog_study_tags` (core): `key` (değişmez), `parent_key` (NULL =
  düzey; düzey → ders → konu, en fazla 3 seviye), `labels`, `position`,
  `status` (`active` / `retired`). Veri; platform panelinden yönetilir,
  katalogla aynı sürüm numarasını (`catalog_version`) artırır ve
  `GET /v1/catalog` yanıtında döner.
- Her düzeyin ve dersin altında `other` ("Diğer") vardır; ders seçimi
  zorunlu, konu isteğe bağlıdır.
  Anahtar bütün listede tekil olduğu için "Diğer"in anahtarı
  `<üst anahtar>_other`'dır (#259); "Diğer"in alt etiketi olmaz. Bu içerik
  kuralını `catalog.CheckStudyTags` denetler (seed ve panel); içerik
  seçimini `catalog.CheckStudySelection` doğrular.
- Kaldırılan etiket `retired` olur: yeni içerikte seçilemez, eski içerik
  göstermeye devam eder. Anahtar değişmez, kayıt silinmez.
- Yayınevi ya da marka adı etiket olamaz (panel doğrulaması değil,
  platform sahibinin içerik kuralı).
- İlk liste Notion taslağından seed olarak gelir (platform sahibi onaylar).

## Gerekçe

- **Ortak eşya:** tek modül hem kitap kulübüne hem kamp tayfasına yarar;
  ayrı "ekipman kütüphanesi" kod tekrarı olurdu.
- **Soru havuzu Soru-Cevap'ın üstünde:** moderasyon, oy ve şikâyet zaten
  var; yeni tablo seti gereksiz.
- **Sunucu URL açmaz:** tutulmayan veri çalınamaz; açılmayan bağlantı da
  sunucuyu bir istek aracına çeviremez (ADR-0011).
- **Plakasız garaj:** kalıcı profil verisinde plaka kişiyi araca ve konuma
  bağlayan gereksiz bir veridir.

## Sonuçlar

- Migration (core): `tayfa_modules` CHECK genişler; `library_items`,
  `library_loans`, `library_queue`, `questions.pool`, `materials`,
  `blocked_domains`, `garage_vehicles`, `garage_tayfa_visibility`,
  `catalog_study_tags`; yükleme amaçları `library_item`,
  `question_pool`, `material_note`, `garage_vehicle`.
- Şikâyet nedenlerine `paid_tutoring`; Topluluk Kuralları güncellenir
  (platform sahibi, Notion ve site).
- ADR-0027'deki ana sayfa kartları bu modüllerin vitrinidir.

## Değerlendirilen alternatifler

- **Garajı tayfa başına ayrı tutmak:** Reddedildi; aynı aracın her tayfada
  yeniden girilmesi gerekirdi.
- **Materyalde bağlantı önizlemesi:** Reddedildi; SSRF ve iz sürme.
- **PDF yükleme:** Reddedildi (2026-11-15 kararı): telif, zararlı dosya,
  depolama.
