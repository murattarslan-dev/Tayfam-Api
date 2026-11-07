# Ev cluster'ı kurulumu (WSL2 + k3s)

Dev ortamının çalıştığı ev cluster'ını sıfırdan kurmak için adımlar
(ADR-0006). Windows 10/11 ve WSL2 gerekir.

## 1. WSL ayarları

**PowerShell'de** WSL sürümünü kontrol et; WSL 2.x ve dağıtım satırında
`VERSION 2` görülmeli:

```powershell
wsl --version
wsl -l -v
```

**Windows'ta** `%UserProfile%\.wslconfig` dosyasını oluştur. Not Defteri
dosyayı `.wslconfig.txt` olarak kaydedebilir; uzantıyı kontrol et.

```ini
[wsl2]
memory=4GB
processors=4
swap=2GB
vmIdleTimeout=-1

[experimental]
autoMemoryReclaim=gradual
```

**Ubuntu'da** systemd'yi aç (k3s servis olarak çalışır) ve WSL'i Windows'tan
yalıt (ADR-0011): `C:` sürücüsü bağlanmaz, Linux tarafından Windows
programları çalıştırılamaz. Böylece cluster'dan kaçan bir saldırgan Windows
dosyalarına ulaşamaz.

```bash
sudo tee /etc/wsl.conf > /dev/null <<WSLCONF
[boot]
systemd=true

[user]
default=$USER

[automount]
enabled=false
mountFsTab=false

[interop]
enabled=false
appendWindowsPath=false
WSLCONF
cat /etc/wsl.conf
```

Bu ayardan sonra Ubuntu'dan `/mnt/c` ve `cmd.exe`, `explorer.exe` gibi
Windows programları kullanılamaz. Windows'tan WSL'e erişim (`wsl.exe`,
`\\wsl$`) çalışmaya devam eder; Windows tarafının güvenliği (güncellemeler,
disk şifreleme) bu yüzden önemlidir.

**PowerShell'de** `wsl --shutdown`, ardından Ubuntu'yu tekrar aç ve kontrol et:

```bash
systemctl is-system-running   # running veya degraded
free -h                        # ~4 GB
nproc                          # 4
ls /mnt/c 2>&1                 # "No such file": yalıtım çalışıyor
```

## 2. k3s

Traefik ve servicelb kapalı; dış trafik Cloudflare Tunnel'dan gelir.

```bash
cd ~
sudo apt update && sudo apt -y upgrade
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server --disable traefik --disable servicelb --write-kubeconfig-mode 600" sh -

mkdir -p ~/.kube
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $USER:$USER ~/.kube/config
chmod 600 ~/.kube/config
echo 'export KUBECONFIG=$HOME/.kube/config' >> ~/.bashrc
source ~/.bashrc

kubectl get nodes -o wide   # Ready
kubectl get pods -A         # Running / Completed
```

## 3. WSL'in açılışta başlaması

k3s, WSL açılınca systemd ile başlar. WSL'in Windows'a girişte açılması ve
terminaller kapansa da açık kalması için **yönetici PowerShell'de** (dağıtım
adını `wsl -l -v` çıktısına göre düzelt):

```powershell
$a = New-ScheduledTaskAction -Execute "wsl.exe" -Argument "-d Ubuntu-22.04 --exec sleep infinity"
$t = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit 0 -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName "Tayfam WSL" -Action $a -Trigger $t -Settings $s -Description "Tayfam dev cluster icin WSL'i acik tutar"
Start-ScheduledTask -TaskName "Tayfam WSL"
```

Test: tüm Ubuntu terminallerini kapat, birkaç dakika bekle; `wsl -l -v`
dağıtımı `Running` göstermeli.

## 4. Cloudflare Tunnel

Tunnel token'ı bir şifredir; sohbetlere, issue'lara veya repoya yazılmaz.

1. Cloudflare → **Zero Trust → Networks → Tunnels → Create a tunnel →
   Cloudflared**, ad: `tayfam-dev`.
2. Kurulum ekranında ortam olarak Docker'ı seç ve komuttaki `--token`'dan
   sonraki değeri (`eyJ…`) kopyala. Sihirbaz, bir bağlantı gelene kadar ilerlemez;
   önce aşağıdaki adımları yap.
3. Token'ı cluster'a secret olarak ekle (`read -s` ekranda ve geçmişte
   göstermez; istem çıkınca yapıştırıp Enter'a bas):

   ```bash
   kubectl create namespace tayfam-dev
   read -s -p "Tunnel token: " TUNNEL_TOKEN; echo
   echo "uzunluk: ${#TUNNEL_TOKEN}"   # birkaç yüz olmalı
   kubectl -n tayfam-dev create secret generic cloudflared-token --from-literal=token="$TUNNEL_TOKEN"
   unset TUNNEL_TOKEN
   ```

4. cloudflared'i Flux kurulduktan sonra repodaki manifest çalıştırır
   (`deploy/`). Elle kurulum gerekirse aynı manifest
   `kubectl apply -k deploy/apps/dev` ile uygulanabilir.
5. Sihirbazda bağlantı görününce **Published application route** ekle:
   - Subdomain `api-dev`, domain `tayfam.app`, path boş
   - Service: `HTTP` → `gateway.tayfam-dev.svc.cluster.local:8080`

### Bilinen sorunlar

| Belirti | Sebep | Çözüm |
|---------|-------|-------|
| `"cloudflared tunnel run" requires the ID or name` | Secret'taki token boş | Secret'ı yeniden oluştur, `uzunluk` değerini kontrol et |
| `Failed to dial a quic connection` | Ağ UDP 7844'ü engelliyor | `--protocol http2` (manifestte varsayılan) |
| Tarayıcıda alan adı açılmıyor | İki seviyeli alt alan adı (`api.dev.…`) ücretsiz sertifikanın dışında | Tek seviye kullan: `api-dev.tayfam.app` |

## 5. Doğrulama

```bash
kubectl -n tayfam-dev logs deploy/cloudflared --tail=10   # Registered tunnel connection ... protocol=http2
curl -s https://api-dev.tayfam.app/v1/status
```

## Bilgisayar donduktan veya elektrik kesildikten sonra

Ani kapanmada containerd'nin diske yazdığı image katmanları bozulabilir.
Belirti: pod'lar `CrashLoopBackOff`, logda `exec format error`.

Tek tek image silmek, bozuk katmanlar önbellekte kaldığı için yetmeyebilir.
containerd'nin image deposunu sıfırlamak güvenlidir; cluster durumu
(namespace'ler, secret'lar, Flux) ayrı tutulur ve silinmez, image'lar yeniden
indirilir:

```bash
sudo systemctl stop k3s
sudo /usr/local/bin/k3s-killall.sh
sudo rm -rf /var/lib/rancher/k3s/agent/containerd
sudo systemctl start k3s
kubectl get pods -A -w
```

Dosya sistemi kontrolü: `sudo dmesg | grep -iE "ext4|error"`. `recovery
complete` satırı ext4'ün günlüğünü onardığını gösterir.
