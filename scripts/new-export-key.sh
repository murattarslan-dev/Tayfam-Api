#!/usr/bin/env bash
# identity'nin KVKK veri dışa aktarma sarma anahtarını üretir ve SOPS ile
# şifreli secret dosyası olarak yazar (ADR-0021 madde 2). Platform sahibinin
# bilgisayarında, sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-export-key.sh dev
#   ./scripts/new-export-key.sh prod
#
# Anahtar AES-256 (32 bayt) ve sürümlüdür ("1:<base64>"). Yalnızca bellekte
# üretilir ve doğrudan sops'a verilir; açık hali diske, terminale veya kabuk
# geçmişine yazılmaz. Kaybedilirse yalnızca son 7 günün hazır dışa
# aktarmaları açılamaz. Rotasyonda eski sürüm en az 7 gün tutulur, yenisi
# eklenir ("1:<eski>,2:<yeni>"; en yüksek sürümle sarılır).
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam>}
out="deploy/apps/$env/secrets/identity-export-key.sops.yaml"

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
if [ -e "$out" ]; then
  echo "$out zaten var. Rotasyonda eski sürüm silinmez, yenisi eklenir;" >&2
  echo "bunun için dosyayı 'sops $out' ile açıp düzenleyin." >&2
  exit 1
fi
mkdir -p "$(dirname "$out")"

key=$(head -c 32 /dev/urandom | base64 -w0)
tmp="$out.tmp"
trap 'rm -f "$tmp"' EXIT
printf '%s\n' \
  "# identity veri dışa aktarma sarma anahtarı ($env). SOPS ile şifreli." \
  "apiVersion: v1" \
  "kind: Secret" \
  "metadata:" \
  "  name: identity-export-key" \
  "type: Opaque" \
  "stringData:" \
  "  IDENTITY_DATA_EXPORT_KEYS: \"1:$key\"" |
  sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
unset key
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "yazıldı: $out"
# Liste ayrı kustomization'daysa (prod) yeniden yazılır; dev'de liste
# ortamın kustomization.yaml'ındadır ve satır oraya eklenir.
add_secret_to_list "$env" "${out##*/}"
