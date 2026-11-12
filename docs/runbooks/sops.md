# SOPS ile gizli bilgiler

Gizli bilgiler repoda **SOPS + age** ile şifreli tutulur ve Flux tarafından
cluster içinde çözülür (ADR-0006). age özel anahtarı yalnızca platform
sahibinin bilgisayarında (`~/.config/sops/age/keys.txt`, parola yöneticisinde
yedekli) ve cluster'da (`flux-system/sops-age`) bulunur.

## Tayfam Ops ile (terminalsiz)

R2'yi bağlama, push'u açma ve veri dışa aktarma anahtarı aşağıdaki
betiklerle aynı işi yapan yerel bir arayüzle, terminal komutu yazmadan
yapılabilir (issue #148). Araç yalnızca bu bilgisayarda (WSL) çalışır,
yalnızca `127.0.0.1`'e bağlanır, dışarıya hiç istek yapmaz ve betikleri
(`scripts/new-r2-secret.sh`, `new-chat-fcm-secret.sh`, `new-integrity-secret.sh`, `new-export-key.sh`)
çağırır: değerler yalnızca bellekte tutulur ve betiğe stdin ile verilir.
Push/PR yapmaz; dosyaları üretir ve klasörü Windows Gezgini'nde açar.

**Bir kez kurulum (Windows'ta):**

1. Gezgin'in sol panelinde *Linux* altında WSL dağıtımınızın adını ve
   reponun yolunu (örn. `/home/<kullanıcı>/tayfam-api`) bulun.
2. Repodaki `scripts/tayfam-ops.bat` dosyasını Masaüstü'ne kopyalayın, sağ
   tık → *Düzenle* ile en üstteki iki satırı (`WSL_DISTRO`, `REPO_DIR`)
   doldurup kaydedin. İsterseniz sağ tık → *Kısayol oluştur* ile görev
   çubuğuna sabitleyin.
3. WSL'de `go`, `make`, `sops`, `jq` ve `kubectl` (ya da `kustomize`)
   kurulu olmalıdır; eksik olan aracın ana sayfasında yazar. age özel
   anahtarı gerekmez (şifreleme `.sops.yaml`'daki açık anahtarla yapılır).

**Kullanım:**

1. `tayfam-ops.bat`'a çift tıklayın. Bir pencere açılır ve tarayıcı tek
   kullanımlık bağlantıyla açılır (açılmazsa penceredeki bağlantıyı bir
   kez kullanın; WSL'den: `make ops`).
2. Ortamı (dev/prod) seçin ve kartlardan birini açın: **R2'yi bağla**,
   **Push'u aç**, **Uygulama bütünlüğü**, **Dışa aktarma anahtarı**. Panel adımları (bucket,
   lifecycle, token; Firebase servis hesabı) aşağıdaki bölümlerdeki gibidir.
3. Sonuç sayfasında üretilen/değişen dosyalar, `check-secret-refs.sh`
   sonucu ve sonraki adım yazar. **Klasörü aç** ile `deploy/apps/<ortam>`
   Gezgin'de açılır; listedeki dosyaları sohbete sürükleyin, PR'ı oturum
   açar. Birleşince ilgili Deployment Headlamp'ten yeniden başlatılır.
4. Push'ta indirdiğiniz JSON dosyasını Windows'ta silin (araç onu yalnızca
   belleğe okur, Windows'taki kopyaya dokunmaz).
5. İşiniz bitince pencereyi kapatın (araç bir saat istek gelmezse kendisi
   de kapanır).

prod, `.sops.yaml`'daki prod alıcısı yazılana kadar (VDS kurulumu adım e)
kapalı görünür. Güvenlik: `Host` başlığı yalnızca `127.0.0.1:<port>` /
`localhost:<port>`; bağlantıdaki token tek kullanımlıktır ve HttpOnly,
SameSite=Strict çereze çevrilir; her POST'ta Origin ve CSRF kontrolü;
`Content-Security-Policy: default-src 'self'`, sayfada script yok;
`Cache-Control: no-store`; formlarda `autocomplete=off`.

**Kurallar**

- `AGE-SECRET-KEY-...` ile başlayan özel anahtar hiçbir yere yapıştırılmaz:
  sohbet, issue, PR, commit mesajı, log.
- Şifreli dosya repoya girmeden önce `ENC[AES256_GCM` içerdiği kontrol edilir
  (`scripts/new-signing-key.sh` bunu kendisi yapar).
- Açık değerler terminale yazdırılmaz. Kontrol gerektiğinde yalnızca anahtar
  kimliği (kid) gibi gizli olmayan kısımlar gösterilir.

## Ortamlar ve alıcılar

Her cluster'ın kendi age anahtarı vardır (ADR-0006). prod dağıtım PR'ından
itibaren `.sops.yaml`'da
`deploy/apps/dev/secrets/` dev'in, `deploy/apps/prod/secrets/` prod'un açık
anahtarına şifrelenir; başka bir ortam dizini hiçbir kurala uymaz ve sops
şifrelemeyi reddeder. Tüm `scripts/new-*.sh` betikleri ilk argüman olarak
ortamı alır (`dev` | `prod`) ve şifreli dosyayı yazmadan önce, dosyanın başka
bir ortamın secret'larında kullanılan bir anahtara şifrelenmediğini kontrol
eder (`scripts/lib/secrets.sh`).

- **dev:** özel anahtar platform sahibinin bilgisayarında
  (`~/.config/sops/age/keys.txt`) ve ev cluster'ında; secret listesi
  `deploy/apps/dev/kustomization.yaml`'dadır.
- **prod:** özel anahtar yalnızca VDS cluster'ında (`flux-system/sops-age`)
  ve parola yöneticisinde; `keys.txt`'ye eklenmez. Üretim için açık anahtar
  yeter. Kurulum: [VDS kurulumu](vds-kurulumu.md), adım d–e. Secret listesi
  `deploy/apps/prod/secrets/kustomization.yaml`'dadır ve betikler onu
  kendileri yeniden yazar.
- Bir ortamın secret'larının tam olduğunu kontrol etmek:
  `./scripts/check-secret-refs.sh prod` (dev'de tunnel token'ı elle
  oluşturulduğu için `./scripts/check-secret-refs.sh dev cloudflared-token`).

## İlk kurulum (bir kez, cluster başına)

Aşağıdaki adımlar ev cluster'ı (dev) içindir; prod için
[VDS kurulumu](vds-kurulumu.md) adım d.

1. sops ve age kurulur (sürüm ve checksum'lar için bu dosyanın geçmişine veya
   kurulum notlarına bakın).
2. age anahtarı üretilir ve cluster'a verilir:

   ```sh
   mkdir -p ~/.config/sops/age && chmod 700 ~/.config/sops/age
   age-keygen -o ~/.config/sops/age/keys.txt
   chmod 600 ~/.config/sops/age/keys.txt
   kubectl -n flux-system create secret generic sops-age \
     --from-file=age.agekey=$HOME/.config/sops/age/keys.txt
   ```

3. `age-keygen`'in yazdığı açık anahtar (`age1...`) `.sops.yaml`'a yazılır.
   Açık anahtar gizli değildir.
4. Flux'ın çözme ayarı uygulanır (bir kez; şifreli dosya olmasa da zararsızdır):

   ```sh
   kubectl apply -f deploy/clusters/home/tayfam-dev.yaml
   ```

## identity imzalama anahtarı

### Üretme

```sh
./scripts/new-signing-key.sh dev        # kid = yıl-ay, örn. 2026-11 (prod: ... prod)
git add deploy/apps/dev/secrets/identity-signing-key.sops.yaml
```

### Rotasyon

Eski anahtarla imzalanmış token'lar en fazla 15 dakika yaşar. Eski anahtarın
**özel** yarısı rotasyondan sonra hiçbir yerde tutulmaz; yalnızca açık yarısı,
token'ları doğrulanabilsin diye kısa süre yayında kalır (ADR-0004, ADR-0011).

1. Eski anahtarın açık yarısını çıkar (gizli tohum terminale yazdırılmaz):

   ```sh
   f=deploy/apps/dev/secrets/identity-signing-key.sops.yaml
   old=$(sops -d --extract '["stringData"]["AUTH_SIGNING_KEY"]' "$f")
   kid=${old%%:*}
   pub=$( { printf '\x30\x2e\x02\x01\x00\x30\x05\x06\x03\x2b\x65\x70\x04\x22\x04\x20'
            printf '%s' "${old#*:}" | base64 -d; } |
          openssl pkey -inform DER -pubout -outform DER | tail -c 32 | base64 -w0)
   unset old
   echo "AUTH_RETIRED_KEYS=$kid:$pub"
   ```

2. Bu değeri `deploy/apps/dev/kustomization.yaml`'daki `tayfam-env`
   ConfigMap'ine `AUTH_RETIRED_KEYS` olarak ekle (gizli değildir).
3. Yeni anahtarı üret: `FORCE=1 ./scripts/new-signing-key.sh dev 2026-12`
4. İkisini aynı PR'da birleştir. identity yeni anahtarla imzalamaya başlar;
   diğer servisler yeni kid'i ilk gördüklerinde açık anahtarları yeniden alır.
5. En az 30 dakika sonra `AUTH_RETIRED_KEYS`'i kaldıran ikinci bir PR aç.
   Aynı anahtar 8 saatlik panel (admin) token'larını da imzalar; emekli
   anahtar kaldırılınca açık panel oturumu kapanır ve yönetici yeniden giriş
   yapar. Bu kabul edilebilir; panel oturumunu korumak istenirse 5. adım 8
   saat sonraya bırakılır.

**Anahtar sızdıysa** 1. ve 2. adımlar atlanır: eski anahtar emekli listesine
eklenmez, böylece onunla imzalanmış token'lar hemen geçersiz olur ve tüm
kullanıcıların erişim token'ı yenilenir (refresh token'lar etkilenmez).

## Veritabanı kullanıcı parolaları

Servis başına iki Postgres kullanıcısı vardır (ADR-0015): sahip `<servis>`
(`secrets/<servis>-db`, yalnızca migrate initContainer'ı) ve çalışma zamanı
kullanıcısı `<servis>_app` (`secrets/<servis>-app-db`, servis container'ı).

```sh
./scripts/new-db-secrets.sh dev        # yalnızca eksik olanları üretir, kustomization'a ekler
git add deploy/apps/dev/secrets/ deploy/apps/dev/kustomization.yaml
```

Rotasyon (CloudNativePG `cnpg.io/reload` etiketiyle parolayı kendisi
günceller; servis pod'u yeni secret'ı yeniden başlayınca alır):

```sh
FORCE=1 ./scripts/new-db-secrets.sh dev core:app     # ya da core:owner
```

Birleştirdikten sonra `kubectl -n tayfam-dev rollout restart deploy/core`.
Parola değiştikten sonra yeni bağlantılar eski parolayla açılamaz; restart
geciktirilmez.

## Push (FCM) servis hesabı

`chat-fcm` isteğe bağlıdır (ADR-0019): yoksa chat push göndermeden çalışır
ve **internete hiç çıkamaz** (en az yetki). Push bir ortamda açılırken
aynı PR'da üç şey eklenir: secret, chat'in internet çıkışı ve (prod'da)
zorunluluk.

1. Firebase projesinde yalnızca "Firebase Cloud Messaging API Admin" rolü
   olan bir servis hesabı açılır, JSON anahtarı indirilir ve hemen
   şifrelenir (betik indirilen açık dosyayı siler; Tayfam Ops'ta "Push'u
   aç"). Betik 2. ve 3. adımdaki dosyaları ve satırları da kendisi yazar
   (varsa dokunmaz):

   ```sh
   ./scripts/new-chat-fcm-secret.sh dev ~/Downloads/<anahtar>.json
   ```

2. `deploy/apps/<ortam>/chat-egress-internet.yaml` dosyası açılır ve
   `deploy/apps/<ortam>/kustomization.yaml`'da `resources`'a
   `- chat-egress-internet.yaml` satırı eklenir:

   ```yaml
   # chat → internet 443: push (FCM HTTP v1, fcm.googleapis.com) ve servis
   # hesabıyla erişim token'ı (oauth2.googleapis.com) (ADR-0019 madde 8).
   # Yalnızca chat-fcm secret'ı olan ortamda bulunur. NetworkPolicy alan
   # adıyla sınırlayamaz; adresler chat'in FCM istemcisinde sabittir ve
   # yönlendirme izlenmez. Cluster içi ve özel adresler hariç.
   apiVersion: networking.k8s.io/v1
   kind: NetworkPolicy
   metadata:
     name: chat-egress-internet
   spec:
     podSelector:
       matchLabels: { app.kubernetes.io/name: chat }
     policyTypes: [Egress]
     egress:
       - to:
           - ipBlock:
               cidr: 0.0.0.0/0
               except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8]
         ports:
           - { protocol: TCP, port: 443 }
   ```

3. **Yalnızca prod** (push prod'da zorunludur): `deploy/apps/prod/chat-fcm-required.yaml`
   açılır ve `deploy/apps/prod/kustomization.yaml`'da `patches`'e
   `- path: chat-fcm-required.yaml` eklenir. Secret artık isteğe bağlı
   değildir (`check-secret-refs.sh prod` onu arar) ve chat değer yoksa
   başlamaz:

   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: chat
   spec:
     template:
       spec:
         containers:
           - name: chat
             env:
               - name: CHAT_FCM_REQUIRED
                 value: "true"
               - name: CHAT_FCM_SERVICE_ACCOUNT
                 valueFrom:
                   secretKeyRef: { name: chat-fcm, key: service-account.json, optional: false }
   ```

```sh
git add deploy/apps/<ortam>/
./scripts/check-secret-refs.sh <ortam>     # dev: ... dev cloudflared-token
```

Birleştirdikten sonra `kubectl -n tayfam-<ortam> rollout restart deploy/chat`;
log'da "push açık (FCM)" görünür. `NS=tayfam-<ortam> ./scripts/netpol-test.sh`
7. bölümde Google uçlarını açık bekler.

## Dosyalar (R2)

`core-r2` isteğe bağlıdır (ADR-0020 madde 6): yoksa core dosya yükleme
olmadan çalışır (`/v1/uploads` ve `/v1/media/urls` 503 döner) ve
**internete hiç çıkamaz** (en az yetki). Dosyalar bir ortamda açılırken
aynı PR'da iki şey eklenir: secret ve core'un R2'ye çıkışı. Ortamlar
hiçbir kaynağı paylaşmaz (ADR-0006): dev ve prod için ayrı bucket, ayrı
token.

Platform sahibinin listesi (ortam başına bir kez; `<ortam>` = `dev` |
`prod`):

1. **Bucket:** Cloudflare panelinde R2 → *Create bucket*: ad
   `tayfam-<ortam>-private`, konum *Automatic* (ya da *Eastern Europe*
   ipucu). *Settings*'te *Public access* kapalı kalır, `r2.dev` alt alanı
   açılmaz, özel alan adı bağlanmaz (dosyalar yalnızca imzalı linkle
   okunur). CORS kuralı gerekmez (yalnızca mobil istemci, ADR-0017).
2. **Lifecycle kuralı:** bucket → *Settings* → *Object lifecycle rules* →
   *Add rule*: ad `incoming-24h`, önek `incoming/`, *Delete objects*
   **1 gün** sonra. (Tamamlanmayan multipart yüklemeler için varsayılan
   kural kalır.) `incoming/` istemcinin yüklediği, henüz denetlenmemiş
   varyantların karantinasıdır; denetimden geçenler `media/` altına
   kopyalanır ve bu kurala girmez.

   İkinci kural (KVKK veri dışa aktarma, ADR-0021): ad `exports-8d`, önek
   `exports/`, *Delete objects* **8 gün** sonra. Şifreli dışa aktarma
   dosyalarını core 7 gün dolunca kendisi siler; bu kural bir hata ya da
   kesinti durumunda yedek temizliktir. Bucket ve token aynıdır; yeni
   token gerekmez. R2'si zaten açık bir ortamda yalnızca bu kural eklenir.
3. **API token'ı (en az yetki):** R2 → *Manage R2 API Tokens* → *Create
   API token*: ad `tayfam-<ortam>-core`, izin **Object Read & Write**,
   *Apply to specific buckets only* → yalnızca `tayfam-<ortam>-private`.
   Süre: sınırsız ya da rotasyon takviminize göre. Mümkünse *Client IP
   Address Filtering*'e cluster'ın çıkış IP'si (prod: VDS'in IP'si)
   yazılır; token sızsa bile başka yerden kullanılamaz. *Admin Read &
   Write* veya hesap düzeyinde token **kullanılmaz**; yedek (Barman)
   token'ı ayrıdır ve bu bucket'a yetkisi yoktur (ADR-0011 madde 11).
   Panel *Access Key ID* ve *Secret Access Key*'i bir kez gösterir;
   sayfayı kapatmadan 4. adıma geçin.
4. **Secret:** dört değer istemde yapıştırılır, ekranda görünmez (Tayfam
   Ops'ta "R2'yi bağla"). Betik 5. adımdaki dosyayı ve satırları da
   kendisi yazar:

   ```sh
   ./scripts/new-r2-secret.sh <ortam>
   # Account ID (R2 sayfasının sağında), bucket adı, Access Key ID, Secret Access Key
   ```

5. **Ağ:** `deploy/apps/<ortam>/core-egress-r2.yaml` dosyası açılır ve
   `deploy/apps/<ortam>/kustomization.yaml`'da `resources`'a
   `- core-egress-r2.yaml` satırı eklenir:

   ```yaml
   # core → internet 443: Cloudflare R2 (S3 API, <hesap>.r2.cloudflarestorage.com)
   # (ADR-0020 madde 6). Yalnızca core-r2 secret'ı olan ortamda bulunur.
   # NetworkPolicy alan adıyla sınırlayamaz (R2 Cloudflare anycast'idir);
   # adres core'da hesap kimliğinden kurulur, yapılandırma başka adres
   # veremez ve yönlendirme izlenmez. Cluster içi ve özel adresler hariç.
   apiVersion: networking.k8s.io/v1
   kind: NetworkPolicy
   metadata:
     name: core-egress-r2
   spec:
     podSelector:
       matchLabels: { app.kubernetes.io/name: core }
     policyTypes: [Egress]
     egress:
       - to:
           - ipBlock:
               cidr: 0.0.0.0/0
               except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8]
         ports:
           - { protocol: TCP, port: 443 }
   ```

```sh
git add deploy/apps/<ortam>/
./scripts/check-secret-refs.sh <ortam>     # dev: ... dev cloudflared-token
```

Birleştirdikten sonra `kubectl -n tayfam-<ortam> rollout restart deploy/core`;
log'da "dosya yükleme açık (R2)" görünür. `NS=tayfam-<ortam> ./scripts/netpol-test.sh`
5. bölümde 443 çıkışını açık bekler.

**İlk kez açıldığında bir kez doğrulayın** (R2'nin imzalı başlıkları ve
koşullu kopyalamayı uyguladığını): uygulamadan ya da `POST /v1/uploads`
ile bir yükleme açın ve dönen linklerden birine **farklı boyutta** bir
dosya gönderin; R2 `403` dönmelidir. Doğru dosyalarla `200` döner ve
`POST /v1/uploads/{id}/complete` yüklemeyi `ready` yapar (core log'unda
"yükleme hazır"; koşullu kopyalama, `x-amz-copy-source-if-match`,
desteklenmeseydi tamamlama 503 dönerdi). Sonuç PR'a yazılır.

Rotasyon: panelde yeni token → `FORCE=1 ./scripts/new-r2-secret.sh <ortam>`
→ birleştirme ve `rollout restart` → eski token panelde silinir.

## Veri dışa aktarma anahtarı (identity-export-key)

KVKK veri dışa aktarma dosyası (ADR-0021) talep başına rastgele bir
anahtarla şifrelenir; o anahtar identity'nin veritabanında bu secret'taki
sürümlü anahtarla **sarılı** durur. Secret isteğe bağlıdır: yoksa identity
başlar, `/v1/me/data-exports` uçları `503 unavailable` döner; bozuksa
identity başlamaz. Dışa aktarmanın çalışması için ortamda dosyalar da
(yukarıdaki "Dosyalar (R2)", `exports-8d` kuralı dahil) açık olmalıdır.
identity'ye R2 anahtarı ya da yeni ağ kuralı **gerekmez**: dosyayı
core'un imzaladığı linkle yükler (mevcut `identity-egress-internet`, 443).

Platform sahibinin adımı (ortam başına bir kez; Tayfam Ops'ta "Dışa
aktarma anahtarı"):

```sh
./scripts/new-export-key.sh dev      # ya da prod; anahtar ekrana yazılmaz
git add deploy/apps/<ortam>/
./scripts/check-secret-refs.sh <ortam>
```

Birleştirdikten sonra `kubectl -n tayfam-<ortam> rollout restart deploy/identity`;
log'da "veri dışa aktarma açık" görünür.

Rotasyon: `sops deploy/apps/<ortam>/secrets/identity-export-key.sops.yaml`
ile değeri `1:<eski>,2:<yeni>` yapın (yeni değeri `head -c 32 /dev/urandom
| base64 -w0` ile editörün içinden üretin; terminale yazdırmayın). Eski
sürüm en az 7 gün kalır, sonra silinebilir. Anahtar kaybolursa yalnızca
son 7 günün hazır dosyaları açılamaz.

## Uygulama bütünlüğü (identity-integrity)

ADR-0022: identity oturumların resmi uygulamaya ait olduğunu Play
Integrity ile doğrular. Secret isteğe bağlıdır: `INTEGRITY_MODE` tanımsız
ya da `off` iken identity onsuz başlar. `report` ya da `enforce` iken
secret, paket adı ve sertifika özeti zorunludur; biri eksikse identity
**başlamaz** (sessizce denetimsiz çalışmak, çalışan bir denetim gibi
görünürdü). identity'nin internet çıkışı zaten vardır
(`identity-egress-internet`, 443); ağ kuralı eklenmez.

Platform sahibinin adımları (ortam başına bir kez):

1. **Play Console** → uygulama → **Protected with Play** → *Play
   Integrity API* yanında **Get started** → **Link Cloud project**: Google
   girişi ve Firebase için kullanılan mevcut Cloud projesini seçin
   (bağlama API'yi o projede kendiliğinden açar).
2. **Google Cloud** (aynı proje) → *IAM & Admin → Service Accounts →
   Create*: ad `tayfam-integrity-<ortam>`. Rol atamayın: Google'ın belgesi
   token çözmek için bir IAM rolü tanımlamıyor, bağlı projedeki hesap
   yeterli görünüyor. Push'un servis hesabını (`tayfam-push-<ortam>`)
   kullanmayın. *Keys → Add key → JSON* ile anahtarı indirin. (Denetimler
   log'da sürekli `reason=unavailable` ile dönerse yetki eksik demektir;
   oturum bakar.)
3. Anahtarı hemen şifreleyin (Tayfam Ops'ta **Uygulama bütünlüğü**, ya da):

   ```sh
   ./scripts/new-integrity-secret.sh prod ~/Downloads/<anahtar>.json
   ```

   Betik indirilen açık dosyayı siler; Tayfam Ops'ta yüklediyseniz
   Windows'taki kopyayı siz silin. Üretilen dosyayı sohbete bırakın, PR'ı
   oturum açar.
4. **Play Console** → **Protected with Play** → *Play Integrity API*
   yanında **Manage** → **Change responses**: cihaz bütünlüğünde
   **`MEETS_STRONG_INTEGRITY`**'yi açıp kaydedin (varsayılan yalnızca
   `MEETS_DEVICE_INTEGRITY`'dir; değişiklik hemen, prod dahil geçerli
   olur). Not: Android 13 ve üstünde STRONG, cihazın son bir yıl içinde
   güvenlik güncellemesi almış olmasını da ister; `report` dönemindeki
   `reason=device` oranı bunu gösterir.
   Aynı uygulamada *App signing* (uygulama imzalama) sayfasından **app
   signing key certificate → SHA-256 certificate fingerprint** değerini
   kopyalayın (`AB:CD:…` biçimi olduğu gibi kabul edilir). Upload key'in
   değil, app signing key'in özeti.
5. Paket adını ve 4. adımdaki SHA-256 özetini sohbete yazın; ikisi de
   gizli değildir. Oturum ortamın `tayfam-env`'ine şunları ekleyen PR'ı
   açar:

   ```yaml
   - INTEGRITY_MODE=report
   - INTEGRITY_ANDROID_PACKAGE=<paket adı>
   - INTEGRITY_ANDROID_CERT_SHA256=<özet>
   # isteğe bağlı; varsayılan MEETS_STRONG_INTEGRITY
   # - INTEGRITY_ANDROID_DEVICE_LEVEL=MEETS_DEVICE_INTEGRITY
   ```

   **Sıra önemli:** secret PR'ı birleşip identity yeniden başlamadan
   `INTEGRITY_MODE=report` yazılırsa identity başlamaz. İkisi aynı PR'da
   da olabilir.

Birleştikten sonra identity'yi yeniden başlatın; log'da
`uygulama bütünlüğü açık` (mod `report`) görünür. Her denetim
`bütünlük denetimi` satırıdır (platform, durum, neden; kimlik yok):
`report` dönemindeki `reason=device` oranına bakılarak `enforce`'a geçiş
ve eşik ayrıca kararlaştırılır (ADR-0022 §6). `enforce` gateway'de de aynı
`INTEGRITY_MODE` değişkenini okur.

Rotasyon: Google Cloud'da yeni JSON anahtarı üretin,
`deploy/apps/<ortam>/secrets/identity-integrity.sops.yaml`'ı silip
betiği yeniden çalıştırın; birleşip identity yeniden başlayınca eski
anahtarı konsolda silin.

## Cloudflare Tunnel token'ı

```sh
./scripts/new-tunnel-token.sh prod     # token istemde yapıştırılır, ekranda görünmez
```

prod'da token baştan SOPS'tadır. dev'de hâlâ elle oluşturulmuş
`cloudflared-token` secret'ıdır; token yenilenirken (ADR-0011 madde 13) aynı
betikle `dev` için şifreli dosya üretilir, `deploy/apps/dev/kustomization.yaml`'a
eklenir ve birleştikten sonra elle oluşturulan secret silinir.

## age anahtarı kaybolursa

Şifreli dosyalar açılamaz. Yeni bir age anahtarı üretilir (İlk kurulum, 2-3),
`flux-system/sops-age` yeniden oluşturulur ve tüm secret'lar yeniden üretilir.
Bu yüzden `keys.txt`'nin parola yöneticisinde yedeği tutulur.
