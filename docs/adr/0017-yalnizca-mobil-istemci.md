# ADR-0017: Yalnızca mobil istemci; web derlemesi yalnızca prod dışında

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-10
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md) ("İstemci tarafında saklama" bölümünün yerine geçer), [ADR-0005](0005-gercek-zamanli-iletisim-ve-chat.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0008](0008-kvkk-ve-platform-yonetimi.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0014](0014-websocket-erisim-tokeniyla.md) (5. maddenin yerine geçer), [ADR-0016](0016-desteklenen-istemci-surumu.md); tayfam-app ADR-0011; issue #124

## Bağlam

Platform sahibi 2026-11-10'da karar verdi: Tayfam yalnızca Android ve
iOS'ta yayınlanır, web istemcisi yoktur (tayfam-app ADR-0011; Notion'da
Proje Planı §1, §8 ve Özellik Haritası güncellendi). Uygulamanın web
derlemesi yalnızca geliştirme sırasında uygulamayı yerel makinede hızlıca
açmak içindir; hiçbir yerde yayınlanmaz, dev API'ye (`api-dev.tayfam.app`)
yerel makineden bağlanır, mobil kod yolunu kullanır (token gövdede gelir,
`Authorization: Bearer` ile gider, yalnızca bellekte tutulur) ve WebSocket
kurmaz.

API'de web istemcisi için kurulan yol bu kararla gereksiz saldırı yüzeyi
olarak kaldı: HttpOnly çerezler (`tayfam_at`, `tayfam_rt`), çerezle kimlik
doğrulama, çerezli isteklere karşı CSRF başlığı, `https://tayfam.app` için
kimlik bilgili CORS ve WebSocket'te çerez + `Origin` yolu. Çerez taşıyan
bir kimlik, tarayıcının kendiliğinden eklediği (ambient) bir kimliktir;
CSRF ve siteler arası WebSocket ele geçirmesi tam olarak buna dayanır.

Yönetim paneli (`admin.tayfam.app`, ADR-0008) bu karardan etkilenmez: ayrı
bir host'ta, `admin.Mux` ile uygulama API'sinin yönlendiricisine hiç
girmeden, kendi `__Host-` çerezleri, kendi CSRF koruması ve Cloudflare
Access ile çalışır.

## Karar

### 1. Token'lar yalnızca gövdede ve başlıkta

- Giriş ve yenileme token'ları her zaman yanıt gövdesinde döner
  (`access_token`, `refresh_token` zorunlu alanlardır). gateway uygulama
  API'sinde hiçbir çerez yazmaz, silmez, okumaz.
- Erişim token'ı yalnızca `Authorization: Bearer` başlığından okunur.
  Çerezle gelen istek (eski `tayfam_at` dahil) **kimliksiz sayılır**:
  `unauthenticated` (401), iç servise çağrı yapılmaz.
- Refresh token yalnızca `POST /v1/auth/refresh` gövdesinde gelir; gövde
  zorunludur. Çıkış ve hesap silme çerez silmez.

### 2. CORS: prod'da yok, dev'de yalnızca yerel geliştirme kaynağı

