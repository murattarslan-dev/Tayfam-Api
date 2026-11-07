# ADR-0006: Dağıtım ve ortamlar

- **Durum:** Kabul edildi
- **Tarih:** 2026-11-07
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0001](0001-mikroservis-mimarisi.md), [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0004](0004-kimlik-dogrulama-ve-yetkilendirme.md)

## Bağlam

Servisler Kubernetes üzerinde çalışacak (ADR-0001). Henüz bir VPS yok ve
gerçek kullanıcılar yok. Bütçe düşük tutulmalı, ama ileride kişisel verileri
Türkiye'deki bir VPS'te barındırmak hedefleniyor. Kurulum, tek bir node'dan
başka bir node'a kod ve manifest değişmeden taşınabilmeli.

## Karar

### İki aşamalı barındırma

| Aşama | Nerede | Ne çalışır |
|-------|--------|-----------|
| **1. Ev** | Murat'ın bilgisayarında Hyper-V üzerinde bir Ubuntu VM | `dev` ortamı. Gerekirse küçük bir pilot için `prod`. |
| **2. VPS** | Türkiye lokasyonlu, KVM tabanlı bir VPS | `prod`. `dev` evdeki VM'de kalır. |

- Her makine ayrı, tek node'lu bir **k3s** cluster'ıdır.
- 2. aşamaya geçmek için yeni node'a k3s ve Flux kurulup aynı repoya
  bağlanır; Postgres yedekten geri yüklenir.
- Gerçek kullanıcı verisi taşıyan `prod`, 2. aşamadan itibaren yalnızca VPS'te
  çalışır.

### Cluster

- k3s, varsayılan ingress (Traefik) kapalı olarak kurulur; dış trafik
  Cloudflare Tunnel ile gelir.
- Her ortam kendi namespace'indedir: `tayfam-dev`, `tayfam-prod`. Altyapı
  bileşenleri kendi namespace'lerindedir (`flux-system`, `cnpg-system`,
  `observability`).
- Her pod'un CPU ve bellek istekleri (requests) ve limitleri tanımlıdır.

### Dış trafik

- Cluster içinde çalışan **cloudflared**, Cloudflare Tunnel üzerinden gelen
  trafiği yalnızca gateway servisine iletir. Makinede SSH dışında açık port
  yoktur; ev bağlantısında modemde port açılmaz.
- Alan adları: `api.tayfam.app` (prod), `api.dev.tayfam.app` (dev). Platform
  yönetim paneli `admin.tayfam.app` üzerindendir (ADR-0008).
- dev alan adı Cloudflare Access ile korunur; yalnızca Murat ve test
  cihazları erişebilir.
- Flutter web istemcisi Cloudflare Pages'te barındırılır (`app.tayfam.app`);
  bu cluster'ın kapsamı dışındadır.

### Postgres

- **CloudNativePG** operatörü kullanılır. Her ortamda tek instance'lı bir
  Postgres cluster'ı vardır; içinde servis başına veritabanı ve kullanıcı
  bulunur (ADR-0002).
- Postgres image'ı **PostGIS** içerir (konum sorguları, veri modeli v13).
  Veritabanları **ICU `tr-TR`** yerel ayarıyla oluşturulur; Türkçe büyük/küçük
  harf dönüşümleri ("İ" ↔ "i", "I" ↔ "ı") doğru çalışır.
- Yedekler Barman Cloud ile Cloudflare R2'ye alınır: sürekli WAL arşivi ve
  günlük tam yedek, 30 gün saklama. Her ortamın yedek bucket'ı ayrıdır.
- Geri yükleme en az bir kez dev ortamında denenir ve adımları runbook'a
  yazılır.

### Image'lar ve dağıtım

- GitHub Actions her servis için container image'ı derler ve private
  **GHCR**'a gönderir. Image'lar commit SHA'sı ile etiketlenir.
- Manifestler `deploy/` altında **Kustomize** ile tutulur: ortak `base` ve
  ortama özel `overlays/dev`, `overlays/prod`. Cluster tanımları
  `deploy/clusters/<cluster>/` altındadır.
- **Flux** her cluster'da çalışır ve repoyu kendisi çeker. GitHub'da cluster
  erişim anahtarı tutulmaz.
- **dev:** `main`'e giren her değişiklik, Flux image otomasyonu ile otomatik
  olarak dağıtılır.
- **prod:** Yalnızca `vX.Y.Z` formatındaki git etiketleri dağıtılır. Sürüm
  çıkarmak bilinçli bir adımdır.
- Migration'lar, her servisin pod'unda servis binary'sinin `migrate` alt
  komutunu çalıştıran bir init container ile uygulanır (ADR-0003). goose aynı
  anda yalnızca bir migration'ın çalışmasını kilitle garanti eder.

