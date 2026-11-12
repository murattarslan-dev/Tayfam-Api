# prod ortamı: VDS kurulumu (k3s + Flux)

ADR-0006'nın 2. aşaması: `prod`, Türkiye'deki KVM tabanlı bir VDS'te, tek
node'lu ayrı bir k3s cluster'ında çalışır. `dev` evdeki WSL cluster'ında
kalır ([ev cluster'ı](ev-cluster-kurulumu.md)). İki cluster hiçbir şeyi
paylaşmaz: ayrı age anahtarı, ayrı secret'lar, ayrı tunnel, ayrı Google OAuth
client'ı, ayrı Postgres.

```
vX.Y.Z etiketi ──► prod iş akışı: etiketin main commit'i, o commit'in imzalı image'ları (digest)
                   deploy/ → oci://ghcr.io/murattarslan-dev/tayfam-deploy:vX.Y.Z, cosign imzası, "prod" etiketi
                                         │
VDS: Flux OCIRepository ─────────────────┘ yalnızca prod iş akışının refs/tags/v* imzasını kabul eder
     Flux Kustomization ──► deploy/apps/prod (secret'lar cluster içinde prod age anahtarıyla çözülür)
```

**Durum:** Pilot öncesi; gerçek kullanıcı yok, prod bir süre deneme alanı
olarak kullanılır, ama kurallar prod kurallarıdır. **Postgres yedeği yoktur**
(R2 kurulu değil); pilot öncesi şarttır, bkz. [Pilot öncesi](#pilot-öncesi-şartlar).

**Kurallar**

- Açık gizli değerler (age özel anahtarı, tunnel token'ı, parolalar)
  terminale, diske, sohbete, issue'ya veya log'a yazılmaz (CLAUDE.md kural 6).
  Aşağıdaki komutlar bunu kendileri sağlar; çıktılarını kopyalamak güvenlidir.
- Repo public'tir: IP adresi, e-posta, kullanıcı adı repoya ve PR'lara girmez.
  Bunlar yalnızca aşağıdaki `export` satırlarında, senin terminalinde durur.
- 6443 (Kubernetes API) internete hiçbir zaman açılmaz; kubectl SSH tüneliyle
  çalışır.

## Ön koşullar

- Prod ortam PR'ı (#121: `deploy/apps/prod`, betikler, bu runbook) ve prod
  dağıtım PR'ı (#122: `.github/workflows/prod.yml`, `deploy/clusters/vds`,
  `.sops.yaml`) `main`'de, bu sırayla.
- Bilgisayarında (komutları çalıştırdığın terminal): `git`, `ssh`, `sops`,
  `age`, `kubectl`, `flux` (2.10.0, CI ile aynı), `nc`. Ev cluster'ının
  kurulu olduğu WSL'i kullanıyorsan bkz. adım d'deki not.

## Değişkenler

Her yeni terminalde, komutlardan önce. Yer tutucuları kendi değerlerinle
değiştir; bu satırlar repoya girmez.

```bash
# Bilgisayarında
export VDS_IP='<vds-ipv4-adresi>'          # sağlayıcının panelinden
export VDS_USER='<sunucu-kullanici-adi>'   # VDS'te açılacak yönetici kullanıcı
export K3S_VERSION='<k3s-surumu>'          # ev cluster'ıyla aynı: orada `k3s --version`, örn. v1.37.2+k3s1
export KUBECONFIG="$HOME/.kube/tayfam-prod.yaml"   # dev cluster'ıyla karışmasın diye ayrı dosya
export TAYFAM_REPO="$HOME/tayfam-api"      # repo klonu
```

## a) VDS seçimi (kontrol listesi)

- [ ] **Sanallaştırma KVM** (OpenVZ/LXC değil): k3s kendi çekirdek
      modüllerini (iptables/nftables, overlay, br_netfilter) ve cgroup v2'yi
      ister; container tabanlı VPS'te çalışmaz.
- [ ] **Bellek ≥ 4 GB, tercihen 8 GB.** Prod istek/limitleri 4 GB'a göre
      ayarlıdır (`deploy/apps/prod/resources.yaml`); gözlem stack'i (Grafana,
      Loki, Tempo, Prometheus) yalnızca 8 GB'ta cluster içinde düşünülür,
      aksi halde Grafana Cloud kullanılır (ADR-0006).
- [ ] **2 vCPU** (ayrılmış/dedicated tercih edilir).
- [ ] **≥ 40 GB SSD/NVMe.** Postgres 15 Gi alır; image'lar ve log'lar için pay.
- [ ] **Ubuntu 24.04 LTS** imajı.
- [ ] **Türkiye lokasyonu** (kişisel veri Türkiye'de, ADR-0006).
- [ ] Sabit bir **IPv4**.
- [ ] **Giden TCP 443 ve 7844 açık** (cloudflared HTTP/2 ile 7844'e bağlanır);
      sağlayıcı giden trafiği filtreliyorsa sor.
- [ ] Panelde **VNC/seri konsol** (SSH ayarı bozulursa kurtarma yolu) ve
      mümkünse **anlık görüntü (snapshot)**.
- [ ] Sağlayıcı hesabında **2FA**.
- [ ] Aylık 500 TL altı.

## b) Sunucu sertleştirme

**Bilgisayarında:** VDS'e özel bir SSH anahtarı (yorum alanına e-posta
yazılmaz) ve bir SSH takma adı.

```bash
ssh-keygen -t ed25519 -f ~/.ssh/tayfam_vds -C tayfam-vds
cat >> ~/.ssh/config <<EOF

Host tayfam-vds
  HostName $VDS_IP
  User $VDS_USER
  IdentityFile ~/.ssh/tayfam_vds
  IdentitiesOnly yes
EOF
chmod 600 ~/.ssh/config
# Açık anahtarı root'a bir kez kopyala (sağlayıcının verdiği root parolasıyla
# ya da panelin "SSH anahtarı ekle" alanından):
ssh-copy-id -i ~/.ssh/tayfam_vds.pub "root@$VDS_IP"
```

**VDS'te, root olarak** (`ssh -i ~/.ssh/tayfam_vds root@$VDS_IP`). Bu
oturumu, yeni kullanıcıyla girişi denemeden kapatma.

```bash
VDS_USER='<sunucu-kullanici-adi>'   # yukarıdakiyle aynı

# Güncellemeler, güvenlik duvarı, otomatik güvenlik güncellemeleri
apt update && apt -y full-upgrade
apt -y install ufw unattended-upgrades
timedatectl set-timezone UTC        # her yerde UTC (CLAUDE.md)

# Yönetici kullanıcı: yalnızca anahtarla girer; sudo parolası parola
# yöneticisinde tutulur (istem çıkınca gir, ekranda görünmez).
adduser --gecos "" "$VDS_USER"
usermod -aG sudo "$VDS_USER"
install -d -m 700 -o "$VDS_USER" -g "$VDS_USER" "/home/$VDS_USER/.ssh"
install -m 600 -o "$VDS_USER" -g "$VDS_USER" /root/.ssh/authorized_keys "/home/$VDS_USER/.ssh/authorized_keys"

# SSH: yalnızca anahtar, root ve parola girişi kapalı, yalnızca bu kullanıcı.
# 10- öneki, imajın 50-cloud-init.conf'undaki "PasswordAuthentication yes"
# satırından önce okunur (sshd ilk değeri kullanır). Yalnızca yerel tünel
# (-L) açık: kubectl bununla çalışır.
cat > /etc/ssh/sshd_config.d/10-tayfam.conf <<EOF
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthenticationMethods publickey
AllowUsers $VDS_USER
MaxAuthTries 3
LoginGraceTime 20
X11Forwarding no
AllowAgentForwarding no
AllowTcpForwarding local
PermitTunnel no
EOF
sshd -t && systemctl restart ssh
sshd -T | grep -E '^(permitrootlogin|passwordauthentication|kbdinteractiveauthentication|allowtcpforwarding) '
# beklenen: permitrootlogin no, passwordauthentication no, kbdinteractiveauthentication no, allowtcpforwarding local

# Güvenlik duvarı: içeri yalnızca SSH. 6443 (Kubernetes API) açılmaz.
# Pod'lar node'daki API sunucusuna (servis token'larının JWKS'i, ADR-0013;
# CloudNativePG durum yazımı) cni0 köprüsünden gelir; bu kural yalnızca o
# arayüzden, yalnızca pod ağından gelen trafiğe izin verir.
ufw default deny incoming
ufw default allow outgoing
ufw allow OpenSSH
ufw allow in on cni0 from 10.42.0.0/16 comment 'k3s pod -> node (API, kubelet)'
ufw --force enable
ufw status verbose

# Otomatik güvenlik güncellemeleri; çekirdek güncellemesinde gece yeniden
# başlatma (01:30 UTC = 04:30 TR). Yeniden başlatmada prod birkaç dakika kesilir.
dpkg-reconfigure -f noninteractive unattended-upgrades
cat > /etc/apt/apt.conf.d/52tayfam-unattended <<'EOF'
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "01:30";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
EOF
unattended-upgrade --dry-run 2>&1 | tail -3
```

**Bilgisayarında, yeni bir terminalde** (root oturumu hâlâ açıkken):

```bash
ssh -t tayfam-vds 'sudo -v && echo "giriş ve sudo tamam"'   # -t: sudo parola için terminal ister
ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no "root@$VDS_IP" true
# beklenen: "Permission denied (publickey)" — parola ve root girişi kapalı
```

İkisi de beklendiği gibiyse root oturumunu kapat.

**İkinci SSH anahtarı (zorunlu).** Sağlayıcıda konsol (VNC) yoksa tek anahtar
tek erişim yoludur; bilgisayar yanında değilken sunucuya girilemez (2026-11-12
yaşandı). İkinci bir cihazda (ör. telefonda Termux) ayrı, parolalı bir anahtar
üret ve açık anahtarını ekle:

```bash
echo 'ssh-ed25519 AAAA... tayfam-telefon' >> ~/.ssh/authorized_keys
```

Bağlantı boşta koparsa istemcinin `~/.ssh/config`'ine `ServerAliveInterval 30`
ekle; uzun işleri `tmux new -A -s kurulum` içinde çalıştır.

**Ubuntu 26.04: AppArmor.** 26.04'te `kernel.apparmor_restrict_unprivileged_unconfined=1`
iken container içinde sonradan başlatılan süreçler "yığılmış" bir AppArmor
profili alır ve aynı container'daki süreçler arası sinyaller reddedilir
(`dmesg`: `apparmor="DENIED" operation="signal"`); Postgres bağlantıları
"authentication" aşamasında asılı kalır, CNPG initdb bitmez. Container'ların
kendi profilleri değişmeden:

```bash
echo 'kernel.apparmor_restrict_unprivileged_unconfined = 0' | sudo tee /etc/sysctl.d/60-tayfam-apparmor.conf
sudo sysctl --system | grep apparmor_restrict_unprivileged_unconfined   # = 0
```

## c) k3s

Traefik ve servicelb kapalı (dış trafik Cloudflare Tunnel'dan gelir),
secret'lar diskte şifreli (ADR-0011 madde 6), sürüm ev cluster'ıyla aynı ve
sabit. **Servis CIDR'ı (10.43.0.0/16) ve cluster alan adı varsayılan
kalır:** NetworkPolicy'ler API sunucusunu `10.43.0.1` ile, servisler token
issuer'ını `https://kubernetes.default.svc.cluster.local` ile bekler.

**VDS'te** (`ssh tayfam-vds`):

```bash
K3S_VERSION='<k3s-surumu>'   # yukarıdakiyle aynı
# Kurulum betiği de sürüme sabit (get.k3s.io değil); betik ikiliyi
# sürümün sha256 listesiyle doğrular.
curl -sfL "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION}/install.sh" -o /tmp/k3s-install.sh
sudo INSTALL_K3S_VERSION="$K3S_VERSION" \
  INSTALL_K3S_EXEC="server --disable traefik --disable servicelb --write-kubeconfig-mode 600 --secrets-encryption --node-name tayfam-prod" \
  sh /tmp/k3s-install.sh
rm /tmp/k3s-install.sh
sudo k3s secrets-encrypt status | head -2    # Encryption Status: Enabled
sudo k3s kubectl get nodes -o wide          # Ready

# Sunucuda sudo'suz kubectl/flux: k3s'in kubectl'i KUBECONFIG verilmezse
# root'un /etc/rancher/k3s/k3s.yaml'ını okur ve "permission denied" verir.
mkdir -p ~/.kube
sudo install -m 600 -o "$USER" -g "$USER" /etc/rancher/k3s/k3s.yaml ~/.kube/config
echo 'export KUBECONFIG=$HOME/.kube/config' >> ~/.bashrc && export KUBECONFIG=$HOME/.kube/config
kubectl get nodes                           # sudo'suz, Ready
# kubeconfig'i bilgisayarına almak için geçici, yalnızca senin okuyabildiğin kopya
sudo install -m 600 -o "$USER" -g "$USER" /etc/rancher/k3s/k3s.yaml ~/k3s.yaml
```

**Bilgisayarında:** kubeconfig'i al, sunucudaki kopyayı sil, yerel portu
dev'in 6443'üyle çakışmasın diye 16443 yap.

```bash
mkdir -p ~/.kube
scp tayfam-vds:k3s.yaml "$KUBECONFIG" && ssh tayfam-vds 'rm ~/k3s.yaml'
chmod 600 "$KUBECONFIG"
kubectl config set-cluster default --server=https://127.0.0.1:16443
kubectl config rename-context default tayfam-prod
```

kubectl kullanacağın her oturumda **ayrı bir terminalde** tüneli aç; iş
bitince Ctrl-C ile kapat:

```bash
ssh -N -L 16443:127.0.0.1:6443 tayfam-vds
```

```bash
kubectl config current-context   # tayfam-prod
kubectl get nodes                # tayfam-prod   Ready
```

Dışarıdan 6443'ün kapalı olduğunu doğrula (zaman aşımı beklenir):

```bash
nc -vz -w 5 "$VDS_IP" 6443 || echo "kapalı (beklenen)"
```

## d) age anahtarı (prod'un kendi anahtarı)

Her cluster'ın kendi age anahtarı vardır (ADR-0006): dev cluster'ı ele
geçirilse prod secret'larını çözemez. Anahtar **senin bilgisayarında**
üretilir ve kubectl ile doğrudan cluster'a yüklenir; VDS'in diskine dosya
olarak, GitHub'a, sohbete veya log'a girmez.

Secret üretmek için yalnızca **açık** anahtar gerekir. Özel anahtar
cluster'da ve parola yöneticinde durur; bilgisayarında kalıcı dosya olarak
tutulmaz. Özellikle dev cluster'ının çalıştığı WSL'in
`~/.config/sops/age/keys.txt`'sine **eklenmez** (ev cluster'ından kaçan
saldırgan prod'u da çözerdi, ADR-0011 hasar sınırlama).

```bash
umask 077
d=$(mktemp -d -p /dev/shm 2>/dev/null || mktemp -d)   # mümkünse yalnızca bellekte
age-keygen -o "$d/prod.agekey" 2>/dev/null
export PROD_AGE_PUB=$(age-keygen -y "$d/prod.agekey")
echo "prod age açık anahtarı: $PROD_AGE_PUB"           # açık anahtar, gizli değil
kubectl create namespace flux-system --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flux-system create secret generic sops-age --from-file=age.agekey="$d/prod.agekey" \
  && kubectl -n flux-system get secret sops-age      # secret GERÇEKTEN oluştu mu?
```

**Secret oluştuğu doğrulanmadan özel anahtarı silme.** Komutlar `&&` ile
bağlı değilse kubectl'in hatası (ör. KUBECONFIG ayarsız) gözden kaçar, anahtar
silinir ve o açık anahtara şifrelenen hiçbir secret çözülemez (2026-11-11
yaşandı: bütün prod secret'ları yeniden üretildi). `get secret` hata verirse
dur; anahtar `$d`'de durur.

Şimdi `$d/prod.agekey` dosyasını parola yöneticine **dosya eki** olarak
ekle (içeriği ekrana açmadan; ör. "Tayfam prod age anahtarı"). Sonra:

```bash
shred -u "$d/prod.agekey" 2>/dev/null || rm -f "$d/prod.agekey"; rmdir "$d"
kubectl -n flux-system describe secret sops-age | tail -2   # age.agekey: … bytes (değer gösterilmez)
```

Prod secret'ını **çözmek** gerektiğinde (ör. imzalama anahtarı rotasyonu,
[sops.md](sops.md)) özel anahtarı parola yöneticisinden geçici olarak
`/dev/shm`'e al, `SOPS_AGE_KEY_FILE=<dosya>` ile çalıştır, ardından sil.

## e) prod secret'ları

`.sops.yaml`'daki prod kuralı, sen açık anahtarı koyana kadar bir yer tutucu
içerir ve sops prod dosyalarını şifrelemeyi reddeder; dev kuralı yalnızca
`deploy/apps/dev/` için geçerlidir. Betikler ayrıca, bir prod secret'ı başka
bir ortamın anahtarına şifrelenmişse dosyayı yazmadan durur.

**Önce Cloudflare'de prod tunnel'ını oluştur** (token bu adımda gerekiyor):
Zero Trust → Networks → Tunnels → Create a tunnel → Cloudflared, ad
`tayfam-prod`. Kurulum ekranında Docker'ı seç; komuttaki `--token`'dan
sonraki değeri (`eyJ…`) panoya kopyala. Sihirbaz bağlantı gelene kadar
bekler; adım i'ye kadar bekleyebilir.

**Bilgisayarında:**

```bash
cd "$TAYFAM_REPO" && git fetch origin && git switch -c chore/prod-secretlari origin/main
sed -i "s/YER_TUTUCU_PROD_AGE_ACIK_ANAHTARI/$PROD_AGE_PUB/" .sops.yaml
git diff .sops.yaml                       # yalnızca prod kuralının age satırı değişmeli

./scripts/new-signing-key.sh prod         # identity token imzalama anahtarı
./scripts/new-db-secrets.sh prod          # 3 sahip + 3 çalışma zamanı kullanıcısı (ADR-0015)
./scripts/new-birthdate-key.sh prod       # doğum tarihi şifreleme anahtarı (ADR-0012)
./scripts/new-chat-message-key.sh prod    # kanal mesajı anahtarı (prod'da zorunlu)
./scripts/new-tunnel-token.sh prod        # istemde token'ı yapıştır, Enter (ekranda görünmez)

./scripts/check-secret-refs.sh prod       # "başvurulan 10 secret'ın tamamı tanımlı"
grep -L 'ENC\[AES256_GCM' deploy/apps/prod/secrets/*.sops.yaml   # çıktı boş olmalı
grep -l "$PROD_AGE_PUB" deploy/apps/prod/secrets/*.sops.yaml | wc -l   # 10
```

Panoyu temizle (token'ı kopyaladıktan sonra başka bir şey kopyala).
Commit'i ve PR'ı adım h'deki Google client ID'siyle birlikte aç (aşağıda).

## f) Flux

**Bilgisayarında** (tünel açık):

```bash
command -v flux >/dev/null || curl -s https://fluxcd.io/install.sh | sudo FLUX_VERSION=2.10.0 bash
flux --version                     # 2.10.0
flux check --pre
flux install --components=source-controller,kustomize-controller
kubectl -n flux-system get pods    # iki pod Running
```

Flux tanımları adım e'nin PR'ından bağımsızdır; adım e'nin dalı
`origin/main`'den açıldığı için `deploy/clusters/vds` oradakiyle aynıdır:

```bash
kubectl apply -k "$TAYFAM_REPO/deploy/clusters/vds"
flux get sources oci    # ilk sürüm etiketine kadar "prod" etiketi yok: hazır değil (beklenen)
```

`deploy/clusters/vds` elle uygulanır; değiştiğinde aynı komutla yeniden
uygulanır ([Flux kurulumu](flux-kurulumu.md), "İmza doğrulaması").

## g) Cloudflare

1. **Tunnel** `tayfam-prod`: adım e'de oluşturuldu; token'ı SOPS'tadır
   (dev'deki gibi elle oluşturulmuş secret yoktur).
2. **api.tayfam.app:** tunnel'a *Published application route* ekle (cloudflared
   adım i'de bağlandıktan sonra sihirbaz ilerler):
   - Subdomain `api`, domain `tayfam.app`, path boş
   - Service: `HTTP` → `gateway.tayfam-prod.svc.cluster.local:8080`
3. **admin.tayfam.app'i dev'den prod'a taşı** (ADR-0008, 2026-11-10'dan
   itibaren panel prod'a aittir):
   - `tayfam-dev` tunnel'ından `admin.tayfam.app` rotasını sil.
   - `tayfam-prod` tunnel'ına ekle: subdomain `admin`, Service `HTTP` →
     `gateway.tayfam-prod.svc.cluster.local:8080`.
   - Access uygulaması: mevcut **"Tayfam Panel"** (`admin.tayfam.app`) aynen
     kalır; politika yalnızca senin hesabın, *Session Duration* **8 saat**,
     *Cookie settings → SameSite* **None** ([platform yöneticisi](platform-yoneticisi.md),
     "Cloudflare Access ayarı"). Uygulamanın *AUD tag*'ini ve takım alan
     adını not al (gizli değildir).
   - Panel değerlerini prod overlay'ine ekleyen PR'ı aç (Google client ID'si
     adım h'de hazır olunca, aynı PR olabilir). `deploy/apps/prod/kustomization.yaml`,
     `tayfam-env` literal'lerine:

     ```yaml
           - ADMIN_HOST=admin.tayfam.app
           - CF_ACCESS_TEAM_DOMAIN=<takim>.cloudflareaccess.com
           - CF_ACCESS_AUD=<access-aud-tag> # gitleaks:allow
           - ADMIN_GOOGLE_CLIENT_ID=<prod-web-client-id>
     ```

     Dördü birlikte girer: CF_ACCESS_* ikisi boşken gateway paneli hiç kurmaz
     ve `admin.tayfam.app` 404 döner; biri varken diğeri yoksa ya da
     `ADMIN_GOOGLE_CLIENT_ID` yoksa gateway başlamaz.
   - dev overlay'indeki `ADMIN_HOST`/`CF_ACCESS_*`/`ADMIN_GOOGLE_CLIENT_ID`
     rota taşındıktan sonra işlevsizdir; ayrı bir PR'la kaldırılır (dev panel
     kapanır). dev'de panel gerekirse ayrı bir ad (`admin-dev.tayfam.app`) ve
     ayrı Access uygulamasıyla açılır.

## h) Google OAuth prod client'ı

ADR-0006 ortam ayrımı: prod dev'in client'ını kullanmaz.

1. Google Cloud Console'da prod için ayrı bir proje (ör. "Tayfam prod") →
   *APIs & Services → OAuth consent screen* (uygulama adı Tayfam; deneme
   döneminde *Testing* + test kullanıcısı olarak kendi hesabın yeterli).
2. *Credentials → Create credentials → OAuth client ID → Web application*,
   ad "Tayfam prod web":
   - Authorized JavaScript origins: `https://admin.tayfam.app` (web
     istemcisi yok, ADR-0017; `https://tayfam.app` gerekmez)
   - Authorized redirect URIs: `https://admin.tayfam.app/auth/google/callback`
3. Client ID'yi (gizli değil; client secret kullanılmaz ve hiçbir yere
   yazılmaz) adım e'nin dalında `deploy/apps/prod/kustomization.yaml`'a yaz:
   `- GOOGLE_CLIENT_IDS=<prod-web-client-id>`. Mobil istemcilerin prod
   client'ları açılınca virgülle eklenir.

**Bu değer boşken identity başlamaz** (`GOOGLE_CLIENT_IDS: zorunlu ortam
değişkeni tanımlı değil`); gateway'in `/v1/status`'u identity'yi
`unavailable` gösterir. Bilinçli: prod dev'in client'ıyla hiç çalışmaz.

Adım e ve h'nin değişikliklerini tek PR'da aç:

```bash
cd "$TAYFAM_REPO"
git add .sops.yaml deploy/apps/prod/
git commit -m "chore(deploy): prod age alıcısı, secret'ları ve Google client ID'si"
git push -u origin chore/prod-secretlari
```

`.sops.yaml`'a dokunduğu için bu PR'ı **sen** birleştirirsin (ADR-0010).
CI yeşil olmalı.

## i) İlk sürüm ve doğrulama

**GitHub ayarları (bir kez):**

- *Settings → Environments → prod*: (iş akışı ilk çalışınca oluşur; önceden
  de oluşturulabilir) **Required reviewers**: kendin; **Deployment branches
  and tags → Selected → Add rule → Tag** `v*`. Böylece her prod dağıtımı
  senin onay tıklamanı bekler.
- *Settings → Rules → Rulesets → New tag ruleset*: hedef `v*`; **Restrict
  updates** ve **Restrict deletions** açık (yayınlanmış bir etiket başka
  commit'e taşınamaz, silinemez).

**Etiketi at.** `main`'in son commit'inin CI'ı ("Dev dağıtımı" dahil) yeşil
bitmiş olmalı; prod iş akışı yeniden derlemez, o commit'in imzalı
image'larını kullanır.

```bash
cd "$TAYFAM_REPO" && git fetch origin
git log -1 --oneline origin/main
git tag -a v0.1.0 -m "v0.1.0" origin/main
git push origin v0.1.0
```

GitHub → Actions → "Prod sürümü" → onayla. İş şunları doğrular: etiket
`vX.Y.Z` biçiminde ve `main`'in birinci ebeveyn zincirindeki bir commit'i
gösteriyor; dört image o commit için `main` CI'ında üretilmiş ve imzalı;
prod'un tüm secret'ları overlay'de var.

**Bilgisayarında** (tünel açık):

```bash
flux reconcile source oci tayfam-deploy
flux get sources oci                   # READY True, "verified" içerir
flux get kustomizations                # infra ve tayfam-prod True
kubectl -n tayfam-prod get pods        # cloudflared, gateway, identity, core, chat, tayfam-pg-1
kubectl -n tayfam-prod get databases.postgresql.cnpg.io
```

`databases` `APPLIED false` ve "incompatible with the collation of the
template database" gösteriyorsa template1'i bir kez yeniden oluştur:
[Flux kurulumu → Veritabanı oluşmuyor](flux-kurulumu.md#veritabanı-oluşmuyor-template1-yerel-ayarı),
`NS=tayfam-prod` ile.

```bash
kubectl -n tayfam-prod logs deploy/cloudflared --tail=5   # Registered tunnel connection ... protocol=http2
```

cloudflared bağlanınca Cloudflare'de adım g.2–3'ü tamamla.

```bash
curl -s https://api.tayfam.app/v1/status        # dört servis "ok"
cd "$TAYFAM_REPO" && NS=tayfam-prod ./scripts/netpol-test.sh   # NetworkPolicy'ler uygulanıyor (ADR-0011 madde 5)
nc -vz -w 5 "$VDS_IP" 6443 || echo "6443 kapalı (beklenen)"
```

`/v1/status` bir servisi `unavailable` gösteriyorsa: identity için önce
`GOOGLE_CLIENT_IDS` (adım h); core/chat için servis token'ı JWKS'i, yani
ufw'nin cni0 kuralı (adım b) ve `kubectl -n tayfam-prod logs deploy/core`.

**Sonraki sürümler:** yalnızca yeni bir `vX.Y.Z` etiketi. **Geri dönüş:**
GitHub → Actions → önceki etiketin "Prod sürümü" çalışması → *Re-run all
jobs*; o sürümün artifact'ı yeniden `prod` olarak etiketlenir. Migration'lar
geri alınmaz; şemayı değiştiren sürümlerde geri dönüş ayrıca planlanır.

## j) İlk yönetici

Panel açık olmalı (adım g.3'ün PR'ı birleşmiş ve sürümle dağıtılmış).

1. Prod API'ye (`api.tayfam.app`) bağlanan bir istemciyle **Google ile**
   giriş yapıp kaydını tamamla (prod client'ı, adım h).
2. Kendini ekle: [platform yöneticisi](platform-yoneticisi.md) → "İlk yönetici",
   önce `export NS=tayfam-prod` (tünel açık, `KUBECONFIG` prod).
3. `https://admin.tayfam.app` → Access girişi → Google ile panel girişi.

## k8s ve k3s bakımı

- **k3s yükseltme:** önce dev'de, sonra aynı sürümle VDS'te; adım c'deki
  kurulum komutu yeni `K3S_VERSION` ile yeniden çalıştırılır (bayraklar
  aynı kalır).
- **Yeniden başlatma:** unattended-upgrades çekirdek güncellemesinde 01:30
  UTC'de başlatır; k3s ve pod'lar kendiliğinden kalkar. Kontrol:
  `kubectl -n tayfam-prod get pods`, `curl -s https://api.tayfam.app/v1/status`.
- **Gözlem:** başlangıçta cluster içinde gözlem stack'i yoktur; log'lar
  `kubectl logs` ile. Bellek yetmezse Collector Grafana Cloud'a gönderir
  (ADR-0006, 2026-11-10 revizyonu).

## Pilot öncesi şartlar

Prod ilk gerçek kullanıcı verisini almadan önce (ADR-0006, ADR-0010, ADR-0011):

- [ ] **Postgres yedekleri:** Barman Cloud → Cloudflare R2, prod'a ayrı
      bucket ve yalnızca yedek yazabilen anahtar (ADR-0011 madde 11), sürekli
      WAL arşivi + günlük tam yedek, 30 gün saklama; `postgres`
      NetworkPolicy'sine R2 çıkışı. **Şu an prod yedeksizdir**: VDS veya
      disk kaybında veri geri gelmez.
- [ ] Yedekten geri yükleme en az bir kez denenmiş ve runbook'a yazılmış
      (ADR-0006, ADR-0011 ilke 7).
- [ ] ADR-0010 "Pilot öncesi esneme": platform sahibinin ikinci bir emrine
      kadar sürer (2026-11-10); pilotta sürüp sürmeyeceği onun kararıdır.
- [ ] GitHub `prod` ortamında zorunlu onay ve `v*` etiket kural seti (adım i).
- [ ] Gözlem kararı (cluster içi ya da Grafana Cloud) ve şüpheli olay
      bildirimleri (ADR-0011 madde 10).
- [ ] Google OAuth consent screen *In production* ve doğrulama; hukuki
      yapılacaklar (ADR-0008).
- [ ] VDS'in sağlayıcı paneli ve Cloudflare hesabında 2FA (ADR-0011 madde 2).
- [ ] **Yayına kadar geçerli gevşetmeler geri alınır** (ADR-0006 revizyon
      2026-11-12): `deploy/clusters/vds/tayfam-prod.yaml` tekrar `prod`
      etiketine ve `prod.yml` imzasına; `ci.yml` "Dev dağıtımı" adımındaki
      `apps/prod` sabitlemesi kaldırılır. (`deploy/hostops` 2026-11-12'de
      kaldırıldı; `kubectl get ns tayfam-hostops` NotFound olmalı.)
- [ ] **Bütün prod anahtarları yenilenir:** age anahtarı (özel kısmı bu kez
      parola yöneticisine de yedeklenir), imzalama, veritabanı, doğum tarihi,
      mesaj, dışa aktarma anahtarları, tünel, R2 ve FCM; prod'a ayrı Google
      OAuth client'ı (`GOOGLE_CLIENT_IDS`). Test verisi silinir.
- [ ] Sağlayıcıda konsol (VNC) yok: en az iki ayrı cihazda SSH anahtarı ve
      sağlayıcıdan snapshot/kurtarma seçeneği netleşmiş olmalı.
