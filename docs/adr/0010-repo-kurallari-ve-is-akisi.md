# ADR-0010: Repo kuralları ve geliştirme iş akışı

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Revizyon:** 2026-11-07:
  - Dal koruması ve ayrı PR kimliği askıya alındı.
  - Repolar public oldu ve MIT lisansıyla yayınlanır. `main`, onay zorunluluğu
    olmayan bir kural setiyle korunur; ayrı PR kimliğine gerek kalmadı (bkz.
    "Dal koruması").
- **Revizyon:** 2026-11-07 (2): Pilot öncesine kadar süreli esneme; hassas
  alanların çoğunu mimari oturumu birleştirebilir (bkz. "Pilot öncesi esneme").
- **Revizyon:** 2026-11-10: Esnemenin süresi platform sahibinin kararıyla
  "ikinci bir emre kadar" oldu; pilot veya prod'un açılması onu kendiliğinden
  bitirmez.
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0003](0003-kod-yapisi-ve-sozlesmeler.md), [ADR-0006](0006-dagitim-ve-ortamlar.md)
- **Kaynak:** Notion: Proje Planı, "Geliştirme İş Akışı" bölümü

## Bağlam

Kodun büyük kısmı Claude Code alt oturumlarında yazılacak ve PR olarak
açılacak. Bir mimari oturumu PR'ları ADR'lere ve kodlama kurallarına göre
inceleyecek. İnceleme sohbet hafızasına değil, repodaki dosyalara dayanmalıdır.
Güvenlik açısından hassas alanlarda son söz platform sahibinde olmalıdır.

Karar aşamasında (ADR-0001–0010) ADR'ler doğrudan `main`'e yazıldı. Bu ADR,
kod aşamasından itibaren geçerli olan kuralları belirler.

## Karar

### Repolar ve belgeler

- API ve istemci ayrı **public** repolardadır: `Tayfam-Api` ve istemci reposu.
  Kod MIT lisanslıdır: isteyen kendi sunucusunda ücretsiz çalıştırabilir. Gelir
  modeli kodun değil, Tayfam'ın işlettiği sunucunun kullanımıdır. "Tayfam" adı
  ve logosu lisans kapsamında değildir.
- Public repo nedeniyle:
  - Repoya ve geçmişine hiçbir gizli bilgi girmez; GitHub secret scanning ve
    push protection açıktır. SOPS ile şifrelenmiş secret'lar repoda durabilir,
    age özel anahtarı asla.
  - Güvenlik, adreslerin veya yapılandırmanın gizli olmasına dayanmaz
    (örneğin `admin.tayfam.app` ADR'lerde yazılıdır; korumayı Cloudflare
    Access ve admin token'ı sağlar).
  - Güvenlik açıkları `SECURITY.md`'deki yolla, herkese açık issue yerine özel
    olarak bildirilir.
  - CI yalnızca `pull_request` olayıyla çalışır; fork'tan gelen PR'lar yazma
    yetkisi ve secret'lara erişim almaz. `pull_request_target` ve self-hosted
    runner kullanılmaz.
- Her repoda:
  - `ARCHITECTURE.md`: sisteme genel bakış ve ADR'lere bağlantılar.
  - `docs/adr/`: gerekçeli karar kayıtları. Kararı değiştiren her PR yeni bir
    ADR ekler veya mevcut ADR'yi "Yerine geçildi" olarak işaretler.
  - `CLAUDE.md`: kodlama kuralları, klasör yapısı, güvenlik ilkeleri, test
    beklentileri. Alt oturumların ve incelemenin tek kural kaynağıdır.
  - `docs/runbooks/`: kurulum, yedekten geri yükleme ve işletim adımları.

### İş paketleri

- Mimari, küçük iş paketlerine bölünür; her paket bir GitHub issue'dur. Issue,
  kabul kriterlerini ve ilgili ADR'leri içerir.
- Her alt oturum tek bir issue alır; kodu ve testleri yazar, issue'ya bağlı bir
  PR açar.
- PR şablonu şunları ister: ilgili ADR'ler, yapılan değişiklik, test kanıtı,
  güvenlik kontrol listesi ve kararla çelişen bir şey olup olmadığı.

### Dal ve birleştirme kuralları

- `main` korumalıdır: doğrudan push yok, yalnızca PR ile birleştirme.
- Birleştirme yöntemi squash'tır; commit mesajları Conventional Commits
  biçimindedir (`feat:`, `fix:`, `docs:` …).
- Mimari oturumu, rutin PR'ları ADR'lere ve `CLAUDE.md`'ye göre inceler ve
  birleştirir.
- Hassas alanlara dokunan PR'larda mimari oturumu inceleme yorumunu yazar,
  **son onayı ve birleştirmeyi platform sahibi yapar.**

### Hassas alanlar (CODEOWNERS)

Aşağıdaki yollara dokunan PR'lar platform sahibinin onayı olmadan
birleştirilemez:

- Kimlik doğrulama: `internal/identity/`, `proto/tayfam/identity/`
- Yetki kontrolleri: core'daki izin tablosu ve rol/üyelik kodu
- E2E ve DM arşivi: chat'teki anahtar, zarf ve arşiv kodu
- Tüm migration'lar: `db/*/migrations/` (veri silen migration'lar dahil)
- Platform yönetim paneli: gateway'deki admin yönlendiricisi ve `AdminService`
  proto'ları
