#!/usr/bin/env bash
# Bir ortamın manifestlerinde adı geçen her Secret'ın aynı overlay'de
# tanımlı olduğunu kontrol eder (pod'ların CreateContainerConfigError'da,
# Postgres'in parolasız kalmaması için). Değerlere bakmaz; yalnızca adlar.
#
#   ./scripts/check-secret-refs.sh prod
#   ./scripts/check-secret-refs.sh dev cloudflared-token   # cluster'da elle
#                                                          # oluşturulan secret
#
# İkinci ve sonraki argümanlar overlay dışında (cluster'da elle) oluşturulan,
# bu yüzden eksik sayılmayacak secret adlarıdır. prod'da hiç yoktur.
set -euo pipefail
cd "$(dirname "$0")/.."

env=${1:?kullanım: $0 <ortam> [harici-secret...]}
shift
dir="deploy/apps/$env"
[ -d "$dir" ] || { echo "ortam yok: $dir" >&2; exit 1; }

if command -v kubectl >/dev/null; then
  build=(kubectl kustomize "$dir")
elif command -v kustomize >/dev/null; then
  build=(kustomize build "$dir")
else
  echo "kubectl veya kustomize gerekli" >&2
  exit 1
fi
rendered=$("${build[@]}")

# Başvurulan adlar: secretKeyRef, secretRef (envFrom), passwordSecret ve
# initdb.secret bloklarının altındaki "name:" değerleri.
referenced=$(awk '
  {
    match($0, /^ */); ind = RLENGTH
    line = $0; sub(/^ *(- )?/, "", line)
  }
  depth >= 0 && ind <= depth { depth = -1 }
  depth >= 0 && line ~ /^name: / { sub(/^name: /, "", line); print line; next }
  line ~ /^(secretKeyRef|secretRef|passwordSecret|secret):$/ { depth = ind }
  BEGIN { depth = -1 }
' <<<"$rendered" | tr -d "\"'" | sort -u)

# Tanımlı Secret'lar: kind: Secret belgelerinin metadata.name'i.
defined=$(awk '
  /^---$/ { if (secret && name != "") print name; secret = 0; name = ""; meta = 0; next }
  /^kind: Secret$/ { secret = 1 }
  /^metadata:$/ { meta = 1; next }
  /^[^ ]/ { meta = 0 }
  meta && /^  name: / { name = $2 }
  END { if (secret && name != "") print name }
' <<<"$rendered" | tr -d "\"'" | sort -u)

external=$(printf '%s\n' "$@" | sed '/^$/d' | sort -u)
missing=$(comm -23 <(printf '%s\n' "$referenced") <(printf '%s\n%s\n' "$defined" "$external" | sed '/^$/d' | sort -u) | sed '/^$/d')

if [ -n "$missing" ]; then
  echo "$env: overlay'de tanımlı olmayan secret'lar:" >&2
  printf '  - %s\n' $missing >&2
  echo "Üretim: docs/runbooks/sops.md (prod: docs/runbooks/vds-kurulumu.md, adım e)." >&2
  exit 1
fi
echo "$env: başvurulan $(printf '%s\n' "$referenced" | sed '/^$/d' | wc -l | tr -d ' ') secret'ın tamamı tanımlı."
