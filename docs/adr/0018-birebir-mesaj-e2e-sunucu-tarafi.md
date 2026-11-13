# ADR-0018: Birebir mesajda uçtan uca şifreleme: sunucu tarafı

- **Durum:** Önerildi
- **Tarih:** 2026-11-10
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md), [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md) (bu ADR "Direkt mesajlar" bölümünü uygular), [ADR-0009](0009-veri-modelinin-servislere-dagilimi.md) (chat tablolarının adları ve `devices` bölünmesi), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0014](0014-websocket-erisim-tokeniyla.md), [ADR-0015](0015-veritabani-rolleri.md), [ADR-0017](0017-yalnizca-mobil-istemci.md) (yalnızca mobil); tayfam-app ADR-0010 (Signal kütüphanesi, AGPL kabul edildi 2026-11-10) ve ADR-0011 (yalnızca mobil); issue #127
- **Kaynak:** Notion: Veri Modeli (taslak v14) "Sohbet", "E2E güvenlik kodu uyarısı", "Saklama süreleri"

## Bağlam

ADR-0005 birebir mesajların Signal protokolüyle uçtan uca şifreleneceğini,
sunucunun yalnızca açık anahtarları, opak zarfları, kullanıcının geçmiş
anahtarıyla şifreli arşivi ve eşleştirme mesajlarını tutup ileteceğini
söyler; düz sürüm (issue #95) önce çalıştı. İstemci tarafında Signal'in
resmi Rust çekirdeği kullanılacak (tayfam-app ADR-0010, platform sahibi
2026-11-10); uygulama yalnızca mobildir ([ADR-0017](0017-yalnizca-mobil-istemci.md), tayfam-app ADR-0011), web
istemcisi ve tarayıcıda anahtar saklama yoktur.

ADR-0005 ve ADR-0009 sunucu tarafının şeklini çizer ama şunları açık
bırakır: E2E cihazının kimliğe nasıl bağlanacağı (erişim token'ında
identity'nin cihaz kimliği yoktur, yalnızca oturum `sid` vardır), boyut ve
adet sınırları, ön anahtar paketini kimin çekebileceği, eşleştirme ve
anahtar yenilemenin sunucudaki durum makinesi, düz sürümde yazılmış
mesajların ne olacağı ve temizlik işleri. Bu ADR bunları karara bağlar.

**Sunucu hiçbir kriptografi yapmaz** (CLAUDE.md kural 4): imza doğrulamaz,
şifre çözmez, anahtar üretmez. Yalnızca biçimi (uzunluk, Signal anahtar
türü baytı) ve yetkiyi denetler, saklar ve iletir.

## Karar

### 1. Cihaz = oturum

- Bir E2E cihazı, onu kaydeden **oturuma** (erişim token'ının `sid`'i)
  bağlıdır; oturum başına en fazla bir cihaz vardır. identity her girişte
  yeni bir cihaz ve oturum açar (`internal/identity/app/session`), yani
  identity'nin cihazı ile oturumu 1:1'dir. chat bu yüzden ADR-0009'daki
  `device_keys.device_id` yerine `session_id` tutar; identity'ye
  değişiklik ve yeni olay gerekmez.
- Oturum bittiğinde cihaz da biter: `session.revoked` (çıkış, başka
  cihazdan kapatma, token yeniden kullanımı, cihaz kaldırma),
  `user.sanctioned` ve `user.deleted` zaten chat'e gelir (ADR-0009). chat
  cihazı, ön anahtarlarını, ona bekleyen zarfları, geçmiş anahtarı
  paylaşımlarını ve eşleştirme isteklerini siler; `user_key_events`'e
  `device_removed` yazar. ADR-0009'daki "`device.removed` olayı" bu
  olaylarla karşılanır.
- Biten oturum, elindeki erişim token'ı (≤ 15 dk) geçerli kalsa da yeni
  cihaz kaydedemez: chat biten oturumları bir gün tutar
  (`e2e_ended_sessions`); yaptırımda kullanıcının o andan önce verilmiş
  token'ları reddedilir (`user_e2e_state.sessions_ended_at`, token'ın
  `iat`'ıyla karşılaştırılır).
- Sunucu cihaza kendi kimliğini (UUIDv7) verir; Signal adresi için
  kullanıcı başına bir **cihaz numarası** (1–127) ayırır. Numara artarak
  verilir, 127'den sonra en küçük boş numaraya döner; böylece eski bir
  numaranın kısa sürede yeni cihaza geçip karşı taraftaki eski oturumla
  karışması zorlaşır. İstemci cihazları her zaman sunucu kimliğiyle ayırt
  eder.
- **Kullanıcı başına en fazla 5 cihaz** (gerçek zamanlı bağlantı sınırıyla
  aynı). Fazlası reddedilir; kullanıcı eski oturumunu hesap ayarlarından
  kapatır.
- Aynı oturumda ikinci kayıt: aynı kimlik anahtarı ve kayıt kimliğiyle
  gelirse var olan cihaz döner (tekrar deneme); farklıysa reddedilir.
  İstemci kendi cihazını kaldırıp (`DELETE /v1/me/e2e/device`) yeniden
  kaydolabilir. Böylece "kimlik anahtarı değişti" her zaman "cihaz
  çıkarıldı + eklendi" olarak görünür; `identity_changed` olay türü
  şemada kalır ama yazılmaz.
- 90 gün görünmeyen cihaz silinir (v14 "Saklama süreleri"; oturum da 90
  gün kullanılmazsa biter, ADR-0004). Görülme, cihazın zarf listesi,
  kaydı, ön anahtar yüklemesi ve gönderimiyle saatte en çok bir kez
  güncellenir.

### 2. Anahtarlar ve ön anahtarlar (X3DH + PQXDH)

- Cihaz kaydı: kayıt kimliği, kimlik anahtarı, imzalı ön anahtar, imzalı
  **son çare Kyber ön anahtarı** (PQXDH zorunludur; libsignal'in güncel
  paketleri Kyber ön anahtarı ister) ve isteğe bağlı tek kullanımlık EC ve
  Kyber ön anahtarları.
- v14'teki `device_signed_prekeys` ayrı tablo değildir: her cihazın tek bir
  geçerli imzalı ön anahtarı ve tek bir son çare Kyber ön anahtarı vardır,
  `device_keys` satırında durur ve yüklemeyle değişir. v14'teki
  `device_one_time_prekeys.consumed_at` yerine kullanılan ön anahtar
  **silinir** (tutulmayan veri çalınamaz); tek kullanımlık EC ve Kyber
  ön anahtarları aynı tabloda, türle ayrılır.
- **Tek kullanımlık ön anahtar bir kez verilir:** paket çekilirken her
  cihaz için en eski ön anahtar `FOR UPDATE SKIP LOCKED` ile seçilip aynı
  ifadede silinir. Eşzamanlı iki çekme aynı anahtarı alamaz (gerçek
  Postgres testiyle sabit). Tek kullanımlık Kyber anahtarı yoksa son çare
  Kyber anahtarı, EC anahtarı yoksa EC'siz paket döner (Signal'in
  davranışı).
- **Azalma sinyali:** bir çekmeden sonra tek kullanımlık EC veya Kyber ön
  anahtar sayısı 10'un altına düşerse cihazın açık bağlantılarına
  `e2e.prekeys_low` çerçevesi gider; `GET /v1/me/e2e/device` sayıları ve
  `prekeys_low` bayrağını her zaman döner. İstemci açılışta da bakar.
- **Paket kimden çekilebilir:** kendi diğer cihazları için kullanıcının
  kendisi; başka kullanıcı için yalnızca o an ona **mesaj
  gönderebilecek** kişi: core'un `CheckDirectMessage` cevabı "yazamaz"
  değilse ve konuşma durumu gönderime izin veriyorsa (bekleyen veya
  reddedilmiş isteği olan gönderen çekemez; yeni istek açacaksa günlük
  istek sınırı dolmamış olmalı). Gönderimle aynı kurallar (ADR-0005, v7).
  Böylece yabancılar tek kullanımlık ön anahtarları tüketemez. Ayrıca
  kullanıcı başına hız sınırı vardır.
- Cihaz listesi (açık anahtarlar, numaralar, kayıt kimlikleri) ve
  `user_key_events`: kendisi, onunla görünür bir konuşması olan veya ona
  yazabilecek kişi görür. Güvenilen anahtarlar ve "doğrulandı" bilgisi
  yalnızca istemcidedir (ADR-0005).

### 3. Zarflar

- Birebir mesaj gönderimi tek yoldur (`SendDirectMessage`, ADR-0005 "Mesaj
  protokolü"): istek zarflar (`envelopes`) taşır (düz metin `body` yolu
  #151 ile kalktı, §7). Mesaj `dm_messages`'a içeriksiz,
  `encryption = 'e2e'` ve gönderen cihazla yazılır;
  kim kime yazabilir, istek akışı, günlük sınır, okundu bilgisi, içeriksiz
  bildirim ve şikâyet akışı aynen geçerlidir.
- Gönderen cihaz alıcının **tüm** cihazları ve kendi kullanıcısının
  **diğer tüm** cihazları için birer zarf gönderir. Küme birebir aynı
  olmalıdır; eksik, fazla ya da kayıt kimliği değişmiş cihaz varsa mesaj
  yazılmaz, `ERROR_REASON_E2E_DEVICES_CHANGED` eksik ve eskimiş cihaz
  kimlikleriyle döner (Signal'in 409/410 davranışı); istemci cihaz
  listesini tazeleyip yeniden dener. Alıcının hiç cihazı yoksa
  `ERROR_REASON_E2E_RECIPIENT_UNAVAILABLE`.
- Zarf türü Signal `CiphertextMessageType`'ıdır: 2 (`whisper`) veya 3
  (`prekey`). İçerik 1 B – **64 KiB** (ADR-0005); bir gönderimde en fazla
  16 zarf (5 + 4 cihaz yeter). REST gövde sınırı bu uç için 1,5 MiB'dir (base64).
- Zarf, hedef cihaz teslim aldığını bildirince (REST ya da WebSocket
  `dm.envelope_ack`) silinir; teslim edilemeyen zarf **30 gün** sonra
  silinir. Mesaj gönderenince silinirse bekleyen zarfları ve iki tarafın
  arşiv kayıtları da hemen silinir.
- Canlı iletim: zarf, hedef cihazın oturumuna ait açık bağlantılara
  `dm.envelope` çerçevesiyle gider; ayrıca iki tarafa içeriksiz
  `dm.message_created` (yalnızca kimlikler ve `encryption`). Kaçırılan
  zarflar `GET /v1/me/e2e/envelopes` ile çekilir. Teslim en az bir
  keredir; istemci zarf kimliğiyle tekrarı ayıklar.
- WebSocket'ten **zarflı gönderim yoktur**: istemci çerçevesi 64 KiB ile
  sınırlıdır (ADR-0005) ve zarflı mesaj birkaç zarf taşır. Gönderim
  REST'tedir; WebSocket teslim ve teslim onayı içindir.

### 4. Geçmiş arşivi ve geçmiş anahtarı

- Kullanıcının **geçmiş anahtarı sürümü** sunucuda tutulur
  (`user_e2e_state.history_key_version`), anahtarın kendisi asla.
- Arşiv kaydı `(kullanıcı, mesaj)` başına birdir; ilk yazan kazanır
  (tekrar deneme değişiklik yapmaz). Yalnızca **eşleştirilmiş** cihaz yazar
  ve okur; kayıt güncel sürümle yazılır (eski sürüm
  `ERROR_REASON_E2E_HISTORY_KEY_STALE`). Mesaj, kullanıcının taraf olduğu
  ve silinmemiş bir mesaj olmalıdır; olmayanlar sessizce atlanır, cevap
  kabul edilenleri listeler. Kayıt 1 B – 32 KiB, çağrı başına en fazla 50.
- Arşiv kullanıcı silene (konuşma bazında ya da tümü) veya hesap silinene
  kadar tutulur; mesaj kalıcı silinince (30 gün) kaydı da gider.
- **İlk cihaz:** kullanıcının eşleştirilmiş cihazı yoksa cihaz yeni bir
  geçmiş anahtarı üretir ve `ClaimHistoryKey` ile bildirir: sürüm bir
  artar, cihaz eşleştirilmiş olur. Eşleştirilmiş başka cihaz varsa
  reddedilir (`ERROR_REASON_E2E_PAIRED_DEVICE_EXISTS`): yeni cihaz QR ile
  eşleşmelidir. Hiç eşleştirilmiş cihaz kalmamışsa eski arşiv okunamaz
  olur; istemci bunu kullanıcıya söyler (ADR-0005).

### 5. Eşleştirme (QR)

- Eşleştirilmemiş cihaz bir **eşleştirme isteği** açar (ömrü **10
  dakika**, cihaz başına bir açık istek; yenisi eskisinin yerine geçer).
  QR'ı istemci üretir: istek kimliği, yeni cihazın bu iş için ürettiği
  açık anahtar ve tek kullanımlık doğrulama kodu. Sunucu QR içeriğinin hiçbir
  parçasını saklamaz; doğrulama kodu yalnızca istemciler arasındadır.
- Aynı kullanıcının eşleştirilmiş cihazı isteği **cevaplar**: geçmiş
  anahtarlarını (tüm sürümler) yeni cihazın açık anahtarıyla şifreleyip
  (opak, en fazla 16 KiB) güncel sürüm numarasıyla gönderir. Yeni cihazın
  bağlantılarına `e2e.link_answered` gider.
- Yeni cihaz cevabı alır ve **tamamlar**: cihaz eşleştirilmiş olur, istek
  ve cevap silinir. Bu arada anahtar yenilenmişse tamamlama
  `ERROR_REASON_E2E_HISTORY_KEY_STALE` ile reddedilir; yeni istek açılır.
- Başka kullanıcının, süresi geçmiş, cevaplanmış (cevaplarken) ya da
  bilinmeyen istek `ERROR_REASON_E2E_LINK_REQUEST_NOT_FOUND`'dır.
- **İstemci notu (2026-11-11, Tayfam-App ADR-0020; sunucuyu değiştirmez):**
  istemci "bu iş için üretilmiş açık anahtar" yerine QR'da yeni cihazın
  **Signal kimlik açık anahtarını** taşır. Eşleştirme cevabı ve geçmiş
  anahtarı paylaşımları (§6), kullanıcının kendi cihazları arasındaki
  **Signal oturumuyla** şifrelenir; yeni bir kripto yapı taşı eklenmez ve
  aktarım PQXDH ile korunur. Eski cihaz QR'daki kimlik anahtarını sunucunun
  cihaz listesindekiyle birebir karşılaştırır; tutmazsa cevap vermez.
  Sunucu açısından cevap yine opak ve 16 KiB sınırında olduğu için sözleşme
  ve davranış aynıdır.

### 6. Geçmiş anahtarının yenilenmesi

- Eşleştirilmiş bir cihaz kaldırılırsa ve eşleştirilmiş başka cihaz
  kalırsa `user_e2e_state.rotation_needed` açılır; `GET /v1/me/e2e/device`
  bunu söyler.
- Eşleştirilmiş cihazlardan biri yeni anahtarı üretir ve **diğer tüm
  eşleştirilmiş cihazlar** için ayrı paylaşımla (`history_key_shares`,
  opak, en fazla 16 KiB) `RotateHistoryKey` çağırır. Sürüm tam olarak bir
  artmalıdır; küme eksik veya fazlaysa
  `ERROR_REASON_E2E_DEVICES_CHANGED`. Paylaşımı alan cihazın
  bağlantılarına `e2e.history_key_shared` gider; cihaz paylaşımı çekip
  onaylayınca silinir, onaylanmayan 30 gün sonra silinir.
- Yenilemede eski arşiv kayıtları eski sürümle kalır (ADR-0005).

### 7. Düz sürümden geçiş

- Geçiş dönemi (2026-11-10 – 2026-11-12): kullanıcının veya karşı tarafın
  kayıtlı E2E cihazı varsa düz metinli gönderim reddedildi
  (`ERROR_REASON_E2E_REQUIRED`); düz sürümde yazılmış mesajlar
  `encryption = server` ile döndü.
- **Düz yol kaldırıldı** (platform sahibinin kararı 2026-11-11, issue
  #151; istemcinin E2E'si 2026-11-12'de tamamlandı, pilot öncesi gerçek
  kullanıcı verisi yok):
  - `SendDirectMessage` zarfsız isteği ve dolu `body`'yi
    `ERROR_REASON_VALIDATION` ile reddeder; REST'te `body` bilinmeyen
    alandır. WebSocket'in `dm.send` çerçevesi kalktı (gönderim yalnızca
    REST'tedir). `ERROR_REASON_E2E_REQUIRED` artık dönmez.
  - chat migration 00014 `encryption = 'server'` mesajları siler,
    `body_encrypted` ve `key_version` kolonlarını düşürür ve
    `encryption`'ı yalnızca `'e2e'`ye kısıtlar. Sunucunun birebir mesaj
    metni tutacağı bir yer kalmadı; bir bileşen ele geçirilse de şema
    sunucu tarafından okunabilir birebir mesajı kabul etmez (ADR-0011).
  - Birebir mesaj servisi `CHAT_MESSAGE_KEYS`'e bağlı değildir; anahtar
    yalnızca kanal mesajları içindir.
  - Proto alanları (`body`, `MESSAGE_ENCRYPTION_SERVER`,
    `ERROR_REASON_E2E_REQUIRED`) `buf breaking` için silinmez,
    `deprecated` işaretlidir.

### 8. Sınırlar

| Konu | Sınır |
|------|-------|
| Kullanıcı başına cihaz | 5 |
| Cihaz numarası | 1–127 |
| Kayıt kimliği | 1–16380 |
| Ön anahtar kimliği | 0–16777215 (24 bit) |
| EC açık anahtar | 33 B, ilk bayt 0x05 |
| İmza | 64 B |
| Kyber açık anahtar | 1569 B (tür baytı + Kyber-1024/ML-KEM-1024) |
| Tek kullanımlık ön anahtar | çağrı başına türüne göre en fazla 100, cihazda türüne göre en fazla 200 |
| Azalma eşiği | türüne göre 10'un altı |
| Zarf içeriği | 1 B – 64 KiB; gönderim başına en fazla 16 zarf |
| Arşiv kaydı | 1 B – 32 KiB; çağrı başına en fazla 50 |
| Eşleştirme cevabı, anahtar paylaşımı | 1 B – 16 KiB |
| Eşleştirme isteği ömrü | 10 dakika |
| Geçmiş anahtarı sürümü | 1–32767 |
| Paket çekme hızı | kullanıcı başına dakikada 20, patlama 10 |
| Anahtar işlemleri hızı (kayıt, yükleme, eşleştirme, yenileme) | kullanıcı başına dakikada 30, patlama 10 |
| Arşiv yazma hızı | kullanıcı başına dakikada 120, patlama 60 |

Gönderim, birebir ve kanal mesajlarıyla ortak hız sınırını kullanır
(issue #101).

### 9. Saklama ve temizlik

Hepsi chat'in saatlik `e2e-retention` işindedir; tüketiciler idempotenttir
(ADR-0002).

| Kayıt | Ne zaman silinir |
|-------|------------------|
| Zarf | Teslim onayında; teslim edilmezse 30 gün sonra; mesaj silinince hemen |
| Geçmiş anahtarı paylaşımı | Onayda; onaylanmazsa 30 gün sonra |
| Eşleştirme isteği | Tamamlanınca; süresi geçince |
| Cihaz ve ön anahtarları | Oturum bitince (`session.revoked`, `user.sanctioned`, `user.deleted`); kendi isteğiyle; 90 gün görünmeyince |
| `user_key_events` | 90 gün sonra |
| Biten oturum kaydı | 1 gün sonra |
| Arşiv kaydı | Kullanıcı silince; mesaj kalıcı silinince; hesap silinince |
| Hesap silme | Kullanıcının cihazları, arşivi, durumu, olayları, ona ve ondan bekleyen zarflar |

### 10. Gizlilik ve log

- Zarf içeriği, arşiv kaydı, eşleştirme cevabı, anahtar paylaşımı ve
  açık anahtarlar log'a, metriğe, trace'e, `domain_events`'e ve hata
  mesajlarına girmez (CLAUDE.md kural 3). Hata ayrıntısı yalnızca alan adı
  ve cihaz kimliği taşır. Bu, servisin tüm yollarını log yakalayarak
  geçen bir testle sabittir.
- Sunucu `encryption = 'e2e'` mesajı çözemez: satırda metin yoktur (CHECK)
  ve zarfı ancak hedef cihaz çözebilir. E2E yolları `CHAT_MESSAGE_KEYS`'e
  ihtiyaç duymaz.
- Sunucunun bildiği meta veri: kim kime ne zaman yazdı, hangi cihazlar,
  zarf boyutları, okunma. ADR-0005'te bilinçli kabul edilmiştir.

### 11. Veritabanı

`db/chat/migrations/00010_e2e.sql`: `device_keys`, `device_one_time_prekeys`,
`user_key_events`, `user_e2e_state`, `e2e_ended_sessions`, `dm_envelopes`,
`dm_archive_entries`, `device_link_requests`, `history_key_shares`. `chat_app`
yetkileri aynı migration'dadır (ADR-0015 düzeni): varsayılan `SELECT,
INSERT`'e ek olarak yalnızca gereken `DELETE` ve kolon düzeyinde `UPDATE`.
Kimlik anahtarı ve kayıt kimliği hiçbir zaman güncellenemez.

## Gerekçe

- Cihazı oturuma bağlamak, identity'ye dokunmadan mevcut olaylarla her
  çıkışta anahtarların temizlenmesini sağlar; istemci de çıkışta
  anahtarları siler (tayfam-app ADR-0010). Çalınan bir erişim token'ı başka
  oturumun cihazına zarf alamaz, anahtar yükleyemez.
- Ön anahtar paketini gönderim kuralına bağlamak, yabancıların tek
  kullanımlık ön anahtarları tüketmesini (ve böylece PQXDH/X3DH'nin
  ileri gizlilik kazancını azaltmasını) engeller.
- Zarf kümesinin birebir eşleşmesi, sunucunun bir cihazı sessizce
  dışarıda bırakmasını değil, istemcinin bunu fark etmesini sağlar;
  "güvenlik kodu değişti" uyarısı cihaz listesi ve `user_key_events` ile
  desteklenir.
- Silme (consumed_at yerine) ve kısa saklama süreleri, ele geçirilen bir
  veritabanında eski anahtar materyalinin ve teslim edilmemiş zarfların
  azalmasını sağlar.

## Sonuçlar

- chat yeni bir uygulama paketi (`internal/chat/app/e2e`), yeni bir gRPC
  servisi (`E2EService`) ve gateway'de `/v1/me/e2e/…`, `/v1/users/{user_id}/e2e/…`
  uçları kazanır.
- Yaptırım ve tüm oturumların kapanması kullanıcının tüm cihazlarını
  siler; eşleştirilmiş cihaz kalmazsa geçmiş arşivi okunamaz olur (kurtarma
  kodu eklenene kadar; ADR-0005).
- ADR-0009'daki chat tablo adları bu ADR'ye göre güncellenir; Notion veri
  modeli v14'e not düşülmelidir (platform sahibi).
- Çok kopyalı chat'e geçişte (ADR-0005 "Ölçekleme") zarf ve eşleştirme
  çerçeveleri de LISTEN/NOTIFY ile taşınır.

## Değerlendirilen alternatifler

- **E2E cihazını identity'nin `devices` kimliğine bağlamak:** ADR-0009'un
  ilk çizimi. Token'da cihaz kimliği yok; identity'ye yeni claim veya
  `device.removed` olayı gerekirdi, oysa oturum ile cihaz zaten 1:1.
- **Ön anahtar paketini herkese açmak (Signal'in kendisi gibi):**
  Signal'de telefon numarası olan herkes yazabilir; Tayfam'da yazma izni
  sınırlıdır, paket de aynı sınırla korunur.
- **`consumed_at` ile işaretleme:** v14'teki taslak. Kullanılmış anahtarı
  saklamak bir şey kazandırmaz.
- **WebSocket'ten zarflı gönderim:** çerçeve sınırını büyütmek gerekirdi;
  REST zaten tekrar denenebilir yoldur.
- **Düz sürüm mesajlarını hemen silmek:** pilot öncesi kayıp yok, ama
  geliştirme ortamında istemci E2E'yi bitirene kadar birebir mesaj
  çalışmaz hale gelirdi. Ayrı işe bırakıldı; istemci E2E'yi bitirince
  #151 ile yapıldı (§7).
