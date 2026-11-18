# ADR-0037: Katalog ayrıntıları ve şema ekleri (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Karar veren:** Murat Arslan (2026-11-18 katalog oturumu). Ürün kararları
  Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri (2026-11-18)"
  sayfasındadır; bu ADR onların mimariye taşınmasıdır.
- **İlgili:** [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0026](0026-tayfa-istatistikleri-ve-vitrin.md),
  [ADR-0028](0028-etkinlik-fotograf-albumu.md),
  [ADR-0030](0030-arkadaslik-puani-ve-arkadas-onerisi.md),
  [ADR-0038](0038-simdi-baslat.md); Tayfam-App ADR-0031
- **Değiştirdiği:** ADR-0025 §2 (`interest_key` → `interest_keys`), §3
  (tanım şemasına `optional`, `organizer_choice`, tür bayrakları), §4
  (bileşen listesi ve parametreler, birimler), §5 (kişisel ölçümü
  organizatörün girmesi); ADR-0026 §2 (`log` istatistiği), §3 (kişisel
  katkıda `entered_by`). ADR-0025 ve ADR-0026'ya revizyon notu düşüldü.

## Bağlam

ADR-0025 tür kataloğunun modelini, ADR-0026 istatistiği kurdu. Katalog
tasarımı tür tür ilerleyince (2026-11-18; 15 ilgi alanı, yaklaşık 70 tür)
modelin ifade edemediği ihtiyaçlar çıktı:

- Aynı türün farklı etkinliklerde farklı davranması (havuzda yoklama yok,
  açık suda var; eşleşme ağacı eleme ya da lig).
- Planlanan değerle (yürüyüşte planlanan mesafe) gerçekleşen ölçümün iki kez
  girilmesi; derecenin süre ölçümüyle iki kez girilmesi.
- Günlükten istatistik ("bu yıl 84 kuş türü") ve serbest metnin aynı şeyi
  farklı yazması.
- Gol gibi kişisel ama katılımcının kendi girmemesi gereken ölçümler.
- Aynı masa oyunu gecesinde aynı anda birden çok masa; uygulamada olmayan
  oyuncu.
- Birden fazla ilgi alanına uyan türler (Müze / sergi: Sanat ve Gezi).
- Katılımın kendisinin özel nitelikli veri sayılabileceği türler (kan
  bağışı); çocuk ve yaşlı fotoğrafı riski olan ziyaretler.

## Karar

### 1. Tür kaydı

- `catalog_event_types.interest_key` yerine **`interest_keys`** (text[],
  1–3 öğe, ilki ana ilgi alanı). Tür, ilgi alanlarının hepsinde listelenir;
  istatistik tür anahtarına bağlı olduğu için bölünmez. Tayfa ilgi
  alanlarının varsayılan ana sayfa bileşenleri (ADR-0027) değişmez.
- Tür düzeyinde iki bayrak (tanımda, `flags`):
  - **`sensitive_attendance`**: bu türün etkinliklerinde katılım
    `user_activity_records`'a yazılmaz, profil istatistiğine (`user_stats`)
    ve arkadaşlık puanına (ADR-0030) girmez; katılımcı listesi yalnızca
    organizatöre döner, diğer katılımcılar yalnızca sayıyı görür. Tayfa
    toplamına yalnızca ortak metriklerden etkinlik sayısı ve toplam
    katılım girer. Bayrağı taşıyan ilk tür: Kan bağışı.
  - **`album_notice`**: albüme yükleme isteği `consent_ack: true` ister;
    yoksa `album-consent-required`. İstemci yüklemeden önce "Yüzü
    görünenlerden (çocuklarda velisinden) izin aldın mı?" onayını gösterir.
    Kontrol sunucudadır; istemcide atlanamaz. Bayrağı taşıyan tür:
    Ziyaret.
- `personal_scope_forbidden` (ADR-0025 §4) tür bayrakları arasına taşınır.

