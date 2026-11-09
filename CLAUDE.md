# CLAUDE.md — Tayfam API

Bu dosya, bu repoda çalışan her oturumun (insan ya da ajan) uyması gereken
kuralları içerir. İnceleme bu dosyaya ve `docs/adr/` altındaki kararlara
göre yapılır. Bir kuralla çelişen bir şey yapman gerekiyorsa **dur, PR
açıklamasında açıkça belirt**; sessizce sapma.

## Proje

Repo public'tir ve MIT lisanslıdır. Yazdığın her şeyin herkes tarafından
okunacağını varsay: koda, testlere, commit mesajlarına ve PR açıklamalarına
gerçek kullanıcı verisi, secret veya iç adres/şifre girmez.

Tayfam; kulüplerin ve arkadaş gruplarının (tayfaların) etkinlik, duyuru,
soru-cevap ve sohbet ihtiyaçlarını tek uygulamada karşılar. Bu repo Go
backend'idir.

- Mimari özet: [`ARCHITECTURE.md`](ARCHITECTURE.md)
- Kararlar ve gerekçeleri: [`docs/adr/`](docs/adr/README.md)
- Ürün kararları ve veri modeli (v14): Notion'daki "Tayfam — Proje Planı".
  Veri modelinin servislere dağılımı ADR-0009'dadır.

## Kararlar ve Notion

Ürün kararlarının kaynağı Notion'daki **"Tayfam — Proje Planı"** sayfası ve alt
sayfalarıdır (veri modeli, özellik haritası, KVKK aydınlatma metni, platform
yönetim paneli, topluluk kuralları, kötüye kullanım ve genç kullanıcı
güvenliği). Mimari kararların kaynağı `docs/adr/`'dir.

- **Karar almadan önce oku.** Bir özellik tasarlamadan, tablo veya alan
  eklemeden, bir kuralı yorumlamadan ya da seçenek önermeden önce Notion'daki
  ilgili sayfayı ve ilgili ADR'leri oku (Notion MCP bağlantısıyla). Notion'da
  zaten verilmiş bir kararı yeniden tasarlama.
- **Çelişkiyi sakla değil, söyle.** Notion ile ADR veya kod çelişiyorsa ya da
  Notion'daki karardan sapmak gerekiyorsa bunu açıkça belirt ve platform
  sahibine sor. Sapma kabul edilirse hem ADR'ye hem Notion'daki ilgili sayfaya
  yazılır (örnek: ADR-0012'deki şifreli doğum tarihi, veri modeli v14'te not
  olarak duruyor).
- **Yeni kararı yaz.** Platform sahibiyle birlikte yeni bir karar alınırsa
  aynı oturumda Notion'daki ilgili sayfaya işlenir (ürün kararı) ve gerekiyorsa
  ADR eklenir veya güncellenir (mimari karar). Notion'daki proje planının yol
  haritası tamamlanan işlerle güncel tutulur.
- **Erişim yoksa varsayma.** Notion'a ulaşılamıyorsa bunu söyle; Notion'da ne
  yazdığını tahmin ederek ilerleme.
- Notion'a gerçek kullanıcı verisi, secret veya iç adres/parola yazılmaz;
  repoyla aynı kural geçerlidir.

## Güvenlik ilkesi

Hedef: sisteme sızmak olabildiğince zor olmalı; **sızılsa bile verilebilecek
hasar en aza inmeli** ([ADR-0011](docs/adr/0011-guvenlik-ilkesi-ihlal-varsayimi.md)).
Yazdığın her şeyi bir saldırganın gözüyle düşün:

- **İhlal varsay.** Diğer katmanların aşılmış olabileceğini varsay. "Gateway'den
  geçti", "içeriden geliyor", "istemci zaten kontrol ediyor" gerekçe değildir.
- **En az yetki.** Kod, kullanıcı, token ve CI işi yalnızca gerekene erişsin;
  yetkiyi geniş verip sonra daraltmayı planlama.
- **Hasarı sınırla.** "Bu bileşen ele geçirilirse neye ulaşır?" sorusunun
  cevabı dar olmalı. Cevap genişse tasarımı değiştir ve PR'da yaz.
- **Tutmadığın veri çalınamaz.** Gerekmeyen veriyi saklama, loglama, döndürme.
- **Doğrula.** Kimliği, imzayı, girdiyi her katmanda yeniden doğrula.

## Kesin kurallar

Bunların ihlali PR'ın reddedilmesi için yeterlidir.

1. **Servis sınırları.** Bir servis (`gateway`, `identity`, `core`, `chat`)
   başka bir servisin `internal/<servis>/` paketini import edemez. Diğer
   servislere yalnızca `gen/` altındaki gRPC client'larıyla erişilir.
   `internal/platform/` hiçbir servis paketini import edemez ve iş mantığı
   içermez. Bu kurallar `golangci-lint` (depguard) ile zorlanır.
