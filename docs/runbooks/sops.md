# SOPS ile gizli bilgiler

Gizli bilgiler repoda **SOPS + age** ile şifreli tutulur ve Flux tarafından
cluster içinde çözülür (ADR-0006). age özel anahtarı yalnızca platform
sahibinin bilgisayarında (`~/.config/sops/age/keys.txt`, parola yöneticisinde
yedekli) ve cluster'da (`flux-system/sops-age`) bulunur.

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

`chat-fcm` isteğe bağlıdır (ADR-0019): yoksa chat push göndermeden çalışır.
Firebase projesinde yalnızca "Firebase Cloud Messaging API Admin" rolü olan
bir servis hesabı açılır, JSON anahtarı indirilir ve hemen şifrelenir
(betik indirilen açık dosyayı siler):

```sh
./scripts/new-chat-fcm-secret.sh dev ~/Downloads/<anahtar>.json
git add deploy/apps/dev/secrets/ deploy/apps/dev/kustomization.yaml
```

Birleştirdikten sonra `kubectl -n tayfam-dev rollout restart deploy/chat`;
log'da "push açık (FCM)" görünür.

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
