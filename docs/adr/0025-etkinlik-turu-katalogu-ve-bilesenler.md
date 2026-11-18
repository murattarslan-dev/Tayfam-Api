# ADR-0025: Etkinlik türü kataloğu ve bileşen modeli (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15)
- **Tarih:** 2026-11-15
- **Revizyon:** 2026-11-18 (platform sahibi, sohbette;
  [ADR-0041](0041-v3-tek-seferde-gecis-ve-yayin.md)): §10'daki aşamalı
  taşıma ve §8'deki geriye uyumluluk kaldırıldı; v3 tek seferde yayınlanır.
- **Revizyon:** 2026-11-18 (platform sahibi, sohbette;
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md)): `interest_keys`,
  `optional` / `organizer_choice` / `prefill_from`, tür bayrakları,
  bileşen listesi (`timer`, `pool_link`), masa ve ön ayar, misafir oyuncu,
  tayfa sözlüğü, yeni birimler; kişisel ölçümü organizatör girebilir.
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 3 (v3) — Dinamik Etkinlik Kataloğu ve Bileşenler"). Bu ADR onların
  mimariye taşınmasıdır.
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0016](0016-desteklenen-istemci-surumu.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md);
  [ADR-0026](0026-tayfa-istatistikleri-ve-vitrin.md),
  [ADR-0027](0027-tayfa-ana-sayfa-bilesenleri.md); Tayfam-App ADR-0024

## Bağlam

Faz 1'de yedi etkinlik türü vardır (`convoy`, `camp`, `hike`, `fishing`,
`gaming`, `sports`, `general`) ve koda gömülüdür:

- core'da `events.type`, `tayfa_interests.event_type` ve
  `user_activity_records.event_type` üzerinde CHECK kısıtları; identity'de
  `profile_details.interests` üzerinde aynı listeyle CHECK.
- Türe özel 1:1 tablolar (`event_convoy`, `event_camp`, `event_hike`,
  `event_fishing`, `event_gaming`, `event_sports`) ve onlara bağlı alt
  tablolar (`event_convoy_vehicles`, `event_camp_items`,
  `event_hike_checkins`, `fishing_catches`); her biri `events (id, type)`
  bileşik anahtarıyla türe kilitlenir.
- OpenAPI'de `EventType` enum'u; istemcide `features/etkinlik/turler/`
  altında tür başına klasör.

Yeni bir tür bugün migration, sözleşme değişikliği ve uygulama sürümü
ister. v3'ün ilk derinliği türleri onlarcaya çıkarmak ve **yeni türü kod
değiştirmeden** eklemektir (platform sahibi, 2026-11-15):

- Türleri **yalnızca platform sahibi** belirler; tayfaya özel tür ya da alan
  yoktur. Katalogda olmayan için "Genel" vardır.
- Türler ortak **bileşenlerden** kurulur (yazboz, kura, eşleşme ağacı,
  kutlama, ölçüm…). Bileşen kod ister, tür istemez.
- Bugünkü türe özel araçlar bileşenlere **taşınır**; tek sistem kalır.
- Bugünkü "Spor" ve "Oyun" türleri `Spor (diğer)` / `Oyun (diğer)` olarak
  kalır, alt türler yanına eklenir.
- Yazboz ve eşleşme ağacında skoru organizatör ve atadığı hakemler girer,
  organizatör her skoru düzeltebilir; canlı güncellenir.
- Para, bahis ya da "ortaya konan" alanı hiçbir bileşende yoktur.
- İlgi alanı türleri gruplar (Çevre → Çöp toplama, Fidan dikimi); etkinlik
  oluştururken türleri öne çıkarır, ana sayfa bileşenlerinin varsayılanını
  belirler (ADR-0027).

## Karar

### 1. Kavramlar

