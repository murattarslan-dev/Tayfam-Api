# ADR-0038: Şimdi başlat — doğaçlama etkinlik, QR ile katılım ve şimdi bildirimi (v3)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-18, sohbette)
- **Tarih:** 2026-11-18
- **Karar veren:** Murat Arslan (2026-11-18 sohbeti). Ürün kararları Notion
  "Faz 3 — Katalog ayrıntıları ve şema ekleri (2026-11-18)" §D'dedir.
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md),
  [ADR-0008](0008-kvkk-ve-platform-yonetimi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0019](0019-push-bildirimleri.md),
  [ADR-0029](0029-bosum-bos-vakit.md),
  [ADR-0030](0030-arkadaslik-puani-ve-arkadas-onerisi.md),
  [ADR-0032](0032-anlik-etkinlik.md),
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md); Tayfam-App
  ADR-0032
- **Değiştirdiği:** ADR-0032 §2 (anlık etkinlik başlangıcı "şimdi" olabilir),
  §4 (anlık tayfada QR ile onaysız katılım). ADR-0032'ye revizyon notu
  düşüldü.

## Bağlam

Anlık etkinlik (ADR-0032) gerçekte planlıdır: altı adımlı sihirbaz,
başlangıç gelecekte olmalı (`cleanTime`: `start.After(now)`; istemci en
erken bir sonraki tam saat + 1 önerir), bitiş zorunlu, yanındaki kişi ancak
arkadaşsa ya da linkle istek bırakıp onay alırsa katılır. v3 bileşenleri
(yazboz, kronometre, takım kurma) en çok doğaçlama anlarda gerekir: "masa
kurduk, batak oynayacağız". Platform sahibinin kararları (2026-11-18):

- Tek ekranlı **Şimdi başlat**: hem tayfa içinde hem tayfa dışında (anlık).
- Tayfa etkinliğinde **tayfa üyelerine**, tayfa dışı etkinlikte
  **arkadaşlara** bildirim.
- Yüz yüze **QR ile katılım**; misafir oyuncu (ADR-0037 §6).
- Tayfada Şimdi başlat için ayrı ayar, varsayılan "üyeler".
- 18 yaş altına ek kısıt yok.

## Karar

### 1. Etkinlik: `starts_now`

- Oluşturma isteğinde `starts_now: true` verilirse `start_time` ve
  `end_time` gönderilmez. Sunucu `starts_at = now` (sunucu saati), `ends_at
  = now + 12 saat` yazar ve `events.open_ended = true` işaretler.
  `cleanTime`'daki "başlangıç gelecekte" kuralı yalnızca bu yol için
  kalkar; istemci saatine güvenilmez.
- Etkinlik hemen **sürüyor** durumundadır: check-in, bileşenler ve albüm
  açıktır.
- **Bitir:** organizatör `POST …/events/{id}/finish` ile `ends_at = now`
  yazar, `open_ended` kalkar. Bitirilmeyen etkinlik 12 saatte kendiliğinden
  biter (iş gerekmez; `ends_at` zaten yazılı). 7 günlük düzeltme penceresi
  (ADR-0025 §5) `ends_at`'tan sayılır.
- `starts_now` ile `recurrence` ve `discoverable` kabul edilmez.
- Yer: yüz yüzede buluşma noktası bugünkü gibi zorunludur. İstemci yer
  seçiciyi cihaz konumuyla başlatır, kullanıcı onaylar; gönderilen nokta
  kullanıcının bilerek paylaştığı içeriktir (ADR-0008 ayrımı). Cihaz
  konumu ayrıca gönderilmez, saklanmaz. Online şimdi etkinliği bağlantıyla
  açılır.
- Başlık boşsa sunucu başlık üretmez (API metin döndürmez); istemci
  çeviri dosyasından üretip gönderir.

### 2. Kim başlatabilir

- **Tayfa içinde:** yeni ayar `tayfas.now_create_min_role` (`member`
  varsayılan / `moderator` / `admin`); izin tablosunda yeni işlem
  `EventCreateNow`, ayarla düşen rol (ADR-0004, varsayılan "hayır"
  korunur). Ayarı yalnızca yönetici değiştirir.
- Kişi başına aynı anda **en fazla 1 açık** şimdi etkinliği (tayfa içi ve
  anlık toplam). Anlık şimdi etkinliği ADR-0032'deki "3 açık anlık
  etkinlik" sınırına da sayılır.
- **Anlık (tayfa dışı):** ADR-0032'nin akışı aynen; `POST
  /v1/instant-events` `starts_now` kabul eder.

### 3. QR ile katılım

- Organizatör (ve tayfa etkinliğinde yöneticiler) `POST
  …/events/{id}/join-qr` ile kısa ömürlü bir **katılım token'ı** alır:
  rastgele 128 bit (`crypto/rand`), core'da yalnızca SHA-256 özeti
  saklanır, ömrü **2 dakika**; istemci süre dolmadan yenisini ister. QR
  yalnızca token'ı taşır (`tayfam.app/q#<token>`; token fragment'ta,
  davet linki kuralıyla aynı). Ekran görüntüsüyle paylaşılan QR birkaç
  dakikada geçersizdir.