### Gizli bilgiler

- Gizli bilgiler **SOPS + age** ile şifrelenerek repoda tutulur; Flux
  cluster'da çözer.
- age özel anahtarı yalnızca cluster'da ve Murat'ın bilgisayarında bulunur.
  Her cluster'ın kendi anahtarı vardır.

### Ortam ayrımı

Ortamlar hiçbir kaynağı paylaşmaz:

| Kaynak | dev | prod |
|--------|-----|------|
| Postgres cluster | ayrı | ayrı |
| R2 bucket'ları (dosyalar, yedekler) | ayrı | ayrı |
| Google / Apple OAuth client'ları | ayrı | ayrı |
| Firebase projesi (push) | ayrı | ayrı |
| Token imzalama anahtarları | ayrı | ayrı |
| Alan adı | `api.dev.tayfam.app` | `api.tayfam.app` |

### Ağ güvenliği

- Her namespace'te varsayılan olarak tüm trafik yasaktır (default deny).
  İzinler NetworkPolicy ile açıkça tanımlanır:
  - cloudflared → gateway
  - gateway → identity, core, chat
  - chat → core; outbox relay'leri için ADR-0002'deki yönler
  - servisler → kendi Postgres'leri
  - dış internet çıkışı yalnızca ihtiyacı olan servislere: identity (Google ve
    Apple anahtarları), chat (FCM), core (R2)
- Servisler arası mTLS şimdilik yoktur. İleride bir service mesh (örneğin
  Linkerd) ile ayrı bir öğrenme adımı olarak eklenebilir.

### Gözlemlenebilirlik

- Servisler **OpenTelemetry** ile metrik, log ve trace üretir. Trace bağlamı
  gRPC çağrıları boyunca taşınır; bir isteğin gateway'den veritabanına kadar
  yolu tek trace'te görülür.
- Cluster'da bir OpenTelemetry Collector verileri toplar.
- Varsayılan hedef cluster içindeki hafif bir Grafana stack'idir (Prometheus,
  Loki, Tempo, Grafana). Bellek yetmezse yalnızca Collector ayarı değiştirilerek
  Grafana Cloud'un ücretsiz katmanına geçilir.

### Yerel geliştirme

- Günlük geliştirme için Docker Compose kullanılır: Postgres ve dört servis.
  Hızlı geri bildirim döngüsü sağlar.
- Kubernetes'e özgü davranışlar dev cluster'ında denenir.

## Gerekçe

- Ev VM'i ile başlamak, öğrenme aşamasında maliyeti sıfıra yakın tutar.
- k3s, Flux ve Tunnel birlikte, kurulumu makineden bağımsız hale getirir:
  taşıma, yeni bir node'u aynı repoya bağlamaktan ibarettir.
- dev'in 2. aşamada evde kalması, VPS'te yalnızca prod çalışmasını sağlar;
  daha küçük ve ucuz bir VPS yetebilir.
- Flux'ın repoyu kendisinin çekmesi (pull), CI'ın cluster'a erişim yetkisi
  taşımasından daha güvenlidir.

## Sonuçlar

- Ev VM'i dev ortamı için 4 vCPU, 8 GB sabit bellek ve 60 GB disk ayrılarak
  kurulur. Dinamik bellek kapalı olmalıdır.
- Elektrik veya internet kesintisinde dev ortamı erişilemez olur; bu kabul
  edilmiştir.
- Kurulum, yedekten geri yükleme ve VPS'e geçiş adımları `docs/runbooks/`
  altında belgelenir.
- Cloudflare tarafında iki tunnel, DNS kayıtları ve Access politikası
  tanımlanır.

## Değerlendirilen alternatifler

- **Hemen VPS kiralamak:** Daha güvenilir, ama gerçek kullanıcı yokken gereksiz
  maliyet.
- **Tek cluster'da dev ve prod (VPS'te):** Tek makine yönetilir, ama daha fazla
  bellek gerektirir ve dev'deki bir hata prod'u etkileyebilir.
- **Docker Compose ile dağıtım:** Daha basit, ama ADR-0001'deki öğrenme
  hedefini karşılamaz.
- **CI'dan `kubectl apply` ile dağıtım (push):** Kurulumu kolay, ama GitHub'da
  cluster yetkisi tutmayı gerektirir.
- **Postgres'i cluster dışında, doğrudan VM'de çalıştırmak:** İşletmesi basit,
  ama operatör tabanlı veritabanı yönetimi ve R2'ye yedekleme deneyimini
  kaybettirir.
- **Workers VPC:** Worker'ların özel ağdaki servislere erişmesini sağlar;
  gateway Worker'da çalışmadığı için bu mimaride karşılığı yoktur.
