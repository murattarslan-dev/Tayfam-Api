# Pilot öncesi sıfırlama

Pilot öncesi denemelerden kalan veriyi tek seferde temizler: **platform
yöneticileri dışındaki bütün hesaplar, bütün tayfalar, etkinlikler ve onlara
bağlı her şey** silinir. Yalnızca prod'da, gerçek kullanıcı yokken
çalıştırılır; gerçek kullanıcı verisi varsa kullanılmaz (hesap silme akışı
ve KVKK süreçleri atlanır).

Araç: `scripts/pilot-sifirla.sh` ve `scripts/pilot-sifirla/{chat,core,identity}.sql`.
Bayraksız çalıştırıldığında her veritabanında işi yapar, sayıları yazar ve
**ROLLBACK** eder; `--uygula` ile COMMIT eder ve namespace adıyla onay ister.
Korunacak hesaplar her seferinde `platform_admins`'ten okunur; tablo boşsa
hiçbir şey silinmez. Yarıda kalırsa aynı komut tekrar çalıştırılır.

## Ne kalır, ne gider

| Veritabanı | Kalır | Gider |
|---|---|---|
| identity | yöneticilerin hesabı, oturumları, cihazları, onayları; `platform_admins`; `platform_audit_logs` (yalnızca eklenir); katalog kopyası; outbox | diğer bütün hesaplar (oturum, cihaz, yenileme token'ı, kimlik, profil ayrıntısı, yaptırım cascade ile); bütün veri dışa aktarma kayıtları; silinenlerin `user.profile_updated` olayları |
| core | etkinlik kataloğu (`catalog_*`), engelli alan adları, iş kayıtları, outbox; yöneticilerin profili, sosyal ayarları, müsaitliği, garajı, avatarı ve garaj fotoğrafları | bütün tayfalar, üyelikler, davetler, etkinlikler ve bileşenleri, duyurular, soru-cevap, anketler, raporlar, tayfa denetim kayıtları, arkadaşlıklar, engeller, rozetler, istatistikler, Keşfet kayıtları; diğer kullanıcıların profil ve ayarları |
| chat | yöneticilerin profili, cihaz anahtarları, E2E durumu, push token'ları; outbox | bütün kanallar ve mesajlar, birebir sohbetler, bildirimler ve push kuyruğu, bildirim tercihleri; diğer kullanıcıların anahtarları ve token'ları |

- **Dosyalar:** R2'deki nesneler doğrudan silinmez; `uploads` satırları
  `deleting` durumuna alınır ve core'un medya işi nesneyi, ardından satırı
  normal yoldan siler.
- **Outbox:** olaylar silinmez (identity'de silme trigger'la yasak, core
  katalog yayını `catalog.changed` olaylarını okur). Hesap silme akışındaki
  gibi silinen kullanıcılar aktör olmaktan çıkarılır.
- **Koruma trigger'ları:** core'da denetim kaydının TRUNCATE yasağı ve
  üyelik/anket korumaları yalnızca o transaction için kapatılır
  (`SET LOCAL session_replication_role = replica`); identity ve chat'te açık
  kalır.

## Çalıştırma

VDS'te, kubectl'in prod cluster'ını gösterdiği terminalde
([VDS kurulumu](vds-kurulumu.md) adım c).

Önce test cihazlarında uygulamayı kapat. Silinen hesapların yenileme
token'ları hemen gider, ama erişim token'ları en fazla 15 dakika daha geçerli
kalır. Bu sürede açık kalan bir istemci yeni veri yazabilir.

```sh
cd "$TAYFAM_REPO" && git fetch origin && git switch --detach origin/dev
export NS=tayfam-prod

# 1. Deneme: sayıları gösterir, hiçbir şey silmez
./scripts/pilot-sifirla.sh
```

Çıktıda önce `Korunacak platform yöneticisi: N` satırı gelir; N, panel
yöneticilerinin sayısıdır. Ardından her veritabanı için bir `önce` ve bir
`sonra` satırı yazılır ve çıktı `Bitti (ROLLBACK)` ile biter. `sonra`
satırlarında `tayfa`, `etkinlik`, `kanal` ve `dm` 0 olmalıdır. identity'de
`kullanici` ile `korunan` sayısı eşit olmalıdır.

```sh
# 2. Yedek (prod'da otomatik yedek yok). Yalnızca VDS'te kalır.
umask 077; mkdir -p ~/sifirlama-yedek
for db in identity core chat; do
  kubectl -n "$NS" exec tayfam-pg-1 -c postgres -- pg_dump -Fc -d "$db" > ~/sifirlama-yedek/"$db".dump
done
ls -l ~/sifirlama-yedek

# 3. Uygula (namespace adını yazarak onaylarsın)
./scripts/pilot-sifirla.sh --uygula
```

## Kontrol

```sh
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d identity -tAc \
  "SELECT count(*) = (SELECT count(*) FROM platform_admins) FROM users"
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d core -tAc \
  "SELECT (SELECT count(*) FROM tayfas), (SELECT count(*) FROM events), (SELECT count(*) FROM uploads WHERE status = 'deleting')"
```

Beklenen: `t`; ardından `0|0|<N>`. N, birkaç dakika içinde 0'a iner (medya
işi R2'yi temizler).

Uygulamada yönetici hesabıyla gir: Tayfa sekmesi boş, profil ve avatar
yerinde olmalıdır. Panel kullanıcı sayısı yönetici sayısına iner.

Her şey yerindeyse yedeği sil:

```sh
rm -r ~/sifirlama-yedek
```

## Geri dönüş

Bir şey ters giderse, adım 2'deki yedekten ilgili veritabanını geri yükle.
Bunu mimari oturumuyla birlikte yap: servisleri durdurmak ve outbox
konumlarını kontrol etmek gerekir.

## Hata durumları

| Çıktı | Anlamı |
|-------|--------|
| `platform_admins boş; hiçbir şey silinmedi` | Panel yöneticisi yok. Önce [kendini ekle](platform-yoneticisi.md). |
| `NS ayarlı değil` | `export NS=tayfam-prod` eksik. |
| `Onaylanmadı; hiçbir şey silinmedi.` | Onayda namespace adı yanlış yazıldı. |
| `cannot execute ... in a read-only transaction` | `tayfam-pg-1` şu an birincil değil. `PG_POD=<birincil pod> ./scripts/pilot-sifirla.sh` ile tekrar çalıştır (birincil: `kubectl -n "$NS" get pods -l cnpg.io/instanceRole=primary`). |
| `ERROR: ...` (diğer) | O veritabanının transaction'ı geri alındı. Önceki veritabanları uygulanmış olabilir. Çıktıyı mimari oturumuna ilet; düzeltmeden sonra aynı komut tekrar çalıştırılır. |