### 2. Tanım şeması ekleri

ADR-0025 §3'teki şemaya, bilinen anahtar listesine eklenerek:

- **`optional`** (bileşen düzeyi): `{"default": true|false}`. Organizatör
  etkinliği kurarken ya da etkinlik başlamadan önce bileşeni açıp kapatır;
  seçim `event_components (event_id, component_key, enabled)` satırında
  tutulur. `optional` olmayan bileşen her zaman açıktır. Kapalı bileşenin
  uçları `component-disabled` döner.
- **`organizer_choice`** (parametre düzeyi): bir parametrenin değeri yerine
  seçenek listesi ve varsayılan: `{"organizer_choice": ["elimination",
  "league", "swiss"], "default": "swiss"}`. Seçim yine `event_components`'ta
  (`params` jsonb, yalnızca seçenekli parametreler) tutulur ve tanımdaki
  listeye karşı doğrulanır. Etkinlik başladıktan sonra değiştirilemez.
- **`prefill_from`** (ölçüm düzeyi): bir alanın anahtarı. Etkinlik bitince
  ölçüm, alanın değeriyle **önerilen değer** olarak döner (`suggested`);
  organizatör onaylayınca ya da düzeltince yazılır. Onaylanmamış öneri
  istatistiğe girmez. Birim ve sınırlar ölçümün kendi tanımından gelir;
  alan ile ölçüm aynı birimde olmalıdır (katalog doğrulaması).
- Kural yine **bildirimseldir**: koşul, ifade ya da betik eklenmez.
  "Açık suda yoklama" kuralı yazılmaz; organizatör açar.

### 3. Bileşen değişiklikleri

| Bileşen | Değişiklik |
|---------|-----------|
| `measure` | `scope: personal` + `entered_by: organizer` geçerli: değeri organizatör ve atadığı hakemler kişi başına girer (gol). `entered_by: self` (bugünkü kural) yalnızca check-in'li katılımcı, kendi adına. |
| `ranking` | `source: <measure key>`: sıralama aynı etkinlikteki ölçümden hesaplanır, ayrı giriş yok. `source` yoksa bugünkü gibi elle. Yön (`asc`/`desc`) parametre. |
| `log` | `stat`: `count` / `distinct:<alan>` / `sum:<alan>` (birden çok olabilir, en fazla 3). Metin alanında `vocabulary: {"namespace": "<ad>"}` (§4). |
| `scoreboard` | **Masalar** ve **ön ayarlar** (§5), sonuç modu `wdl`, **misafir oyuncu** (§6). |
| `bracket` | İsviçre eşitlik bozma sırası sabit: puan → aralarındaki maç → Buchholz. Rating/ELO yok. |
| `vehicles` | `seats: true`: araç sahibi boş koltuk sayısını (0–8) yazar; katılımcı bir araca yer ister, araç sahibi onaylar. Masraf ya da para alanı yok. |
| `vote` | Seçenek kaynağı: `free`, `participants`, `vehicles`, `album`, `route_stops`. |
| `timer` (yeni) | Modlar `countdown` / `stopwatch` / `chess_clock` (`organizer_choice` olabilir), `presets` (saniye, en fazla 6). **Sunucuda veri yok**: tablo, uç ve olay yoktur; yalnızca tanımda yer alır ki istemci çizsin. |
| `pool_link` (yeni) | Etkinliği tayfanın soru havuzundaki sorulara bağlar (en fazla 50). Soru havuzu modülü kapalıysa bileşen gösterilmez. Modülün ADR'siyle birlikte gelir. |

Bileşen listesi (20): `route`, `meeting_point`, `schedule`, `checklist`,
`roll_call`, `vehicles`, `teams`, `draw`, `bracket`, `scoreboard`,
`ranking`, `vote`, `celebration`, `measure`, `goal`, `log`, `questions`,
`timer`, `pool_link` (+ temel: katılım, kontenjan, check-in, albüm).

