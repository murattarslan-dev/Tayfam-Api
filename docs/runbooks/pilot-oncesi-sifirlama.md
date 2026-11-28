# Pilot öncesi prod sıfırlama

Canlıya açılmadan önce prod'daki bütün kullanıcı verisini bir kez siler;
şema, migration geçmişi, katalog, engelli alan adları ve iş çalışma kayıtları
kalır. Ardından platform sahibi yeniden kaydolur, demo verisi yazılır ve
mağaza inceleme hesabı hazırlanır (issue #369,
[ADR-0046](../adr/0046-pilot-oncesi-sifirlama.md)).

**Geri dönüşü yoktur.** Tek geri dönüş yolu 1. adımdaki yedektir.

Araç, servis başına bir SQL betiğidir (`db/<servis>/reset/pilot-oncesi-sifirlama.sql`).
Betikler Postgres pod'unda `psql` ile, her biri kendi veritabanında ve tek
transaction içinde çalışır; servis ikililerinde yoktur.

| Ayar (PGOPTIONS) | Değer | Anlamı |
|------------------|-------|--------|
| `tayfam.mod` | `kuru` | Yalnızca sayar, hiçbir şey değiştirmez |
| | `medya` | Yalnızca core: bütün yüklemeleri silme kuyruğuna alır; R2 nesnelerini çalışan core siler |
| | `sil` | Siler |
| `tayfam.ortam` | `prod` (ya da `dev`) | `medya` ve `sil` için zorunlu |
| `tayfam.onay` | `PROD-SIFIRLA` (ya da `DEV-SIFIRLA`) | `medya` ve `sil` için zorunlu, elle ve birebir |

Kilitler: ayar yoksa hiçbir şey yapmaz; betik yalnızca kendi veritabanında
çalışır; şemada betiğin bilmediği bir tablo varsa çalışmaz; `sil` servis
bağlıyken çalışmaz; core `sil` yüklemeler R2'den silinmeden çalışmaz;
**gerçek kullanıcı eşiği aşılmışsa çalışmaz** (aşağıda). Çıktı yalnızca
tablo adları ve sayılardır.

## Silinenler ve korunanlar

| Veritabanı | Korunan | Silinen |
|------------|---------|---------|
| identity | `catalog_copy`, `catalog_interests`, `goose_db_version` | Hesaplar, giriş kimlikleri, cihazlar, oturumlar, yenileme token'ları, onay kayıtları, profil ayrıntıları, ad geçmişi, dışa aktarma talepleri, yaptırımlar, panel denetim kaydı, **`platform_admins`**, outbox ve tüketici konumları |
| core | `catalog_*` (7 tablo), `blocked_domains` (ekleyen boşalır), `job_runs`, `goose_db_version`; outbox'ta yalnızca `catalog.changed` | Tayfalar, üyelikler, davetler, etkinlikler ve bütün bileşenleri, duyurular, soru-cevap, anketler, arkadaşlık, engel, öneri, rozet ve istatistik sonuçları, kütüphane, materyal, garaj, şikâyetler, platform yasakları, profil kopyaları, yüklemeler (R2 dahil), denetim kaydı, diğer outbox olayları ve tüketici konumları |
| chat | `goose_db_version` | Kanallar, mesajlar, birebir sohbetler, E2E anahtarları, zarflar ve arşiv, bildirimler ve tercihleri, push token'ları ve kuyruğu, profil kopyaları, outbox ve tüketici konumları |

`platform_admins` silinir: satır `users`'a bağlıdır ve hesabın silinmesiyle
anlamını yitirir. Sen 5. adımda yeniden kaydolup kendini yeniden eklersin.

## Gerçek kullanıcı eşiği (betiğin kendini kapatması)

| Veritabanı | Sayılan | `sil`/`medya` reddedilir |
|------------|---------|--------------------------|
| identity | Silinmemiş, demo olmayan, platform yöneticisi olmayan hesap | 5'ten fazlaysa |
| core | Adında "Demo" geçmeyen tayfalardaki farklı üye | 6'dan fazlaysa |
| chat | Push token'ı ya da E2E cihaz anahtarı olan farklı kullanıcı | 6'dan fazlaysa |