| Kavram | Nedir | Nerede tanımlanır | Değişince |
|--------|-------|-------------------|-----------|
| **Bileşen** | Bir işlev bloğu (rota, liste, yazboz…) | Kod: core + istemci | Kod ve uygulama sürümü |
| **Tür** | Ad, ikon, ilgi alanı, mod, alanlar, bileşenler ve parametreleri | Veri: core'daki katalog | Yalnızca katalog kaydı |
| **İlgi alanı** | Türleri gruplayan başlık | Veri: core'daki katalog | Yalnızca katalog kaydı |

Tür tanımı **tamamen bildirimseldir**: hangi alanlar, hangi sınırlar, hangi
bileşenler, hangi parametrelerle. Tanımda çalıştırılabilir kural, ifade,
betik, şablon, HTML ya da URL yoktur. Sunucu da istemci de tanımı yalnızca
bilinen anahtarlar üzerinden yorumlar.

### 2. Katalog core'dadır

Etkinlikler core'da olduğu için katalog da core'dadır (ADR-0002, veri tek
sahipte). Tablolar (ayrıntı migration'da):

- `catalog_interests`: `key` (değişmez, `^[a-z][a-z0-9_]{1,39}$`),
  `position`, `labels` (dil → metin), `icon` (istemcinin sabit ikon
  setinden bir anahtar), `status` (`active` / `retired`),
  `default_widgets` (bu ilgi alanını seçen tayfada varsayılan açık gelen ana
  sayfa bileşenleri; ADR-0027).
- `catalog_event_types`: `key` (değişmez), `interest_key`, `labels`,
  `icon`, `modes` (`in_person`, `online` ya da ikisi), `status`, `version`,
  `definition` (jsonb). Her değişiklik `version`'ı artırır; eski sürümler
  `catalog_event_type_versions`'ta değişmez satır olarak kalır.
- `catalog_meta`: tek satır; kataloğun bütününün sürümü (`catalog_version`),
  her değişiklikte artar.

Kurallar:

- **Anahtar değişmez, kayıt silinmez.** Bir tür ya da ilgi alanı
  kaldırılmak istenirse `retired` olur: yeni etkinlikte seçilemez, eski
  etkinlikler ve geçmiş kayıtlar onu göstermeye devam eder.
- **Etkinlik tür sürümüne bağlanır.** `events` tablosuna
  `type_version` eklenir. Etkinlik oluşturulduğu andaki tanımla doğrulanır
  ve gösterilir. Tanım sonradan değişirse eski etkinliklerin verisi
  bozulmaz; yeni bileşen geriye dönük eklenmez, kaldırılan bileşenin verisi
  silinmez, yalnızca yeni etkinliklerde kullanılmaz.
- `events.type` CHECK kısıtı kaldırılır; yerine katalogla aynı veritabanında
  `(type, type_version)` → `catalog_event_type_versions` foreign key'i gelir.
  `user_activity_records.event_type` ve `tayfa_interests` de katalog
  anahtarlarına bağlanır (`tayfa_interests` artık `interest_key` tutar; bkz.
  §8).
- Katalog yalnızca platform yönetim panelinden değişir (§7).

### 3. Tür tanımının şeması

`definition` jsonb, Go'da sabit bir şemaya karşı doğrulanır (bilinmeyen
anahtar reddedilir; boyut üst sınırı 16 KiB):

```json
{
  "fields": [
    {"key": "difficulty", "kind": "choice", "required": false,
     "options": ["1", "2", "3", "4", "5"], "labels": {"tr": "Zorluk"}}
  ],
  "components": [
    {"kind": "route"},
    {"kind": "roll_call", "params": {"alarm": true}},
    {"kind": "measure", "key": "distance", "params": {
      "unit": "km", "scope": "total", "entered_by": "organizer",
      "min": 0, "max": 1000, "decimals": 2, "showcase": true}}
  ]
}
```

- **Alan türleri** kapalı listedir: `text` (en fazla 120), `long_text`
  (en fazla 1000), `integer`, `decimal`, `choice`, `multi_choice`,
  `boolean`, `date`, `duration`. Para birimi, URL, dosya, konum ve kişi alanı
  **yoktur**. Seçenek listeleri, sınırlar ve zorunluluk tanımdadır.
