#!/usr/bin/env bash
# Bir Kustomize dosyasındaki servis image'larını değiştirilemez digest'e
# sabitler (ADR-0011). CI'ın dev dağıtımı işi kullanır.
#
#   scripts/pin-images.sh deploy/apps/dev/kustomization.yaml gateway=sha256:… core=sha256:…
set -euo pipefail

file=$1
shift
for pair in "$@"; do
  svc=${pair%%=*}
  digest=${pair#*=}
  if [[ ! $svc =~ ^[a-z]+$ ]] || [[ ! $digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
    echo "geçersiz değer: $pair" >&2
    exit 1
  fi
  name="ghcr.io/murattarslan-dev/tayfam-$svc"
  if ! grep -qx "  - name: $name" "$file"; then
    echo "$name, $file içinde bulunamadı" >&2
    exit 1
  fi
  # İsim satırından sonraki satır (newTag veya digest) digest ile değiştirilir.
  awk -v name="  - name: $name" -v digest="$digest" '
    $0 == name { print; getline; print "    digest: " digest; next }
    { print }
  ' "$file" > "$file.tmp"
  mv "$file.tmp" "$file"
done
