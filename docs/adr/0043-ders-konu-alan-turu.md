# ADR-0043: Ders/konu listesine bağlı tür alanı (`study_tags`)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-23, sohbette; issue
  #333). "Seçenekler" bölümündeki ayrıntı seçimleri ajanın önerisidir ve
  platform sahibinin onayına sunulmuştur.
- **Tarih:** 2026-11-23
- **Karar veren:** Murat Arslan (2026-11-23, sohbette: "eğitim türlerine
  ders/konu seçimi eklenecek"). Ürün kararı Notion "Faz 3 — Katalog
  ayrıntıları ve şema ekleri (2026-11-18)" §B.14'tedir ("Alanlarda ders/konu
  etiketleri Tayfam'ın ders-konu listesinden gelir"); bu ADR onu mimariye
  taşır.
- **İlgili:** [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0027](0027-tayfa-ana-sayfa-bilesenleri.md) §6,
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md),
  [ADR-0040](0040-tayfa-modulleri-v3.md) §5,
  [ADR-0042](0042-katalog-degisikligi-olayi.md); Tayfam-App ADR-0024
- **Değiştirdiği:** ADR-0025 §3 (kapalı alan türü listesine `study_tags`
  eklenir). ADR-0037 "Uygulama notları (#260)"daki "Eğitim türlerinin
  ders/konu alanları tanımda yoktur" notu bu kararla kapanır.

## Bağlam

Notion'daki katalog Eğitim türlerine ders-konu listesinden (ADR-0040 §5,
`catalog_study_tags`: düzey → ders → konu) gelen alanlar ister:

| Tür | Notion'daki alan |
|-----|------------------|
| Çalışma grubu / etüt `study_group` | dersler (çoklu) |
| Soru çözüm oturumu `problem_session` | dersler |
| Akran konu anlatımı `peer_teaching` | ders, konu |
| Deneme sınavı `mock_exam` | ders/sınav türü |

ADR-0025 §3'ün alan türleri kapalı listedir ve hiçbiri bu listeye bağlı
değildir. `choice` ile seçenekleri tanıma kopyalamak mümkün değildir:
liste 294 etiketlidir, platform panelinden değişir ve emekli etiket eski
içerikte görünmeye devam etmelidir. Bu yüzden katalog seed'i (#260) dört
türü bu alanlar olmadan ekledi ve karar platform sahibine bırakıldı.

## Karar

### 1. Alan türü: `study_tags`

Kapalı alan türü listesine (ADR-0025 §3) bir tür eklenir. Tanımdaki biçimi:

```json
{"key": "subjects", "kind": "study_tags", "labels": {"tr": "Dersler"},
 "required": false, "min_depth": "subject", "max_depth": "topic",
 "max_selected": 5}
```

| Anahtar | Değerler | Varsayılan | Anlamı |
|---------|----------|------------|--------|
| `min_depth` | `level`, `subject` | `subject` | Seçilebilecek en sığ etiket |
| `max_depth` | `level`, `subject`, `topic` | `topic` | Seçilebilecek en derin etiket; `min_depth`'ten sığ olamaz |
| `max_selected` | 1–10 | 1 | En fazla seçim; 1 ise alan tek seçimlidir |

- **Değer:** etiket anahtarlarının listesi (1 – `max_selected`, tekrar
  yok), istemcinin verdiği sırayla saklanır:
  `["tyt_math", "ayt_physics_other"]`. Her öğe seçilen **en derin**
  etikettir; anahtar listede tekil olduğu ve etiket yer değiştirmediği için
  (üst etiket sütununa UPDATE yetkisi yok, 00067) konu anahtarı dersini ve
  düzeyini de belirler. Tek seçimli alanın değeri de bir öğeli listedir.
- "Düzey → ders → konu" yolu böylece tek anahtarla ifade edilir: ders ile
  konusunu birlikte seçmek için konuyu seçmek yeter. Aynı daldan iki öğe
  (`tyt_math` ve onun konusu) reddedilmez; anlamı değişmez ve denetimi her
  öğe için ek sorgu isterdi.
- Değer yalnızca anahtardır; ad ve sıra katalogdan gelir. Serbest metin
  yoktur, bu yüzden kötüye kullanım filtresi gerekmez.
