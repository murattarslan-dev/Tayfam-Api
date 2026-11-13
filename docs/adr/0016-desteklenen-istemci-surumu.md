# ADR-0016: Desteklenen en düşük istemci sürümü

- **Durum:** Kabul edildi (platform sahibi, 2026-11-10)
- **Tarih:** 2026-11-10
- **Revizyon:** 2026-11-10, web istemcisi kaldırıldı
  ([ADR-0017](0017-yalnizca-mobil-istemci.md), issue #124): `web` platformu
  yalnızca prod dışında (yerel geliştirme derlemesi) hizmet alır. prod'da
  `web/<sürüm>` başlığı `invalid-client-version` (400) alır,
  `GET /v1/client-config` `web`'i listelemez ve `CLIENT_*_WEB` verilirse
  gateway başlamaz. Aşağıdaki web ve tarayıcı WebSocket'i ifadeleri bu
  kurala göre okunur.
- **Karar veren:** Murat Arslan
- **İlgili:** [ADR-0003](0003-kod-yapisi-ve-sozlesmeler.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md), [ADR-0014](0014-websocket-erisim-tokeniyla.md); issue #114

## Bağlam

Mobil uygulama mağazadan dağıtılır; yeni bir sürüm yayınlansa da eski
sürüm kullanıcının cihazında açık kalabilir. Hatalı ya da güvensiz bir
sürümü devreden çıkarmanın tek yolu, sunucunun o sürüme hizmet vermeyi
bırakması ve uygulamanın kullanıcıyı mağazaya yönlendirmesidir. Platform
sahibi 2026-11-09'da bunu istedi (Notion: "İstemci Geçiş Özeti", en düşük
desteklenen istemci sürümü); kod itme (Shorebird vb.) güvenlik gerekçesiyle
şimdilik yok.

İstemci açılışta, girişten önce en düşük sürümü öğrenebilmeli; açık kalmış
eski bir uygulama da bir sonraki isteğinde yakalanmalı. Değerler kod
değişikliği olmadan, ortam başına değişebilmeli.

## Karar

### Uç

`GET /v1/client-config` platform başına (`android`, `ios`, `web`)
`min_supported_version` ve `latest_version` döner. Token istemez (açık uç;
CLAUDE.md kural 8): istemci girişten önce çağırır. Kişisel veri yoktur,
herkese aynı cevaptır; yapılandırılmamış alan cevapta yer almaz.

- IP başına hız sınırı: dakikada 60, en fazla 30'luk ani istek (bellek içi
  token bucket, ADR-0004). Uç uygulama açılışında bir kez çağrılır; sınır
  CGNAT arkasındaki çok kullanıcıya yetecek kadar gevşektir.
- `Cache-Control: public, max-age=60`: değişen bir en düşük sürüm en geç bir
  dakikada görünür; zorlama zaten her istekte sunucuda yapılır.
- Token istemeyen işlemlerin listesi (`security: []`: `getStatus`,
  `signIn`, `refresh`, `getClientConfig`) testle sabitlenir; listeye ekleme
  testi değiştirmeyi gerektirir.

### Başlık biçimi

İstemci her istekte tek bir başlık gönderir:

```
X-Tayfam-Client-Version: <platform>/<sürüm>      örn. android/1.4.2
```

- Platform `android`, `ios` veya `web` (küçük harf; `X-Tayfam-Client` ile
  aynı adlar). Sürüm Semantic Versioning 2.0.0'dır; `v` öneki, eksik parça
  ve baştaki sıfır kabul edilmez. Başlık en fazla 80 bayttır.
- Platform ve sürüm aynı başlıktadır. CSRF için var olan `X-Tayfam-Client`
  yalnızca durum değiştiren isteklerde zorunludur (ADR-0004); sürüm
  denetimi okuma isteklerinde de çalışmalı ve iki başlığın birbirini
  tutmaması gibi bir durum olmamalı. Tek başlık, tek ayrıştırma.

### Zorlama

gateway'de, tüm REST uçlarından ve WebSocket el sıkışmasından önce
çalışan bir middleware:

| İstek | Sonuç |
|-------|-------|
| Başlık yok | **Kabul** |
| Sürüm ≥ platformun en düşük sürümü, ya da platformda en düşük sürüm tanımsız | Kabul |
| Sürüm < en düşük sürüm | `client-outdated`, **426** |
| Bozuk başlık, bilinmeyen platform, birden çok başlık | `invalid-client-version`, 400 |

- **Başlıksız istek kabul edilir.** Tarayıcılar WebSocket el sıkışmasına
  başlık ekleyemez; curl, health check ve izleme araçları başlık göndermez.
  Başlığı zorunlu yapmak bunları kırar ve hiçbir koruma getirmez: sürüm
  denetimi bir güvenlik kontrolü değildir, istemci istediği sürümü
  söyleyebilir. Amaç, başlığı gönderen kendi uygulamamızın eski
  sürümlerini durdurmaktır; tayfam-app her istekte gönderir.
- **Hiçbir zaman reddedilmeyenler:** `GET /v1/status` (health check) ve
  `GET /v1/client-config` (eski sürüm güncellemesi gerektiğini
  öğrenebilmeli). CORS ön isteği (`OPTIONS`) de denetlenmez.
- **Yönetim paneli dışarıda:** `ADMIN_HOST`'a gelen istekler uygulama
  API'sinin yönlendiricisine hiç girmez (`admin.Mux`); panel bir tarayıcı
  uygulamasıdır ve sürüm başlığı taşımaz.
- **Sıra:** CORS'tan sonra, CSRF başlığı ve token denetiminden önce. Eski
  sürüm token'sız da olsa `client-outdated` alır; web uygulaması reddi
  okuyabilsin diye CORS başlıkları yine yazılır.
