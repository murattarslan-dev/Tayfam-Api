#!/usr/bin/env bash
# identity'nin token imzalama anahtarını üretir ve SOPS ile şifreli secret
# dosyası olarak yazar (ADR-0004, ADR-0006). Platform sahibinin bilgisayarında,
# sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-signing-key.sh dev            # kid: yıl-ay, örn. 2026-11
#   ./scripts/new-signing-key.sh dev 2026-11b   # kid elle
#
# Anahtar yalnızca bellekte üretilir ve doğrudan sops'a verilir; açık hali
# diske, terminale veya kabuk geçmişine yazılmaz. Çıktı dosyası şifrelidir ve
# repoya commit edilebilir. Rotasyon: docs/runbooks/sops.md
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam> [kid]}
kid=${2:-$(date -u +%Y-%m)}
out="deploy/apps/$env/secrets/identity-signing-key.sops.yaml"

[[ $kid =~ ^[a-z0-9][a-z0-9-]{0,31}$ ]] || { echo "geçersiz kid: $kid" >&2; exit 1; }
[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
if [ -e "$out" ] && [ "${FORCE:-}" != 1 ]; then
  echo "$out zaten var. Rotasyon için docs/runbooks/sops.md'ye bakın (FORCE=1 ile üzerine yazılır)." >&2
  exit 1
fi
mkdir -p "$(dirname "$out")"

seed=$(head -c 32 /dev/urandom | base64 -w0)
tmp="$out.tmp"
trap 'rm -f "$tmp"' EXIT
# .sops.yaml'daki kural dosya adına göre alıcıyı (age açık anahtarı) seçer.
printf '%s\n' \
  "# identity token imzalama anahtarı ($env, kid=$kid). SOPS ile şifreli." \
  "apiVersion: v1" \
  "kind: Secret" \
  "metadata:" \
  "  name: identity-signing-key" \
  "type: Opaque" \
  "stringData:" \
  "  AUTH_SIGNING_KEY: \"$kid:$seed\"" |
  sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
unset seed
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
# Başka bir ortamın age anahtarına şifrelenmişse yazılmaz (ADR-0006).
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT

grep -q 'ENC\[AES256_GCM' "$out" || { echo "HATA: dosya şifrelenmemiş görünüyor, commit etmeyin: $out" >&2; exit 1; }
echo "yazıldı: $out (kid=$kid)"
# Liste ayrı kustomization'daysa (prod) yeniden yazılır; dev'de liste
# ortamın kustomization.yaml'ındadır.
write_secret_list "$env" ||
  grep -qxF "  - secrets/${out##*/}" "deploy/apps/$env/kustomization.yaml" ||
  echo "deploy/apps/$env/kustomization.yaml'a (resources) ekleyin: secrets/${out##*/}"