- Alan değerleri `event_field_values` (event_id, field_key, değer jsonb)
  tablosunda tutulur ve yazılırken etkinliğin tür sürümüne göre doğrulanır.
  Serbest metin alanları kötüye kullanım filtresinden geçer (ADR-0012).
- Bir türde aynı bileşen birden fazla kez kullanılabiliyorsa (ör. iki ayrı
  ölçüm) her birinin tanımda benzersiz bir `key`'i vardır.

### 4. Bileşen kütüphanesi

Bileşen listesi kapalıdır ve kodla büyür. İlk liste (Notion Faz 3 §3):

| `kind` | İşlev | Parametreler (özet) | Taşınan Faz 1 yapısı |
|--------|-------|--------------------|----------------------|
| `route` | Güzergâh | — | `event_convoy.route` |
| `meeting_point` | Buluşma noktası | — | `events.meeting_*` (temel alan olarak kalır) |
| `schedule` | Saatli program akışı | en fazla madde | — |
| `checklist` | Kim ne getiriyor/yapıyor/hangi bölüm | etiket (malzeme, görev, bölüm), atama açık mı | `event_camp_items` |
| `roll_call` | Çıkan/dönen takibi | `alarm` (sessize alınamaz), dönüş toleransı | `event_hike_checkins`, `event_hike` alanları |
| `vehicles` | Katılan araçlar | plaka açık mı | `event_convoy_vehicles` |
| `teams` | Takım kurma | rastgele / dengeli / elle; takım büyüklüğü (2 = ikili eşleştirme) | — |
| `draw` | Kura | — | — |
| `bracket` | Eşleşme ağacı | eleme / lig / İsviçre | — |
| `scoreboard` | Yazboz ve skor | oyuncu/takım, el el ya da tek maç, hedef puan, yüksek/düşük kazanır | — |
| `ranking` | Derece / sıralama | süre ya da sıra | — |
| `vote` | Etkinliğe bağlı oylama | seçenek kaynağı (serbest, katılımcı, araç…) | — |
| `celebration` | Kazanan ekranı ve tayfa bildirimi | kaynak bileşen | — |
| `measure` | Birimli ölçüm | birim, `scope` (`personal` / `total`), `entered_by`, sınırlar, `showcase` (vitrine çıkabilir mi), `sensitive` (yalnızca kişiye görünür; ör. deneme neti) | — |
| `goal` | Hedef çubuğu | bağlı ölçüm, hedef değer | — |
| `log` | Kayıt girişi (av, kitap, kuş, zirve…) | kayıt alanları (§3 alan türleriyle), konum açık mı ve gizlilik seviyesi | `fishing_catches`, `event_fishing.spot_visibility` |
| `questions` | Söyleşide canlı soru ve oy | anonim soru açık mı | — |

- **Birimler kapalı listedir** (`kg`, `piece`, `bag`, `km`, `m`,
  `altitude_m`, `page`, `minute`, `hour`, `unit`, `goal`, `point`…) ve
  istemcinin çeviri dosyasındadır; ölçümün kendi adı tanımdaki `labels`'tan
  gelir.
- Her bileşenin verisi kendi tablolarındadır (`event_checklist_items`,
  `event_scores`, `event_measurements`, `event_log_entries`…), hepsi
  `(event_id, component_key)` ile etkinliğe ve tanımdaki bileşene bağlanır.
  Bir bileşenin tablosuna, etkinliğin tür sürümünde o bileşen yoksa yazılamaz
  (uygulama katmanında ve tetikleyiciyle).
- **Para yok.** Hiçbir bileşende para, bahis, ödül tutarı ya da "ortaya
  konan" alanı yoktur; şema bunu ifade edecek bir alan türü de sunmaz.
