#!/usr/bin/env bash
# identity'nin uygulama bütünlüğü (Play Integrity) servis hesabı anahtarını
# SOPS ile şifreli secret dosyası olarak yazar (issue #165, ADR-0022 §8).
# Platform sahibinin bilgisayarında, sops ve jq kurulu iken çalıştırılır:
#
#   ./scripts/new-integrity-secret.sh prod ~/Downloads/<indirilen-anahtar>.json
#
# Google Cloud konsolu anahtarı yalnızca dosya olarak verir. Betik dosyayı
# okuyup doğrudan sops'a verir, şifreli dosyayı yazar ve indirilen açık
# dosyayı siler (shred varsa önce üzerine yazar). Anahtar terminale veya
# kabuk geçmişine yazılmaz. Dosya yerine "-" verilirse anahtar stdin'den
# okunur (Tayfam Ops); o zaman silinecek dosya yoktur.
#
# Servis hesabının tek yetkisi Play Integrity API'yi çağırmaktır; push
# servis hesabı (chat-fcm) yeniden kullanılmaz. identity'nin internete 443
# çıkışı zaten vardır (identity-egress-internet); ağ kuralı yazılmaz.
# Secret varken bile INTEGRITY_MODE=off ise denetim kapalıdır; mod, paket
# adı ve sertifika özeti tayfam-env'de ayrıca açılır
# (docs/runbooks/sops.md "Uygulama bütünlüğü").
#
# Rotasyon: konsolda yeni anahtar üretin, secret dosyasını silip betiği
# yeniden çalıştırın, sonra eski anahtarı konsolda silin.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam> <servis-hesabı.json>}
src=${2:?kullanım: $0 <ortam> <servis-hesabı.json>}
out="deploy/apps/$env/secrets/identity-integrity.sops.yaml"

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
[ "$src" = - ] || [ -f "$src" ] || { echo "dosya yok: $src" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq kurulu değil" >&2; exit 1; }
if [ -e "$out" ]; then
  echo "$out zaten var; rotasyonda önce silin." >&2
  exit 1
fi
# Anahtar yalnızca bellekte tutulur ve jq'ya boru ile verilir.
if [ "$src" = - ]; then json=$(cat); else json=$(cat "$src"); fi
# Biçim denetimi; değerler yazdırılmaz.
printf '%s' "$json" | jq -e '.type == "service_account" and (.project_id | type == "string") and (.private_key | startswith("-----BEGIN PRIVATE KEY-----")) and (.client_email | type == "string")' >/dev/null 2>&1 ||
  { unset json; echo "HATA: Google servis hesabı anahtarı (JSON) değil" >&2; exit 1; }
mkdir -p "$(dirname "$out")"

tmp="$out.tmp"
trap 'rm -f "$tmp"; unset json' EXIT
jq -n --rawfile v <(printf '%s' "$json" | jq -c .) \
  '{apiVersion: "v1", kind: "Secret", metadata: {name: "identity-integrity"}, type: "Opaque", stringData: {"service-account.json": ($v | rtrimstr("\n"))}}' |
  sops --encrypt --filename-override "$out" --input-type json --output-type yaml /dev/stdin > "$tmp"
unset json
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "yazıldı: $out"
if [ "$src" != - ]; then
  if command -v shred >/dev/null; then shred -u "$src"; else rm -f "$src"; fi
  echo "indirilen açık anahtar silindi: $src"
fi
add_secret_to_list "$env" "${out##*/}"
