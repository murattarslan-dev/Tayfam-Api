# ADR-0014: Veritabanında sahip ve çalışma zamanı kullanıcısının ayrılması

- **Durum:** Önerildi
- **Tarih:** 2026-11-09
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)

## Bağlam

identity ve core çalışma zamanında kendi veritabanlarının sahibi olan
kullanıcıyla bağlanıyordu. Sahip her şeyi yapabilir: trigger'ları kapatır
(`ALTER TABLE … DISABLE TRIGGER`), `audit_logs`'u boşaltır, kısıtları ve
tabloları siler. Bu durumda core'daki üyelik ve denetim kaydı trigger'ları
yalnızca uygulama hatalarına karşı korur; servisi ele geçiren saldırgana
karşı korumaz (ADR-0011 sertleştirme listesi madde 8, issue #39).

## Karar

1. **Servis başına iki Postgres kullanıcısı.**
   - `<servis>` (sahip): veritabanının, şemanın ve tüm nesnelerin sahibi.
     Yalnızca pod'un `migrate` initContainer'ı kullanır; parolası
     `secrets/<servis>-db`'dedir. Mevcut kullanıcı adı korunur: canlı
     veritabanlarında sahiplik devri (`REASSIGN OWNED`) ve yeniden
     adlandırma gerekmez.
   - `<servis>_app` (çalışma zamanı): servis container'ı bununla bağlanır;
     parolası `secrets/<servis>-app-db`'dedir. Hiçbir nesnenin sahibi
     değildir, rol özelliği (CREATEDB, CREATEROLE, BYPASSRLS) ve üyeliği
     yoktur. Servis container'ı sahibin parolasını görmez.
2. **Kullanıcıları CloudNativePG `managed.roles`, yetkileri migration'lar
   verir.** Her servisin `*_app_role` migration'ı `CONNECT`, şemada `USAGE` ve
   tablo başına yalnızca sorguların (`db/<servis>/queries`) gerektirdiği
   DML'i verir. `TRUNCATE`, `REFERENCES`, `TRIGGER` hiçbir tabloda verilmez;
   `goose_db_version`'a yetki yoktur; `TEMP` ve şemada `CREATE` yoktur.
3. **Yalnızca eklenen tablolar yetki düzeyinde de öyledir.** `audit_logs`,
   `domain_events`, `consent_records`: yalnızca `SELECT, INSERT`. Trigger'lar
   ikinci savunma hattı olarak kalır.
4. **Varsayılan yetkiler dardır.** Sonraki migration'larda açılan tablolar
   `ALTER DEFAULT PRIVILEGES` ile yalnızca `SELECT, INSERT` alır. `UPDATE` ve
   `DELETE` gereken tablo bunu kendi migration'ında açıkça verir.
5. **Testler aynı rollerle çalışır.** `dbtest` her test veritabanını sahip
   kullanıcıyla oluşturup migration'ı onunla uygular; testlere çalışma
   zamanı kullanıcısıyla davranan bir havuz verir. Her servisin
   `TestAppRolePrivileges` testi tablo yetkilerinin tam listesini sabitler;
   trigger kapatma, `TRUNCATE`, `DROP`, denetim kaydı güncelleme gibi
   denemelerin reddedildiği de testle sabittir.

## Gerekçe

- Servisi ele geçiren saldırgan uygulamanın yapabildiğinden fazlasını
  yapamaz: denetim kaydını ve outbox'ı silemez, üyelik korumasını kapatamaz,
  şemayı değiştiremez. Trigger'lar böylece gerçek bir güvenlik sınırı olur.
- Mevcut kullanıcıyı sahip olarak bırakmak geçişin en az riskli yoludur:
  veri, tablo ve sahiplik değişmez; migration yalnızca `GRANT` ekler; geri
  dönüş servisin eski secret'a dönmesidir.
- Yetkilerin migration'da olması onları sürümlü, gözden geçirilmiş ve testli
  yapar; CNPG yalnızca kullanıcıyı ve parolasını yönetir.

## Sonuçlar

- Yeni bir sorgu yeni bir yetki gerektiriyorsa aynı PR'da bir migration
  ile verilir ve `TestAppRolePrivileges` güncellenir; unutulursa testler
  yetki hatasıyla düşer.
- Arka plan işleri (30 günlük kalıcı silme, hesap silmede denetim
  kaydındaki `actor_id`'nin boşaltılması) geldiğinde ihtiyaç duydukları
  yetki ayrıca ve en dar biçimde verilir (örneğin kolon düzeyinde
  `UPDATE (actor_id)` ya da sahibe ait `SECURITY DEFINER` bir fonksiyon).
- Sahip kullanıcının adında `_owner` eki yoktur; sahip olduğu servis adını
  taşır.
- Platform sahibi ortam başına bir kez `scripts/new-db-secrets.sh` ile
  çalışma zamanı secret'larını üretir.

## Değerlendirilen alternatifler

- **Yeni `<servis>_owner` kullanıcısı açıp sahipliği devretmek:** Adlandırma
  daha açık olurdu, ama canlı veritabanında süper kullanıcıyla
  `REASSIGN OWNED` ve `ALTER DATABASE … OWNER` gerektirir; identity'nin
  initdb ile oluşturulan sahibi CNPG'nin yönetmediği bir kullanıcıdır ve
  geçiş sırasında migrate initContainer'ının hangi kullanıcıyla bağlanacağı
  ayrıca sıralanmalıdır. Kazanç yalnızca isimdir.
- **Mevcut kullanıcıyı `ALTER ROLE … RENAME` ile yeniden adlandırmak:**
  Süper kullanıcı adımı, secret'ın kullanıcı adının değişmesi ve CNPG
  `managed.roles` ile senkron bir geçiş gerektirir; aynı kazanç.
- **Varsayılan yetki vermemek:** Her tablo açıkça verilirdi; ama yalnızca
  eklenen tablolar için `SELECT, INSERT` güvenli bir başlangıçtır ve
  yetki listesi testi her durumda açık karar ister.
- **Row Level Security:** Tayfa kapsamını veritabanında zorlar; ayrı bir
  maddedir (ADR-0011 madde 9) ve bu ayrımın üstüne kurulur.
