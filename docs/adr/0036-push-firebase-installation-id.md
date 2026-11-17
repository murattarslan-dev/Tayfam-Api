# ADR-0036: Push hedefi olarak Firebase Installation ID (FID)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-17, sohbette)
- **Tarih:** 2026-11-17
- **Karar veren:** Murat Arslan (2026-11-17 sohbeti, mimari oturumunun
  aktarımıyla; issue #230). Seçenek B, "önce sunucu": sunucu yeni kimliğe
  geçer, istemci (Tayfam-App #212) bundan sonra yükselir.
- **İlgili:** [ADR-0003](0003-kod-yapisi-ve-sozlesmeler.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0019](0019-push-bildirimleri.md)
- **Değiştirdiği:** ADR-0019 madde 1 ("push token'ı" artık bir FCM kayıt
  token'ı ya da bir FID'dir), madde 5 (geçersiz kimlik alanı `message.fid`
  de olabilir) ve madde 6'daki istek gövdesi. ADR-0019'a revizyon notu
  düşüldü.

## Bağlam

Tayfam-App #212 oturumu, firebase_messaging 17'de `getToken` /
`deleteToken` / `onTokenRefresh`'in kullanımdan kalktığını ve yerlerine
gelen `register()` / `onRegistered`'ın FCM kayıt token'ı yerine Firebase
Installation ID (FID) verdiğini buldu. Sunucu iki yerde kayıt token'ı
bekliyordu: sözleşmedeki `PushTokenRegistration.token` ("FCM kayıt
token'ı") ve chat'in FCM gönderimi (`messages:send` gövdesinde
`message.token`). Sahada eski istemciler kayıt token'ı göndermeye devam
edecektir.

## Doğrulama (resmi belgeler, 2026-11-17)

Bulgular yalnızca Firebase / Google'ın resmi belgelerinden ve
firebase_messaging'in pub.dev sayfasından alındı:

1. **Yeni API hangi kimliği veriyor?** Flutter "Get started" sayfası:
   cihazı FCM'e `register()` ile kaydet; "The FID is delivered
   asynchronously through the `onRegistered` stream"; `onRegistered`'a
   `register()`'dan önce abone ol; her `register()` çağrısında (zaten
   kayıtlı olsa da) FID yeniden yayınlanır. `getToken` / `onTokenRefresh`
   için: "This feature is deprecated. Use Firebase Installation IDs, as this
   method will be removed in a future release." Uyarı: FID API'leriyle
   kullanımdan kalkmış kayıt token'ı API'lerini aynı anda kullanma.
   firebase_messaging 17.0.0 değişiklik günlüğü: "add FID registration
   APIs". Android `FirebaseMessaging` referansı: `getToken` ve
   `deleteToken` kullanımdan kalktı (`register()` / `unregister()`);
   `register` uygulama örneğini FCM'e FID'iyle bağlar.
2. **FCM HTTP v1 ona nasıl gönderir?** Uç değişmez:
   `POST https://fcm.googleapis.com/v1/projects/<proje>/messages:send`.
   `Message`'ın hedef alanı bir oneof'tur (`token`, `fid`, `topic`,
   `condition`; yalnızca biri). `fid`: "Firebase Installation ID (FID) to
   send a message to." `token`: "Deprecated: Use `fid` instead. During the
   transition period, this field also accepts a Firebase Installation ID
   (FID)." Admin SDK örnekleri FID'i `fid` alanıyla gönderir. İzin ve
   kapsam değişmez: aynı servis hesabı, aynı `firebase.messaging` kapsamı
   (Cloud Messaging API açık olmalı; ADR-0019 madde 6).
3. **Kimlik ne zaman değişir, nasıl silinir?** FID; uygulama kaldırılıp
   yeniden kurulunca, uygulama verisi silinince ya da uzun süre
   kullanılmadığı için arka uçta silinince yenilenir; değişince
   `onRegistered` yeni FID'i verir ve istemci sunucuya yeniden yükler.
   İstemcide `unregister()` uygulama örneğinin FCM kaydını kaldırır (FID'i
   silmez; otomatik başlatma açıksa sonraki açılışta yeniden kaydolur).
   Sunucu tarafında geçersiz kayıt `UNREGISTERED` (404) ya da yalnızca
   gövde tamamen geçerliyken `INVALID_ARGUMENT` (400) ile anlaşılır ve
   kaydı silinir. Android'de 270 gün etkin olmayan kayıt FCM'de süresi
   dolmuş sayılır.
4. **Eski kayıt token'ları ne zamana kadar çalışır?** Belgeler bir tarih
   vermez: iki sistem "fully co-supported"tır; `token` alanı "deprecated
   and will be removed" der ama tarih yoktur. Bu yüzden sunucu iki türü
   de destekler; eski türün kaldırılması ayrı bir karardır.

Kaynaklar:

- https://firebase.google.com/docs/cloud-messaging/flutter/get-started
- https://firebase.google.com/docs/reference/fcm/rest/v1/projects.messages
- https://firebase.google.com/docs/cloud-messaging/send-message
- https://firebase.google.com/docs/cloud-messaging/manage-tokens
- https://firebase.google.com/docs/projects/manage-installations
- https://firebase.google.com/docs/reference/android/com/google/firebase/messaging/FirebaseMessaging
- https://pub.dev/packages/firebase_messaging/changelog (17.0.0)

Belgeler #212 oturumunun bulgusunu doğruladı; çelişki yoktur.

## Karar

### 1. Sözleşme: açık tür alanı, geriye uyumlu

- REST `PUT /v1/me/push-token` gövdesine isteğe bağlı
  `token_type: registration_token | fid` eklenir; gönderilmezse
  `registration_token` sayılır (sahadaki eski sürümler değişmeden
  çalışır). Bilinmeyen değer 400'dür.
- gRPC: `RegisterPushTokenRequest.token_type` (`PushTokenType`, alan 3);
  `UNSPECIFIED` kayıt token'ıdır. Yeni alan ve enum `buf breaking`'den
  geçer.
- Biçim değişmez: 1–1024 bayt, `^[A-Za-z0-9:_.-]+$`. FID bu alfabeye
  uyar; resmi belgelerde FID'in biçimi bir sözleşme olarak verilmediği
  için ayrıca daraltılmaz.
- Oturum başına bir kayıt kuralı aynen geçerlidir: aynı oturum token'dan
  FID'e (ya da tersine) yeniden `PUT` ile geçer, satır türüyle birlikte
  yenilenir. İstemci, Firebase'in uyarısına uygun olarak iki API'yi aynı
  anda kullanmaz.

### 2. Veritabanı

- chat `push_tokens.token_type text NOT NULL DEFAULT 'registration_token'`
  (`CHECK IN ('registration_token', 'fid')`; migration 00019). Mevcut
  satırlar eski tür sayılır. `chat_app` yalnızca kaydı yenilerken türü
  yazabilir (`UPDATE (token_type)`).
- UNIQUE (token), 60 gün saklama, kullanıcı başına 10 sınırı, oturum
  bitişi / yaptırım / hesap silmede silme ve çıkıştaki `DELETE` aynen
  geçerlidir; türden bağımsızdır.

### 3. Gönderim

- `registration_token` → `message.token`, `fid` → `message.fid`; ikisi
  hiçbir zaman birlikte gönderilmez. FID'i `message.token`'da göndermek
  belgelere göre geçiş döneminde çalışsa da kullanılmaz: o alan kalkacak.
- Geçersiz kimlik ADR-0019 madde 5'teki gibi silinir: `UNREGISTERED`
  (404), `SENDER_ID_MISMATCH` (403) ve alan ihlali `message.token` **ya da
  `message.fid`** olan `INVALID_ARGUMENT` (400).
- Kimlik (token ya da FID) log'a, metriğe, olaya ve hata mesajına girmez
  (CLAUDE.md kesin kural 3, ADR-0019 madde 10); testlerle sabittir.

## Gerekçe

- Açık bir tür alanı, sunucunun bir değerin biçiminden tahmin yürütmesini
  gerektirmez; biçim tahmini Google'ın değiştirebileceği bir ayrıntıya
  dayanırdı.
- Varsayılanın eski tür olması, sahadaki istemcileri ve mevcut satırları
  dokunmadan doğru bırakır; istemci yükseldiğinde aynı oturum kaydını
  yeniler.
- Ayrı alanla göndermek, belgelerin "kaldırılacak" dediği `token`
  alanına yeni bir bağımlılık eklemez.

## Sonuçlar

- Tayfam-App #212: firebase_messaging 17'ye geçen istemci `onRegistered`'a
  abone olur, `register()` çağırır ve her FID'i
  `PUT /v1/me/push-token {token, platform, token_type: "fid"}` ile
  kaydeder; çıkışta `DELETE /v1/me/push-token` aynen çalışır.
- Eski tür (`registration_token`) Firebase `token` alanını kaldırana kadar
  desteklenir; kaldırma tarihi duyurulunca ayrı bir issue açılır.
- Notion veri modeli v15 `push_tokens`'a `token_type` notu düşülür.

## Değerlendirilen alternatifler

- **FID'i `message.token`'da göndermek:** Belgelere göre geçiş döneminde
  çalışır, sözleşme değişmezdi; ama alan kaldırılacak ve tür bilgisi
  olmadan ileride doğru alana geçilemez.
- **Türü değerin biçiminden çıkarmak:** Sözleşme değişmezdi; ama FID
  biçimi resmi bir sözleşme değildir, yanlış sınıflama sessizce push
  kaybettirir.
- **Yeni bir uç (`/v1/me/push-installation`):** İki kayıt yolu ve oturum
  başına iki satır ihtimali getirir; oturum başına tek kayıt kuralı
  bozulur.
