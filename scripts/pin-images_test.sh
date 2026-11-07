#!/usr/bin/env bash
# scripts/pin-images.sh için testler. CI'ın Test işinde çalışır.
set -euo pipefail
cd "$(dirname "$0")/.."

tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.tmp"' EXIT
cp deploy/apps/dev/kustomization.yaml "$tmp"

a=sha256:$(printf 'a%.0s' {1..64})
b=sha256:$(printf 'b%.0s' {1..64})

./scripts/pin-images.sh "$tmp" gateway="$a" chat="$b"
grep -A1 -x "  - name: ghcr.io/murattarslan-dev/tayfam-gateway" "$tmp" | grep -qx "    digest: $a"
grep -A1 -x "  - name: ghcr.io/murattarslan-dev/tayfam-chat" "$tmp" | grep -qx "    digest: $b"
if grep -A1 -x "  - name: ghcr.io/murattarslan-dev/tayfam-core" "$tmp" | grep -q digest; then
  echo "sabitlenmeyen servis değişmemeliydi" >&2; exit 1
fi

for bad in "gateway=latest" "gateway=sha256:xyz" "yok=$a" "Gateway=$a"; do
  if ./scripts/pin-images.sh "$tmp" "$bad" 2>/dev/null; then
    echo "geçersiz girdi kabul edildi: $bad" >&2; exit 1
  fi
done

echo "pin-images: tüm testler geçti"
