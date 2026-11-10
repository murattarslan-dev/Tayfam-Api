# shellcheck shell=bash
# scripts/new-*.sh betiklerinin ortak yardımcıları (ADR-0006, docs/runbooks/sops.md).
# Betikler repo kökünden source eder:  . scripts/lib/secrets.sh
#
# Burada hiçbir açık değer işlenmez: yalnızca şifreli dosyaların sops
# metadata'sındaki age alıcıları (açık anahtarlar, gizli değildir) ve dosya
# adları okunur.

# secret_recipients <şifreli-dosya>
# Dosyanın şifrelendiği age açık anahtarlarını (sıralı, tekil) yazar.
secret_recipients() {
  awk '
    /^sops:/ { s = 1; next }
    s && /^[^ ]/ { s = 0 }
    s && $1 == "recipient:" { print $2 }
  ' "$1" | sort -u
}

# check_own_recipient <ortam> <şifreli-dosya>
# Her cluster'ın kendi age anahtarı vardır (ADR-0006): bir ortamın secret'ı,
# başka bir ortamın secret'larının şifrelendiği bir anahtara şifrelenmişse
# hata verir. Böylece .sops.yaml'da prod için ayrı kural yokken prod
# secret'ı dev anahtarına (ev cluster'ına) şifrelenip repoya giremez.
check_own_recipient() {
  local env=$1 file=$2 mine other f
  mine=$(secret_recipients "$file")
  if [ -z "$mine" ]; then
    echo "HATA: age alıcısı bulunamadı: $file" >&2
    return 1
  fi
  for f in deploy/apps/*/secrets/*.sops.yaml; do
    [ -e "$f" ] || continue
    case $f in "deploy/apps/$env/"*) continue ;; esac
    other=$(secret_recipients "$f")
    if [ -n "$(comm -12 <(printf '%s\n' "$mine") <(printf '%s\n' "$other"))" ]; then
      echo "HATA: $env secret'ı başka bir ortamın age anahtarına şifrelendi ($f ile aynı alıcı)." >&2
      echo ".sops.yaml'da $env için kendi alıcısıyla ayrı bir kural olmalı (docs/runbooks/sops.md)." >&2
      return 1
    fi
  done
}

# write_secret_list <ortam>
# Ortam secret listesini ayrı bir kustomization'da tutuyorsa
# (deploy/apps/<ortam>/secrets/kustomization.yaml, prod) listeyi dizindeki
# *.sops.yaml dosyalarından yeniden yazar. Tutmuyorsa (dev: liste ortamın
# kendi kustomization.yaml'ındadır) hiçbir şey yapmaz ve 1 döner.
write_secret_list() {
  local dir="deploy/apps/$1/secrets" f
  [ -f "$dir/kustomization.yaml" ] || return 1
  local files=()
  for f in "$dir"/*.sops.yaml; do
    [ -e "$f" ] && files+=("${f##*/}")
  done
  {
    # Başlık yorumu korunur: ilk "resources:" satırına kadar olan kısım.
    awk '/^resources:/ { exit } { print }' "$dir/kustomization.yaml"
    if [ ${#files[@]} -eq 0 ]; then
      echo "resources: []"
    else
      echo "resources:"
      printf '  - %s\n' "${files[@]}"
    fi
  } > "$dir/kustomization.yaml.tmp"
  mv "$dir/kustomization.yaml.tmp" "$dir/kustomization.yaml"
  echo "liste güncellendi: $dir/kustomization.yaml (${#files[@]} dosya)"
}
