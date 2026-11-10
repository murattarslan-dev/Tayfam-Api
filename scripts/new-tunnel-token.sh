#!/usr/bin/env bash
# Cloudflare Tunnel token'ını SOPS ile şifreli secret dosyası olarak yazar
# (ADR-0006, ADR-0011 sertleştirme listesi madde 13). Platform sahibinin
# bilgisayarında, sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-tunnel-token.sh prod
#
# Token Cloudflare panelinde üretilir (Zero Trust → Networks → Tunnels); bu
# betik onu ekranda göstermeden okur (read -s), yalnızca bellekte tutar ve
# doğrudan sops'a verir; açık hali diske, terminale veya kabuk geçmişine
# yazılmaz. Rotasyon: Cloudflare'de token yenilenir, betik FORCE=1 ile
# yeniden çalıştırılır.
#
# dev'deki token hâlâ cluster'da elle oluşturulmuş secret'tır
# (docs/runbooks/ev-cluster-kurulumu.md); dev için bu betik o token
# yenilenirken kullanılır ve elle oluşturulan secret ardından silinir.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam>}
out="deploy/apps/$env/secrets/cloudflared-token.sops.yaml"

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
if [ -e "$out" ] && [ "${FORCE:-}" != 1 ]; then
  echo "$out zaten var (FORCE=1 ile üzerine yazılır; Cloudflare'de token'ı önce yenileyin)." >&2
  exit 1
fi
mkdir -p "$(dirname "$out")"

# Standart girdiden okunur; terminalde istem gösterilir ve yazılan görünmez.
read -rs -p "Tunnel token ($env, ekranda görünmez): " token || true
[ -t 0 ] && echo
# Token base64 kodlu bir JSON'dur ("eyJ" ile başlar) ve birkaç yüz karakterdir.
# Değerin kendisi değil, yalnızca biçimi kontrol edilir.
if [[ ! $token =~ ^eyJ[A-Za-z0-9+/=_-]{100,}$ ]]; then
  unset token
  echo "HATA: token biçimi beklenmedik (boş, eksik kopyalanmış ya da fazladan karakter var)." >&2
  exit 1
fi

tmp="$out.tmp"
trap 'rm -f "$tmp"' EXIT
printf '%s\n' \
  "# Cloudflare Tunnel token'ı ($env). SOPS ile şifreli." \
  "apiVersion: v1" \
  "kind: Secret" \
  "metadata:" \
  "  name: cloudflared-token" \
  "type: Opaque" \
  "stringData:" \
  "  token: \"$token\"" |
  sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
unset token
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "yazıldı: $out"
write_secret_list "$env" ||
  grep -qxF "  - secrets/${out##*/}" "deploy/apps/$env/kustomization.yaml" ||
  echo "deploy/apps/$env/kustomization.yaml'a (resources) ekleyin: secrets/${out##*/}"
