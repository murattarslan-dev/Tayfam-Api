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

# add_list_item <kustomization> <anahtar> <öğe>
# Üst düzey bir listeye (resources, patches) "  - <öğe>" satırını, listenin
# son öğesinden sonra ekler. Satır zaten varsa dosyaya dokunmaz (tekrar
# çalıştırmada aynı sonuç). "<anahtar>: []" ya da hiç olmayan anahtar da
# karşılanır.
add_list_item() {
  local file=$1 key=$2 item=$3
  if grep -qxF "  - $item" "$file"; then
    return 0
  fi
  if grep -qx "$key: *\[\]" "$file"; then
    awk -v key="$key" -v line="  - $item" '
      $0 ~ "^" key ": *\\[\\]$" { print key ":"; print line; next } { print }
    ' "$file" > "$file.tmp"
  elif grep -qx "$key:" "$file"; then
    # Listenin sonu: anahtardan sonraki girintili (öğe, devamı ya da yorum)
    # satırların sonuncusu.
    awk -v key="$key:" -v line="  - $item" '
      { l[NR] = $0 }
      $0 == key { inb = 1; last = NR; next }
      inb && /^ / { last = NR; next }
      inb { inb = 0 }
      END { for (i = 1; i <= NR; i++) { print l[i]; if (i == last) print line } }
    ' "$file" > "$file.tmp"
  else
    { cat "$file"; printf '\n%s:\n  - %s\n' "$key" "$item"; } > "$file.tmp"
  fi
  mv "$file.tmp" "$file"
  echo "eklendi: $file ($key: $item)"
}

# add_secret_to_list <ortam> <dosya-adı>
# Secret'ı ortamın listesine ekler: prod'da secrets/kustomization.yaml
# yeniden yazılır (write_secret_list), dev'de ortamın kustomization.yaml'ına
# "secrets/<dosya-adı>" eklenir.
add_secret_to_list() {
  write_secret_list "$1" ||
    add_list_item "deploy/apps/$1/kustomization.yaml" resources "secrets/$2"
}

# write_manifest <ortam> <ad>
# Açılan özelliğin ağ kuralını ya da yamasını deploy/apps/<ortam>/<ad>.yaml
# olarak yazar; içerik docs/runbooks/sops.md'deki ile birebir aynıdır
# (scripts/secrets_test.sh karşılaştırır). Dosya aynı içerikle varsa
# dokunmaz; farklı içerikle varsa üzerine yazmaz, hata verir.
write_manifest() {
  local env=$1 name=$2 out="deploy/apps/$1/$2.yaml" body
  body=$(manifest_body "$name") || { echo "HATA: bilinmeyen manifest: $name" >&2; return 1; }
  if [ -e "$out" ]; then
    if [ "$(cat "$out")" = "$body" ]; then
      echo "zaten var: $out"
      return 0
    fi
    echo "HATA: $out var ama içeriği docs/runbooks/sops.md'dekinden farklı; elle kontrol edin." >&2
    return 1
  fi
  printf '%s\n' "$body" > "$out"
  echo "yazıldı: $out"
}

# manifest_body <ad>: write_manifest'in içerikleri (gizli değer içermez).
manifest_body() {
  case $1 in
    core-egress-r2)
      cat <<'YAML'
# core → internet 443: Cloudflare R2 (S3 API, <hesap>.r2.cloudflarestorage.com)
# (ADR-0020 madde 6). Yalnızca core-r2 secret'ı olan ortamda bulunur.
# NetworkPolicy alan adıyla sınırlayamaz (R2 Cloudflare anycast'idir);
# adres core'da hesap kimliğinden kurulur, yapılandırma başka adres
# veremez ve yönlendirme izlenmez. Cluster içi ve özel adresler hariç.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: core-egress-r2
spec:
  podSelector:
    matchLabels: { app.kubernetes.io/name: core }
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8]
      ports:
        - { protocol: TCP, port: 443 }
YAML
      ;;
    chat-egress-internet)
      cat <<'YAML'
# chat → internet 443: push (FCM HTTP v1, fcm.googleapis.com) ve servis
# hesabıyla erişim token'ı (oauth2.googleapis.com) (ADR-0019 madde 8).
# Yalnızca chat-fcm secret'ı olan ortamda bulunur. NetworkPolicy alan
# adıyla sınırlayamaz; adresler chat'in FCM istemcisinde sabittir ve
# yönlendirme izlenmez. Cluster içi ve özel adresler hariç.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: chat-egress-internet
spec:
  podSelector:
    matchLabels: { app.kubernetes.io/name: chat }
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
            except: [10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8]
      ports:
        - { protocol: TCP, port: 443 }
YAML
      ;;
    chat-fcm-required)
      cat <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: chat
spec:
  template:
    spec:
      containers:
        - name: chat
          env:
            - name: CHAT_FCM_REQUIRED
              value: "true"
            - name: CHAT_FCM_SERVICE_ACCOUNT
              valueFrom:
                secretKeyRef: { name: chat-fcm, key: service-account.json, optional: false }
YAML
      ;;
    *) return 1 ;;
  esac
}
