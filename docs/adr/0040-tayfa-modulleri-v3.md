# ADR-0040: v3 tayfa modülleri — ortak kütüphane, soru havuzu, materyal kütüphanesi, üye garajı ve ders-konu listesi

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Revizyon:** 2026-11-23 (platform sahibi, sohbette; issue #321): ortak
  kütüphanede ödünç adımları bildirim üretir — uygulama içi **ve push**
  (içeriksiz). Yeni istek ve sıraya girme kalemin sahibine; onay, ret ve
  teslim isteyene. `library_due` ve `library_loan_orphaned` uygulama içi
  kalır. Ayrıntı §1 "Uygulama notları (#321)".
- **Uygulama notları onayı:** 2026-11-23 (platform sahibi, sohbette, issue
  #323): #270, #271, #261 ve #272 uygulama notlarındaki ayrıntı kararları
  ve engelli alan adı başlangıç listesi onaylandı.
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

**Uygulama notları (#270):** (ayrıntı kararları 2026-11-23'te sohbette onaylandı (#323))

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
  ~~ADR'nin istemediği diğer adımlar (yeni istek, onay, teslim) bildirim
  üretmez.~~ 2026-11-23'te değişti: aşağıda "Uygulama notları (#321)".
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

**Uygulama notları (#321, platform sahibinin 2026-11-23 kararı):**

- Beş yeni bildirim türü (chat enum 29–33, chat 00024):
  `library_loan_requested` (bir üye istedi, ödünç isteği onda) ve
  `library_queue_joined` (bir üye sıraya girdi) kalemin sahibine;
  `library_loan_approved`, `library_loan_declined`,
  `library_loan_handed_over` isteyene. İade alındı (`returned`) bildirim
  üretmez: adımı sahibi atar, borç alan eşyayı kendi elinden verir.
- Hedef kalemdir: yeni hedef türü `library_item` (chat enum 10), kimlik
  kalemin kimliği; istemci kalemi açar (sahibi sırayı ve etkin ödüncü,
  borç alan kendi ödüncünü görür). Gruplama da kalem başınadır. Aktör
  adımı atan üyedir. Eşya adı, kişi adı, tarih taşınmaz.
- Alıcı yalnızca öbür taraftır; etkin üye olmalıdır ve aktörle arasında iki
  yönden engel varsa bildirim hiç yazılmaz (uygulama içi de, push da;
  `notify.Unblocked`). Aynı istek tekrarlanınca (zaten istemiş ya da
  sırada) yeni bildirim yoktur.
- Push gider: yalnızca `push_<tür>_title` / `_body` anahtarları ve
  kimlikler (ADR-0019 madde 4). Kapsam tayfanın tercihidir; beşi de
  kişisel sayılır (ADR-0019 madde 3 listesi): `mentions` seviyesinde gider,
  `none` durdurur.
- Bildirim üretmeyen yan etkiler: ödüncün bitmesiyle sıradakine otomatik
  açılan istek (`lending.Promote`), kalemin geri çekilmesi ya da üyenin
  ayrılmasıyla iptal olan istekler ve borç alanın kendi isteğini geri
  alması. Bunlar sonraki bir kararın konusudur.

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

**Uygulama notları (#271):** (2026-11-23'te sohbette onaylandı (#323))

- **Aynı uçlar, iki havuz:** soru havuzu ayrı servis değildir; core
  `QAService` ve `/v1/tayfas/{tayfa_id}/questions…` uçları `pool`
  (`qa` / `question_pool`) ile çalışır. Liste ve oluşturma havuzu adlandırır
  (verilmezse `qa`); kimlikle çalışan her uç havuzu satırdan okur ve o
  havuzun modülünü ister (`module-disabled`). Etiket uçları `qa`'nındır.
  Sorunun havuzu değişmez; cevabın havuzu sorununkidir (bileşik foreign
  key `(question_id, tayfa_id, pool)`).
- **Havuz sorusu:** başlık ve serbest etiket yoktur; metin `body`'dedir
  (≤ 10000). Ders zorunlu, konu isteğe bağlı; yalnızca anahtar saklanır
  (`subject_key`, `topic_key`, `catalog_study_tags`'e foreign key) ve
  yeni seçim `catalog.CheckStudySelection` ile doğrulanır; emekliye
  ayrılan etiket soruda kalır. Soru ve cevap metin ve/veya en fazla 2
  fotoğraftır (`photo1_upload_id`, `photo2_upload_id`; yükleme amacı
  `question_pool`, bağlama türleri `question` / `answer`; görünürlük:
  tayfanın etkin üyesi ve modül açık). Dosya/PDF ve bağlantı alanı yoktur.
  Metin denetim karakteri içeremez; kötüye kullanım filtresinin takılma
  noktası `qa.checkTerms`.
- **Silme:** yazarın kendi silmesi metni ve fotoğrafları hemen boşaltır
  (dosyalar silinir, geri alınamaz); moderatörün silmesi gizler, yönetici
  30 gün içinde geri alır (Soru-Cevap'la aynı). Hesap silinince içerik
  kalır, yazar boşalır; kişinin fotoğraf dosyaları silinir.
- **Şikâyet:** hedefler bugünkü `question` / `answer`'dır; modül kontrolü
  sorunun havuzundandır. Yeni neden `paid_tutoring` (her hedefte
  seçilebilir). 18 yaş altı önceliği bugünkü kuraldır; yeni bir iletişim
  yolu (DM, iletişim alanı, bağlantı önizlemesi) açılmaz.
- **`pool_link`:** `event_pool_links` (`event_id`, `component_key`,
  `question_id`, `position`); en fazla `max_links` (1–50, varsayılan 50).
  Liste bütünüyle yazılır (organizatör, moderatör, yönetici); bağlanan
  soru o tayfanın görünür havuz sorusudur (tetikleyici ikinci savunma).
  Silinen soru listede görünmez. Modül kapalıyken bileşen
  `module-disabled` döner.
- Dışa aktarma yeni bölüm almaz: `questions` ve `answers` bölümlerine
  `pool` (sorularda ayrıca `subject_key`, `topic_key`) eklendi. Ana sayfa
  bileşeni `question_pool` (modül bileşeni). "Çözülen soru" istatistiği
  (ADR-0026) bu PR'da değil.

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

**Uygulama notları (#261):** (2026-11-23'te sohbette onaylandı (#323))

- **Tablo:** `materials` (`kind` `link` / `note_photo`, `title` 1–80 tek
  satır, `url` ≤ 500 ve `domain` yalnızca bağlantıda, `photo1..3_upload_id`
  yalnızca not fotoğrafında, `subject_key` zorunlu / `topic_key` isteğe
  bağlı `catalog_study_tags`'e foreign key, soft delete). Yeni etiket
  seçimini `catalog.CheckStudySelection` doğrular; değişmeyen emekli etiket
  kalır. Üye başına tayfada en fazla 200 materyal (`material-limit`).
  Liste sayfalıdır (`page_size` ≤ 50, opak `page_token`), ders/konu/tür/
  `mine` ile süzülür.
- **Bağlantı kuralları** (`internal/core/app/material/link`, yalnızca metin
  ayrıştırır): `https`, kullanıcı bilgisi yok, IP adresi (her yazımıyla;
  son etiketi rakamla başlayan ad dahil) ve köşeli IPv6 yok, 443 dışında
  port yok, boşluk/denetim/biçim karakteri (bidi, sıfır genişlik) ve ters
  bölü yok, alan adı LDH ve ASCII (IDN punycode olarak gelir; benzer harf
  oyunu kapanır), özel kullanım ve iç adlar (`localhost`, `.local`,
  `.internal`, `.onion`, `.test` …) yok. Normalleştirme: şema ve alan adı
  küçük harf, varsayılan port ve sondaki nokta atılır, yol/sorgu/parça
  `net/url` ile kaçışlanır; sınır normalleştirilmiş adrese uygulanır.
  Paketin bağımlılıklarında ağ paketi olmadığını test denetler
  (`TestNoNetwork`); materyal paketi de ağ paketi içe aktarmaz.
- **Engelli alan adı:** tek kaynak SQL `material_domain_blocked(domain)`
  (alan adı ve bütün üst alan adları, birincil anahtar üzerinden). Liste,
  okuma, düzenleme ve şikâyet bu fonksiyonla gizler; materyal silinmez.
  Servis listeyi yalnızca okur; yönetimi #262 (platform paneli).
  Notion bir başlangıç listesi tanımlamadığı için migration en dar güvenli
  yorumla iki grup ekler (platform sahibi panelden değiştirir): bağlantı
  kısaltıcılar (sunucu bağlantıyı açmadığı için engelli alan adını
  gizleyebilirler) ve birebir sohbet/grup davet adresleri (WhatsApp,
  Telegram, Discord, Signal; 16–17 yaş kullanıcılara tayfa dışı iletişim
  yolu açmamak için, §2'deki "yeni bir iletişim yolu açılmaz" ilkesi).
  **Başlangıç listesi platform sahibinin 2026-11-23 kararıyla onaylı**
  (#323) ve Notion "Kötüye Kullanım ve Genç Kullanıcı Güvenliği" sayfasına
  işlendi. Panelden yönetim #262 ile geldi (ADR-0025 §7 uygulama notları);
  ön ayarlar gibi engelli alan adları da `catalog_version`'ı artırmaz.
- **Fotoğraflar:** yükleme amacı `material_note`, bağlama türü `material`;
  görünürlük tayfanın etkin üyesi ve `materials` modülü açık. Sıra
  değişebildiği için bırakılan fotoğrafı tek tetikleyici
  (`materials_release_photos`) silme kuyruğuna alır.
- **Silme:** yazarın kendi silmesi fotoğrafları hemen bırakır;
  moderatörün ya da platformun kaldırması yalnızca gizler. Geri alma yok;
  30 gün sonra kalıcı silme. Hesap silinince materyal kalır, yazar boşalır,
  kişinin fotoğraf dosyaları silinir.
- **Yetki:** `material.add`, `material.edit_own`, `material.delete_own`
  (üye; susturulmuşken yalnızca kendi silmesi), `material.remove_any`
  (moderatör). Şikâyet hedefi `material` (konusu yazarı); panel bağlantıyı
  açmaz, metin olarak gösterir. Kötüye kullanım filtresinin takılma
  noktası `material.checkTerms` (başlık ve bağlantı).
- Dışa aktarma bölümü `materials` (39); ana sayfa bileşeni `materials`
  (modül bileşeni). Hatalar 2720–2722: `material-not-found`,
  `blocked-link`, `material-limit`.

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

**Uygulama notları (#272):** (2026-11-23'te sohbette onaylandı (#323))

- **Tablolar:** `garage_vehicles` (`owner_id`, `make` 1–40, `model` ≤ 40,
  `model_year` 1900–gelecek yıl, `color` ≤ 30, `note` ≤ 200; metinler tek
  satır, boşluklar tekleştirilir; `photo1..3_upload_id`; soft delete;
  plaka kolonu yok) ve `garage_tayfa_visibility` (`user_id`, `tayfa_id`,
  `visible`). Kişi başına en fazla 10 silinmemiş araç (`garage-limit`;
  sahibin yazımları danışma kilidiyle sıraya girer).
- **Görünürlük:** tek kaynak SQL `garage_visible_in(owner, tayfa)` (satır
  ya da `automotive` varsayılanı). Tayfa garajı yalnızca sahibi etkin üye
  olan araçları döner. Fotoğraf görünürlüğü `garage_vehicle_seen_by`:
  aracın görünür olduğu, `garage` modülü açık ortak bir tayfada ikisi de
  etkin üye. Göstermek `garage.show` (üye, susturulmuşken değil), gizlemek
  `TayfaView`.
- **Araçlar bileşeni:** `SetVehicle`'da `garage_vehicle_id` yalnızca
  çağıranın kendi garajından; marka/model/yıl/renk kopyalanır (istekte boş
  olmalı), `event_vehicles.vehicle_id` garaj aracına foreign key (kalıcı
  silinince `NULL`). Garajdaki sonraki düzenleme etkinlikteki kopyayı
  değiştirmez. Plaka yalnızca etkinlik kaydında.
- **Silme:** sahibin silmesi fotoğrafları hemen bırakır; moderatörün
  (`garage.remove_any`, aracın görünür olduğu tayfada) ya da platformun
  kaldırması aracı her yerde gizler; 30 gün sonra kalıcı silme. Hesap
  silinince araçlar ve görünürlük satırları hemen silinir.
- Yükleme amacı `garage_vehicle` (tayfasız, avatar gibi); şikâyet hedefi
  `garage_vehicle` (konusu sahibi); kötüye kullanım filtresinin takılma
  noktası `garage.checkTerms`. Dışa aktarma bölümleri `garage_vehicles`
  (40) ve `garage_visibility` (41); ana sayfa bileşeni `garage` (modül
  bileşeni). Hatalar 2730–2731: `garage-vehicle-not-found`,
  `garage-limit`.

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
