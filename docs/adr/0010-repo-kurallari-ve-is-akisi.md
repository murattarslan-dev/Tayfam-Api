# ADR-0010: Repo kuralları ve geliştirme iş akışı

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
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

- API ve istemci ayrı private repolardadır: `Tayfam-Api` ve istemci reposu.
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

### Ön koşullar

Bu kuralların uygulanabilmesi için iki şey gerekir:

1. **Dal koruması:** GitHub, private repolarda dal korumasını yalnızca ücretli
   planlarda (Pro, Team, Enterprise) sunar. Repo private kalacaksa hesabın
   GitHub Pro'ya geçmesi gerekir.
2. **Ayrı bir PR kimliği:** GitHub'da PR'ı açan kişi kendi PR'ını onaylayamaz.
   Alt oturumlar PR'ları platform sahibinin hesabıyla açarsa, hassas
   alanlardaki CODEOWNERS onayı verilemez. Bu yüzden PR'lar ayrı bir kimlikle
   (bir GitHub App veya bot hesabı) açılır; platform sahibi onaylayan taraf
   olur.

Ön koşullar sağlanana kadar kurallar CI ve PR şablonu ile uygulanır; dal
koruması ön koşullar sağlanınca açılır.

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
- GitHub Pro ve PR kimliği kararları platform sahibine aittir.

## Değerlendirilen alternatifler

- **Tüm PR'ları platform sahibinin onaylaması:** En güvenli, ama rutin
  değişikliklerde darboğaz yaratır.
- **Repoyu public yapmak:** Dal koruması ücretsiz olur, ama kod ve altyapı
  yapılandırması herkese açılır.
- **Doğrudan `main`'e çalışmaya devam etmek:** Hızlı, ama alt oturumların
  paralel çalıştığı bir düzende inceleme ve CI kapısını ortadan kaldırır.