2. **Veritabanı sahipliği.** Her servis yalnızca kendi veritabanına bağlanır.
   Başka servisin verisi gerekiyorsa gRPC çağrısı veya olay (outbox) kullanılır
   (ADR-0002, ADR-0009). Veritabanları arası foreign key yoktur.
3. **Kişisel veri log'a girmez.** Log, metrik ve trace'lere mesaj metni,
   konum, e-posta, token, refresh token, davet token'ı, gizli anket oyu
   yazılmaz. Kullanıcı konumu hiçbir yerde saklanmaz ve loglanmaz (ADR-0008). Kullanıcının bilerek paylaştığı yer (buluşma noktası, konvoy rotası, av yeri) içeriktir ve saklanabilir; cihazın anlık konumu saklanmaz (ayrım ADR-0008'de).
4. **Kendi kriptografini yazma.** E2E için Signal protokolü kullanılır.
   Kanal mesajı şifrelemesi AES-256-GCM ile, standart kütüphaneyle yapılır.
   Rastgele değerler yalnızca `crypto/rand` ile üretilir.
5. **Yetki her istekte sunucuda kontrol edilir.** Tayfaya ait her sorgu
   `tayfa_id` ve üyelik kontrolüyle çalışır (IDOR'a karşı). İzinler core'daki
   tek izin tablosundan okunur; varsayılan cevap "hayır"dır (ADR-0004).
6. **Gizli bilgi repoya girmez.** Anahtarlar, token'lar ve parolalar ortam
   değişkeninden okunur; `deploy/apps/<ortam>/secrets/` altında yalnızca SOPS
   ile şifrelenmiş halleri bulunur (ADR-0006). Gizli değerler platform
   sahibinin bilgisayarında üretilip doğrudan sops'a verilir; açık halleri
   diske, terminale, sohbete veya log'a yazılmaz (`docs/runbooks/sops.md`).
   age özel anahtarı GitHub'a (Actions secret dahil) hiçbir zaman eklenmez.
7. **Sözleşme önce gelir.** REST değişikliği önce `api/openapi/gateway.yaml`'a,
   gRPC değişikliği önce `proto/`'ya yazılır; sonra kod üretilir
   (`make generate`) ve üretilen kod commit edilir (ADR-0003). buf eklentileri
   `go.mod`'daki `tool` direktifleriyle sabittir; CI `gen/`'in güncel olduğunu
   ve proto'nun geriye uyumlu kaldığını (`buf breaking`) kontrol eder.
8. **İç servisler kimliği kendileri doğrular.** gateway'in ilettiği kullanıcı
   kimliğine güvenilmez; her servis JWT imzasını kendisi doğrular (ADR-0011).
   gRPC sunucuları `auth.Guard` ile kurulur ve her metod varsayılan olarak
   geçerli token ister. Token istemeyen (açık) bir metod eklemek hassas alan
   değişikliğidir; PR'da gerekçelendirilir ve testle sabitlenir. Doğrulanmış
   kullanıcı `auth.CallerFrom(ctx)` ile okunur; zincirleme çağrılarda token
   `auth.ForwardToken` ile olduğu gibi iletilir.
9. **Bağımlılıklar sabitlenir.** GitHub Actions commit SHA'sıyla, container
   image'ları sürüm etiketiyle (tercihen digest) sabitlenir; `latest`, `main`
   gibi kayan referanslar kullanılmaz. Mevcut CI'daki etiketle sabitlenmiş
   Actions, ADR-0011 sertleştirme listesindeki 3. maddeyle düzeltilir.

## Klasör yapısı

```
cmd/<servis>/main.go        yalnızca kurulum: config, bağımlılıklar, çalıştırma
internal/<servis>/
  transport/                gRPC handler'ları (gateway'de HTTP/WebSocket); dönüştürme yapar, iş mantığı içermez
  app/                      iş mantığı; taşıma katmanından bağımsız
  store/                    sqlc ile üretilen sorgular ve repository'ler
internal/platform/          ortak altyapı: config, log, sunucular, health, outbox
proto/tayfam/<servis>/v1/   iç gRPC sözleşmeleri
gen/                        üretilmiş kod (elle düzenlenmez)
api/openapi/gateway.yaml    dış REST sözleşmesi
db/<servis>/migrations/     goose migration'ları
db/<servis>/queries/        sqlc sorguları
deploy/                     Kustomize manifestleri ve Flux tanımları
docs/adr/                   mimari kararlar
docs/runbooks/              işletim adımları
```

## Kodlama kuralları

- **Go sürümü** `go.mod`'daki sürümdür. Yeni bağımlılık eklemeden önce standart
  kütüphanenin yetip yetmediğine bak; eklediğin her bağımlılığı PR'da gerekçele.
- **Context** her I/O yapan fonksiyonun ilk parametresidir. Goroutine'ler
  context ile sonlandırılabilir olmalıdır.
- **Hatalar** `fmt.Errorf("...: %w", err)` ile sarılır. İstemciye dönen hatalar
  RFC 9457 (`application/problem+json`) biçimindedir ve iç ayrıntı sızdırmaz.
- **Log** `log/slog` ile, JSON biçiminde yazılır. Global logger kullanılmaz;
  logger bağımlılık olarak geçirilir.
- **Global durum yok.** Paket seviyesinde değiştirilebilir değişken
  kullanılmaz; bağımlılıklar `main.go`'da kurulup açıkça geçirilir.
- **Config** yalnızca ortam değişkenlerinden, `internal/platform/config` ile
  okunur. Eksik zorunlu değer servisin başlamasını engeller.
- **Zaman** her yerde UTC `time.Time`, veritabanında `timestamptz`.
- **ID'ler** UUIDv7'dir ve sunucuda üretilir. İstemcinin ürettiği değer yalnızca
  `client_request_id` olarak tekrar gönderim korumasında kullanılır.
- **Girdi doğrulama** hem Go'da hem veritabanında (`CHECK`) yapılır. Uzunluk
  sınırları veri modeli v13'teki tablodadır.
- **Türkçe metin.** Tekillik gereken isimlerde `name_normalized` alanı
  `strings.ToLowerSpecial(unicode.TurkishCase, …)` ile üretilir.
- **SQL** elle yazılır ve sqlc ile üretilir; string birleştirerek sorgu
  kurulmaz. ORM kullanılmaz.
- **Silme.** İçerik tablolarında soft delete (`deleted_at`, `deleted_by`); kalıcı
  silme 30 gün sonra arka plan işiyle yapılır.
- **Servisler arası yan etkiler** aynı transaction içinde `domain_events`'e
  yazılır; tüketici metodlar idempotent olmalıdır (ADR-0002).
- **Arayüz metinleri** API'den Türkçe dönmez; istemci çeviri dosyalarını
  kullanır. API hata kodları makine tarafından okunabilir (`type` alanı).

## Test beklentileri

- Her PR testleriyle gelir. `app` katmanı birim testleriyle, `store` katmanı
  gerçek Postgres'e karşı test edilir: `internal/platform/db/dbtest` her test
  için geçici bir veritabanı açar. Sunucu `TEST_DATABASE_URL` ile verilir;
  yerelde `make test-db`, CI'da Postgres service container'ı. CI'da
  `REQUIRE_TEST_DATABASE` ayarlı olduğu için bu testler atlanamaz.
- İzin tablosu her rol ve işlem için tabloya dayalı testlerle korunur.
- Hata düzeltmesi, hatayı yeniden üreten bir testle gelir.
- `go test -race ./...` yerelde geçmeden PR açılmaz.

## İş akışı

- Her iş bir GitHub issue'dur. Bir oturum yalnızca bir issue üzerinde çalışır.
- Dal adı: `<tür>/<kısa-açıklama>` (`feat/tayfa-olusturma`, `fix/token-yenileme`).
- Commit mesajları Conventional Commits biçimindedir (`feat:`, `fix:`,
  `docs:`, `refactor:`, `test:`, `chore:`).
- PR şablonundaki her başlık doldurulur.
- **`main`'e doğrudan push yapılmaz.** `main` bir kural setiyle korunur
  (ADR-0010).
- **CI geçmeden hiçbir PR birleştirilmez.**
- **Platform sahibinin işi en aza indirilir.** Cluster komutları, panel
  ayarları ve hesap işlemleri gibi yalnızca platform sahibinin yapabileceği
  işler, yapılabilecek her şey önceden hazırlandıktan sonra tek bir listede,
  kopyalanıp çalıştırılabilir halde istenir. Aynı şey iki kez istenmez;
  cevabı alınmış bir adım tekrar sorulmaz.
- **Hassas alanlar** (`.github/CODEOWNERS`'taki yollar) değiştiren PR'ları
  yalnızca platform sahibi birleştirir. Bu PR'ları açarken açıklamaya
  "Hassas alan" etiketini ve hangi alana dokunduğunu yaz.
- **Pilot öncesi esneme (süreli, ADR-0010):** İlk gerçek kullanıcı verisine
  kadar mimari oturumu hassas alanların çoğunu birleştirebilir: CI tamamen
  yeşil olmalı, içerik platform sahibiyle sohbette konuşulmuş olmalı ve PR
  açıklamasında "Sohbette onaylandı" notu bulunmalıdır. `.github/`,
  `deploy/clusters/`, `.sops.yaml`, `scripts/new-signing-key.sh` ve
  `CLAUDE.md` her zaman yalnızca platform sahibine kalır. Pilottan önce bu
  madde kaldırılır.
- Mimari bir kararı değiştiren PR, yeni bir ADR ekler.

## Komutlar

```sh
make generate   # proto/ değişince gen/ altındaki kodu üretir (buf gerekir)
make build      # tüm servisleri derler
make test       # go test -race ./...
make lint       # golangci-lint
make check      # build + test + lint (PR açmadan önce)
make run SERVICE=gateway   # bir servisi yerelde çalıştırır
make up         # docker compose ile yerel ortam (geçici imzalama anahtarıyla)
make dev        # dört servisi Docker olmadan yerelde çalıştırır
```
