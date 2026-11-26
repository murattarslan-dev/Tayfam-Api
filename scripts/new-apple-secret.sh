#!/usr/bin/env bash
# Apple ile girişi (ADR-0045) bir ortamda açan secret'ı SOPS ile şifreli
# dosya olarak yazar. Platform sahibinin bilgisayarında, sops ve jq kurulu
# iken çalıştırılır:
#
#   ./scripts/new-apple-secret.sh dev <team-id> <key-id> <bundle-kimliği> ~/Downloads/AuthKey_<key-id>.p8
#
# Apple, Sign in with Apple anahtarını (.p8) yalnızca bir kez ve yalnızca
# dosya olarak verir. Betik dosyayı okuyup doğrudan sops'a verir, şifreli
# dosyayı yazar ve indirilen açık dosyayı siler (shred varsa önce üzerine
# yazar). Anahtar terminale veya kabuk geçmişine yazılmaz. Dosya yerine "-"
# verilirse anahtar stdin'den okunur (Tayfam Ops); o zaman silinecek dosya
# yoktur.
#
# Secret dört değer taşır: APPLE_CLIENT_IDS (iOS bundle kimliği),
# APPLE_TEAM_ID, APPLE_KEY_ID ve APPLE_PRIVATE_KEY. İlk üçü gizli değildir;
# anahtarla aynı dosyada durmalarının sebebi Apple girişinin tek dosyayla,
# yarım yapılandırma olmadan açılmasıdır: identity dördünü birlikte bekler,
# biri eksikse başlamaz (ADR-0045 §5). Dosya yoksa Apple girişi kapalıdır.
#
# identity'nin internete 443 çıkışı zaten vardır (identity-egress-internet);
# ağ kuralı yazılmaz.
#
# Rotasyon: Apple Developer portalında yeni anahtar üretin, secret dosyasını
# silip betiği yeni Key ID ile yeniden çalıştırın; birleşip identity yeniden
# başlayınca eski anahtarı portalda iptal edin.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/secrets.sh

usage="kullanım: $0 <ortam> <team-id> <key-id> <bundle-kimliği> <AuthKey.p8 | ->"
env=${1:?$usage}
team=${2:?$usage}
key_id=${3:?$usage}
bundle=${4:?$usage}
src=${5:?$usage}
out="deploy/apps/$env/secrets/identity-apple.sops.yaml"

[ -d "deploy/apps/$env" ] || { echo "ortam yok: deploy/apps/$env" >&2; exit 1; }
[ "$src" = - ] || [ -f "$src" ] || { echo "dosya yok: $src" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq kurulu değil" >&2; exit 1; }
# Kimlikler gizli değildir; biçimleri Apple'ın verdiği gibidir.
[[ $team =~ ^[A-Z0-9]{10}$ ]] || { echo "HATA: Team ID 10 karakter (A-Z, 0-9) olmalı" >&2; exit 1; }
[[ $key_id =~ ^[A-Z0-9]{10}$ ]] || { echo "HATA: Key ID 10 karakter (A-Z, 0-9) olmalı" >&2; exit 1; }
[[ $bundle =~ ^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$ && ${#bundle} -le 155 ]] ||
  { echo "HATA: bundle kimliği beklenen biçimde değil (ör. com.ornek.uygulama)" >&2; exit 1; }
if [ -e "$out" ]; then
  echo "$out zaten var; rotasyonda önce silin." >&2
  exit 1
fi
# Anahtar yalnızca bellekte tutulur. Windows satır sonları atılır.
if [ "$src" = - ]; then key=$(cat); else key=$(cat "$src"); fi
key=${key//$'\r'/}
# Biçim denetimi; değerler yazdırılmaz. identity anahtarı PKCS #8 PEM
# biçiminde P-256 olarak bekler; yanlış dosya identity'yi başlatmaz.
first=${key%%$'\n'*}
last=${key##*$'\n'}
if [ "$first" != "-----BEGIN PRIVATE KEY-----" ] || [ "$last" != "-----END PRIVATE KEY-----" ]; then
  unset key first last
  echo "HATA: Sign in with Apple anahtarı (.p8, BEGIN/END PRIVATE KEY) değil" >&2
  exit 1
fi
unset first last
if command -v openssl >/dev/null; then
  if ! printf '%s\n' "$key" | openssl pkey -noout -text_pub 2>/dev/null | grep -q -E 'prime256v1|P-256'; then
    unset key
    echo "HATA: anahtar okunamadı ya da P-256 değil" >&2
    exit 1
  fi
else
  echo "uyarı: openssl yok; anahtarın P-256 olduğu denetlenemedi" >&2
fi
mkdir -p "$(dirname "$out")"

tmp="$out.tmp"
trap 'rm -f "$tmp"; unset key' EXIT
jq -n --rawfile k <(printf '%s\n' "$key") --arg team "$team" --arg kid "$key_id" --arg bundle "$bundle" \
  '{apiVersion: "v1", kind: "Secret", metadata: {name: "identity-apple"}, type: "Opaque", stringData: {APPLE_CLIENT_IDS: $bundle, APPLE_TEAM_ID: $team, APPLE_KEY_ID: $kid, APPLE_PRIVATE_KEY: $k}}' |
  sops --encrypt --filename-override "$out" --input-type json --output-type yaml /dev/stdin > "$tmp"
unset key
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
