# ADR-0046: Pilot öncesi prod sıfırlama (bir kerelik, servis başına SQL betiği)

- **Durum:** Önerildi (mimari oturumu, 2026-11-28; issue #369). Sıfırlama
  kararı platform sahibinindir (2026-11-27/28, sohbette: "Önce prod'u
  sıfırla", ardından demo-seed ve mağaza inceleme hesabı); aracın biçimi,
  eşik ve korunan tablolar bu ADR'nin önerisidir ve PR'da onaya sunulur.
- **Tarih:** 2026-11-28
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md),
  [ADR-0007](0007-dosyalar-ve-medya.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md),
  [ADR-0015](0015-veritabani-rolleri.md),
  [ADR-0023](0023-demo-verisi.md),
  [ADR-0035](0035-kesfet-herkese-acik-ve-demo-temizligi.md),
  [ADR-0042](0042-katalog-degisikligi-olayi.md)
- **Değiştirdiği:** ADR-0023 Gerekçe'deki "prod'da toplu silme yapabilen bir
  araç bulundurmamak" ilkesine bir kerelik, kendini kapatan bir istisna
  getirir (aşağıda "Neden ilkeyle çelişmiyor").

## Bağlam

Prod, pilot öncesi bir oyun alanı olarak kullanıldı: platform sahibinin ve
test hesaplarının tayfaları, içerikleri, sohbetleri, cihazları ve demo
verisi var. Canlıya açılmadan önce bunların hepsi silinmeli; şema, migration
geçmişi ve platform sahibinin panelden girdiği katalog kalmalı. Sonra
`demo-seed` yeniden çalışır ve mağaza inceleme hesabı hazırlanır.

`demo-purge` (ADR-0035) yalnızca işaretli demo kayıtlarını normal silme
yollarıyla kaldırır; gerçek hesaplara dokunmaz ve dokunmamalıdır.

Servislerin çalışma zamanı kullanıcıları (`<servis>_app`, ADR-0015)
bilerek dar yetkilidir: `TRUNCATE` yoktur; `audit_logs`, `domain_events`,
`consent_records`, `platform_audit_logs` yalnızca eklenir ve trigger'lar
boşaltmayı reddeder; `platform_admins` salt okunurdur. Bütün veriyi
uygulamanın kendi yolundan silmek mümkün değildir.

## Karar

### 1. Araç: servis başına bir SQL betiği, servis ikilisinde değil

- `db/<servis>/reset/pilot-oncesi-sifirlama.sql` (identity, core, chat).
  Her betik yalnızca kendi veritabanında çalışır ve veritabanının sahibinin
  o servis olduğunu doğrular: core betiği identity'de çalışmaz (kesin kural
  2; veritabanları arası iş yoktur).
- Migration değildir: goose `migrations/`'ı okur, `reset/`'i okumaz. Servis
  ikilisine gömülmez; çalışan hiçbir pod'da bulunmaz.
- Platform sahibi, `platform-yoneticisi.md`'deki gibi Postgres pod'unda
  `kubectl exec … psql` ile çalıştırır; betik stdin'den gelir. İçinde
  `SET LOCAL ROLE <servis>` ile veritabanının sahibine geçer (süper kullanıcı
  yetkisiyle iş yapmaz).
- Hiçbir otomasyon çalıştırmaz; yeni manifest, Job, ServiceAccount veya
  secret yoktur.

### 2. Kapsam

- Her tablo betikte iki listeden birindedir: **silinen** (tamamı `TRUNCATE`)
  ya da **korunan**. Şemada listelerde olmayan bir tablo varsa ya da listedeki
  bir tablo şemada yoksa betik çalışmaz (`TS004`): yeni bir migration'ın
  tablosu sessizce silinmez ya da unutulmaz.
