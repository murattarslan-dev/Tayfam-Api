# ADR-0027: Tayfa ana sayfa bileşenleri ve günün içeriği (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-15)
- **Tarih:** 2026-11-15
- **Revizyon:** 2026-11-18 ([ADR-0040](0040-tayfa-modulleri-v3.md)): dört
  modül bileşeninin (ortak kütüphane, soru havuzu, materyal kütüphanesi,
  üye garajı) veri modeli ve ders-konu listesi.
- **Karar veren:** Ürün kararları platform sahibinindir (2026-11-15, Notion
  "Faz 3 (v3)" §5a ve §4.15). Bu ADR onların mimariye taşınmasıdır.
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0012](0012-dogum-tarihi-ve-kotuye-kullanim.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0026](0026-tayfa-istatistikleri-ve-vitrin.md); Tayfam-App ADR-0024

## Bağlam

Tayfa ana sayfası bugün sabit bölümlerden oluşur (Sıradaki, Yaklaşan,
Duyurular, Açık anket, Son sorular; Tayfam-App ADR-0015). Kapalı modülün
bölümü görünmez; modüller `tayfa_modules`'ta açılıp kapanır.

v3'ün üçüncü derinliği ilgi alanına özel ana sayfadır. Platform sahibinin
kararları (2026-11-15):

- **Her tayfa her bileşeni kullanabilir**, ilgi alanı olmasa bile. İlgi
  alanı yalnızca hangi bileşenlerin **varsayılan açık** geleceğini ve
  listede öne çıkacağını belirler.
- **Yönetici düzenler:** bileşenleri açıp kapatır ve sıralar. "Sıradaki"
  etkinlik kartı her zaman sabittir.
- **Dış içerik yok:** Tayfam hazır içerik (ayet, söz, şiir) sunmaz.
- **Günün içeriği:** Tayfanın kendi havuzu; yönetici ve moderatör ekler;
  havuz sürükle-bırakla sıralanır; bir içerik belirli bir güne sabitlenebilir
  ("her yıl tekrarla" seçeneğiyle); aynı güne iki sabitleme varsa sırada önce
  gelen gösterilir.
- İlk liste: istatistik kartı, yıllık hedef, geri sayım, günün içeriği,
  şampiyonlar, ortak kütüphane, soru havuzu, materyal kütüphanesi, üye
  garajı, günlük özeti.
- Eğitim: ders ve konu etiket listesini Tayfam sağlar (platform panelinden),
  içeriği kullanıcılar ekler; ilk sürümde dosya (PDF) yüklenmez; soru
  havuzunda "Çözüldü" işareti yoktur.

## Karar

### 1. İki tür bileşen

- **Gösterim bileşeni:** Başka bir yerde zaten olan veriyi gösterir; kendi
  verisi yok ya da çok az. İstatistik kartı (ADR-0026), yıllık hedef,
  geri sayım, şampiyonlar, günlük özeti, günün içeriği.
- **Modül bileşeni:** Kendi verisi ve uçları olan bir tayfa modülüdür
  (ortak kütüphane, soru havuzu, materyal kütüphanesi, üye garajı).
  `tayfa_modules`'a yeni modül olarak eklenir; modül kapalıysa uçları
  isteği reddeder (ADR-0004, bugünkü kural). Ana sayfadaki kartı, modülün
  vitrinidir.

Her modül bileşeni kendi ADR'siyle gelir (veri modeli, yetki, KVKK). Bu ADR
yalnızca çerçeveyi ve günün içeriğini tanımlar.

### 2. Bileşen listesi koddadır

Bileşenler (gösterim ve modül) kapalı bir listedir ve core'da sabittir:
anahtar, tür, onu tanıyan en düşük istemci sürümü, varsayılan açık olduğu
ilgi alanları. Varsayılanlar ürün kararıyla değişebileceği için ilgi alanı →
varsayılan bileşen eşlemesi **katalogdadır** (`catalog_interests`'e
`default_widgets`; ADR-0025 §2), bileşen listesinin kendisi koddadır.