- Okutan kullanıcı `POST /v1/join-qr` (gövdede token) ile katılır:
  - **Anlık tayfa:** onay beklemeden üye olur (`joined_via = 'qr'`) ve
    etkinliğe "geliyorum" ile yazılır. Fiziksel olarak organizatörün
    yanında olmak onay yerine geçer. ADR-0032'deki "linkle gelen her
    zaman istek bırakır" kuralına istisna yalnızca QR'dır.
  - **Kalıcı tayfa:** tayfanın katılım politikasına uyar: açık tayfada
    tayfaya ve etkinliğe katılır; onaylı tayfada katılım isteği bırakır
    (`pending`). Kapalı tayfanın sözü korunur. Zaten üye olan okutursa
    etkinliğe "geliyorum" ve check-in yapılır.
- Banlı, engelli (organizatörle iki yönden), silinmiş ya da platform banlı
  hesap katılamaz; cevap token geçersizmiş gibi `invalid-join-qr`
  (ayrım yok, ADR-0011).
- Etkinlik başına QR ile en fazla **30** katılım; kullanıcı başına dakikada
  5 deneme (gateway hız sınırı).
- `memberships_joined_via` CHECK'ine `qr` eklenir; tetikleyicideki kurallar
  `qr`'ı `code` gibi ele alır (davet kimliği yok).

### 4. Şimdi bildirimi

Yeni bildirim türü `now_started` (chat `notifications.type` CHECK'i ve
ADR-0019 tablosu). Alıcıları **core** belirler ve `notification.requested`
olayına yazar; chat teslim eder.

**Tayfa etkinliği → tayfa üyeleri**

- Alıcı: organizatör dışındaki bütün aktif üyeler.
- Kullanıcı tayfa başına "Şimdi bildirimleri"ni kapatabilir: chat'te
  `notification_preferences`'a yeni `scope_type = 'tayfa_now'` (seviye
  `all` / `none`). Tayfanın genel seviyesi `none` ise şimdi bildirimi de
  gitmez.
- **Sınır:** tayfa başına günde (İstanbul günü) en fazla **3** şimdi
  bildirimi. Sonraki şimdi etkinlikleri açılır ama bildirim üretmez;
  yalnızca tayfa akışında görünür. Sayaç core'da, etkinlik oluşturmayla
  aynı transaction'da.

**Anlık etkinlik → arkadaşlar**

- Alıcı listesini **organizatör seçer**: istemci listeyi önceden doldurur
  (önce bugün "Boşum" demiş arkadaşlar, ADR-0029; sonra arkadaşlık puanı
  sırası, ADR-0030 §12), organizatör düzenler. Sunucu her alıcının kabul
  edilmiş arkadaş olduğunu, iki yönden engel olmadığını ve hesabın canlı
  olduğunu yeniden doğrular (ADR-0032 §4 kuralı).
- Yayın başına en fazla **30** alıcı; kişi başına günde en fazla **2**
  şimdi yayını. Şimdi yayını, ADR-0032'deki günlük 10 doğrudan davet
  sınırından ayrı sayılır.
- Bildirim bir **davettir**: alıcı "Katıl" ile doğrudan etkin üye olur
  (davet eden yönetici olduğu için, bugünkü doğrudan davet davranışı).
- **Alıcı ayarı** (core, `/v1/me/social-settings`): `now_from_friends`
  = `all` (varsayılan) / `close` / `off`. `close`: yalnızca alıcının
  arkadaşlık puanı sıralamasında ilk 20'deki arkadaşlarından. Alıcının
  profillemesi kapalıysa (puan yok) `close`, `all` gibi davranır; istemci
  bu durumda seçeneği açıklamasıyla gösterir. `off` olan kullanıcı
  listede seçilebilir görünmez (istemci önceden doldururken atlar;
  sunucu yine de alıcıdan düşürür, ayarın varlığını göndericiye
  belli etmeden).

**İçerik (ADR-0019 korunur)**