- **Konum.** `log` bileşenindeki konum, bugünkü av günlüğü gibi kişinin
  bilerek paylaştığı içeriktir (ADR-0008); log'a, denetim kaydına ve olaya
  yazılmaz. Gizlilik seviyesi (`self`, `participants`, `tayfa`) bileşen
  parametresidir; mera kuralı böylece genelleşir.
- **Sağlık verisi tutulmaz.** Kan bağışı gibi türlerde ölçüm yalnızca
  `scope: total` ve `entered_by: organizer` olabilir; kişi bazında değer
  tanımda yasaklanır (katalog doğrulamasında tür bazında bayrak:
  `personal_scope_forbidden`).

### 5. Yetki

Bileşen işlemleri core'un tek izin tablosuna eklenir (ADR-0004); varsayılan
"hayır"dır. Örnekler:

| İşlem | Kim |
|-------|-----|
| Takım kur, kura çek, eşleşme ağacını başlat | Organizatör |
| Hakem ata / kaldır | Organizatör |
| Skor gir | Organizatör ve **o masaya/maça atanmış hakem** |
| Skor düzelt | Organizatör |
| Kişisel ölçüm gir (`scope: personal`) | Check-in'i doğrulanmış katılımcı, kendi adına |
| Toplam ölçüm gir (`scope: total`) | Organizatör |
| Liste maddesi üstlen | Katılımcı (bugünkü kamp malzemesi kuralı) |
| Soru gönder / oyla | Katılımcı |
| "Cevaplandı" işaretle | Organizatör |

Yazma pencereleri bugünkü katılım düzeltme kuralını izler: ölçüm, skor ve
kayıtlar etkinlik bitiminden sonra 7 gün düzenlenebilir, sonra donar
(istatistiklerin kaynağı donmuş veridir, ADR-0026).

`questions` bileşeninde anonim soru diğer katılımcılara **ve tayfa
yönetimine** anonimdir; yazar yalnızca sunucuda tutulur. Tayfa yönetimi
anonim soruyu yazarını görmeden silebilir. Soru şikâyet edilirse yazar
yalnızca platform yönetimine, şikâyet kaydında görünür (ADR-0012 şikâyet
akışı). İstemciye dönen hiçbir yanıtta, platform paneli dışında, anonim
sorunun yazarı yer almaz.

### 6. Canlı güncelleme

Skor, eşleşme ağacı, kura ve canlı soru değişiklikleri core'da aynı
transaction içinde `domain_events`'e yazılır ve chatrelay ile chat'e gider
(ADR-0002, ADR-0005). Olay yalnızca kimlikleri taşır
(`event_id`, `component_key`); içerik gitmez. chat, etkinliği izleyen ve
erişimi olan bağlantılara `event.component_changed` çerçevesi gönderir;
istemci ilgili bileşeni REST'ten yeniden çeker. Erişim, kanal kitlesindeki
gibi her abonelikte core'a sorulur (ADR-0024 §1 modeli).

### 7. Katalog yönetimi

- Platform yönetim paneli (`admin.tayfam.app`, ADR-0008) ilgi alanı ve tür
  ekleme, düzenleme ve `retired` yapma ekranları kazanır. Panel core'a
  admin token'ıyla gRPC üzerinden yazar; her değişiklik değişmez denetim
  kaydına yazılır.
- Kaydetmeden önce sunucu tanımı doğrular ve **önizleme** üretir (hangi
  bileşenler, hangi alanlar, hangi istemci sürümü gerekir).
- Her bileşenin, onu tanıyan **en düşük istemci sürümü** core'da sabittir.
  Bir tür, prod'daki `min_supported_version`'ın tanımadığı bir bileşen
  içeriyorsa panel uyarır (yayına alınabilir; eski istemci o bölümde
  "güncelle" gösterir, §9).

### 8. Dağıtım ve sözleşme

- `GET /v1/catalog`: ilgi alanları ve aktif türlerin güncel tanımları,
  `catalog_version` ile. `ETag` = `catalog_version`; istemci açılışta ve
  `If-None-Match` ile çağırır. Token ister (katalog herkese aynıdır ama
  açık uç sayısını artırmamak için; ADR-0016'daki gibi girişten önce
  gerekmez).