- Yapılandırma: `WEB_DEV_ORIGINS` (virgülle ayrılmış; eski
  `CORS_ALLOWED_ORIGINS`'in yerine). Ad, değerin ne olduğunu söyler: yerel
  web geliştirme derlemesinin kaynağı. Eski değişken okunmaz.
- Her değer tam olarak `http://localhost:<port>` ya da
  `http://127.0.0.1:<port>` olmalıdır (port 1–65535, baştaki sıfır yok;
  yol, sorgu, kullanıcı bilgisi, `https`, başka host, `*` ya da `null`
  yok). Tarayıcı `Origin` başlığını bu biçimde yazar; karşılaştırma birebir
  dize eşitliğidir. Geçersiz değer **gateway'in başlamasını engeller**:
  uzak bir kaynağa sessizce CORS açmaktansa çalışmamak tercih edilir.
- **Ortam kuralı:** `TAYFAM_ENV=prod` iken liste boş olmak zorundadır;
  doluysa gateway başlamaz. Böylece prod'da localhost'a (ya da herhangi bir
  kaynağa) izin vermek bir yapılandırma hatası değil, imkânsızdır.
  `local` ve `dev`'de liste boş olabilir (CORS yok) ya da yerel kaynaklar
  içerir. Varsayılan değer boştur.
- İzinli kaynağa verilen başlıklar: `Access-Control-Allow-Origin` (kaynağın
  kendisi), ön istekte `Access-Control-Allow-Methods` (GET, POST, PUT,
  PATCH, DELETE), `Access-Control-Allow-Headers` (yalnızca
  `Authorization`, `Content-Type`, `X-Tayfam-Client`,
  `X-Tayfam-Client-Version`, `X-Request-Id`), `Access-Control-Max-Age`
  (600) ve yanıtta `Access-Control-Expose-Headers: X-Request-Id`.
  **`Access-Control-Allow-Credentials` hiçbir zaman yazılmaz.**
- İzinsiz kaynağın ön isteği 403 alır; diğer istekleri CORS başlığı
  olmadan işlenir (tarayıcı yanıtı okuyamaz). Her yanıtta `Vary: Origin`.
- Ortam değerleri: prod'da `WEB_DEV_ORIGINS` yok; dev'de
  `http://localhost:3000` (web derlemesi `--web-port 3000` ile açılır);
  `docker-compose.yml` ve `scripts/dev.sh` de aynı değeri kullanır.

### 3. `X-Tayfam-Client` tutulur; `web` yalnızca prod dışında

- Başlık durum değiştiren her istekte zorunlu kalır. Kabul edilen değerler
  `android`, `ios`; prod dışında (`local`, `dev`) ayrıca `web`. prod'da
  `web` ya da bilinmeyen değer `missing-client-header` (400) alır.
- **Gerekçe değişti:** Çerez kalkınca CSRF'in ön koşulu (tarayıcının
  kendiliğinden eklediği kimlik) yoktur; başka bir sitenin formu ya da
  `fetch`'i kullanıcının token'ını taşıyamaz. Başlık artık birincil CSRF
  koruması değil, ikinci katmandır (ADR-0011 ihlal varsayımı): bir tarayıcı
  bu özel başlığı ancak CORS ön isteğinden geçerek ekleyebilir ve ön istekten
  yalnızca prod dışındaki yerel kaynak geçer. İleride biri yanlışlıkla
  kendiliğinden eklenen bir kimlik getirirse (ör. aynı host'ta bir çerez)
  durum değiştiren uçlar yine korunur. Mobil uygulama başlığı zaten
  gönderiyor; tutmanın maliyeti yok, kaldırmak ise sözleşmeyi gevşetirdi.
- Giriş gövdesindeki `platform: web` da yalnızca prod dışında kabul edilir;
  prod'da `invalid-request` (400). identity'deki `PLATFORM_WEB` değeri ve
  oturum listesindeki `web` platform adı yerinde kalır (proto değişmez;
  dev'deki geliştirme oturumları için).

### 4. `X-Tayfam-Client-Version` ve `web` platformu

- prod'da `web` platformu hizmet almaz: `web/<sürüm>` başlığı sürümü ne
  olursa olsun `invalid-client-version` (400) alır; `GET /v1/client-config`
  cevabında `web` yer almaz (sözleşmede `web` artık zorunlu değil).
  `clientversion.Policy.Without(Web)` ile uygulanır; politika tek yerde,
  tabloya dayalı testlerle korunur.
- **Neden 400 ve 426 değil:** 426 "güncelle" demektir ve istemciyi mağazaya
  yönlendirir; web derlemesi için güncel bir sürüm yoktur. prod'da web
  geçersiz bir platformdur, bozuk başlıkla aynı ele alınır.
- prod'da `CLIENT_MIN_VERSION_WEB` ya da `CLIENT_LATEST_VERSION_WEB`
  verilirse gateway başlamaz: hizmet verilmeyen bir platformu
  yapılandırmak, yanlış bir ortam dosyasının işaretidir. prod configMap'inden
  bu değerler kaldırıldı; dev'de kalır.
- Başlıksız istek ADR-0016'daki gibi kabul edilir (curl, health check).

### 5. WebSocket'te `Origin` her zaman reddedilir

- Yalnızca mobil uygulamalar bağlanır ve `Origin` göndermez; tarayıcı
  WebSocket'te her zaman gönderir. `Origin` başlığı taşıyan her
  `GET /v1/realtime` isteği, kaynağı ne olursa olsun (dev'deki yerel kaynak
  dahil) `forbidden-origin` (403) alır. Bunu hem gateway hem chat yapar
  (ADR-0011); chat'teki izinli kaynak listesi ve `CORS_ALLOWED_ORIGINS`
  okuması kaldırıldı.
- Token yalnızca `Authorization` başlığından okunur; gateway çerezi chat'e
  iletmez (değişmedi).

## Gerekçe

- Kendiliğinden eklenen kimlik (çerez) olmayınca CSRF ve siteler arası
  WebSocket ele geçirmesi sınıfı ortadan kalkar; kalan korumalar ikinci
  katmandır.
- Tutmadığın veri çalınamaz: tarayıcıda token taşıyan bir yol yoktur;
  prod'da tarayıcıdan gelen bir isteğin yanıtı hiçbir kaynağa açılmaz.
- prod kuralları yapılandırmaya değil, `TAYFAM_ENV`'e bağlıdır ve
  başlangıçta denetlenir: yanlış bir configMap prod'da CORS açamaz, web
  platformunu etkinleştiremez; servis başlamaz.
- Web geliştirme derlemesi dev'de mobil kod yoluyla çalışmaya devam eder;
  geliştirme kolaylığı kaybolmaz.

## Sonuçlar

- `api/openapi/gateway.yaml`: `cookie` güvenlik şeması kaldırıldı; çerez
  açıklamaları silindi; `Session` token alanları ve `RefreshRequest`
  gövdesi zorunlu; `ClientConfig.platforms.web` isteğe bağlı.
- Çerezli web istemcisine dönmek yeni bir ADR gerektirir; bu ADR'deki
  `web` yalnızca yerel geliştirme derlemesidir.
- dev'deki yerel web derlemesi için platform sahibi tarafında: dev Google
  OAuth istemcisinde *Authorized JavaScript origins*'e
  `http://localhost:3000` eklenmiş olmalı; `api-dev.tayfam.app` Cloudflare
  Access arkasındaysa tarayıcının ön isteği (OPTIONS) Access'ten geçmelidir
  (Access uygulamasının CORS ayarı). Bunlar API kodu değildir.
- prod Google OAuth web istemcisinde `https://tayfam.app` JavaScript
  kaynağı gerekmez; yalnızca panel (`https://admin.tayfam.app`) kalır
  (`docs/runbooks/vds-kurulumu.md`).
- identity giriş platformunu ortamdan bağımsız kabul eder (`web` dahil);
  prod'daki `web` reddi gateway'dedir. Platform adı istemcinin beyanıdır
  ve bir güvenlik denetimi değildir; identity'ye ayrıca eklenmedi.

## Değerlendirilen alternatifler

- **`X-Tayfam-Client`'ı tamamen kaldırmak:** Çerezsiz API'de CSRF zaten
  mümkün değil; ama başlık ücretsiz bir ikinci katmandır ve mobil istemci
  zaten gönderiyor. Kaldırmak ileride kendiliğinden eklenen bir kimlik
  eklenirse korumasız uçlar bırakırdı.
- **`web`'i her ortamda kabul etmek:** Basit, ama prod'da var olmayan bir
  istemci adını ve platformunu açık bırakır; "prod'da web yok" kararını
  kodda görünmez kılar.
- **prod'da `web/...` için 426:** İstemciyi olmayan bir güncellemeye
  yönlendirir; geçersiz platform 400 ile daha doğru anlatılır.
- **`CORS_ALLOWED_ORIGINS` adını korumak:** Mevcut prod configMap'indeki
  `https://tayfam.app` değeri yeni kuralla gateway'i durdururdu; yeni ad hem
  anlamı daraltır hem eski değerin yanlışlıkla okunmasını önler.
- **dev'de de CORS'u kapatmak, web derlemesini yerel bir proxy'yle
  çalıştırmak:** Daha dar, ama her geliştiriciye ek kurulum getirir; dev
  verisi gerçek kullanıcı verisi değildir ve izin yalnızca loopback
  kaynağıyla, kimlik bilgisi olmadan verilir.