- Push'ta ad, başlık, tür, semt ve konum **yoktur**; yalnızca
  `push_now_started_title/_body` çeviri anahtarları ("Şimdi bir şey
  başladı — bakmak ister misin?"). 2026-11-18 sohbetinde konuşulan
  "kilit ekranında yalnızca semt" önerisi ADR-0019'un "push'ta konum
  asla" kuralıyla çeliştiği için **uygulanmaz**; platform sahibine
  bildirildi.
- Uygulama içi bildirim listesi, açıldığında etkinliği bugünkü yetki
  kurallarıyla REST'ten çeker; tam konum yalnızca üyeye ve davetliye
  görünür.
- `now_started` sessize alınabilir (`hike_overdue` gibi zorunlu değil).

### 5. KVKK ve log

- QR token'ı, alıcı listesi ve konum log'a, metriğe ve olaya girmez;
  olaylar yalnızca kimlik taşır.
- QR token özetleri 1 gün sonra silinir. Şimdi bildirimi sayaçları 2 gün
  tutulur.
- Dışa aktarmada (ADR-0021) şimdi etkinlikleri diğer etkinlikler gibi
  gelir; `now_from_friends` ayarı hesap ayarlarıyla gelir.

## Gerekçe

- **Sunucu saati:** "şimdi"yi istemci belirlerse geçmiş tarihli etkinlik
  açmak (katılım ve istatistik oynatmak) mümkün olurdu.
- **Açık uçlu ama 12 saat sınırlı:** "bitince biter" deneyimi, sonsuza
  kadar "sürüyor" görünen etkinlik olmadan.
- **QR'ın kısa ömrü ve özet saklama:** QR fiziksel varlığın vekilidir;
  uzun ömürlü olsaydı paylaşılan bir ekran görüntüsü kapalı katılımı
  açardı. Token sızsa bile 2 dakika ve 30 katılım sınırı hasarı daraltır.
- **Kalıcı tayfada katılım politikası:** QR bir kolaylıktır, tayfanın
  verdiği "kapalı tayfa" sözünü delmez.
- **Bildirim sınırları:** Şimdi bildirimi en kolay spam kanalıdır;
  günlük sınır ve alıcı ayarı onu arkadaşlar arası bir davet olarak tutar.

## Sonuçlar

- Migration (core): `events.open_ended`, `tayfas.now_create_min_role`,
  `event_join_qr_tokens`, `now_notification_counters`, `joined_via`'ya
  `qr`; `social_settings`'e `now_from_friends`.
- Migration (chat): `notifications.type`'a `now_started`,
  `notification_preferences.scope_type`'a `tayfa_now`.
- OpenAPI: `starts_now`, `…/finish`, `…/join-qr`, `POST /v1/join-qr`,
  `now_recipient_ids`, `now_from_friends`; problem tipleri
  `invalid-join-qr`, `now-limit-reached`.
- İzin tablosu: `EventCreateNow`, `EventFinish`, `EventJoinQrCreate`;
  tablo testleri.

## Uygulama notları (#263, §1–§2)

- `events.open_ended` CHECK'i (`events_open_ended`): açık uçlu etkinlik
  seriye ait olamaz, Keşfet'e çıkamaz, `ends_at` başlangıçtan en fazla 12
  saat sonradır. Açık uçlu etkinlikte zaman düzenlenemez (`update_mask`
  doğrulama hatası); bitiş yalnızca Bitir'le değişir.
- Bitir yalnızca organizatöründür (`EventFinish`, moderatör ve yönetici
  dahil başkası yapamaz); susturulmuş organizatör bitiremez, etkinlik yine
  12 saatte biter. Planlı etkinlikte Bitir `event-not-open-ended`
  (FAILED_PRECONDITION, 2631) alır; bitmiş ya da bitirilmiş etkinlik
  `event-ended`. Sonuçlar bölümündeki problem tiplerine bu eklendi.
- Bitir katılımcılara bildirim üretmez; yeni bitiş `event.updated`
  olayıyla (`finished: true`) chat'e gider, kanal arşivi yeni bitişten
  sayılır. Check-in, bileşenlerin yazma/donma pencereleri, 7 günlük
  düzeltme penceresi, katılım geçmişi süresi ve rozetler zaten `ends_at`'e
  dayanır; ek bir kural gerekmedi. Yoklama alarmı organizatörün yazdığı
  beklenen dönüşe dayanır, bitişe değil.
- `open_ended` kendiliğinden bitişten sonra da `true` kalır; sürüp
  sürmediği `ends_at` ile anlaşılır. Kişi başı 1 açık şimdi etkinliği
  sayımı `open_ended AND ends_at > now` ile yapılır, kullanıcı başına
  advisory kilitle sıralanır.
- `event.created` olayında `starts_now: true` işareti vardır; şimdi
  bildirimi (#265) buradan çıkar.

## Değerlendirilen alternatifler

- **Ayrı "şimdi" modeli (tayfasız etkinlik)**: Reddedildi. ADR-0032 gizli
  tayfa modeli sohbet, check-in, albüm ve moderasyonu zaten taşıyor.
- **Tüm arkadaşlara otomatik bildirim**: Reddedildi. Spam ve "kimin
  çağrıldığı" kontrolü organizatörde olmalı; liste önceden dolu geldiği
  için zahmet yok.
- **Uzun ömürlü (etkinlik boyu) QR**: Reddedildi; §Gerekçe.
- **Push'ta semt**: Reddedildi; ADR-0019.