- Korunanlar:
  - identity: `catalog_copy`, `catalog_interests` (core kataloğunun kopyası,
    ADR-0042), `goose_db_version`.
  - core: `catalog_*` (yedi tablo; ADR-0024, ADR-0025), `blocked_domains`
    (platform kuralı; `created_by` hesap silmede olduğu gibi NULL'a çekilir),
    `job_runs` (iş çalışma kayıtları; kişisel veri yok), `goose_db_version`.
    `domain_events`'te yalnızca `catalog.changed` olayları kalır; tüketici
    konumları silinir ve identity kopyası olayları yeniden alır (olay sürüm
    taşır, identity yalnızca yeni sürümü uygular; tekrar teslim zararsızdır).
  - chat: yalnızca `goose_db_version`.
- `platform_admins` korunmaz: `users`'a FK ile bağlıdır, kullanıcı silinince
  satır anlamsızdır (`ON DELETE CASCADE`). Platform sahibi uygulamada yeniden
  kaydolur ve `platform-yoneticisi.md` "İlk yönetici" adımıyla kendini
  yeniden ekler. Sahibin hesabını korumak, core ve chat'teki profil
  kopyaları silindiği için tutarsız bir hesap bırakırdı.
- `platform_audit_logs` ve core `audit_logs` silinir: silinen hesaplara ve
  tayfalara bağlı test kayıtlarıdır. Yalnızca eklenen tabloların TRUNCATE
  yasağı aynı transaction içinde kapatılıp açılır; başka bir oturum ara
  durumu göremez.

### 3. R2'deki yüklemeler

- R2 nesneleri SQL ile silinemez. core betiğinin `medya` adımı bütün
  `uploads` satırlarını `deleting` durumuna alır; çalışan core'un medya
  temizliği (ADR-0007, `media.Cleanup`) her satırın nesnelerini
  (`incoming/`, `media/`, `exports/`) siler ve satırı kaldırır. Yeni kod ve
  yeni R2 yetkisi gerekmez.
- core `sil` adımı `uploads` boş değilse çalışmaz (`TS007`); R2'si olmayan
  ortam için `tayfam.r2=atla`.

### 4. Kilitler

- `tayfam.mod` zorunludur: `kuru` (yalnızca sayar), `medya` (core), `sil`.
  Değer yoksa hiçbir şey yapmaz.
- `medya` ve `sil` için `tayfam.ortam` (`dev` | `prod`) ve elle yazılan
  `tayfam.onay` (`PROD-SIFIRLA`, `DEV-SIFIRLA`) birebir.
- `sil`, servisin kendi kullanıcılarıyla (`<servis>`, `<servis>_app`) bağlı
  oturum varken çalışmaz (`TS006`): servis 0'a ölçeklenmiş olmalıdır.
- Tek DO bloğu, tek transaction; bir adım hata verirse hiçbir şey silinmez.
  Sonunda silinen her tablonun boş olduğu doğrulanır.
- Çıktı yalnızca tablo adları ve satır sayılarıdır (NOTICE). Kişisel veri,
  kimlik, e-posta yazılmaz; Postgres sorgu metnini log'a yazmaz
  (`log_statement: none`).

### 5. Kendini kapatma: gerçek kullanıcı eşiği

Her betik, yalnızca gerçek kişilerin üretebildiği bir izi sayar; eşik
aşılırsa `medya` ve `sil` reddedilir (`TS005`), `kuru` aşımı yazar:

| Servis | Sayılan | Eşik |
|--------|---------|------|
| identity | silinmemiş, demo kimliği olmayan, `platform_admins`'te olmayan hesap | 5 |
| core | adında "Demo" geçmeyen tayfalardaki farklı üye (ADR-0023 §2) | 6 |
| chat | push token'ı ya da E2E cihaz anahtarı olan farklı kullanıcı (demo hesaplarının cihazı yoktur) | 6 |

Gerekçe: pilot öncesi gerçek hesaplar platform sahibi ve birkaç test hesabıdır
(ikinci Google hesabı, inceleme hesabı). İlk birkaç gerçek kullanıcı katıldığı
anda eşik aşılır ve betik kendiliğinden kullanılamaz olur; gerekirse yeniden
açmak bir PR (eşiği değiştirmek) ister. core ve chat demo hesaplarını
bilemez; onun için demo verisinin bulunmadığı yerleri sayarlar.

## Neden ilkeyle çelişmiyor (ADR-0011, ADR-0023)

- ADR-0023'ün kaygısı, ele geçirilen bir bileşenin toplu silme yapabilmesidir.
  Betik hiçbir servis ikilisinde ve pod'da yoktur; çalıştırmak Postgres
  pod'unda `exec` yetkisi ister, o yetkiyle zaten her şey yapılabilir. Yeni
  bir yetenek eklenmez.
- ADR-0023/0035'te SQL ile yazma ve silme, gerçek veriyle yan yana duran
  demo verisinin denetim kaydını, outbox'ı ve profil kopyalarını atlayacağı
  için reddedildi. Burada üç veritabanı birlikte boşaltılır; tutarlı kalması
  gereken bir kopya yoktur. Her veritabanı kendi outbox'ını ve tüketici
  konumlarını birlikte siler.

## Sonuçlar

- Sıfırlama üç ayrı transaction'dır (veritabanı başına bir); aralarında
  servisler kapalıdır. Biri hata verirse yalnızca o tekrar çalıştırılır;
  betikler tekrar çalıştırılabilir.
- Yeni bir tablo ekleyen her migration, eşik aşılana kadar ilgili betiğin
  listesine de eklenmelidir; `TestPilotReset` (identity, core, chat store)
  bunu sınar.
- Eşik aşıldıktan sonra betikler ve testleri silinebilir (ayrı bir iş).
- Runbook: [`docs/runbooks/pilot-oncesi-sifirlama.md`](../runbooks/pilot-oncesi-sifirlama.md).

## Değerlendirilen alternatifler

- **Her servis ikilisinde `reset-prod` alt komutu (demo-purge gibi):**
  Çalışma zamanı kullanıcısının yetkisi yetmez; ya sahip parolası servis
  container'ına verilir ya da `_app` kullanıcısına `TRUNCATE` ve yalnızca
  eklenen tablolarda `DELETE` açılır. İkisi de ADR-0015'i kalıcı olarak
  gevşetir ve toplu silmeyi çalışan pod'a koyar. Reddedildi.
- **Veritabanlarını silip yeniden oluşturmak (`DROP DATABASE`):** Katalog
  ve engelli alan adları da gider; katalog migration'lardaki ilk haline döner,
  platform sahibinin panelden yaptığı değişiklikler kaybolur. CNPG
  `Database` kaynakları ve rol yetkileri de yeniden kurulmalıdır. Reddedildi.
- **Kullanıcıları tek tek hesap silme akışından geçirmek:** Kapanan hesap
  satırları 30 gün durur, tayfalar 72 saat bekler, denetim kaydı ve outbox
  kalır; "sıfır" durumu oluşmaz. Reddedildi.
- **Silinecekleri betik yerine şemadan çıkarmak (korunan dışındaki her
  tablo):** Yeni bir yapılandırma tablosu sessizce silinirdi. Açık iki liste
  ve sınıflandırma kilidi seçildi.
- **Tarihe bağlı kapanma:** Pilot tarihi belli değil; eşik veriye bakar.