- `GET /v1/catalog/event-types/{key}/versions/{version}`: eski bir
  etkinliğin tanımı için.
- OpenAPI'de `EventType` enum'u kaldırılır; `type` serbest anahtar
  (`^[a-z][a-z0-9_]{1,39}$`) ve `type_version` olur. Türe özel istek ve yanıt
  gövdeleri (`convoy`, `camp`…) kaldırılır; yerine `fields` ve bileşen başına
  uçlar gelir (`/v1/tayfas/{id}/events/{eid}/components/{key}/…`).
  Geriye uyumluluk için Faz 1 alanları, en düşük istemci sürümü v3'e
  çekilene kadar yanıtlarda dolu kalır (§10).
- **CLAUDE.md'den bilinçli sapma:** "API Türkçe metin döndürmez" kuralı
  katalog için uygulanamaz; tür ve alan adları sürüm çıkmadan eklenebilmeli.
  Katalogdaki `labels` dil → metin haritasıdır (`{"tr": "…"}`), platform
  sahibinin yazdığı içeriktir ve istemcinin seçtiği dile göre gösterilir.
  Hata metinleri, birimler ve bileşen arayüz metinleri yine istemcinin
  çeviri dosyasındadır. CLAUDE.md'nin bu istisnayı yazması platform
  sahibinin değişikliğidir.
- **İkonlar:** Tanımdaki `icon`, istemcide sabit bir ikon setinin
  anahtarıdır; sunucu ikon görseli taşımaz. Bilinmeyen anahtar için istemci
  ilgi alanının ikonunu, o da yoksa genel ikonu gösterir.

### 9. Eski istemci

İstemci tanımadığı bir bileşenle karşılaşınca o bölümde "Bu özelliği görmek
için uygulamayı güncelle" gösterir; etkinliğin geri kalanı çalışır.
Tanımadığı bir alan türünü göstermez ve o türde etkinlik oluşturmayı
kapatır. Bir bileşen güvenlik ya da veri bütünlüğü için zorunlu olursa
(ör. `roll_call` alarmı) en düşük istemci sürümü ADR-0016 ile yükseltilir.

### 10. Faz 1'den taşıma

Tek seferde değil, aşamalı:

1. Katalog tabloları ve yedi tür, bugünkü davranışla aynı tanımlarla
   eklenir (`sports` → "Spor (diğer)", `gaming` → "Oyun (diğer)"; anahtarlar
   değişmez, bu yüzden mevcut etkinlikler, katılım geçmişi ve profil ilgi
   alanları taşınmaz).
2. Bileşen tabloları eklenir; türe özel tablolardaki veri ilgili bileşen
   tablolarına kopyalanır (konvoy rotası → `route`, araçlar → `vehicles`,
   kamp malzemesi → `checklist`, yürüyüş yoklaması → `roll_call`, av →
   `log`). `event_gaming` ve `event_sports` alanları `event_field_values`'a
   taşınır.
3. Uçlar her iki yerden de okur, yalnızca yeni yere yazar. En düşük istemci
   sürümü v3'e çekilince eski uçlar ve türe özel tablolar kaldırılır (veri
   silen migration; hassas alan, platform sahibi birleştirir).

Tayfa ilgi alanları: bugün `tayfa_interests` etkinlik türü anahtarı tutar.
Yedi türün her biri bir ilgi alanına eşlenir (örnek anahtarlar: `convoy` →
`automotive`, `camp`/`hike` → `nature`, `fishing` → `water`, `sports` →
`sports`, `gaming` → `games`, `general` → `general`) ve tablo
`interest_key` tutar; aynı ilgi alanına düşen iki tür tek satır olur.
identity'deki `profile_details.interests` için de aynı eşleme yapılır;
CHECK kısıtı kaldırılır, değerler core'un katalog olayıyla
(`catalog.changed`, yalnızca sürüm numarası) doğrulanan bir kopyaya karşı
denetlenir (ADR-0009 kopya modeli).

