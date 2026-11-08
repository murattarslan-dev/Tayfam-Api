#!/usr/bin/env bash
# Servislerin Postgres kullanıcı parolalarını üretir ve her servis için ayrı,
# SOPS ile şifreli bir secret dosyası yazar (ADR-0002, ADR-0006). Platform
# sahibinin bilgisayarında, sops ve age kurulu iken çalıştırılır:
#
#   ./scripts/new-db-secrets.sh dev                 # identity, core, chat
#   FORCE=1 ./scripts/new-db-secrets.sh dev core    # yalnızca core (rotasyon)
#
# Her servis yalnızca kendi secret'ını okur; CloudNativePG aynı secret'la o
# servisin Postgres kullanıcısının parolasını ayarlar. Parolalar yalnızca
# bellekte üretilir ve doğrudan sops'a verilir; açık halleri diske, terminale
# veya kabuk geçmişine yazılmaz.
set -euo pipefail
cd "$(dirname "$0")/.."

env=${1:?kullanım: $0 <ortam> [servis...]}
shift
services=("$@")
[ ${#services[@]} -gt 0 ] || services=(identity core chat)

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }

for svc in "${services[@]}"; do
  case $svc in identity|core|chat) ;; *) echo "bilinmeyen servis: $svc" >&2; exit 1 ;; esac
  out="deploy/apps/$env/secrets/$svc-db.sops.yaml"
  if [ -e "$out" ] && [ "${FORCE:-}" != 1 ]; then
    echo "$out zaten var (FORCE=1 ile üzerine yazılır)." >&2
    exit 1
  fi
done
mkdir -p "deploy/apps/$env/secrets"

for svc in "${services[@]}"; do
  out="deploy/apps/$env/secrets/$svc-db.sops.yaml"
  tmp="$out.tmp"
  # 32 bayt rastgele değer; URL ve bağlantı dizesinde sorun çıkarmasın diye
  # base64url, dolgusuz.
  password=$(head -c 32 /dev/urandom | base64 -w0 | tr '+/' '-_' | tr -d '=')
  printf '%s\n' \
    "# $svc servisinin Postgres kullanıcısı ($env). SOPS ile şifreli." \
    "apiVersion: v1" \
    "kind: Secret" \
    "metadata:" \
    "  name: $svc-db" \
    "  labels:" \
    "    # Parola değişince CloudNativePG kullanıcıyı günceller." \
    "    cnpg.io/reload: \"true\"" \
    "type: kubernetes.io/basic-auth" \
    "stringData:" \
    "  username: $svc" \
    "  password: \"$password\"" |
    sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
  unset password
  grep -q 'ENC\[AES256_GCM' "$tmp" || { rm -f "$tmp"; echo "HATA: şifrelenmemiş görünüyor: $out" >&2; exit 1; }
  mv "$tmp" "$out"
  echo "yazıldı: $out"
done