Yeni birimler: `night`, `nm` (deniz mili), `box` (koli), `section`
(bölüm), `net`.

### 4. Tayfa sözlüğü

Serbest metnin aynı şeyi farklı yazması (`kırlangıç` / `Kırlangıç.`)
istatistiği bozar; Tayfam'ın içerik (kuş, kitap, balık listesi) sağlaması
ise "dış içerik yok" ilkesine aykırıdır. Çözüm tayfanın kendi sözlüğüdür:

- `tayfa_vocabulary` (core): `id`, `tayfa_id`, `namespace`, `value`
  (1–60), `value_normalized` (Türkçe küçük harf + boşluk sadeleştirme),
  `merged_into` (NULL ya da aynı tayfa ve ad alanında başka satır),
  `created_by`, `created_at`. Tekillik: `(tayfa_id, namespace,
  value_normalized)`.
- Kayıt girilirken istemci `vocabulary_id` ya da yeni `value` gönderir;
  yeni değer kötüye kullanım filtresinden geçer (ADR-0012) ve sözlüğe
  eklenir. `log` satırı `vocabulary_id` tutar.
- `GET …/vocabulary/{namespace}?q=` yalnızca üyeye, yalnızca o tayfanın
  değerleriyle döner (en fazla 20, birleştirilmişler hedefe çözülür).
- **Birleştirme:** moderatör ve yönetici (`tayfa.vocabulary.merge`, izin
  tablosu) iki değeri birleştirir; kaynak `merged_into` alır, denetim
  kaydına yazılır, etkilenen yılların istatistiği yeniden hesaplanır
  (ADR-0026 §2, iş idempotent). Birleştirme geri alınabilir (`merged_into`
  boşaltılır).
