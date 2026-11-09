#!/usr/bin/env bash
# Servislerin Postgres kullanıcı parolalarını üretir ve her kullanıcı için ayrı,
# SOPS ile şifreli bir secret dosyası yazar (ADR-0002, ADR-0006, ADR-0014).
# Platform sahibinin bilgisayarında, sops ve age kurulu iken çalıştırılır.
#
# Servis başına iki kullanıcı vardır:
#   <servis>      sahip; migration'ları çalıştırır     secrets/<servis>-db
#   <servis>_app  çalışma zamanı (servis container'ı)  secrets/<servis>-app-db
#
#   ./scripts/new-db-secrets.sh dev                     # tüm servislerin eksikleri
#   ./scripts/new-db-secrets.sh dev identity core       # yalnızca bu servislerin eksikleri
#   FORCE=1 ./scripts/new-db-secrets.sh dev core:app    # core_app parolasını yenile (rotasyon)
#   FORCE=1 ./scripts/new-db-secrets.sh dev core:owner  # core (sahip) parolasını yenile
#
# Var olan dosyalara dokunulmaz; yalnızca eksik olan üretilir. FORCE=1 ile
# yalnızca adı verilen dosyalar yeniden üretilir. Yeni dosya ortamın
# kustomization.yaml'ına (resources) eklenir.
#
# Her servis yalnızca kendi secret'larını okur; CloudNativePG aynı secret'la
# o Postgres kullanıcısının parolasını ayarlar. Parolalar yalnızca bellekte
# üretilir ve doğrudan sops'a verilir; açık halleri diske, terminale veya
# kabuk geçmişine yazılmaz.
set -euo pipefail
cd "$(dirname "$0")/.."

env=${1:?kullanım: $0 <ortam> [servis[:owner|:app]...]}
shift
args=("$@")
# chat'in henüz veritabanı kodu yok; chat_app veritabanıyla birlikte gelir.
[ ${#args[@]} -gt 0 ] || args=(identity core chat:owner)

dir="deploy/apps/$env"
[ -d "$dir" ] || { echo "ortam yok: $dir" >&2; exit 1; }
[ -f "$dir/kustomization.yaml" ] || { echo "kustomization yok: $dir/kustomization.yaml" >&2; exit 1; }
command -v sops >/dev/null || { echo "sops kurulu değil" >&2; exit 1; }

# Üretilecek (servis, tür) çiftleri.
targets=()
for arg in "${args[@]}"; do
  svc=${arg%%:*}
  kind=${arg#"$svc"}
  kind=${kind#:}
  case $svc in identity|core|chat) ;; *) echo "bilinmeyen servis: $svc" >&2; exit 1 ;; esac
  case $kind in
    "") targets+=("$svc:owner" "$svc:app") ;;
    owner|app) targets+=("$svc:$kind") ;;
    *) echo "bilinmeyen tür: $arg (owner ya da app)" >&2; exit 1 ;;
  esac
done
mkdir -p "$dir/secrets"

for target in "${targets[@]}"; do
  svc=${target%%:*}
  kind=${target#*:}
  if [ "$kind" = owner ]; then
    name="$svc-db" user="$svc" what="sahibi (migration)"
  else
    name="$svc-app-db" user="${svc}_app" what="çalışma zamanı kullanıcısı"
  fi
  out="$dir/secrets/$name.sops.yaml"
  if [ -e "$out" ] && [ "${FORCE:-}" != 1 ]; then
    echo "var, dokunulmadı: $out"
    continue
  fi
  tmp="$out.tmp"
  # 32 bayt rastgele değer; URL ve bağlantı dizesinde sorun çıkarmasın diye
  # base64url, dolgusuz.
  password=$(head -c 32 /dev/urandom | base64 -w0 | tr '+/' '-_' | tr -d '=')
  printf '%s\n' \
    "# $svc veritabanının $what ($env). SOPS ile şifreli." \
    "apiVersion: v1" \
    "kind: Secret" \
    "metadata:" \
    "  name: $name" \
    "  labels:" \
    "    # Parola değişince CloudNativePG kullanıcıyı günceller." \
    "    cnpg.io/reload: \"true\"" \
    "type: kubernetes.io/basic-auth" \
    "stringData:" \
    "  username: $user" \
    "  password: \"$password\"" |
    sops --encrypt --filename-override "$out" --input-type yaml --output-type yaml /dev/stdin > "$tmp"
  unset password
  grep -q 'ENC\[AES256_GCM' "$tmp" || { rm -f "$tmp"; echo "HATA: şifrelenmemiş görünüyor: $out" >&2; exit 1; }
  mv "$tmp" "$out"
  echo "yazıldı: $out"

  # Kustomization'a ekle (yoksa): son "- secrets/" satırının ardına.
  entry="  - secrets/$name.sops.yaml"
  if ! grep -qxF -- "$entry" "$dir/kustomization.yaml"; then
    awk -v entry="$entry" '
      { lines[NR] = $0 }
      /^  - secrets\// { last = NR }
      END {
        if (!last) { exit 1 }
        for (i = 1; i <= NR; i++) { print lines[i]; if (i == last) print entry }
      }' "$dir/kustomization.yaml" > "$dir/kustomization.yaml.tmp" ||
      { rm -f "$dir/kustomization.yaml.tmp"; echo "HATA: $dir/kustomization.yaml'da secrets/ satırı yok; elle ekleyin: $entry" >&2; exit 1; }
    mv "$dir/kustomization.yaml.tmp" "$dir/kustomization.yaml"
    echo "eklendi: $dir/kustomization.yaml → secrets/$name.sops.yaml"
  fi
done
