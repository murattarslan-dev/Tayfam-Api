#!/usr/bin/env bash
# Pilot öncesi sıfırlama: platform yöneticileri dışındaki bütün hesapları,
# bütün tayfaları, etkinlikleri ve onlara bağlı içeriği siler. Etkinlik
# kataloğu ve yöneticilerin kendi hesapları kalır.
# Adımlar ve kapsam: docs/runbooks/pilot-oncesi-sifirlama.md.
#
#   NS=tayfam-prod ./scripts/pilot-sifirla.sh            # yalnızca say (ROLLBACK)
#   NS=tayfam-prod ./scripts/pilot-sifirla.sh --uygula   # sil (COMMIT)
#
# Sıra chat → core → identity: yarıda kalırsa aynı komut tekrar çalıştırılır;
# korunacaklar her seferinde platform_admins'ten okunur.
set -euo pipefail

NS="${NS:-}"
PG_POD="${PG_POD:-tayfam-pg-1}"
dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pilot-sifirla"

mode=ROLLBACK
case "${1:-}" in
  "") ;;
  --uygula) mode=COMMIT ;;
  *) echo "kullanım: NS=<namespace> $0 [--uygula]" >&2; exit 2 ;;
esac

# Testlerde PILOT_SIFIRLA_PSQL yerel psql'i gösterir; normalde kubectl exec.
psqlx() {
  if [[ -n "${PILOT_SIFIRLA_PSQL:-}" ]]; then
    ${PILOT_SIFIRLA_PSQL} -X -q -v ON_ERROR_STOP=1 "$@"
  else
    kubectl -n "$NS" exec -i "$PG_POD" -c postgres -- psql -X -q -v ON_ERROR_STOP=1 "$@"
  fi
}

if [[ -z "${PILOT_SIFIRLA_PSQL:-}" && -z "$NS" ]]; then
  echo "NS ayarlı değil (ör. NS=tayfam-prod)" >&2
  exit 2
fi

admins="$(psqlx -d identity -tA -c \
  "SELECT COALESCE(string_agg(user_id::text, ',' ORDER BY user_id), '') FROM platform_admins")"
if [[ -z "$admins" ]]; then
  echo "platform_admins boş; hiçbir şey silinmedi" >&2
  exit 1
fi
count="$(tr ',' '\n' <<<"$admins" | wc -l | tr -d ' ')"
echo "Korunacak platform yöneticisi: $count"

if [[ "$mode" == COMMIT ]]; then
  echo "Bu işlem geri alınamaz. Onaylamak için namespace adını yaz (${NS:-yerel}):"
  read -r answer
  if [[ "$answer" != "${NS:-yerel}" ]]; then
    echo "Onaylanmadı; hiçbir şey silinmedi." >&2
    exit 1
  fi
else
  echo "Deneme: her veritabanında ROLLBACK, hiçbir şey silinmez. Silmek için --uygula."
fi

for db in chat core identity; do
  echo "== $db"
  psqlx -d "$db" -v admins="$admins" -v mode="$mode" -f - <"$dir/$db.sql"
done

echo "Bitti ($mode)."
