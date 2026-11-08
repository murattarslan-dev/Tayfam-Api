#!/usr/bin/env bash
# NetworkPolicy'lerin cluster'da gerçekten uygulandığını doğrular (ADR-0011,
# sertleştirme listesi madde 5). Ev cluster'ında, kubectl erişimi olan bir
# terminalde çalıştırılır:
#
#   ./scripts/netpol-test.sh            # tayfam-dev
#   NS=tayfam-prod ./scripts/netpol-test.sh
#
# Her denemenin beklenen sonucu (ACIK/KAPALI) bellidir; tek bir sapma bile
# betiği hata koduyla bitirir. Test container'ları pod'larda geçici olarak
# kalır; betik sonunda gateway ve cloudflared yeniden başlatılarak temizlenir.
set -euo pipefail

NS=${NS:-tayfam-dev}
IMG=busybox:1.36
svc() { echo "http://$1.$NS.svc.cluster.local:$2$3"; }
grpc() { echo "tcp://$1.$NS.svc.cluster.local:9090"; }

# busybox içinde çalışan kontrol fonksiyonu: "beklenen hedef" çiftleri alır.
# http:// hedefleri wget ile, tcp://host:port hedefleri (gRPC gibi HTTP/1
# konuşmayan portlar) TCP bağlantısıyla denenir.
read -r -d '' CHECK <<'SH' || true
fail=0
while [ $# -gt 0 ]; do
  want=$1; url=$2; shift 2
  case $url in
    tcp://*) hp=${url#tcp://}
             if nc -w 3 "${hp%:*}" "${hp##*:}" </dev/null >/dev/null 2>&1; then got=ACIK; else got=KAPALI; fi ;;
    *)       if wget -q -T 3 -O /dev/null "$url" 2>/dev/null; then got=ACIK; else got=KAPALI; fi ;;
  esac
  if [ "$got" = "$want" ]; then echo "  ok    $want  $url"; else echo "  HATA  beklenen $want, sonuç $got  $url"; fail=1; fi
done
exit $fail
SH

NONROOT='{"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"runAsGroup":65532,"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"seccompProfile":{"type":"RuntimeDefault"}}}'
custom=$(mktemp)
trap 'rm -f "$custom"' EXIT
echo "$NONROOT" > "$custom"

failed=0
run_outside() { # $1: açıklama, $2: ek kubectl run bayrakları, kalan: beklenen/url çiftleri
  local title=$1 extra=$2; shift 2
  echo "$title"
  # shellcheck disable=SC2086
  kubectl run "nptest-$RANDOM" -n default --rm -i --quiet --restart=Never $extra \
    --image="$IMG" -- sh -c "$CHECK" sh "$@" || failed=1
}
run_inside() { # $1: açıklama, $2: uygulama etiketi, kalan: beklenen/url çiftleri
  local title=$1 app=$2; shift 2
  echo "$title"
  local pod name
  pod=$(kubectl -n "$NS" get pod -l "app.kubernetes.io/name=$app" -o name | head -1)
  name="nptest-$RANDOM"
  kubectl -n "$NS" debug "$pod" -c "$name" --image="$IMG" --target="$app" \
    --profile=restricted --custom="$custom" -- sh -c "$CHECK" sh "$@" > /dev/null
  # Ephemeral container'ın çıktısı loglardan okunur (eklenme yarışında kaybolmasın).
  until [ "$(kubectl -n "$NS" get "$pod" -o jsonpath="{.status.ephemeralContainerStatuses[?(@.name=='$name')].state.terminated.reason}")" != "" ]; do sleep 1; done
  kubectl -n "$NS" logs "$pod" -c "$name"
  code=$(kubectl -n "$NS" get "$pod" -o jsonpath="{.status.ephemeralContainerStatuses[?(@.name=='$name')].state.terminated.exitCode}")
  [ "$code" = "0" ] || failed=1
}

run_outside "1) Başka bir namespace'ten (ele geçirilmiş pod):" "" \
  KAPALI "$(svc gateway 8080 /v1/status)" \
  KAPALI "$(grpc identity)" \
  KAPALI "$(grpc core)" \
  KAPALI "$(grpc chat)" \
  ACIK   "http://example.com"

run_outside "2) Başka namespace, sahte 'gateway' etiketiyle:" "--labels=app.kubernetes.io/name=gateway" \
  KAPALI "$(grpc identity)" \
  KAPALI "$(grpc core)"

run_inside "3) gateway'in içinden:" gateway \
  ACIK   "tcp://identity:9090" \
  ACIK   "tcp://core:9090" \
  ACIK   "tcp://chat:9090" \
  KAPALI "tcp://tayfam-pg-rw:5432" \
  KAPALI "http://example.com"

run_inside "4) cloudflared'in içinden:" cloudflared \
  ACIK   "http://gateway:8080/v1/status" \
  KAPALI "tcp://identity:9090" \
  KAPALI "tcp://core:9090" \
  KAPALI "tcp://chat:9090" \
  KAPALI "tcp://tayfam-pg-rw:5432"

run_inside "5) core'un içinden (identity'ye açık anahtar için, Postgres'e):" core \
  ACIK   "tcp://identity:9090" \
  ACIK   "tcp://tayfam-pg-rw:5432" \
  KAPALI "tcp://chat:9090" \
  KAPALI "http://gateway:8080/v1/status" \
  KAPALI "http://example.com"

run_inside "6) identity'nin içinden (Postgres'e ve internete yalnızca 443):" identity \
  ACIK   "tcp://tayfam-pg-rw:5432" \
  ACIK   "tcp://www.googleapis.com:443" \
  KAPALI "tcp://core:9090" \
  KAPALI "tcp://chat:9090" \
  KAPALI "http://gateway:8080/v1/status" \
  KAPALI "http://example.com"

echo "Test container'ları temizleniyor..."
kubectl -n "$NS" rollout restart deployment gateway cloudflared core identity > /dev/null

if [ "$failed" -ne 0 ]; then
  echo "SONUÇ: NetworkPolicy testi BAŞARISIZ" >&2
  exit 1
fi
echo "SONUÇ: tüm NetworkPolicy kontrolleri beklendiği gibi"
