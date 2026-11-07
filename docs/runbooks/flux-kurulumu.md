# Flux kurulumu (ev cluster'ı)

Ev cluster'ının `main`'deki her değişikliği kendiliğinden almasını sağlar
(ADR-0006). Önce [ev cluster'ı kurulumu](ev-cluster-kurulumu.md) tamamlanmış ve
`cloudflared-token` secret'ı oluşturulmuş olmalı.

## Nasıl çalışır

```
main'e commit ──► CI: image'lar (tayfam-<servis>:<sha>)
                  CI: deploy/ → oci://ghcr.io/murattarslan-dev/tayfam-deploy:<sha>, "dev" etiketi
                                      │
ev cluster'ı: Flux OCIRepository ─────┘ (dakikada bir "dev" etiketine bakar)
              Flux Kustomization ──► deploy/apps/dev'i uygular
```

CI cluster'a hiç bağlanmaz; cluster kaynağı kendisi çeker.

## 1. Flux CLI

Ubuntu'da:

```bash
curl -s https://fluxcd.io/install.sh | sudo bash
flux --version
flux check --pre
```

## 2. Flux bileşenleri

Bellek için yalnızca gereken iki bileşen kurulur:

```bash
flux install --components=source-controller,kustomize-controller
kubectl -n flux-system get pods   # iki pod Running
```

## 3. Elle kurulmuş cloudflared'i kaldır

İlk kurulumda cloudflared elle çalıştırıldıysa, Flux'ın kendi manifestini
uygulayabilmesi için silinir (seçici etiketleri farklı ve değiştirilemez).
Tunnel bir iki dakika kesilir. Secret silinmez.

```bash
kubectl -n tayfam-dev delete deployment cloudflared --ignore-not-found
```

## 4. Flux tanımlarını uygula

```bash
git clone https://github.com/murattarslan-dev/Tayfam-Api.git ~/tayfam-api
kubectl apply -k ~/tayfam-api/deploy/clusters/home
```

## 5. Artifact paketini public yap (bir kez)

CI'ın `main` üzerindeki ilk "Dev dağıtımı" işi `tayfam-deploy` paketini GHCR'da
oluşturur. Yeni paketler private başlar; Flux şifresiz çekebilsin diye
**GitHub → Packages → tayfam-deploy → Package settings → Change visibility →
Public**. İçinde yalnızca repodaki `deploy/` dizini vardır.

## 6. Doğrulama

```bash
flux get sources oci
flux get kustomizations
kubectl -n tayfam-dev get pods
curl -s https://api-dev.tayfam.app/v1/status
```

`/v1/status` dört servisi de `ok` göstermelidir.

Değişikliği beklemeden hemen çekmek için:

```bash
flux reconcile source oci tayfam-deploy
flux reconcile kustomization tayfam-dev
```

## Sorun giderme

| Belirti | Bakılacak yer |
|---------|---------------|
| `OCIRepository` hazır değil, `unauthorized` | `tayfam-deploy` paketi hâlâ private (adım 5) |
| Pod'lar `CreateContainerConfigError` | `kubectl -n tayfam-dev describe pod …`; `cloudflared-token` secret'ı var mı? |
| Pod'lar oluşmuyor, namespace olaylarında "violates PodSecurity" | Manifestte `restricted` profiline uymayan bir ayar var |
| Servisler `Running` ama `/v1/status` bir servisi `unavailable` gösteriyor | NetworkPolicy: `deploy/base/networkpolicies.yaml` |
| Kustomization `wait` zaman aşımı | `kubectl -n tayfam-dev get events --sort-by=.lastTimestamp` |

## İmza doğrulaması (ADR-0011)

CI, `main`'deki her commit'te image'ları ve manifest artifact'ını cosign ile
imzalar; artifact içindeki image'lar digest ile sabitlenir. `OCIRepository`
yalnızca bu reponun `ci.yml` iş akışının `main` dalında attığı imzayı kabul
eder.

`deploy/clusters/home` cluster'a elle uygulandığı için, bu tanımlar
değiştiğinde yeniden uygulanmaları gerekir:

```bash
cd ~/tayfam-api && git pull
kubectl apply -k deploy/clusters/home
flux reconcile source oci tayfam-deploy
flux get sources oci   # MESSAGE: "stored artifact ... verified" benzeri
```

İmza doğrulanamazsa Flux yeni artifact'ı uygulamaz ve cluster son doğrulanmış
haliyle çalışmaya devam eder. Sebebi:

```bash
kubectl -n flux-system describe ocirepository tayfam-deploy | tail -20
```

## NetworkPolicy testi (ADR-0011, madde 5)

Kuralların yalnızca yazılı değil, gerçekten uygulandığını doğrular. Yeni bir
NetworkPolicy eklendiğinde, k3s güncellendiğinde veya yeni bir cluster
kurulduğunda çalıştırılır:

```bash
cd ~/tayfam-api && git pull
./scripts/netpol-test.sh
```

Dört senaryo denenir: başka bir namespace'ten erişim, sahte etiketle erişim,
gateway'in içinden ve cloudflared'in içinden. Her satırın beklenen sonucu
bellidir; tek bir sapma betiği hatayla bitirir. Son adımda gateway ve
cloudflared yeniden başlatılır (test container'larını temizlemek için), bu
sırada `api-dev` birkaç saniye kesilebilir.
