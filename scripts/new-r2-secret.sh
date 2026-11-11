#!/usr/bin/env bash
# core'un R2 (dosyalar) erişim bilgisini SOPS ile şifreli secret dosyası
# olarak yazar (issue #135, ADR-0020 madde 6). Platform sahibinin
# bilgisayarında, sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-r2-secret.sh dev
#
# Değerler Cloudflare panelinden gelir (docs/runbooks/sops.md "Dosyalar
# (R2)"): hesap kimliği ve bucket adı gizli değildir; API token'ının erişim
# anahtarı ve gizli anahtarı yalnızca bir kez gösterilir. Betik dördünü de
# ekranda göstermeden okur (read -s), yalnızca bellekte tutar ve doğrudan
# sops'a verir; açık halleri diske, terminale veya kabuk geçmişine yazılmaz.
#
# Token yalnızca bu ortamın private bucket'ına "Object Read & Write" yetkili
# olmalıdır (yedek anahtarı ayrıdır ve dosyaları okuyamaz; ADR-0011 madde
# 11). Rotasyon: panelde yeni token üretin, FORCE=1 ile betiği yeniden
# çalıştırın, birleşip core yeniden başladıktan sonra eski token'ı silin.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam>}
out="deploy/apps/$env/secrets/core-r2.sops.yaml"

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
if [ -e "$out" ] && [ "${FORCE:-}" != 1 ]; then
  echo "$out zaten var (rotasyonda FORCE=1 ile üzerine yazılır)." >&2
  exit 1
fi
mkdir -p "$(dirname "$out")"

# ask <değişken> <istem> <regex>: ekranda göstermeden okur, yalnızca biçimi
# kontrol eder. Değer hiçbir hata mesajına girmez.
ask() {
  local __v
  read -rs -p "$2 ($env, ekranda görünmez): " __v || true
  [ -t 0 ] && echo
  if [[ ! $__v =~ $3 ]]; then
    unset __v
    echo "HATA: $2 biçimi beklenmedik (boş, eksik kopyalanmış ya da fazladan karakter var)." >&2
    exit 1
  fi
  printf -v "$1" '%s' "$__v"
  unset __v
}
ask account "Cloudflare hesap kimliği (Account ID)" '^[0-9a-f]{32}$'
ask bucket "Bucket adı (tayfam-$env-private)" '^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$'
if [ "$bucket" != "tayfam-$env-private" ]; then
  echo "UYARI: bucket adı tayfam-$env-private değil; ortamların bucket'ları ayrı olmalı (ADR-0006)." >&2
fi
ask key_id "R2 API token'ı: Access Key ID" '^[A-Za-z0-9]{20,128}$'
ask secret "R2 API token'ı: Secret Access Key" '^[A-Za-z0-9/+=]{32,256}$'

tmp="$out.tmp"
trap 'rm -f "$tmp"; unset account bucket key_id secret' EXIT
printf '%s\n' \
  "# core'un R2 erişim bilgisi ($env; issue #135, ADR-0020). SOPS ile şifreli." \
  "apiVersion: v1" \
  "kind: Secret" \
  "metadata:" \
  "  name: core-r2" \
  "type: Opaque" \
  "stringData:" \
  "  account-id: \"$account\"" \
  "  bucket: \"$bucket\"" \
  "  access-key-id: \"$key_id\"" \
  "  secret-access-key: \"$secret\"" |
  sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
unset account bucket key_id secret
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "yazıldı: $out"
write_secret_list "$env" ||
  grep -qxF "  - secrets/${out##*/}" "deploy/apps/$env/kustomization.yaml" ||
  echo "deploy/apps/$env/kustomization.yaml'a (resources) ekleyin: secrets/${out##*/}"
echo "Aynı PR'a deploy/apps/$env/core-egress-r2.yaml'ı da ekleyin (docs/runbooks/sops.md \"Dosyalar (R2)\")."
