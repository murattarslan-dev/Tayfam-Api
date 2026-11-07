# Tayfam API

Tayfam'ın Go backend'i: kulüplerin ve arkadaş gruplarının etkinlik, duyuru,
soru-cevap ve sohbet uygulaması.

- Mimari özet: [ARCHITECTURE.md](ARCHITECTURE.md)
- Kararlar: [docs/adr/](docs/adr/README.md)
- Kodlama kuralları ve iş akışı: [CLAUDE.md](CLAUDE.md)

## Hızlı başlangıç

Gerekenler: Go (sürüm `go.mod`'da), isteğe bağlı olarak Docker ve
[golangci-lint](https://golangci-lint.run/) v2.

```sh
make dev                              # dört servisi Docker olmadan çalıştırır
curl http://localhost:8080/v1/status  # gateway ve iç servislerin durumu
```

Docker ile:

```sh
make up
```

PR açmadan önce:

```sh
make check   # derleme + testler (race) + lint
```

## Servisler

| Servis | Dış adres | Ops (probe) |
|--------|-----------|-------------|
| gateway | `:8080` | `:8081` |
| identity | — | `:8081` |
| core | — | `:8081` |
| chat | — | `:8081` |

Her servis ops portunda `GET /livez` ve `GET /readyz` sunar.

## Ortak ortam değişkenleri

| Değişken | Varsayılan | Açıklama |
|----------|-----------|----------|
| `TAYFAM_ENV` | `local` | `local`, `dev` veya `prod` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `OPS_ADDR` | `:8081` | Probe sunucusunun adresi |
| `SHUTDOWN_DRAIN_DELAY` | `0s` | Kapanışta trafiğin boşalması için bekleme |
| `SHUTDOWN_TIMEOUT` | `15s` | Açık isteklerin tamamlanması için süre |

## Lisans ve güvenlik

Kod [MIT](LICENSE) lisanslıdır; kendi sunucunda ücretsiz çalıştırabilirsin.
"Tayfam" adı ve logosu lisans kapsamında değildir. Güvenlik açıklarını
herkese açık issue yerine [SECURITY.md](SECURITY.md)'deki yolla bildir.
