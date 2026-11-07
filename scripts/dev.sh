#!/usr/bin/env bash
# Dört servisi Docker olmadan yerelde çalıştırır. Önce `make build` gerekir
# (make dev bunu kendisi yapar). Ctrl+C hepsini durdurur.
#
#   gateway  http://localhost:8080  (ops: 8081)
#   identity ops: 8091
#   core     ops: 8092
#   chat     ops: 8093
set -euo pipefail
cd "$(dirname "$0")/.."

pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

export TAYFAM_ENV=local

OPS_ADDR=127.0.0.1:8091 ./bin/identity & pids+=($!)
OPS_ADDR=127.0.0.1:8092 ./bin/core & pids+=($!)
OPS_ADDR=127.0.0.1:8093 ./bin/chat & pids+=($!)

OPS_ADDR=127.0.0.1:8081 HTTP_ADDR=127.0.0.1:8080 \
IDENTITY_OPS_URL=http://127.0.0.1:8091 \
CORE_OPS_URL=http://127.0.0.1:8092 \
CHAT_OPS_URL=http://127.0.0.1:8093 \
  ./bin/gateway & pids+=($!)

echo "Servisler çalışıyor. Deneyin: curl http://localhost:8080/v1/status"
wait -n
