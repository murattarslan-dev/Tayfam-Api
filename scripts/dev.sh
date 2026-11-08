#!/usr/bin/env bash
# Dört servisi Docker olmadan yerelde çalıştırır. Önce `make build` gerekir
# (make dev bunu kendisi yapar). Ctrl+C hepsini durdurur.
#
#   gateway  http://localhost:8080  (ops: 8081)
#   identity grpc: 9091, ops: 8091
#   core     grpc: 9092, ops: 8092
#   chat     grpc: 9093, ops: 8093
set -euo pipefail
cd "$(dirname "$0")/.."

pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

export TAYFAM_ENV=local
export AUTH_ISSUER=tayfam-identity AUTH_AUDIENCE=tayfam-api
# Yerel çalıştırmada her seferinde yeni bir imzalama anahtarı üretilir; diske
# yazılmaz, yalnızca identity sürecine verilir.
signing_key="local:$(head -c 32 /dev/urandom | base64 -w0)"

# identity veritabanı: make test-db ile açılan yerel Postgres (docker compose).
export DB_HOST=${DB_HOST:-127.0.0.1} DB_PORT=${DB_PORT:-5432} DB_NAME=${DB_NAME:-identity} \
  DB_USER=${DB_USER:-postgres} DB_PASSWORD=${DB_PASSWORD:-yerel} DB_SSLMODE=${DB_SSLMODE:-disable}
export GOOGLE_CLIENT_IDS=${GOOGLE_CLIENT_IDS:-yerel-gelistirme.apps.googleusercontent.com}
export CONSENT_PRIVACY_NOTICE_VERSION=2026-11-08 CONSENT_TERMS_VERSION=2026-11-08
birthdate_key="1:$(head -c 32 /dev/urandom | base64 -w0)"
./bin/identity migrate
AUTH_SIGNING_KEY=$signing_key IDENTITY_BIRTHDATE_KEYS=$birthdate_key \
  OPS_ADDR=127.0.0.1:8091 GRPC_ADDR=127.0.0.1:9091 ./bin/identity & pids+=($!)
unset signing_key birthdate_key
IDENTITY_GRPC_ADDR=127.0.0.1:9091 OPS_ADDR=127.0.0.1:8092 GRPC_ADDR=127.0.0.1:9092 ./bin/core & pids+=($!)
IDENTITY_GRPC_ADDR=127.0.0.1:9091 OPS_ADDR=127.0.0.1:8093 GRPC_ADDR=127.0.0.1:9093 ./bin/chat & pids+=($!)

OPS_ADDR=127.0.0.1:8081 HTTP_ADDR=127.0.0.1:8080 \
IDENTITY_GRPC_ADDR=127.0.0.1:9091 \
CORE_GRPC_ADDR=127.0.0.1:9092 \
CHAT_GRPC_ADDR=127.0.0.1:9093 \
CORS_ALLOWED_ORIGINS=http://localhost:3000 \
  ./bin/gateway & pids+=($!)

echo "Servisler çalışıyor. Deneyin: curl http://localhost:8080/v1/status"
wait -n