İlk birkaç gerçek kullanıcı geldiğinde betik kendiliğinden çalışmaz olur.
Kuru çalışma eşik aşımını `EŞİK AŞILDI` diye yazar; o durumda dur ve mimari
oturumuna sor.

## Ön koşullar

- Bu değişikliği içeren sürüm prod'a dağıtılmış olmalı ve betikler **aynı
  sürüm etiketinden** alınmalı: betik prod şemasındaki tablo listesini birebir
  ister. Farklı sürümse kuru çalışma `sınıflandırılmamış tablo` ya da
  `betikteki tablo şemada yok` der ve hiçbir şey yapmaz.
- VDS'te kubectl'in prod cluster'ını gösterdiği bir bash terminali ve
  `flux` ([VDS kurulumu](vds-kurulumu.md) adım c).
- Ayrılabilecek yaklaşık 30 dakika. Bu sürede uygulama kapalıdır.
- Mağaza inceleme hesabı için ayrı bir Google hesabı (7. adım).

## 0. Hazırlık

```sh
export NS=tayfam-prod
SURUM=vX.Y.Z          # prod'daki sürüm etiketi (bu PR'ı içeren ya da sonrası)
kubectl -n "$NS" get deploy -o 'custom-columns=AD:.metadata.name,IMAGE:.spec.template.spec.containers[0].image'
#    Beklenen: image etiketleri/digest'leri $SURUM'un image'ları.

# Betikleri sabit etiketten al (repo public; yalnızca betikler gerekir)
R=$(mktemp -d) && git clone -q --depth 1 --branch "$SURUM" \
  https://github.com/murattarslan-dev/Tayfam-Api "$R/repo" && cd "$R/repo"
ls db/*/reset/pilot-oncesi-sifirlama.sql   # üç dosya

# Kısayol: bir servisin pod'ları tamamen gidene kadar bekler
pod_yok() {  # pod_yok <servis>
  for i in $(seq 1 36); do
    [ -z "$(kubectl -n "$NS" get pods -l app.kubernetes.io/name="$1" -o name)" ] && return 0; sleep 5
  done; echo "HATA: $1 pod'u hâlâ ayakta"; return 1
}
# Kısayol: betiği bir veritabanında belirli ayarlarla çalıştırır
sifirla() {  # sifirla <veritabanı> <PGOPTIONS>
  kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- env PGOPTIONS="$2" \
    psql -X -q -v ON_ERROR_STOP=1 -d "$1" < "db/$1/reset/pilot-oncesi-sifirlama.sql"
}
ONAY='-c tayfam.ortam=prod -c tayfam.onay=PROD-SIFIRLA'
```

## 1. Yedek

Prod'da henüz Barman yedeği yoktur ([VDS kurulumu](vds-kurulumu.md),
"Pilot öncesi şartlar"). Sıfırlamadan önce üç veritabanının `pg_dump`'ı
VDS'e alınır. Döküm kişisel veri içerir (test hesapları): yalnızca senin
okuyabileceğin bir dizine yazılır ve doğrulamadan sonra silinir.

```sh
YEDEK="$HOME/tayfam-yedek-$(date -u +%Y%m%dT%H%M%SZ)"
(umask 077 && mkdir "$YEDEK")
for db in identity core chat; do
  kubectl -n "$NS" exec tayfam-pg-1 -c postgres -- pg_dump -Fc -d "$db" > "$YEDEK/$db.dump" || echo "HATA: $db"
done
ls -l "$YEDEK"                      # üç dosya, hiçbiri 0 bayt değil
# Dökümler okunuyor mu (içerik listesi; satır sayısı > 0)
for db in identity core chat; do
  printf '%s: ' "$db"
  kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- pg_restore --list < "$YEDEK/$db.dump" | grep -c 'TABLE DATA'
done
```

