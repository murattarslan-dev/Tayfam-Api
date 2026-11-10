#!/usr/bin/env bash
# chat'in kanal mesajı şifreleme anahtarını üretir ve SOPS ile şifreli secret
# dosyası olarak yazar (ADR-0005, issue #81). Platform sahibinin
# bilgisayarında, sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-chat-message-key.sh dev
#
# Anahtar AES-256 (32 bayt) ve sürümlüdür ("1:<base64>"); identity'nin doğum
# tarihi anahtarından ayrıdır. Yalnızca bellekte üretilir ve doğrudan sops'a
# verilir; açık hali diske, terminale veya kabuk geçmişine yazılmaz.
# Kaybedilirse şifreli kanal mesajları okunamaz: age anahtarı yedekli olduğu
# sürece bu dosyadan geri alınabilir.
#
# Rotasyon: dosyayı 'sops <dosya>' ile açıp yeni sürümü başa ekleyin
# ("2:<yeni>,1:<eski>"); en yüksek sürüm yeni mesajlarda kullanılır, eski
# sürüm o anahtarla yazılmış mesajlar için kalır.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

env=${1:?kullanım: $0 <ortam>}
out="deploy/apps/$env/secrets/chat-message-key.sops.yaml"

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
  "# chat kanal mesajı şifreleme anahtarı ($env). SOPS ile şifreli." \
  "apiVersion: v1" \
  "kind: Secret" \
  "metadata:" \
  "  name: chat-message-key" \
  "type: Opaque" \
  "stringData:" \
  "  CHAT_MESSAGE_KEYS: \"1:$key\"" |
  sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
unset key
grep -q 'ENC\[AES256_GCM' "$tmp" || { echo "HATA: şifrelenmemiş görünüyor" >&2; exit 1; }
check_own_recipient "$env" "$tmp"
mv "$tmp" "$out"
trap - EXIT
echo "yazıldı: $out"
# Liste ayrı kustomization'daysa (prod) yeniden yazılır; dev'de liste
# ortamın kustomization.yaml'ındadır ve orada yoksa elle eklenir.
write_secret_list "$env" ||
  grep -qxF "  - secrets/${out##*/}" "deploy/apps/$env/kustomization.yaml" ||
  echo "deploy/apps/$env/kustomization.yaml'a (resources) ekleyin: secrets/${out##*/}"
