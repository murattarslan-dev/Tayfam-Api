# Sürüm çıkarma (dev → main → prod)

Dal modeli ve gerekçesi: [ADR-0034](../adr/0034-dev-main-dal-modeli-ve-otomatik-surum.md).

```
iş PR'ı ──squash──► dev ──► CI (yalnızca kontroller; dağıtım yok)
                     │
          sürüm PR'ı (dev → main, merge commit; platform sahibinin sohbette onayı)
                     ▼
                   main ──► CI: kontroller → image'lar (imzalı, ci.yml@refs/heads/main)
                              → Dev dağıtımı ("dev" artifact'ı: ev cluster'ı; yayına kadar VDS)
                              → Sürüm etiketi (vX.Y.Z otomatik) ──workflow_dispatch──► prod.yml
                                                                   (imza: prod.yml@refs/tags/vX.Y.Z)
```

Kim ne yapar:

- **Sürüm PR'ını açmak ve birleştirmek:** mimari oturumu, **yalnızca platform
  sahibinin sohbetteki onayıyla**. PR açıklamasına "Sohbette onaylandı" ve
  onayın tarihi yazılır.
- **Etiket:** CI atar. Elle etiket yalnızca minor/major artışı için (aşağıda).

## Sürüm PR'ı

1. `dev`'in son CI çalışması yeşil olmalı.

   ```bash
   git fetch origin
   # sürüme girecek iş PR'ları (dev'in main'de olmayan commit'leri)
   git log --oneline --first-parent origin/main..origin/dev
   # son sürüm
   git tag --list 'v*' --sort=-v:refname | head -n1
   ```

2. PR'ı aç (baş dal `dev`, hedef `main`):

   ```bash
   gh pr create --base main --head dev \
     --title "release: <kısa özet>" \
     --body-file <(git log --format='- %s' --first-parent origin/main..origin/dev)
   ```

   Açıklamada şunlar bulunur: içerdiği PR'lar, migration'lar (geri alınmazlar;
   şemayı değiştiren sürümde geri dönüş planı), prod'da yapılması gereken
   platform adımları (yeni secret, panel ayarı) ve bunların sürümden **önce**
   yapıldığı.

3. CI'ı bekle. "Sürüm PR'ı kaynağı" ve bütün kontroller yeşil olmalı.
   `buf breaking` burada `main`'deki (prod'daki) sözleşmeye karşı çalışır.

4. Platform sahibinin sohbetteki onayından sonra **merge commit ile**
   birleştir (squash değil; `main` kural seti zaten yalnızca bunu kabul eder):

   ```bash
   gh pr merge <numara> --merge
   ```

   `dev` dalı silinmez (varsayılan dal ve kural setiyle korunur). `--delete-branch`
   verme.

## Birleştirmeden sonra

GitHub → Actions → CI (`main`):

1. Test, Lint, Güvenlik, Sözleşmeler, Manifestler, Image (×4)
2. **Dev dağıtımı**: `tayfam-deploy:dev`
3. **Sürüm etiketi**: özetinde yeni `vX.Y.Z`
4. Actions → **Prod sürümü** (`vX.Y.Z` üzerinde, `workflow_dispatch`): etiket
   biçimi, `main`'in birinci ebeveyn zinciri, image imzaları, secret
   referansları → `tayfam-deploy:vX.Y.Z`, imza, `prod` etiketi.
   `prod` ortamında zorunlu onaylayıcı varsa burada onay bekler.

Doğrulama:

```bash
curl -s https://api.tayfam.app/v1/status   # dört servis "ok"
```

## Sorunlar

**"Sürüm etiketi" etiketi attı ama prod iş akışı başlamadı** (ör. ağ hatası)
ya da prod iş akışı düştü ve sebep giderildi: iş yeniden çalıştırılırsa commit
zaten etiketli olduğu için bir şey yapmaz. Prod'u elle başlat:

```bash
gh workflow run prod.yml --ref vX.Y.Z
```

ya da Actions → Prod sürümü → *Run workflow* → "Use workflow from" alanında
**Tags → vX.Y.Z** (dal seçilirse iş ilk adımda düşer).

**"Sürüm etiketi" Dev dağıtımı düştüğü için çalışmadı:** önce sebebi gider;
Actions'ta o CI çalışmasının *Re-run failed jobs* düğmesi Dev dağıtımını ve
ardından Sürüm etiketini çalıştırır.

**Ardışık iki sürüm PR'ı:** `main` CI'ı sırayla çalışır; kuyruğa üçüncü bir
çalışma girerse bekleyen ikinci iptal edilir ve o commit etiket almaz. Bir
sonraki sürüm onu da içerdiği için sorun değildir.

**Geri dönüş:** Actions → önceki sürümün "Prod sürümü" çalışması → *Re-run all
jobs*; o sürümün artifact'ı yeniden `prod` olarak etiketlenir. Yayına kadar VDS
`dev` artifact'ını izlediği için bunun yerine önceki sürüm commit'inin `main`
CI çalışmasında **Dev dağıtımı** işini yeniden çalıştır (*Re-run job*; ardından
gelen Sürüm etiketi, commit zaten etiketli olduğu için bir şey yapmaz).
Kalıcı düzeltme `dev`'e birleşir ve yeni bir sürüm PR'ıyla gelir.
Migration'lar geri alınmaz; şemayı değiştiren sürümde geri dönüş ayrıca
planlanır.

**Acil düzeltme:** `dev`'e iş PR'ı (squash), ardından yalnızca onu içeren
(ya da `dev`'deki her şeyi içeren) sürüm PR'ı. `main`'e doğrudan PR açılamaz.

## minor/major artışı

Otomatik etiket yalnızca yamayı artırır (`v0.1.7` → `v0.1.8`). `v0.2.0` için
platform sahibi, `main`'in son commit'ine (sürümü yayınlanmış olan) etiketi
elle atar. Push olayı prod iş akışını başlatır; aynı içerik yeniden
yayınlanır, sonraki otomatik sürüm `v0.2.1` olur.

```bash
git fetch origin && git log -1 --oneline origin/main
git tag -a v0.2.0 -m "v0.2.0" origin/main
git push origin v0.2.0
```

## Bir kez: GitHub ayarları

ADR-0034'ü getiren PR birleştikten sonra platform sahibi yapar; ayrıntılı
liste o PR'ın açıklamasındadır. Özet:

- `dev` dalı `main`'den oluşturulur ve **varsayılan dal** yapılır.
- Repo: *Allow merge commits* ve *Allow squash merging* açık, *Allow rebase
  merging* kapalı.
- `dev` kural seti: silme ve force push yasak, PR zorunlu, yalnızca squash,
  zorunlu kontroller, istisna yok.
- `main` kural seti: yalnızca merge, zorunlu kontrollere **Sürüm PR'ı
  kaynağı** eklenir; "Require branches to be up to date" ve "Require linear
  history" kapalı.
- `v*` etiket kural seti: Restrict updates ve Restrict deletions (değişmez).
- `prod` ortamı: yalnızca `v*` etiketleri; zorunlu onaylayıcı ADR-0034 açık
  soru 1'e göre.
