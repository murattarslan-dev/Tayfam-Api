#!/usr/bin/env bash
# scripts/lib/secrets.sh ve scripts/check-secret-refs.sh için testler. sops ve
# age gerektirmez: şifreli dosyaların yalnızca sops metadata'sı (age alıcısı)
# taklit edilir. kubectl veya kustomize gerekir.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp -r deploy scripts "$tmp/"
cd "$tmp"
. scripts/lib/secrets.sh

fail() { echo "BAŞARISIZ: $*" >&2; exit 1; }

# Testler secret'sız bir prod'dan başlar: repodaki gerçek (şifreli) prod
# secret'ları yalnızca bu geçici kopyadan çıkarılır ve liste boşaltılır.
rm -f deploy/apps/prod/secrets/*.sops.yaml
write_secret_list prod >/dev/null || fail "prod listesi boşaltılamadı"
# R2/FCM açılmadan önceki prod: dış çıkış kuralları ve FCM zorunluluğu yok
# (10. test bunları gerçek kopyada ayrıca sınar).
rm -f deploy/apps/prod/{core-egress-r2,chat-egress-internet,chat-fcm-required}.yaml
sed -i -e '/^  - core-egress-r2\.yaml$/d' -e '/^  - chat-egress-internet\.yaml$/d' \
  -e '/^  - path: chat-fcm-required\.yaml$/d' deploy/apps/prod/kustomization.yaml

# Gerçek dev dosyalarının alıcısı; açık anahtardır, gizli değildir.
dev_rcpt=$(secret_recipients deploy/apps/dev/secrets/identity-db.sops.yaml)
[ -n "$dev_rcpt" ] || fail "dev alıcısı okunamadı"
[ "$(printf '%s\n' "$dev_rcpt" | wc -l)" -eq 1 ] || fail "dev dosyasında tek alıcı beklenirdi"

fake() { # fake <dosya> <ad> <alıcı...>: yalnızca metadata taşıyan sahte şifreli secret
  local f=$1 name=$2 r
  shift 2
  {
    printf 'apiVersion: v1\nkind: Secret\nmetadata:\n    name: %s\ntype: Opaque\nstringData:\n    k: ENC[AES256_GCM,data:x,type:str]\nsops:\n    age:\n' "$name"
    for r in "$@"; do printf '        - enc: |\n            x\n          recipient: %s\n' "$r"; done
    printf '    version: 3.13.3\n'
  } > "$f"
}
prod_rcpt=age1prodtestaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

# 1) prod secret'ı dev anahtarına şifrelenmişse reddedilir.
fake "$tmp/a.sops.yaml" identity-db "$dev_rcpt"
if check_own_recipient prod "$tmp/a.sops.yaml" 2>/dev/null; then fail "dev alıcılı prod secret'ı kabul edildi"; fi
# 2) Alıcılardan biri dev'inki olsa da reddedilir.
fake "$tmp/a.sops.yaml" identity-db "$prod_rcpt" "$dev_rcpt"
if check_own_recipient prod "$tmp/a.sops.yaml" 2>/dev/null; then fail "dev'i de içeren alıcı listesi kabul edildi"; fi
# 3) Kendi anahtarı kabul edilir; dev kendi dosyalarıyla çakışmaz.
fake "$tmp/a.sops.yaml" identity-db "$prod_rcpt"
check_own_recipient prod "$tmp/a.sops.yaml" >/dev/null || fail "prod alıcısı reddedildi"
check_own_recipient dev deploy/apps/dev/secrets/identity-db.sops.yaml >/dev/null || fail "dev kendi dosyasıyla çakıştı"
# 4) Alıcısı olmayan dosya reddedilir.
printf 'kind: Secret\n' > "$tmp/b.sops.yaml"
if check_own_recipient prod "$tmp/b.sops.yaml" 2>/dev/null; then fail "alıcısız dosya kabul edildi"; fi

# 5) dev'in listesi kendi kustomization.yaml'ındadır: write_secret_list dokunmaz.
before=$(cat deploy/apps/dev/kustomization.yaml)
if write_secret_list dev >/dev/null; then fail "dev'de write_secret_list çalışmamalı"; fi
[ "$before" = "$(cat deploy/apps/dev/kustomization.yaml)" ] || fail "dev kustomization'ı değişti"

# 6) prod: boş liste derlenir; eksik secret'lar check-secret-refs'te görünür.
if ./scripts/check-secret-refs.sh prod >/dev/null 2>&1; then fail "secret'sız prod tamam sayıldı"; fi
./scripts/check-secret-refs.sh dev cloudflared-token >/dev/null || fail "dev secret'ları eksik görünüyor"

# 7) prod'un tüm secret'ları (sahte) oluşunca liste yazılır ve kontrol geçer.
#    chat-fcm (ADR-0019) ve core-r2 (ADR-0020) isteğe bağlıdır (optional: true);
#    yokken de geçer.
for n in identity-signing-key identity-db core-db chat-db identity-app-db core-app-db \
  chat-app-db identity-birthdate-key chat-message-key cloudflared-token; do
  fake "deploy/apps/prod/secrets/$n.sops.yaml" "$n" "$prod_rcpt"
done
write_secret_list prod >/dev/null || fail "prod listesi yazılamadı"
grep -qxF '  - cloudflared-token.sops.yaml' deploy/apps/prod/secrets/kustomization.yaml || fail "liste eksik"
grep -q '^# prod secret' deploy/apps/prod/secrets/kustomization.yaml || fail "başlık yorumu kayboldu"
./scripts/check-secret-refs.sh prod >/dev/null || fail "tüm secret'lar varken prod eksik görünüyor"
# Liste yeniden yazıldığında aynı kalır (idempotent).
cp deploy/apps/prod/secrets/kustomization.yaml "$tmp/k1"
write_secret_list prod >/dev/null
cmp -s "$tmp/k1" deploy/apps/prod/secrets/kustomization.yaml || fail "liste idempotent değil"

# 8) Bir secret silinirse kontrol düşer.
rm deploy/apps/prod/secrets/chat-message-key.sops.yaml
write_secret_list prod >/dev/null
out=$(./scripts/check-secret-refs.sh prod 2>&1) && fail "eksik secret fark edilmedi"
grep -q 'chat-message-key' <<<"$out" || fail "eksik secret adı yazılmadı"

# 9) Ağ kuralları ve yamalar runbook'takiyle birebir aynıdır (Tayfam Ops ve
#    betikler aynı içeriği yazar).
runbook_yaml() { # runbook_yaml <metadata.name>: runbook'ta o adı taşıyan yaml bloğu
  awk -v want="  name: $1" '
    /^ *```yaml$/ { inb = 1; match($0, /^ */); ind = RLENGTH; buf = ""; hit = 0; next }
    inb && /^ *```$/ { inb = 0; if (hit) { printf "%s", buf; exit } next }
    inb { line = substr($0, ind + 1); buf = buf line "\n"; if (line == want) hit = 1 }
  ' "$root/docs/runbooks/sops.md"
}
for m in core-egress-r2 chat-egress-internet chat-fcm-required; do
  name=$m; [ "$m" = chat-fcm-required ] && name=chat
  want=$(runbook_yaml "$name" | grep -v '^$' || true)
  [ -n "$want" ] || fail "runbook'ta $m bloğu bulunamadı"
  [ "$(manifest_body "$m")" = "$want" ] || fail "$m runbook'takinden farklı"
done

# 10) Dosyalar ve kustomization satırları yazılır; tekrar çalıştırmada
#     hiçbir şey değişmez; overlay'ler derlenir.
rm -rf "$tmp/deploy" && cp -r "$root/deploy" "$tmp/"
for env in dev prod; do
  k=deploy/apps/$env/kustomization.yaml
  write_manifest "$env" core-egress-r2 >/dev/null
  add_list_item "$k" resources core-egress-r2.yaml >/dev/null
  write_manifest "$env" chat-egress-internet >/dev/null
  add_list_item "$k" resources chat-egress-internet.yaml >/dev/null
  cmp -s <(manifest_body core-egress-r2) "deploy/apps/$env/core-egress-r2.yaml" || fail "$env: core-egress-r2 dosyası farklı"
  cp "$k" "$tmp/k-$env"
  write_manifest "$env" core-egress-r2 >/dev/null || fail "$env: aynı içerikli dosya reddedildi"
  add_list_item "$k" resources core-egress-r2.yaml >/dev/null
  add_list_item "$k" resources chat-egress-internet.yaml >/dev/null
  cmp -s "$tmp/k-$env" "$k" || fail "$env: kustomization idempotent değil"
  [ "$(grep -cxF '  - core-egress-r2.yaml' "$k")" -eq 1 ] || fail "$env: satır tekrarlandı"
done
# dev: secret satırı listenin sonuna, kendi bloğunda eklenir.
add_secret_to_list dev core-r2.sops.yaml >/dev/null
grep -qxF '  - secrets/core-r2.sops.yaml' deploy/apps/dev/kustomization.yaml || fail "dev secret satırı eklenmedi"
awk '/^resources:/ { r = 1; next } r && /^[^ ]/ { r = 0 } r && /core-r2.sops.yaml/ { ok = 1 } END { exit !ok }' \
  deploy/apps/dev/kustomization.yaml || fail "dev secret satırı resources dışında"
# prod: zorunluluk yaması patches'e eklenir.
write_manifest prod chat-fcm-required >/dev/null
add_list_item deploy/apps/prod/kustomization.yaml patches "path: chat-fcm-required.yaml" >/dev/null
add_list_item deploy/apps/prod/kustomization.yaml patches "path: chat-fcm-required.yaml" >/dev/null
[ "$(grep -cxF '  - path: chat-fcm-required.yaml' deploy/apps/prod/kustomization.yaml)" -eq 1 ] || fail "prod yaması eklenmedi ya da tekrarlandı"
# Farklı içerikli dosyanın üzerine yazılmaz.
echo "# elle değişti" >> deploy/apps/dev/core-egress-r2.yaml
if write_manifest dev core-egress-r2 2>/dev/null; then fail "farklı içerikli dosyanın üzerine yazıldı"; fi
grep -q 'elle değişti' deploy/apps/dev/core-egress-r2.yaml || fail "elle yapılan değişiklik silindi"
sed -i '/elle değişti/d' deploy/apps/dev/core-egress-r2.yaml
# "[]" ve hiç olmayan anahtar.
printf 'kind: Kustomization\nresources: []\n' > "$tmp/k.yaml"
add_list_item "$tmp/k.yaml" resources a.yaml >/dev/null
add_list_item "$tmp/k.yaml" patches "path: b.yaml" >/dev/null
[ "$(cat "$tmp/k.yaml")" = "$(printf 'kind: Kustomization\nresources:\n  - a.yaml\n\npatches:\n  - path: b.yaml')" ] || fail "boş liste/yeni anahtar yanlış yazıldı"
build=(kustomize build); command -v kubectl >/dev/null && build=(kubectl kustomize)
# core-r2 secret dosyası bu testte yok; overlay'in derlendiğini kontrol
# etmek için satır listeden çıkarılır.
sed -i '/secrets\/core-r2.sops.yaml/d' deploy/apps/dev/kustomization.yaml
rendered=$("${build[@]}" deploy/apps/dev) || fail "dev derlenmedi"
grep -q 'name: core-egress-r2' <<<"$rendered" || fail "dev'de core-egress-r2 yok"
rendered=$("${build[@]}" deploy/apps/prod) || fail "prod derlenmedi"
grep -q 'CHAT_FCM_REQUIRED' <<<"$rendered" || fail "prod'da chat-fcm-required yaması uygulanmadı"

cd "$root"
echo "secrets: tüm testler geçti"
