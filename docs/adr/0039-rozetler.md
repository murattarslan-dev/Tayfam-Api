# ADR-0039: Rozetler (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Karar veren:** Murat Arslan (2026-11-18). Ürün kararları Notion "Faz 3 —
  Katalog ayrıntıları ve şema ekleri (2026-11-18)" §E.2.
- **İlgili:** [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0019](0019-push-bildirimleri.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md),
  [ADR-0026](0026-tayfa-istatistikleri-ve-vitrin.md) (§6'daki "ayrı ADR"),
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md)

## Bağlam

ADR-0026 rozetleri `tayfa_stats`, `user_stats` ve `celebration`
bileşeninden besleneceğini söyleyip kurallarını ayrı ADR'ye bıraktı.
Platform sahibinin kararları (2026-11-18): rozetler v3'te; katalog gibi
veri; kişisel, tayfa ve şampiyonluk rozetleri; sıralama yok; hassas
verilerden rozet çıkmaz; rozet geri alınmaz; push yok.

## Karar

### 1. Rozet tanımı veridir

`catalog_badges` (core): `key` (değişmez), `scope` (`user` / `tayfa`),
`labels`, `icon` (istemcinin sabit ikon setinden), `rule` (jsonb), `status`
(`active` / `retired`). Kural kapalı bir şemadır, Go'da doğrulanır:

| `rule.kind` | Anlam | Parametreler |
|-------------|-------|--------------|
| `threshold` | Bir metriğin toplamı eşiği geçti | `metric` (ADR-0026 metrik anahtarı ya da ortak metrik), `value`, `period` (`all_time` / `year`) |
| `first` | Bir türde ya da genel ilk katılım | `event_type` (isteğe bağlı) |
| `champion` | `celebration` bileşeni bu kişiyi/takımı kazanan ilan etti | `event_type` (isteğe bağlı) |

Yeni rozet kod istemez; yeni `rule.kind` ister. İlk liste katalog verisiyle
birlikte gelir (platform sahibi onaylar). Katalog yönetimiyle aynı panel
ekranında yönetilir, aynı denetim kaydına yazılır.

### 2. Kazanma

- `user_badges` (`user_id`, `badge_key`, `period_key`, `tayfa_id` NULL ya da
  şampiyonlukta etkinliğin tayfası, `event_id` NULL ya da şampiyonlukta
  etkinlik, `earned_at`) ve `tayfa_badges` (`tayfa_id`, `badge_key`,
  `period_key`, `earned_at`). Tekillik: rozet + kişi/tayfa + dönem
  (+ şampiyonlukta etkinlik).
- `threshold` ve `first` rozetleri ADR-0026'nın istatistik işinin sonunda,
  aynı iş içinde ve yalnızca donmuş veriden hesaplanır. `champion` rozeti
  kutlama donunca (etkinlik bitiminden 7 gün sonra) verilir; düzeltme
  penceresinde değişen kazanan rozeti oynatmaz.
- **Rozet geri alınmaz:** etkinlik sonradan silinip istatistik düşse de
  verilmiş rozet kalır. Rozet tanımı `retired` olsa da kazanılmışlar
  gösterilir.
- **Çıkmayan rozet:** `sensitive: true` ölçümler (deneme neti) ve
  `sensitive_attendance` türleri (kan bağışı) hiçbir rozet kuralında metrik
  olamaz; katalog doğrulaması reddeder. Misafir oyuncu rozet almaz.

### 3. Görünürlük

- **Kişisel rozet** kişisel istatistikle aynı ayara tabidir
  (`stats_visibility`, ADR-0026 §4; varsayılan kapalı). Kişi kendi
  rozetlerini her zaman görür. Şampiyonluk rozeti, kişinin ayarından
  bağımsız olarak **o tayfanın içinde** şampiyonlar bileşeninde görünür
  (kutlama zaten tayfa içinde görünür bir olaydır).
- **Tayfa rozeti** tayfa üyelerine görünür; Keşfet vitrininde yalnızca
  `stats_showcase` açıksa ve vitrin eşiği (ADR-0026) geçilmişse.
- Sıralama, "en çok rozet" listesi ya da rozet sayısına göre sıralama
  yoktur.

### 4. Bildirim

Yeni bildirim türü `badge_earned` yalnızca **uygulama içi**dir; push
gönderilmez (ADR-0030 §8'deki öneri bildirimiyle aynı yaklaşım). Tayfa
rozetinde bildirim yöneticilere gider.

### 5. KVKK

Kişisel rozetler dışa aktarmada (ADR-0021) kişinin istatistikleriyle gelir.
Hesap silinince `user_badges` aynı transaction'da silinir.

## Gerekçe

- **Veri olarak rozet:** katalogla aynı model; platform sahibi sürüm
  çıkarmadan rozet ekler.
- **Geri alınmaz:** kazanılmış bir şeyin kaybolması kullanıcıya ceza gibi
  gelir; donmuş veriden hesaplandığı için geri alma ihtiyacı da nadirdir.
- **Hassas veriden rozet yok:** "kan bağışçısı" rozeti sağlık verisini
  görünür kılardı.

## Sonuçlar

- Tablolar (core): `catalog_badges`, `user_badges`, `tayfa_badges`.
- OpenAPI: profil ve tayfa yanıtlarında `badges`; Keşfet vitrininde tayfa
  rozetleri; panel uçları.
- chat `notifications.type`'a `badge_earned`.

## Değerlendirilen alternatifler

- **Rozet kurallarını kodda tutmak:** Reddedildi; her rozet sürüm isterdi.
- **Rozetleri v3 dışında bırakmak:** Platform sahibi v3'te istedi.