Geri yükleme gerekirse (yalnızca sıfırlama yanlış giderse; servisler 0'dayken):

```sh
# db: identity | core | chat. Var olan nesneleri silip dökümden kurar.
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- \
  pg_restore --clean --if-exists --single-transaction --exit-on-error -d "$db" < "$YEDEK/$db.dump"
```

R2'deki dosyalar yedeklenmez: geri yüklenen kayıtların görselleri gelmez.

## 2. Kuru çalışma

Hiçbir şey değiştirmez; servisler çalışırken yapılabilir.

```sh
for db in identity core chat; do sifirla "$db" '-c tayfam.mod=kuru'; echo; done
```

Beklenen (sayılar farklıdır):

```
NOTICE:  identity (identity): kuru çalışma, hiçbir şey silinmez
NOTICE:  silinecek  auth_identities                     57
...
NOTICE:  korunur    catalog_interests                   15
NOTICE:  toplam silinecek satır: 1234
NOTICE:  gerçek hesap (demo ve yönetici dışı, silinmemiş): 2 (eşik 5)
```

Kontrol et:

- Üç çıktıda da `ERROR` yok.
- Hiçbir satırda `EŞİK AŞILDI` yok.
- `korunur` satırlarında katalog sayıları sıfır değil (core
  `catalog_event_types`, `catalog_interests` …).

## 3. Sıfırlama

Sıra: Flux'ı durdur → gateway'i kapat → yüklemeleri R2'den sil → identity,
core ve chat'i kapat → üç veritabanını sil → Flux'ı aç.

```sh
# 3.1 Flux ölçeklemeyi geri almasın
flux -n flux-system suspend kustomization tayfam-prod

# 3.2 Dış trafiği kes (uygulama "bağlantı yok" görür)
kubectl -n "$NS" scale deploy/gateway --replicas=0
pod_yok gateway

# 3.3 Yüklemeleri silme kuyruğuna al, core'u yeniden başlat (temizlik açılışta
#     hemen çalışır, sonra 10 dakikada bir)
sifirla core "-c tayfam.mod=medya $ONAY"
kubectl -n "$NS" rollout restart deploy/core && kubectl -n "$NS" rollout status deploy/core --timeout=180s

# 3.4 Kuyruk boşalana kadar bekle (0 yazınca devam et)
for i in $(seq 1 40); do
  n=$(kubectl -n "$NS" exec tayfam-pg-1 -c postgres -- psql -X -tAc 'SELECT count(*) FROM uploads' -d core)
  echo "kalan yükleme: $n"; [ "$n" = 0 ] && break; sleep 30
done
#    20 dakikada 0 olmazsa: kubectl -n "$NS" logs deploy/core | grep -i 'medya'
#    ("nesneler silinemedi" R2 erişimini gösterir); yeniden başlatıp tekrar bekle.

# 3.5 Servisleri kapat
kubectl -n "$NS" scale deploy/identity deploy/core deploy/chat --replicas=0
for s in identity core chat; do pod_yok "$s"; done

# 3.6 Sil (her biri tek transaction; biri hata verirse yalnızca onu tekrar çalıştır)
for db in identity core chat; do sifirla "$db" "-c tayfam.mod=sil $ONAY" || break; echo; done
```

Her çıktının son satırı `NOTICE:  sıfırlandı: N satır silindi` olmalı. Hata
durumları:

| Hata | Anlamı |
|------|--------|
| `tayfam.mod kuru …` (TS001) | Ayar eksik ya da yanlış yazıldı. |
| `sil için tayfam.ortam … tayfam.onay …` (TS002) | Onay ifadesi birebir değil. |
| `bu betik … içindir` (TS003) | Betik başka veritabanında çalıştırıldı. |
| `sınıflandırılmamış tablo` / `şemada yok` (TS004) | Betik prod'daki sürümle aynı etiketten değil. 0. adım. |
| `eşiği geçiyor` (TS005) | Gerçek kullanıcı var. **Dur**, mimari oturumuna sor. |
| `bağlı … oturum var` (TS006) | Servis pod'u hâlâ ayakta. 3.5'i bekle. |
| `uploads boş değil` (TS007) | R2 temizliği bitmedi. 3.4. |
| `lock_timeout` | Başka bir bağlantı tabloyu tutuyor; birkaç saniye sonra tekrar. |

Hiçbiri veri silmez.

```sh
# 3.7 Flux'ı aç: replicas 1'e döner, pod'lar kalkar (migrate adımı bir şey yapmaz)
flux -n flux-system resume kustomization tayfam-prod
flux -n flux-system reconcile kustomization tayfam-prod
for s in identity core chat gateway; do kubectl -n "$NS" rollout status deploy/$s --timeout=300s; done
```

## 4. Doğrulama

```sh
# Bütün "silinecek" satırları 0, korunanlar 2. adımdaki gibi
for db in identity core chat; do sifirla "$db" '-c tayfam.mod=kuru' 2>&1 | grep -v ' 0$'; echo; done
kubectl -n "$NS" get pods                        # hepsi Running
curl -s https://api.tayfam.app/v1/status; echo   # 200 ve sürüm
```

- core'un kuru çıktısında `domain_events (diğer olaylar)` 0, `domain_events
  (catalog.changed)` sıfırdan büyük olabilir.
- R2: Cloudflare paneli → R2 → prod bucket'ı → nesne sayısı. `incoming/`
  altında kalan sahipsiz parçaları lifecycle kuralı 24 saatte siler;
  `media/` ve `exports/` boş olmalı.

Sonra betik klonunu kaldır; yedeği uygulamayı denedikten sonra (en geç
bir hafta içinde) sil:

```sh
cd ~ && rm -rf "$R"
# Her şey yolundaysa:
rm -rf "$YEDEK"
```

## 5. Platform sahibinin yeniden kaydı ve yönetici olarak eklenmesi

Eski hesabın ve oturumların yoktur; telefondaki uygulamada eski token'lar ve
eski cihazın E2E anahtarları kalır.

1. Kullandığın test cihazlarında uygulamanın verilerini temizle (Android:
   Ayarlar → Uygulamalar → Tayfam → Depolama → Verileri temizle) ya da
   uygulamayı kaldırıp yeniden kur.
2. Uygulamada Google ile gir, kaydı (doğum tarihi, onaylar) tamamla.
3. Kendini platform yöneticisi olarak ekle:
   [platform-yoneticisi.md](platform-yoneticisi.md) → "İlk yönetici
   (referans kodu elde yoksa)". Çıktıda tek satır görünmeli.
4. `admin.tayfam.app`'e gir; katalog ve engelli alan adları yerinde olmalı.
5. Uygulamada Profil → Davet et: yeni referans kodunu (`TYF-XXXXXX`) not al.

## 6. Demo verisi

[demo-verisi.md](demo-verisi.md) → "Çalıştırma" (yeni referans kodunla),
ardından "Panel onayı". `Demo hesapları: 54 yeni, 0 zaten vardı` beklenir.

## 7. Mağaza inceleme hesabı

1. İnceleme için **ayrı** bir Google hesabı aç (kişisel hesabın değil;
   adında gerçek bir kişi olmasın, ör. "Tayfam İnceleme"). Parolayı
   parola yöneticine kaydet; repoya, sohbete veya bir dokümana yazma.
2. Bir test cihazında (ya da uygulama verisi temizlenmiş cihazda) bu hesapla
   uygulamaya gir ve kaydı tamamla (yetişkin doğum tarihi, onaylar).
3. Kendi hesabınla Ege Klasikleri Demo'nun tayfa ayarlarından katılım kodunu
   al; inceleme hesabıyla kodu girip katılım isteği gönder; kendi hesabınla
   isteği onayla. İnceleme hesabı tayfayı, etkinlikleri ve sohbeti görmeli.
4. İnceleme hesabından çık (cihazda başka hesap kullanılacaksa verileri
   temizle).
5. Giriş bilgilerini **yalnızca** şuralara yaz:
   - Play Console → Uygulama içeriği → **Uygulama erişimi**: "Tüm işlevler
     veya bazı işlevler kısıtlanmış" → talimat: Google ile giriş, e-posta ve
     parola, "Ege Klasikleri Demo tayfasına üyedir".
   - App Store Connect → uygulama → **App Review Information → Sign-In
     Information**: aynı bilgiler.

İnceleme hesabı identity eşiğinde 1 gerçek hesap sayılır (eşik 5); sıfırlama
bundan sonra yeniden gerekmez.

## Prova (isteğe bağlı, dev)

Aynı adımlar ev cluster'ında `NS=tayfam-dev`, `flux … tayfam-dev`,
`ONAY='-c tayfam.ortam=dev -c tayfam.onay=DEV-SIFIRLA'` ile denenebilir;
betikler dev'deki sürümün etiketinden (ya da `dev` dalından) alınır.
