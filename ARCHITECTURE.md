# Tayfam API — Mimari

Bu belge sisteme genel bir bakış sunar. Her başlığın gerekçesi ilgili ADR'dedir
([`docs/adr/`](docs/adr/README.md)).

## Genel görünüm

```
 Mobil istemci (Android, iOS)
        │ HTTPS (REST, WebSocket)
        ▼
 Cloudflare ── Tunnel ──► cloudflared ──► gateway ──gRPC──► identity
                                             │      ──gRPC──► core ◄──gRPC── chat
                                             └──── /realtime ──► chat
                                                             │
                     PostgreSQL (servis başına ayrı veritabanı) ◄┘
                     Cloudflare R2 (dosyalar, yedekler)
```

Kubernetes (tek node'lu k3s) üzerinde çalışan dört Go servisi vardır. Bu yapı,
mevcut ölçek için gerekenden büyüktür ve dağıtık sistem tecrübesi kazanmak için
**bilinçli olarak** seçilmiştir ([ADR-0001](docs/adr/0001-mikroservis-mimarisi.md)).

## Servisler

| Servis | Sorumluluk | Dışa açık |
|--------|------------|-----------|
| **gateway** | Tek giriş noktası: REST API, erişim token'ı doğrulama, rate limit, iç servislere yönlendirme, platform yönetim paneli (`admin.tayfam.app`) | Evet |
| **identity** | Google/Apple ile giriş, oturumlar, token'lar, KVKK talepleri, platform admin ve yaptırımlar | Hayır |
| **core** | Tayfalar, üyelik ve roller, duyurular, etkinlikler, soru-cevap, anketler, arkadaşlık ve engelleme, medya, şikâyetler | Hayır |
| **chat** | Kanallar, birebir mesajlar (E2E), WebSocket, bildirim merkezi ve push | Hayır |

Sınırlar ve veri sahipliği: [ADR-0002](docs/adr/0002-servis-sinirlari-ve-veri-sahipligi.md),
[ADR-0009](docs/adr/0009-veri-modelinin-servislere-dagilimi.md).

## İletişim

- **Dış:** REST (OpenAPI, spec-first) ve WebSocket. Hatalar RFC 9457.
- **İç, senkron:** gRPC (protobuf, buf ile yönetilir).
- **İç, asenkron:** Outbox. Servis, olayı kendi transaction'ında
  `domain_events`'e yazar; bir relay hedef servisin idempotent gRPC metodunu
  çağırır. Mesaj kuyruğu yoktur.

## Kimlik ve yetki

Google/Apple ID token → identity kendi token'larını verir: 15 dakikalık Ed25519
JWT erişim token'ı ve her kullanımda yenilenen refresh token. gateway erişim
token'ını kendisi doğrular, kullanıcı kimliğini gRPC metadata ile iletir. Tayfa
rolleri core'da, her istekte kontrol edilir
([ADR-0004](docs/adr/0004-kimlik-dogrulama-ve-yetkilendirme.md)).

## Gerçek zamanlı iletişim ve mesajlaşma

WebSocket yalnızca canlı iletim içindir; geçmiş REST ile çekilir. Kanal
mesajları uygulama seviyesinde şifreli saklanır. Birebir mesajlar Signal
protokolüyle uçtan uca şifrelidir; geçmiş, kullanıcının kendi anahtarıyla
şifreli bir arşivde tutulur ([ADR-0005](docs/adr/0005-gercek-zamanli-iletisim-ve-chat.md)).

## Veri ve dosyalar

- PostgreSQL (CloudNativePG), PostGIS, ICU `tr-TR`. Erişim pgx + sqlc,
  migration goose ([ADR-0003](docs/adr/0003-kod-yapisi-ve-sozlesmeler.md)).
- Dosyalar R2'ye istemciden doğrudan, imzalı linkle yüklenir; core işler
  (gerçek tür, EXIF/GPS temizliği, yön, iki boyut) ve görme yetkisi olana
  1 saatlik imzalı linkle sunar ([ADR-0007](docs/adr/0007-dosyalar-ve-medya.md),
  [ADR-0020](docs/adr/0020-goruntu-isleme-ve-medya-sunumu.md)).

## Dağıtım

Önce evdeki VM, sonra prod için Türkiye'de bir VPS; ikisinde de tek node'lu
k3s, Flux ile GitOps, Cloudflare Tunnel, SOPS ile şifreli secret'lar
([ADR-0006](docs/adr/0006-dagitim-ve-ortamlar.md)).

## KVKK ve platform yönetimi

Hesap silmede anında anonimleştirme, veri dışa aktarma, tanımlı saklama
süreleri, Cloudflare Access ve ayrı admin token'ıyla korunan yönetim paneli
([ADR-0008](docs/adr/0008-kvkk-ve-platform-yonetimi.md)).

## Kod yapısı

Tek Go modülü. Her servis `cmd/<servis>` ve `internal/<servis>` altında;
`transport` → `app` → `store` katmanları. Ayrıntılar ve kodlama kuralları:
[`CLAUDE.md`](CLAUDE.md).

## Geliştirme iş akışı

Issue başına bir alt oturum, PR ile birleştirme, CI zorunlu, hassas alanlarda
platform sahibinin onayı ([ADR-0010](docs/adr/0010-repo-kurallari-ve-is-akisi.md)).
