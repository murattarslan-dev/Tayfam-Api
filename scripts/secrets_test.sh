#!/usr/bin/env bash
# scripts/lib/secrets.sh ve scripts/check-secret-refs.sh için testler. sops ve
# age gerektirmez: şifreli dosyaların yalnızca sops metadata'sı (age alıcısı)
# taklit edilir. kubectl veya kustomize gerekir.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp -r deploy scripts "$tmp/"
cd "$tmp"
. scripts/lib/secrets.sh

fail() { echo "BAŞARISIZ: $*" >&2; exit 1; }

# Gerçek dev dosyalarının alıcısı; açık anahtardır, gizli değildir.
dev_rcpt=$(secret_recipients deploy/apps/dev/secrets/identity-db.sops.yaml)
[ -n "$dev_rcpt" ] || fail "dev alıcısı okunamadı"
[ "$(printf '%s\n' "$dev_rcpt" | wc -l)" -eq 1 ] || fail "dev dosyasında tek alıcı beklenirdi"

fake() { # fake <dosya> <ad> <alıcı...>: yalnızca metadata taşıyan sahte şifreli secret
  local f=$1 name=$2 r
  shift 2
  {
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n    name: %s\ntype: Opaque\nstringData:\n    k: ENC[AES256_GCM,data:x,type:str]\nsops:\n    age:\n' "$name"
    for r in "$@"; do printf '        - enc: |\n            x\n          recipient: %s\n' "$r"; done
    printf '    version: 3.13.3\n'
  } > "$f"
}
prod_rcpt=age1prodtestaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

# 1) prod secret'ı dev anahtarına şifrelenmişse reddedilir.
fake "$tmp/a.sops.yaml" identity-db "$dev_rcpt"
if check_own_recipient prod "$tmp/a.sops.yaml" 2>/dev/null; then fail "dev alıcılı prod secret'ı kabul edildi"; fi
# 2) Alıcılardan biri dev'inki olsa da reddedilir.
fake "$tmp/a.sops.yaml" identity-db "$prod_rcpt" "$dev_rcpt"
if check_own_recipient prod "$tmp/a.sops.yaml" 2>/dev/null; then fail "dev'i de içeren alıcı listesi kabul edildi"; fi
# 3) Kendi anahtarı kabul edilir; dev kendi dosyalarıyla çakışmaz.
fake "$tmp/a.sops.yaml" identity-db "$prod_rcpt"
check_own_recipient prod "$tmp/a.sops.yaml" >/dev/null || fail "prod alıcısı reddedildi"
check_own_recipient dev deploy/apps/dev/secrets/identity-db.sops.yaml >/dev/null || fail "dev kendi dosyasıyla çakıştı"
# 4) Alıcısı olmayan dosya reddedilir.
printf 'kind: Secret\n' > "$tmp/b.sops.yaml"
if check_own_recipient prod "$tmp/b.sops.yaml" 2>/dev/null; then fail "alıcısız dosya kabul edildi"; fi

# 5) dev'in listesi kendi kustomization.yaml'ındadır: write_secret_list dokunmaz.
before=$(cat deploy/apps/dev/kustomization.yaml)
if write_secret_list dev >/dev/null; then fail "dev'de write_secret_list çalışmamalı"; fi
[ "$before" = "$(cat deploy/apps/dev/kustomization.yaml)" ] || fail "dev kustomization'ı değişti"

# 6) prod: boş liste derlenir; eksik secret'lar check-secret-refs'te görünür.
if ./scripts/check-secret-refs.sh prod >/dev/null 2>&1; then fail "secret'sız prod tamam sayıldı"; fi
./scripts/check-secret-refs.sh dev cloudflared-token >/dev/null || fail "dev secret'ları eksik görünüyor"

# 7) prod'un tüm secret'ları (sahte) oluşunca liste yazılır ve kontrol geçer.
for n in identity-signing-key identity-db core-db chat-db identity-app-db core-app-db \
  chat-app-db identity-birthdate-key chat-message-key cloudflared-token; do
  fake "deploy/apps/prod/secrets/$n.sops.yaml" "$n" "$prod_rcpt"
done
write_secret_list prod >/dev/null || fail "prod listesi yazılamadı"
grep -qxF '  - cloudflared-token.sops.yaml' deploy/apps/prod/secrets/kustomization.yaml || fail "liste eksik"
grep -q '^# prod secret' deploy/apps/prod/secrets/kustomization.yaml || fail "başlık yorumu kayboldu"
./scripts/check-secret-refs.sh prod >/dev/null || fail "tüm secret'lar varken prod eksik görünüyor"
# Liste yeniden yazıldığında aynı kalır (idempotent).
cp deploy/apps/prod/secrets/kustomization.yaml "$tmp/k1"
write_secret_list prod >/dev/null
cmp -s "$tmp/k1" deploy/apps/prod/secrets/kustomization.yaml || fail "liste idempotent değil"

# 8) Bir secret silinirse kontrol düşer.
rm deploy/apps/prod/secrets/chat-message-key.sops.yaml
write_secret_list prod >/dev/null
out=$(./scripts/check-secret-refs.sh prod 2>&1) && fail "eksik secret fark edilmedi"
grep -q 'chat-message-key' <<<"$out" || fail "eksik secret adı yazılmadı"

cd "$root"
echo "secrets: tüm testler geçti"
