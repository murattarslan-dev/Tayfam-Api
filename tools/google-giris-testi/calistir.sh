#!/usr/bin/env bash
# Google giriş test sayfasını http://localhost:3000 adresinde sunar (yalnızca
# bu makineden erişilir). Google client'ında bu köken izinlidir.
set -euo pipefail
cd "$(dirname "$0")"
echo "Tarayıcıda aç: http://localhost:3000  (durdurmak için Ctrl+C)"
exec python3 -m http.server 3000 --bind 127.0.0.1