### 3. Düzen

- `tayfa_home_widgets` (core): `tayfa_id`, `widget_key`, `enabled`,
  `position`, `config` (jsonb, bileşene göre kapalı şema), `updated_by`,
  `updated_at`. Birincil anahtar `(tayfa_id, widget_key)`: aynı bileşen bir
  tayfada bir kez.
- **Sıradaki** kartı tabloda yoktur; istemci her zaman en üstte gösterir.
  Bugünkü sabit bölümler (Yaklaşan, Duyurular, Açık anket, Son sorular) bu
  tabloya taşınır; böylece onlar da sıralanabilir. Modülü kapalı olan
  bölümün satırı `enabled` olsa da gösterilmez.
- **Varsayılanlar:** Tayfa oluşturulurken ve bir ilgi alanı **eklendiğinde**,
  o ilgi alanının varsayılan bileşenleri için satır yoksa `enabled = true`
  satır eklenir. Var olan satıra dokunulmaz: yöneticinin kapattığı bileşen,
  ilgi alanı değişince kendiliğinden açılmaz. İlgi alanı kaldırılınca hiçbir
  bileşen kapanmaz.
- **Yetki:** Düzeni yalnızca yönetici değiştirir
  (`tayfa.home.update`); okuma tayfa üyeliği ister. Değişiklik denetim
  kaydına yazılır.
- **Sözleşme:** `GET /v1/tayfas/{id}` yanıtına `home_widgets` (anahtar,
  sıra, `config`; yalnızca `enabled`) eklenir. Düzenleme
  `PUT /v1/tayfas/{id}/home-widgets` ile bütün liste olarak yapılır
  (sıra ve açık/kapalı tek istekte; kısmi güncelleme yok).
- **Eski istemci** tanımadığı bileşen anahtarını atlar (ADR-0025 §9'daki
  "güncelle" kartı ana sayfada gösterilmez; ana sayfa sade kalır). Ayarlar
  ekranında tanımadığı bileşen "Uygulamayı güncelle" etiketiyle listelenir.

### 4. Gösterim bileşenlerinin ayarları (`config`)

| Bileşen | `config` | Not |
|---------|----------|-----|
| `stats` | gösterilecek metrikler (en fazla 4), yıl | ADR-0026; tayfa içinde eşik yok |
| `goal` | metrik, hedef değer, yıl | Ölçümden dolar; manuel artırma yok |
| `countdown` | başlık (en fazla 60), hedef tarih | Tarih geçince kart kendiliğinden gizlenir |
| `daily` | — | Havuz ayrı tabloda (§5) |
| `champions` | kaynak türler, dönem (son 30 gün / bu yıl) | `celebration` ve `scoreboard`'dan |
| `log_summary` | kaynak `log` bileşeni, dönem | Örn. "Bu yıl görülen 84 kuş türü" |

Serbest metin (`countdown` başlığı) kötüye kullanım filtresinden geçer
(ADR-0012).

### 5. Günün içeriği

- `tayfa_daily_items` (core): `id`, `tayfa_id`, `body` (1–500 karakter),
  `attribution` (isteğe bağlı, en fazla 120; "Atatürk", "Yunus Emre"…),
  `position`, `pin_month`, `pin_day`, `pin_year` (NULL: her yıl),
  `created_by`, soft delete alanları.
- **Seçim:** Gün, Europe/Istanbul saat dilimine göre belirlenir (uygulama
  yalnızca Türkçe ve Türkiye odaklı; tayfa başına saat dilimi ayrı karar).
  O gün için sabitleme varsa (`pin_year` o yıl ya da NULL), sabitlenenler
  arasında `position`'ı en küçük olan gösterilir. Yoksa sabitlenmemiş
  içerikler arasından **sıradaki**: tayfa başına tutulan
  `tayfa_daily_state.next_position` gösterilir ve gün değişince ilerler;
  sona gelince başa döner. Sabitlenmiş içerik sıradaki dönüşe girmez.
