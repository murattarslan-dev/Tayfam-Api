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

## İlk kurulum (bir kez, cluster başına)

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
./scripts/new-signing-key.sh dev        # kid = yıl-ay, örn. 2026-11
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

## age anahtarı kaybolursa

Şifreli dosyalar açılamaz. Yeni bir age anahtarı üretilir (İlk kurulum, 2-3),
`flux-system/sops-age` yeniden oluşturulur ve tüm secret'lar yeniden üretilir.
Bu yüzden `keys.txt`'nin parola yöneticisinde yedeği tutulur.