- `study_tags` **yalnızca tür alanıdır**: `log` bileşeninin kayıt
  alanlarında kullanılamaz (kayıt alanları uygulama katmanında veritabanına
  bakmadan doğrulanır; kişisel kayda ders etiketi bugün bir ihtiyaç
  değildir). `prefill_from` ve günlük istatistiği (`stat`) bu alanı
  kullanamaz (sayısal değil, metin/seçim değil).
- Tanımın doğrulayıcısı (`definition.Parse`) yalnızca biçimi denetler;
  hangi etiketlerin var olduğu tanıma girmez. Tür, liste değişince yeniden
  sürüm almaz.

### 2. Doğrulama

Değer iki adımda, etkinliği yazan transaction'da doğrulanır:

1. **Biçim** (`definition.Field.Clean`, veritabanısız): JSON dizisi, 1 –
   `max_selected` öğe, her öğe katalog anahtarı biçiminde, tekrar yok.
2. **Liste** (`catalog.CheckStudyValues`, `catalog_study_tags`'e karşı):
   her anahtar listede vardır, derinliği `min_depth`–`max_depth`
   aralığındadır ve **seçilebilir**dir: kendisi ve üst etiketleri `active`.

**Emekli etiket** (ADR-0040 §5 ile aynı kural):

- Yeni seçimde emekli etiket (ya da emekli bir etiketin altındaki etiket)
  seçilemez; `validation` döner.
- Etkinliğin **o alandaki mevcut değerinde** olan anahtar emekli olsa da
  düzenlemede kalabilir: alan değerleri güncellemede tamamen yeniden
  gönderildiği için (EventUpdateRequest), istemci değişmeyen emekli seçimi
  geri gönderir ve reddedilmez. Yeni eklenen her anahtar yine
  seçilebilir olmalıdır.
- Seri düzenlemesinde (bütün seriye uygulanan güncelleme) karşılaştırma
  düzenlenen etkinliğin değerine göredir; serinin bütün örnekleri aynı
  değeri alır.
- Etiket silinmediği için (00067: DELETE yok) saklanan anahtar her zaman
  listede bulunur; istemci emekli etiketi de adıyla gösterir.

Hata: `validation`, alan `fields.<alan anahtarı>` (diğer alan türleriyle
aynı); hangi öğenin reddedildiği yazılmaz.

### 3. Sözleşme

- **REST:** yeni uç ya da alan yoktur. `CatalogField`'a `study_tags`
  türü ve `min_depth`, `max_depth` anahtarları eklenir; `max_selected`'in
  açıklaması genişler. `EventFieldValues`'ta değer "metin dizisi"dir.
  Etiket listesi zaten `GET /v1/catalog` → `study_tags`'tedir (ADR-0040 §5).
- **gRPC:** tanım `catalog.proto`'da sıkıştırılmış JSON olarak geçtiği için
  mesaj değişmez; `event.proto` (`Event.fields`) ve `admin.proto`
  (`CatalogPreviewField.kind`) yorumları güncellenir.
- **Eski istemci:** ADR-0025 §9 aynen: tanımadığı alan türünü göstermez ve
  o türde etkinlik oluşturmayı kapatır. Alan türleri için en düşük istemci
  sürümü kaydı yoktur (yalnızca bileşenler için, ADR-0025 §7); Faz 3 tek
  seferde 0.3.0 ile yayınlandığı için (ADR-0041) istemci bu türü aynı
  sürümde tanır. İleride yayından sonra yeni alan türü eklenirse alan
  türleri de en düşük sürüm kaydına alınmalıdır.

### 4. İstemcide gösterim

- **Seçim:** istemci listeyi `GET /v1/catalog` → `study_tags`'ten alır ve
  yalnızca seçilebilir etiketleri (`status: active`, üstleri de aktif)
  `min_depth`–`max_depth` aralığında sunar. Tek seçimde ortak ders/konu
  seçicisi (Tayfam-App #282, `DersKonuSecici`) kullanılır; düzey seçilebilen
  alanda (`min_depth: level`) ve çoklu seçimde seçici genişletilir. Seçim
  düzey → ders → konu sırasıyla daraltılır; "Diğer" her düzeyde ve derste
  vardır.
- **Gösterim:** her öğe üst zinciriyle gösterilir ("TYT › Matematik ›
  Türev"); adlar katalogdaki `labels`'tan (ADR-0025 §8 istisnası), alan adı
  tanımdaki `labels`'tan, diğer arayüz metinleri çeviri dosyasından gelir.
  Emekli etiket de adıyla gösterilir (liste emeklileri de taşır); listede
  hiç bulunmayan anahtar (bozuk ya da eski önbellek) gösterilmez.
- **Düzenleme:** istemci değişmeyen emekli seçimi kaldırmadan geri gönderir;
  kullanıcı onu kaldırabilir ama yeniden ekleyemez.
- Etiketle etkinlik süzme ya da arama bu kararın kapsamında değildir.

### 5. KVKK ve gizlilik

- Değer etkinliğin içeriğidir (organizatörün seçtiği konu); katılımcıya ait
  kişisel veri değildir ve yalnızca katalog anahtarı tutar. Diğer alan
  değerleri gibi `event_field_values`'tadır, yalnızca etkinliği görebilene
  (`GetEvent`) döner; listelerde ve Keşfet'te dolmaz.
- Etkinliği açanın dışa aktarmasına diğer alan değerleriyle birlikte girer
  (ADR-0021; ek iş yok). Hesap silmede etkinlik içeriğinin bugünkü kuralı
  geçerlidir.
- Log, metrik, olay ve denetim kaydına alan değeri yazılmaz (bugünkü kural).
- Aydınlatma metnine yeni veri kategorisi eklenmez.
- **Genç kullanıcı:** düzey seçimi (ör. LGS) etkinliğin hangi yaş grubuna
  hitap ettiğini ima edebilir. Bu bilgi zaten tayfanın ve türün adında
  olabilir; alan yeni bir görünürlük açmaz (değer yalnızca etkinliği
  görebilene döner). Deneme netinin hassasiyeti (`sensitive`) değişmez;
  ders etiketi neti kimseye açmaz.

### 6. Eğitim türleri (migration 00068)

Dört Eğitim türü **sürüm 2** alır (sürüm 1 satırları değişmez; o sürümle
açılmış etkinlikler onunla kalır, ADR-0025 §2). Bileşenler sürüm 1 ile
aynıdır, yalnızca alan eklenir:

| Tür | Alan | Tanım |
|-----|------|-------|
| `study_group` | `subjects` "Dersler" | ders ya da konu, en fazla 5, isteğe bağlı |
| `problem_session` | `subjects` "Dersler" | ders ya da konu, en fazla 5, isteğe bağlı |
| `peer_teaching` | `subject` "Ders ve konu" | ders ya da konu, tek seçim, **zorunlu** |
| `mock_exam` | `exam` "Sınav / ders" | düzey ya da ders (`min_depth: level`, `max_depth: subject`), tek seçim, isteğe bağlı |

`catalog_meta.catalog_version` bir artar; `catalog.changed`'i
`catalog-publish` işi yayınlar (ADR-0042 §5).

## Seçenekler

Platform sahibinin "ders/konu seçimi eklenecek" kararının açık bıraktığı
yerler; seçilen her biri **kalın**dır.

1. **Alan türünün biçimi.**
   - (a) **Tek tür `study_tags`, derinlik ve sayı tanımda.** Dört türün
     dört farklı ihtiyacını (çoklu ders; tek ders+konu; düzey ya da ders)
     tek türle karşılar; istemcide tek seçici.
   - (b) `choice` / `multi_choice` gibi iki tür (`study_tag`,
     `study_tag_list`). Daha çok tür, aynı doğrulama iki kez.
   - (c) Ders ve konu için iki ayrı alan (`subject` + `topic`). Konunun
     derse bağlılığı alanlar arası kural ister; tanım bildirimsel kalmaz
     (ADR-0037 "koşul yok" ilkesi).
2. **Değerin biçimi.**
   - (a) **En derin etiketin anahtarı (liste).** Tek anahtar yolu
     belirler; değer jsonb'de `multi_choice` gibi metin dizisidir.
   - (b) Modüllerdeki gibi `{"subject_key", "topic_key"}` nesnesi. Düzey
     seçimini ifade etmez; değer nesne olur ve `EventFieldValues`'ın
     bugünkü "metin dizisi" biçiminden çıkar. İstemci seçici çıktısını
     (`DersKonuSecimi{ders, konu?}`) `konu ?? ders` ile (a)'ya çevirir.
3. **"Dersler" (`problem_session`) tek mi çoklu mu?** Notion yalnızca
   `study_group` için "(çoklu)" yazar ama `problem_session` için de çoğul
   "dersler" der. **Çoklu (en fazla 5)** seçildi: soru çözüm oturumu birden
   çok derse yayılabilir; tek seçim istenirse türün yeni sürümüyle
   `max_selected: 1` yapılır.
4. **"Ders/sınav türü" (`mock_exam`).** "Sınav türü" listedeki **düzeydir**
   (LGS, TYT, AYT…); deneme bütün bir sınav ya da tek ders olabilir. **Tek
   seçim, düzey ya da ders** (`min_depth: level`, `max_depth: subject`)
   seçildi; konu denemesi istenirse `max_depth: topic` yapılır.
5. **Zorunluluk.** Notion söylemez. **Yalnızca `peer_teaching` zorunlu**
   (konu anlatımının konusu etkinliğin kendisidir, `gaming`'in oyunu ve
   `sports`'un dalı gibi); diğerlerinde isteğe bağlı, çünkü etüt ve soru
   oturumu her dersi kapsayabilir ve Şimdi başlat (ADR-0038) tek ekranda
   alan istemez.
6. **Emekli etiket düzenlemede.** **Kalır** (ADR-0040 §5 ve modüllerdeki
   kuralla aynı); reddetmek, etiket emekliye ayrılınca eski etkinliğin
   başka bir alanını düzenlemeyi imkânsız kılardı.
7. **`log` kayıt alanında kullanım.** **Kapalı.** Kayıt alanları
   veritabanına bakmadan doğrulanıyor; açmak `logbook`'a liste denetimi
   eklemeyi gerektirir ve bugün ihtiyaç yok.
8. **Down migration.** Sürüm geri gidemez (00067 tetikleyicisi). Down,
   yalnızca kendi transaction'ında bu tetikleyiciyi kapatıp dört türü
   sürüm 1'e döndürür, ikinci sürüm satırlarını siler ve tetikleyiciyi
   yeniden açar. Sürüm 2 ile açılmış etkinlik varsa foreign key'de durur
   (00066'daki geri alma planıyla aynı).

## Gerekçe

- **Tek tür, parametreli:** Bileşen parametrelerinde olduğu gibi farklı
  kullanımlar koşul eklemeden tanımdaki sabit değerlerle ifade edilir
  (ADR-0037 §2 ilkesi).
- **Değer yalnızca anahtar:** "Tutmadığın veri çalınamaz" (ADR-0011);
  etiket adı değişince eski etkinlik yeni adla görünür, yeniden yazma
  gerekmez.
- **Liste tanıma girmez:** Ders listesi platform panelinden değişir (#262);
  her değişiklikte türlerin yeni sürüm alması gereksiz olurdu.
- **Doğrulama yazma transaction'ında:** Ele geçirilmiş ya da eski istemci
  emekli ya da uydurma etiket yazamaz; denetim sunucudadır.

## Sonuçlar

- `definition`: `KindStudyTags`, `Field.MinDepth` / `MaxDepth`,
  `definition.StudyKeys`; `catalog.CheckStudyValues`; event paketinde
  oluşturma, düzenleme ve seri düzenlemesi bu denetimi çağırır.
- OpenAPI `CatalogField` / `EventFieldValues` açıklamaları; `event.proto`,
  `admin.proto` yorumları.
- Migration 00068: dört Eğitim türünün sürüm 2'si; `catalog_version` +1.
- Tayfam-App: `KatalogAlanlari`'na `study_tags` alanı, seçici ve gösterim
  (Tayfam-App #307).
- Notion: Eğitim türleri tablosu ve §F.1 "Açık" maddesi (1) bu kararla
  güncellenmelidir (platform sahibi / ana oturum).

## Değerlendirilen alternatifler

- **Ders listesini `choice` seçenekleri olarak tanıma kopyalamak:**
  294 etiket seçenek sınırını (50) ve tanımın 16 KiB sınırını aşar, panel değişikliğinde bütün
  türlerin yeni sürüm alması gerekir, emekli etiket kuralı kurulamaz.
- **Ders/konu seçimini bir bileşen yapmak:** Bileşen kendi tablosu ve
  uçları olan bir işlevdir (ADR-0025 §4); ders seçimi etkinliğin bir
  özelliğidir, alan değerleriyle birlikte yazılır ve okunur.
- **`event_field_values` yerine ayrı bağ tablosu (foreign key ile):**
  Etiketler silinmediği için foreign key yeni bir güvence vermez; tek alan
  türü için ikinci saklama yolu açardı. Etiketle süzme gerekirse (bugün
  kapsam dışı) jsonb dizini ya da bağ tablosu o zaman eklenir.