- Ad alanı tanımdadır; türler paylaşabilir (`fish_species` hem
  Balıkçılık'ta hem Dalış günlüğünde).
- KVKK: değer içeriktir; kişinin eklediği değerler dışa aktarmada
  (ADR-0021) kişinin günlük kayıtlarıyla birlikte gelir. Hesap silinince
  sözlük değeri kalır, `created_by` boşalır (tayfanın ortak verisi).

### 5. Yazboz: masa ve ön ayar

- Bir yazboz bileşeni birden çok **masa** taşır: `event_score_tables`
  (`id`, `event_id`, `component_key`, `preset_key`, `label` 1–30, `status`).
  Masa başına hakem atanır (ADR-0025 §5'teki "o masaya atanmış hakem").
  Masa sayısı en fazla 20.
- **Ön ayar** katalogdadır (`catalog_score_presets`: `key`, `labels`,
  `params` — oyuncu sayısı aralığı, takım/eşli, `round` / `single` /
  `first_to`, hedef puan, kazanan `high` / `low` / `first`, sonuç modu
  `points` / `wdl`). Ön ayar veridir; yeni oyun sürüm istemez. Tür tanımı
  hangi ön ayarların seçilebileceğini listeler.
- İlk ön ayarlar: Batak, 101 Okey, Okey, Tavla, Pişti, Langırt, Takımlı
  tur oyunu, Serbest. **Marka adı ön ayar adı olmaz** (ör. "Kelime
  anlatma").
- Para, bahis ya da ödül alanı hiçbir ön ayarda yoktur (ADR-0025 §4).

### 6. Misafir oyuncu

- Yazbozda ve eşleşme ağacında oyuncu, uygulamada hesabı olmayan biri
  olabilir: `event_guest_players` (`id`, `event_id`, `nickname` 1–24,
  `created_by`). Takma ad kötüye kullanım filtresinden geçer.
- Misafiri yalnızca organizatör ve masanın hakemi ekler; etkinlik başına en
  fazla 30.
- Misafir hiçbir istatistiğe, kutlama bildirimine ve arkadaşlık puanına
  girmez; profil, kişisel kayıt ve iletişim alanı yoktur.
- Etkinlik bitiminden **30 gün** sonra takma ad silinir; skor satırı
  "Misafir" olarak kalır (şampiyonlar bileşeni misafiri adsız gösterir).

### 7. ADR-0026 değişiklikleri

- `log` metrikleri: `<tür>.<bileşen>.<stat>` (ör.
  `birding.sightings.distinct_species`). `distinct` sözlükte birleştirilmiş
  değerleri hedefleriyle sayar.
- Kişisel katkı listesinde `entered_by: organizer` olan kişisel ölçümler de
  yer alır (gol krallığı tayfa içinde görünür; vitrinde yalnızca toplam,
  §3 kuralı değişmez).
- `sensitive_attendance` türleri ortak metriklerden yalnızca etkinlik
  sayısı ve toplam katılıma girer; "farklı katılımcı sayısı" ve kişisel
  istatistik hesaplanmaz.

## Gerekçe

- **`optional` ve `organizer_choice`:** Koşul dili eklemeden aynı türün
  farklı kullanımlarını karşılar; karar organizatörde, kural verideki
  sabit listede kalır. Tanım yine bildirimseldir, sunucu doğrular.
- **Önerilen değer:** Organizatör aynı sayıyı iki kez girmez, ama plan
  istatistiğe doğrudan da girmez (iptal edilen ya da yarıda kalan yürüyüş).
- **Tayfa sözlüğü:** Tayfam içerik sağlayıcı olmaz; ciddi bir kuşçu
  tayfası bilimsel adlarla, arkadaş grubu gevşek bir sözlük kurar.
  Normalizasyon tek başına "kırlangıç / ev kırlangıcı" belirsizliğini
  çözmez; birleştirme çözer.
- **Misafir oyuncu:** Masa oyunu gecesinde herkesin uygulamayı kurmasını
  beklemek yazbozu kullanılamaz kılar; hesapsız ve kısa ömürlü takma ad
  tutulan veriyi en aza indirir.
- **`sensitive_attendance`:** Kan bağışına katılım sağlık verisi
  sayılabilir; tutulmayan veri çalınamaz (ADR-0011).

## Sonuçlar

- Yeni tablolar (core): `event_components`, `tayfa_vocabulary`,
  `event_score_tables`, `catalog_score_presets`, `event_guest_players`.
- İzin tablosuna: `tayfa.vocabulary.merge`, misafir ekleme, masa yönetimi.
- OpenAPI: `consent_ack` (albüm), `suggested` (ölçüm), `vocabulary_id`,
  `optional`/`organizer_choice` seçimleri, misafir ve masa uçları;
  problem tipleri `album-consent-required`, `component-disabled`.
- `timer` ve `pool_link` en düşük istemci sürümü kaydına girer
  (ADR-0025 §7).
- Avukat incelemesine: kan bağışı katılımı ve `album_notice`'in yeterliliği.
  İnanç tayfaları ve etkinlikleri için ek gizleme yapılmaz (platform sahibi,
  2026-11-18).

## Değerlendirilen alternatifler

- **Tanımda koşul ("yer = açık su ise yoklama")**: Reddedildi. Tanımı
  bildirimsel olmaktan çıkarır, istemci ve sunucuda iki yorumlayıcı ister.
- **Tayfam'ın sağladığı tür listeleri (kuş, balık)**: Reddedildi. Dış
  içerik sağlamamak ürün ilkesi; bakım yükü büyük.
- **Misafiri hesapsız kullanıcı olarak modellemek**: Reddedildi. Kimlik,
  KVKK ve kötüye kullanım yüzeyini büyütür; ihtiyaç yalnızca bir takma ad.
- **Ayrı tür olarak "Müze (Gezi)" ve "Müze (Sanat)"**: Reddedildi.
  İstatistiği böler, katalog tekrarlanır; `interest_keys` yeterli.
