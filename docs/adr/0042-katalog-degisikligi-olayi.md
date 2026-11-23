# ADR-0042: Katalog değişikliği olayı (`catalog.changed`) ve identity'deki kopya

- **Durum:** Kabul edildi (platform sahibi, 2026-11-23, sohbette; issue #280)
- **Tarih:** 2026-11-23
- **Karar veren:** Murat Arslan (2026-11-23, issue #280 yorumu: seçenek b)
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0013](0013-servis-kimligi.md),
  [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md)
- **Değiştirdiği:** ADR-0025 §10 ("`catalog.changed`, yalnızca sürüm
  numarası"): olay sürümle birlikte ilgi alanı anahtarlarını ve durumlarını
  taşır. ADR-0002 ve ADR-0009'a core → identity olay yönü işlendi.

## Bağlam

İlgi alanları core'daki kataloğun kaydıdır (ADR-0025 §2). identity profil
ilgi alanlarını (`profile_details.interests`) kataloğun kendi veritabanındaki
salt okunur kopyasına karşı denetler: `catalog_copy` (yansıtılan katalog
sürümü) ve `catalog_interests` (anahtar ve durum; issue #237, PR #279).

ADR-0025 §10 kopyanın core'un `catalog.changed` olayıyla, olayın yalnızca
sürüm numarası taşıyacağı biçimde güncelleneceğini yazıyordu. Bu üç soruyu
açık bırakıyordu (issue #280):

1. **Taşıma:** core → identity olay yönü ADR-0002/0009'da yoktu.
2. **İçerik:** yalnızca sürüm taşıyan olayda identity içeriği core'dan
   okumak zorundaydı; olay işlenirken senkron okumak ADR-0002'nin döngüsel
   çağrı yasağına takılıyordu (core → identity teslimi sırasında identity →
   core çağrısı).
3. **Yayın:** olayı yayınlayacak bir yazma yolu yoktu.

Karara kadar geçici kural: ilgi alanı ekleyen ya da emekliye ayıran her core
migration'ı, aynı değişikliği yapan bir identity migration'ıyla gelecekti.

## Karar

### 1. Taşıma: core'un outbox'ı, ayrı tüketici

`catalog.changed` core'un `domain_events` tablosuna `outbox.Write` ile
yazılır ve core'un relay'i onu identity'nin yeni
`CatalogCopyService.ApplyCatalogChange` metoduna teslim eder (ADR-0002
outbox deseni). Tüketicinin adı `identity.catalog`'dur; panel denetim
kayıtlarını taşıyan `identity` tüketicisinden ayrıdır, böylece biri
takılırsa öteki beklemez.

- Metodu yalnızca `tayfam-core` ServiceAccount'unun servis token'ı çağırabilir
  (ADR-0013; `PlatformAuditService` ile aynı politika). Kullanıcı token'ı
  UNAUTHENTICATED, başka servis PERMISSION_DENIED alır.
- Ek senkron çağrı yoktur: identity teslim sırasında core'a hiçbir şey
  sormaz.

### 2. İçerik: sürüm + ilgi alanı anahtarları ve durumları

Yük (payload):

```json
{"catalog_version": 7,
 "interests": [{"key": "automotive", "status": "active"},
               {"key": "dance", "status": "retired"}]}
```

- `interests` kataloğun **tam** ilgi alanı listesidir (emekliler dahil);
  yalnızca anahtar ve durum (`active` / `retired`). Ad, ikon, sıra ve
  varsayılan bileşenler kopyaya girmez; istemci onları `GET /v1/catalog`'tan
  alır. Kişisel veri yoktur.
- Olay konusu ve aktörü yoktur (kataloğun bütünü hakkındadır).
- Yük 8 KiB sınırına tabidir (`domain_events_payload`); bir ilgi alanı en
  fazla ~60 bayt tuttuğu için bu ~130 ilgi alanı demektir. Katalog buna
  yaklaşırsa (bugün 16) bu ADR yeniden ele alınır. identity tek istekte en
  fazla 200 ilgi alanı kabul eder.
- Yayınlayan, sürümü ilgi alanlarından **önce** okur. Böylece aradaki bir
  değişiklik olsa bile olayın içeriği sürümünden eski olamaz (en kötü
  ihtimalle eski sürüm numarasıyla yeni içerik gider; yeni sürümün olayı
  ardından gelir).

### 3. identity'de uygulama: tek adım, idempotent, yalnızca ileri

identity olayı tek transaction'da uygular:

1. `catalog_copy.catalog_version` yalnızca **büyürse** güncellenir. Olayın
   sürümü kopyanınkinden küçük ya da ona eşitse olay hiçbir şey değiştirmez
   ve başarılı sayılır (tekrar gelen ya da sırası karışmış olay; relay en az
   bir kez teslim eder).
2. Listede olan her anahtar eklenir ya da durumu güncellenir.
3. Kopyada olup listede olmayan anahtar **silinmez**, emekliye alınır: eski
   profiller onu göstermeye devam eder (core da ilgi alanını silmez, emekliye
   ayırır; ADR-0025 §2).

Girdi her katmanda yeniden doğrulanır (ADR-0011): sürüm ≥ 1, anahtar biçimi
`^[a-z][a-z0-9_]{1,39}$`, durum `active`/`retired`, tekrar eden anahtar yok,
1–200 ilgi alanı. Geçersiz istek INVALID_ARGUMENT alır; relay onu tekrar
dener ve olay ilerlemez (bozuk bir yük kopyayı kısmen değiştirmez).

### 4. Yetki (ADR-0015, en az yetki)

`identity_app` kopyaya yalnızca gerekeni yazabilir:

| Tablo | Yetki |
|-------|-------|
| `catalog_copy` | `SELECT`, `UPDATE (catalog_version, updated_at)` |
| `catalog_interests` | `SELECT`, `INSERT`, `UPDATE (status)` |

Silme ve anahtar değiştirme yetkisi yoktur. `catalog_copy_forward_only`
tetikleyicisi sürümün geri gitmesini veritabanında da reddeder (servis ele
geçirilse bile kopya eski bir sürüme döndürülemez). Ele geçirilmiş bir
identity'nin yapabileceği en kötü şey kopyaya anlamsız bir ilgi alanı eklemek
ya da bir ilgi alanını emekliye almaktır; profil verisine ve core'a etkisi
yoktur.

### 5. Yayın: yazma yolunda ve katalog yayıncısı işiyle

Olayı iki yol yayınlar:

- **Kataloğu değiştiren yazma yolu** (platform paneli, #262): değişikliği
  ve `catalog_meta.catalog_version` artışını yaptığı transaction'ın son adımı
  olarak `catalog.PublishChanged` çağırır.
- **Katalog yayıncısı işi** (`catalog-publish`, core arka plan işi, 5
  dakikada bir ve açılışta): `catalog_meta.catalog_version` son yayınlanan
  `catalog.changed`'in sürümünden büyükse olayı yayınlar. Katalog
  migration'ları (#260) yalnızca kataloğu ve sürümü değiştirir (bugün de
  yaptıkları gibi); olayı bu iş yayınlar. Migration'lar outbox'ın sıra
  kuralını (`outbox.Write`'ın kilidi ve artan kimliği) SQL'de tekrarlamak
  zorunda kalmaz.

İş, paneli unutan bir yazma yolunu da yakalar. Aynı sürüm iki kez
yayınlanabilir (panel ve iş, iki pod); identity eşit sürümü yok saydığı için
zararsızdır. Bu iş ilk çalıştığında kopya (sürüm 1) core'un güncel sürümüne
gelir.

### 6. Geçici kuralın kalkması

"core migration + eşlenik identity migration" kuralı bu karar birleşince
kalkar: kataloğu değiştiren core migration'ı yalnızca `catalog_version`'ı
artırır, identity migration'ı gerekmez.

## Gerekçe

- **İçeriği olayda taşımak** döngüsel çağrıyı ve identity'nin core'a bağımlı
  olmasını önler; identity kopyayı yalnızca kendi verisiyle günceller. Yük
  küçüktür (anahtar + durum) ve kişisel veri içermez.
- **Yalnızca ileri giden sürüm** olayları idempotent ve sıradan bağımsız
  yapar; outbox'ın en az bir kez teslimi için yeterlidir.
- **Tam liste** kopyanın core'dan sapmasını tek olayla düzeltir; artımlı
  (yalnızca değişen) yükte kaybolan ya da sırası karışan bir olay kopyayı
  kalıcı olarak saptırırdı.
- **Yayıncı işi** migration'ların outbox'a doğrudan yazmasından güvenlidir:
  olay kimliklerinin sırası (ADR-0002) tek yerde, Go'da korunur.

## Sonuçlar

- Yeni gRPC servisi `tayfam.identity.v1.CatalogCopyService`
  (`proto/tayfam/identity/v1/catalog_copy.proto`); REST sözleşmesi değişmez.
- core'da `catalog.PublishChanged` / `catalog.PublishDue`, `catalog-publish`
  işi ve `catalogrelay` tüketicisi; identity'de `catalogcopy` paketi ve
  migration 00016 (yetkiler, tetikleyici).
- Kopya kısa süre (en fazla bir yayıncı turu + relay gecikmesi) eski
  kalabilir: bu arada yeni bir ilgi alanı tayfada seçilebilir ama profilde
  `validation` döner. Panel yayını transaction içinde yaptığında gecikme
  yalnızca relay'inkidir.

## Değerlendirilen alternatifler

- **(a) Yalnızca sürüm; identity içeriği core'dan okur:** teslim sırasında
  identity → core senkron çağrısı (döngü) ya da ayrı bir okuma işi gerekirdi;
  reddedildi.
- **(c) Kopyayı kaldırıp profil ilgi alanlarını core'a sormak:** her profil
  kaydında servisler arası çağrı ve core'a bağımlılık; reddedildi.
- **Migration'ların olayı SQL ile yazması:** outbox'ın sıra kilidini ve
  kimlik üretimini SQL'de tekrarlamak gerekirdi; yayıncı işi seçildi.