### 11. KVKK

- Bileşen verisi kişinin veri dışa aktarmasına girer (ADR-0021): kendi
  ölçümleri, kayıtları, skorları, soruları (anonim olanlar dahil, kendi
  yazdığı için).
- Hesap silmede kişisel ölçüm, kayıt ve sorular silinir; toplam ölçümler ve
  skor tablosundaki yer "Silinmiş kullanıcı" olarak kalır (bugünkü içerik
  kuralı).
- Plaka kuralı (30 gün) `vehicles` bileşeninde aynen sürer.
- Yeni veri kategorileri (ölçüm, skor) aydınlatma metnine eklenir;
  hukukçu listesine yazılır.

## Gerekçe

- **Tür veri, bileşen kod:** Onlarca tür için onlarca tablo ve sürüm
  sürdürülemez. Bileşen sayısı türlerden çok daha yavaş büyür; her biri bir
  kez test edilir.
- **Bildirimsel tanım:** Sunucudan gelen her şey girdi kabul edilir
  (ADR-0011). Kural motoru ya da betik, katalog yönetim yolu ele
  geçirildiğinde istemcide ya da sunucuda kod çalıştırma yüzeyi açardı.
  Kapalı alan ve bileşen listesiyle panel ele geçirilse bile yapılabilecek
  en kötü şey anlamsız bir tür tanımıdır.
- **Tür sürümüne bağlama:** Katalog değişince eski etkinliklerin verisinin
  ve istatistiklerin anlamı değişmemeli.
- **Bileşen başına tablo (jsonb değil):** Skor, ölçüm ve kayıtlar
  istatistiğin kaynağıdır; sorgulanabilir, CHECK'lenebilir ve dizinlenebilir
  olmalılar. jsonb yalnızca tanımda ve tür alanlarının değerlerinde
  kullanılır.
- **Para alanı türü yok:** Bahis ve kumar riskini (ADR-0012) bileşen
  seviyesinde ifade edilemez kılar; filtreye bırakılmaz.

## Sonuçlar

- `events` tablosunun türe özel tablolarla kurduğu bileşik anahtar yapısı
  emekliye ayrılır; yerine `(event_id, component_key)` gelir.
- OpenAPI'de kırıcı değişiklik: `EventType` enum'u ve türe özel gövdeler.
  §10'daki geçiş dönemi boyunca iki yapı birlikte yaşar.
- Platform panelinin kapsamı büyür; katalog yönetimi hassas alandır
  (CODEOWNERS'a `internal/core/app/catalog/` ve migration'ları eklenir).
- Katalog metinleri istemci çeviri dosyası dışında kalır; ikinci dil
  eklendiğinde katalog kayıtları da çevrilmelidir.
- Her yeni bileşen sunucu + istemci + en düşük sürüm notu üçlüsüyle gelir.

## Değerlendirilen alternatifler

- **Tür başına tablo ve enum (bugünkü yapı):** Yeni tür kod ve sürüm ister;
  v3 hedefiyle çelişir.
- **Tamamen jsonb (tür verisi tek kolon):** Esnek, ama istatistik
  sorgularını, CHECK'leri ve dizinleri kaybettirir; doğrulama yalnızca
  uygulamaya kalır.
- **Sunucu tarafından çizilen arayüz (server-driven UI, düzen ve widget
  ağacı):** En esnek, ama bileşen davranışı (kura, ağaç, yoklama alarmı)
  zaten kod istediği için kazancı az; saldırı yüzeyi ve test yükü büyük.
- **Tayfaya özel tür/alan:** Platform sahibi istemedi (2026-11-15);
  moderasyon yükü ve istatistiklerin karşılaştırılamaması.
- **Katalogu ayrı bir servise koymak:** Etkinlik yazma yolu her istekte
  katalogu doğruluyor; servis sınırı yalnızca gecikme ve kopya getirirdi.