- **WebSocket:** Kontrol upgrade'den önce yapılır; eski sürümün isteği
  chat'e ulaşmaz. Web istemcisi WebSocket'te başlık gönderemez; web sürümü
  REST isteklerinde denetlenir.
- **426:** Issue'daki öneriye uyuldu. RFC 9110 §15.5.22, 426 cevabının
  hangi protokole geçileceğini söyleyen bir `Upgrade` başlığı taşımasını
  ister; burada protokol değil uygulama güncellemesi istendiği için
  `Upgrade` gönderilmez. İstemci cevabı `type` alanından tanır
  (`https://tayfam.app/problems/client-outdated`); 426 yalnızca durumu
  ayırt etmeyi kolaylaştırır. Bu, standarttan bilinçli bir sapmadır.

### Sürüm karşılaştırması

Semantic Versioning 2.0.0 önceliği (§11):

- `MAJOR.MINOR.PATCH` sayısal karşılaştırılır (`1.10.0` > `1.9.0`).
- **Ön sürüm, sürümün kendisinden düşüktür:** en düşük sürüm `1.2.0` ise
  `1.2.0-rc.1` reddedilir. Bir sürümün test derlemelerinin kabul edilmesi
  isteniyorsa en düşük sürüm bir ön sürüm olarak yazılır (`1.2.0-rc.1`
  ve sonrası kabul edilir). Ön sürüm parçaları sayıysa sayı olarak,
  değilse ASCII sırasıyla karşılaştırılır; sayı, kelimeden düşüktür.
- **Derleme bilgisi (`+...`) sayılmaz:** `1.2.0+7` == `1.2.0`.
- Yeni bağımlılık eklenmedi (`golang.org/x/mod/semver` go.mod'da yok ve
  `v` öneki ister); küçük bir ayrıştırıcı `internal/gateway/app/clientversion`
  paketindedir ve tabloya dayalı testlerle korunur.

### Yapılandırma

Ortam değişkenleri, `internal/platform/config` ile:

```
CLIENT_MIN_VERSION_ANDROID     CLIENT_LATEST_VERSION_ANDROID
CLIENT_MIN_VERSION_IOS         CLIENT_LATEST_VERSION_IOS
CLIENT_MIN_VERSION_WEB         CLIENT_LATEST_VERSION_WEB
```

- **Eksik değer güvenli varsayılana düşer:** en düşük sürüm boşsa o
  platformda zorlama yoktur; son sürüm boşsa en düşük sürümü alır.
- **Geçersiz değer servisi başlatmaz:** sürüm olarak okunamayan değer ya da
  en düşük sürümden küçük son sürüm. Yanlış yazılmış bir değerle sessizce
  zorlamasız çalışmak, hatalı sürümü devrede bırakırdı.
- Ortam başına değerler `deploy/apps/<ortam>/kustomization.yaml`'daki
  `tayfam-env` configMap'indedir; değiştirmek kod değişikliği gerektirmez.

## Gerekçe

- Zorlamanın gateway'de olması, eski sürümü iç servislere hiç ulaştırmaz
  ve tek bir yerde tutar; iç servislerin bu kurala ihtiyacı yoktur.
- Açık uç yalnızca yapılandırmadan gelen sabit değerleri döndürür; istek
  başına okuduğu tek şey hız sınırı için istemci IP'sidir (log'a yazılmaz).
- Başlıksız isteği kabul etmek, araçları ve tarayıcı WebSocket'ini
  kırmadan asıl amacı (kendi eski uygulamalarımızı durdurmak) sağlar.

## Sonuçlar

- tayfam-app her REST isteğinde ve mobil WebSocket el sıkışmasında
  `X-Tayfam-Client-Version` gönderir; `client-outdated` alınca kullanıcıyı
  mağazaya yönlendirir (metinler ve mağaza bağlantıları istemcide).
- Yeni bir uygulama sürümü yayınlandığında `CLIENT_LATEST_VERSION_*`,
  eski bir sürüm devreden çıkarılacağında `CLIENT_MIN_VERSION_*` güncellenir.
- Başlığı göndermeyen (çok eski ya da başka) bir istemci denetlenemez; bu
  kabul edilmiştir.
- Web uygulaması sunucuyla birlikte dağıtıldığı için web'in en düşük sürümü
  genellikle tanımsız ya da düşük tutulur.

## Değerlendirilen alternatifler

- **Ayrı `X-Tayfam-Client-Platform` başlığı:** İki başlık birbirini tutmayabilir
  ve ikisinin de olduğunu ayrıca denetlemek gerekir; tek başlık daha basit.
- **Platformu `X-Tayfam-Client`'tan okumak:** O başlık okuma isteklerinde
  zorunlu değildir ve CSRF içindir; iki amaca bağlamak birini
  değiştirmeyi zorlaştırır.
- **Başlıksız isteği reddetmek:** Tarayıcı WebSocket'ini, curl'ü ve health
  check'leri kırar; başlık uydurulabildiği için güvenlik de getirmez.
- **Bozuk başlığı başlıksız saymak:** Hatalı bir istemci hiç fark edilmezdi;
  400 geliştirme sırasında hatayı hemen gösterir.
- **Değerleri veritabanında ve yönetim panelinde tutmak:** Kod
  değişikliği olmadan değişir, ama bir tablo, panel ekranı ve yetki
  gerektirir; configMap yeterlidir ve değişiklik Git'te izlenir.
- **`golang.org/x/mod/semver`:** Yeni bağımlılık ve `v` öneki gerektirir.