- Ödeme (Faz 2)
- Dağıtım ve güvenlik yapılandırması: `deploy/` (secret'lar, NetworkPolicy),
  `.github/workflows/`, `CODEOWNERS`, `CLAUDE.md`

### CI kontrolleri

CI geçmeden birleştirme yapılamaz. Zorunlu kontroller:

- `go test -race ./...` ve Postgres gerektiren testler için testcontainers
- `golangci-lint` (servis sınırları için `depguard` dahil, ADR-0003)
- `govulncheck` ve `gosec`
- `buf lint` ve `main`'e karşı `buf breaking`
- Kod üretiminin güncel olması: `make generate` sonrası değişiklik kalmamalı
- Secret taraması (gitleaks)
- Container image'larının derlenebilmesi

Bağımlılık güncellemeleri Dependabot ile haftalık PR olarak gelir.

### Test beklentileri

- `app` katmanı birim testleriyle, `store` katmanı gerçek Postgres'e karşı
  entegrasyon testleriyle test edilir.
- İzin tablosu, her rol ve işlem için tabloya dayalı testlerle korunur.
- Servisler arası olaylar için uçtan uca testler bulunur (örneğin hesap
  silindikten sonra hiçbir serviste kişisel kayıt kalmaması, ADR-0009).
- Bir hata düzeltmesi, hatayı yeniden üreten bir testle gelir.

### Dal koruması

`main` bir GitHub kural setiyle (ruleset) korunur:

- Silme ve force push yasaktır.
- Değişiklik yalnızca PR ile girer; birleştirme yöntemi squash'tır.
- Zorunlu CI kontrolleri geçmeden birleştirme yapılamaz.
- **Onay zorunluluğu yoktur.** Tek geliştiricili bu projede PR'ı açan ile
  birleştiren aynı hesap olabilir; ayrı bir bot veya GitHub App kimliği
  gerekmez.
- Kural setinde istisna (bypass) tanınmaz.

Kural seti dışarıdan gelenlere karşı değil, ajanlara ve hatalara karşıdır:
yetki verilmemiş biri zaten push yapamaz, ancak alt oturumlar platform
sahibinin hesabıyla çalışır.

Onay zorunlu olmadığı için `CODEOWNERS` GitHub tarafından zorlanmaz; hassas
alanların listesi olarak incelemede kullanılır. Hassas alanlara dokunan PR'larda
mimari oturumu inceleme yorumunu yazar ve platform sahibine haber verir;
**birleştirmeyi yalnızca platform sahibi yapar.** Mimari oturumu, platform
sahibinin hesabını kullanıyor olsa da bu PR'ları birleştirmez.

### Pilot öncesi esneme

> **Geçerlilik:** 2026-11-07'den platform sahibinin ikinci bir emrine kadar
> (karar 2026-11-10). Pilot, prod ortamının açılması veya ilk gerçek kullanıcı
> verisi esnemeyi kendiliğinden bitirmez. Platform sahibi kaldırdığında bu
> bölüm silinir ve yukarıdaki kural tam olarak geri gelir. Esneme prod
> dağıtımını kapsamaz: prod'a yalnızca platform sahibinin attığı `vX.Y.Z`
> etiketleri gider (ADR-0006).

Henüz gerçek kullanıcı verisi yoktur ve `main` yalnızca ev cluster'ındaki dev
ortamına dağıtılır; bir hatanın verebileceği zarar sınırlıdır. Bu dönemde
platform sahibinin her PR'ı birleştirmesi işi gereksiz yavaşlattığı için:

- **Mimari oturumu birleştirebilir:** `docs/adr/`, `internal/identity/`,
  `internal/core/app/tayfa/`, `internal/core/app/authz/`, `internal/chat/app/`,
  `internal/platform/auth/`, `proto/`, `db/*/migrations/`, `deploy/base/`,
  `deploy/apps/`. Şartlar: CI'ın tamamı yeşil, değişikliğin içeriği platform
  sahibiyle sohbette konuşulmuş ve PR açıklamasında "Sohbette onaylandı"
  notu ile hangi kararın uygulandığı yazılı. Platform sahibi bu PR'ları
  istediği zaman sonradan inceler.
- **Yalnızca platform sahibi birleştirir** (güven zincirinin kökü; buradaki
  bir hata diğer bütün korumaları geçersiz kılar):
  - `.github/` (CI iş akışları, imzalama, `CODEOWNERS`)
  - `deploy/clusters/` (Flux'ın hangi kaynağa ve imzaya güvendiği, çözme ayarı)
  - `.sops.yaml` (secret'ları kimin çözebileceği) ve
    `scripts/new-signing-key.sh`
  - `CLAUDE.md` (kesin kurallar ve güvenlik ilkesi)
- Bir PR iki gruba birden dokunuyorsa platform sahibi birleştirir; mümkünse
  ikiye bölünür.
- Bu esneme kendi kendini genişletemez: bu bölümü veya listeleri değiştiren
  PR `CLAUDE.md`'ye de dokunacağı için platform sahibine kalır.

### Sürümleme

- `main` dev ortamına otomatik dağıtılır; prod'a yalnızca `vX.Y.Z` etiketleri
  dağıtılır (ADR-0006). Etiketi platform sahibi atar.

## Gerekçe

- Kuralların repoda yazılı olması, birbirinden habersiz alt oturumların aynı
  standartlarda çalışmasını ve incelemenin tekrarlanabilir olmasını sağlar.
- Hassas alanlarda insan onayı, otomatik incelemenin kaçırabileceği güvenlik
  hatalarına karşı son katmandır.
- Küçük, tek amaçlı PR'lar hem incelemeyi hem geri almayı kolaylaştırır.

## Sonuçlar

- Kod aşamasının ilk işi: `CLAUDE.md`, `ARCHITECTURE.md`, `CODEOWNERS`, PR ve
  issue şablonları ve CI iş akışı.
- Hassas alan kuralı GitHub tarafından zorlanmadığı için `CLAUDE.md`'ye ve
  incelemeye dayanır; alt oturumlara verilen talimatlar bu kuralı açıkça
  içermelidir.

## Değerlendirilen alternatifler

- **Tüm PR'ları platform sahibinin onaylaması:** En güvenli, ama rutin
  değişikliklerde darboğaz yaratır.
- **Private repo ve GitHub Pro:** Kod gizli kalır, ama dal koruması için
  ücretli plan gerekir; projenin açık kaynak hedefiyle de örtüşmez.
- **AGPL veya ticari kullanıma kapalı bir lisans:** Kapalı ticari kopyaları
  caydırır, ama platform sahibi kodun serbestçe kullanılmasını tercih etti.
- **Onay zorunlu kural seti ve bot kimliği:** CODEOWNERS'ı GitHub'a zorlatır,
  ama tek geliştirici için ek kimlik yönetimi getirir.
- **Doğrudan `main`'e çalışmaya devam etmek:** Hızlı, ama alt oturumların
  paralel çalıştığı bir düzende inceleme ve CI kapısını ortadan kaldırır.