- Seçim istek anında, sunucuda yapılır ve gün boyu aynıdır (aynı gün içinde
  durum ilerlemez). Hesaplama tembeldir: ilk istekte o günün seçimi yazılır;
  arka plan işi gerekmez. Kimsenin açmadığı günlerde sıra ilerlemez; sıra
  her gösterim gününde bir adım ilerler, böylece havuzdaki içerik kaçmaz.
- **Yetki:** Ekleme, düzenleme, silme, sıralama ve sabitleme yönetici ve
  moderatör (`tayfa.daily.manage`). Okuma tayfa üyeliği. Moderatör
  silmeleri bugünkü kuralla 30 gün geri alınabilir.
- **Çakışma uyarısı:** Aynı güne ikinci sabitleme reddedilmez; yanıt
  `pin_conflict: true` döner, istemci uyarır.
- **Kötüye kullanım:** `body` ve `attribution` filtreden geçer; içerik
  şikâyet edilebilir (yeni hedef türü `daily_item`; tayfa moderasyonuna
  gider).
- **Sınır:** Tayfa başına en fazla 1000 içerik.
- Tayfam hiçbir zaman havuza içerik eklemez (dış içerik yok kararı).

### 6. Eğitim etiket listesi

Ders ve konu etiketleri (Matematik → Türev…) katalogdadır
(`catalog_subjects`, `catalog_topics`; ADR-0025 §2 kuralları: anahtar
değişmez, silinmez, `retired` olur, metinler `labels`'ta). Soru havuzu ve
materyal kütüphanesi ADR'leri bu etiketlere bağlanır. Etiket listesi platform
panelinden yönetilir.

## Gerekçe

- **Gösterim/modül ayrımı:** Modül bileşenlerinin verisi, yetkisi ve KVKK
  yükü ayrı ADR'yi hak ediyor; çerçeve onları beklemeden kurulabilir.
- **Bileşen listesi kodda, varsayılanlar katalogda:** Bileşen kod ister;
  ama "Kitap seçen tayfaya ortak kütüphane açık gelsin" bir ürün ayarıdır,
  sürüm beklememelidir.
- **Varsayılanları yeniden açmamak:** Yöneticinin kapattığı şeyin ilgi alanı
  değişince geri gelmesi sürpriz olurdu.
- **Tembel ve sunucuda seçim:** Tüm üyeler aynı gün aynı içeriği görür;
  istemci saatine güvenilmez; her tayfa için gece çalışan bir işe gerek
  kalmaz.
- **Dış içerik yok:** Platform sahibi içerik sağlayıcı olmaz; kaynak, lisans
  ve inançlar arası tarafsızlık sorumluluğu doğmaz.

## Sonuçlar

- `tayfa_modules` CHECK listesi yeni modüllerle genişler (her modül kendi
  ADR'sinde).
- Bugünkü sabit ana sayfa bölümleri düzen tablosuna taşınır; istemci
  sıralamayı sunucudan alır.
- Yeni şikâyet hedef türü (`daily_item`) ve izin tablosu işlemleri eklenir.
- Aydınlatma metninde yeni bir kişisel veri kategorisi yok (günün içeriği
  tayfa içeriğidir; yazar bilgisi diğer içeriklerdeki gibi).

## Değerlendirilen alternatifler

- **Ana sayfayı tamamen otomatik kurmak:** Platform sahibi yönetici
  düzenlemesini seçti (B).
- **Tayfam'ın lisanslı içerik sunması:** Kaynak, lisans ve tarafsızlık yükü;
  reddedildi.
- **Günün içeriğini istemcide seçmek:** Cihaz saatine ve yerel duruma bağlı
  olurdu; üyeler farklı içerik görebilirdi.
- **Rastgele seçim:** Platform sahibi sıralı havuz ve tarihe sabitleme
  istedi; rastgelelik özel günleri garanti edemez.
