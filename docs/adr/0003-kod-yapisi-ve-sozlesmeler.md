# ADR-0003: Kod yapısı, sözleşmeler ve veritabanı erişimi

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0001](0001-mikroservis-mimarisi.md), [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md)

## Bağlam

Dört servis (gateway, identity, core, chat) tek repoda yaşayacak. Kodun büyük
kısmı ayrı ajan oturumlarında, paralel PR'larla yazılacak. Bu yüzden sınırların
derleyici ve linter tarafından zorlanması, sözleşmelerin tek bir doğruluk
kaynağından üretilmesi gerekir.

## Karar

### Monorepo düzeni

Tek Go modülü: `github.com/murattarslan-dev/tayfam-api`

```
cmd/
  gateway/  identity/  core/  chat/     # sadece main.go: config, bağımlılıkları kur, çalıştır
internal/
  gateway/  identity/  core/  chat/     # servise özel kod
  platform/                             # ortak altyapı: config, log, db, grpc yardımcıları, outbox, token doğrulama
proto/tayfam/<servis>/v1/*.proto        # iç gRPC sözleşmeleri
gen/                                    # proto'dan üretilen Go kodu (repoya commit edilir)
api/openapi/gateway.yaml                # dış REST sözleşmesi
db/<servis>/migrations/                 # goose migration'ları
db/<servis>/queries/                    # sqlc sorguları
docs/adr/
```

### Servis içi katmanlar

Her `internal/<servis>/` altında:

- `transport/`: gRPC handler'ları (gateway'de HTTP ve WebSocket). Sadece
  dönüştürme yapar, iş mantığı içermez.
- `app/`: iş mantığı. Taşıma katmanından (gRPC/HTTP) bağımsızdır, arayüzlerle
  çalışır.
- `store/`: sqlc ile üretilen sorgular ve repository'ler.

core'da `app/` ve `store/` ayrıca domain paketlerine bölünür: `groups`,
`events`, `listings`, `qa`.

### Bağımlılık kuralları

- Bir servis başka bir servisin `internal/` paketini import edemez. Diğer
  servislere yalnızca `gen/` altındaki gRPC client'ları ile erişir.
- `internal/platform/` hiçbir servis paketini import edemez ve domain mantığı
  içermez.
- Bu kurallar `golangci-lint` içindeki `depguard` ile CI'da zorlanır.

### İç sözleşme: protobuf + gRPC

- Proto dosyaları **buf** ile yönetilir: lint, kod üretimi ve `buf breaking`
  ile geriye uyumsuz değişiklik kontrolü.
- Kod üretimi `protoc-gen-go` ve `protoc-gen-go-grpc` ile yapılır; standart
  `google.golang.org/grpc` kullanılır.
- Paketler sürümlüdür (`tayfam.core.v1`). Geriye uyumsuz değişiklik yeni sürüm
  paketi gerektirir.
- Üretilen kod repoya commit edilir; CI, üretilen kodun güncel olduğunu
  kontrol eder.

### Dış sözleşme: OpenAPI

- gateway'in REST API'si **spec-first** yazılır: önce `api/openapi/gateway.yaml`
  güncellenir, sonra kod.
- Go tarafı `oapi-codegen` (strict server) ile üretilir. Flutter client'ı da
  aynı spec'ten üretilecek (istemci tarafı ADR'sinde netleşecek).
- Hata yanıtları RFC 9457 (`application/problem+json`) formatındadır.
- HTTP katmanı standart `net/http` router'ı ile yazılır.

### Veritabanı erişimi

- Sürücü: **pgx v5**. ORM kullanılmaz.
- Sorgular elle SQL olarak yazılır, **sqlc** ile tip güvenli Go koduna
  dönüştürülür.
- Migration'lar **goose** ile, her servis için ayrı klasörde tutulur.
  Migration'lar servis binary'sine gömülür ve `migrate` alt komutuyla
  çalıştırılır. Ne zaman çalıştırılacağı dağıtım ADR'sinde belirlenecek.

## Gerekçe

- Tek modül, servisler arası ortak kodu ve sürüm yönetimini basit tutar.
- Go'nun `internal/` kuralı ve depguard, servis sınırlarını kurala değil araca
  bırakır. Ajanların yanlışlıkla sınır ihlali yapması CI'da yakalanır.
- İş mantığının taşıma katmanından ayrı olması, ADR-0001'deki geri alınabilirlik
  ilkesini sağlar ve test yazmayı kolaylaştırır.
- buf'ın breaking-change kontrolü, servisler arası sözleşmenin sessizce
  bozulmasını engeller.

## Sonuçlar

- Kod üretimi (buf, oapi-codegen, sqlc) için bir `make generate` hedefi ve
  CI kontrolü gerekir.
- Proto veya OpenAPI değişikliği içeren PR'lar, sözleşme değişikliği olarak
  ayrıca dikkatle incelenir.

## Değerlendirilen alternatifler

- **Servis başına ayrı Go modülü:** Daha bağımsız, ama tek kişilik projede
  sürüm ve bağımlılık yönetimini gereksiz yere zorlaştırır.
- **ORM (GORM, ent):** Daha az SQL yazımı, ama sorgular görünmez hale gelir.
  SQL'i doğrudan öğrenme ve kontrol etme tercih edildi.
- **chi router:** Olgun, ama Go 1.22'den beri standart router metod ve path
  eşleştirmeyi destekliyor; ek bağımlılığa gerek yok.
- **Connect-Go (gRPC yerine):** Daha modern, ama standart gRPC'yi öğrenmek
  hedefin parçası.
