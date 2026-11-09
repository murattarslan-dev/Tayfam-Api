# ADR-0015: Veritabanında sahip ve çalışma zamanı kullanıcısının ayrılması

- **Durum:** Önerildi
- **Tarih:** 2026-11-09
- **Karar veren:** Murat Tarslan
- **İlgili:** [ADR-0002](0002-servis-sinirlari-ve-veri-sahipligi.md), [ADR-0006](0006-dagitim-ve-ortamlar.md), [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)

## Bağlam

identity, core ve chat çalışma zamanında kendi veritabanlarının sahibi olan
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
   verir.** Her servisin `*_app_role` migration'ı (identity `00008`, core
   `00016`, chat `00009`) `CONNECT`, şemada `USAGE` ve tablo başına yalnızca
   sorguların (`db/<servis>/queries`) ve arka plan işlerinin (kalıcı silme,
   saklama, hesap silme tüketicisi) gerektirdiği DML'i verir. `TRUNCATE`,
   `REFERENCES`, `TRIGGER` hiçbir tabloda verilmez; `goose_db_version`'a
   yetki yoktur; `TEMP` ve şemada `CREATE` yoktur.
3. **Yalnızca eklenen tablolar yetki düzeyinde de öyledir.** `audit_logs`,
   `domain_events`, `consent_records` temelde yalnızca `SELECT, INSERT`
   alır. Gerekli istisnalar en dar biçimde ve trigger'la sınırlı verilir:
   - `audit_logs` (core): `UPDATE (actor_id)` (hesap silmede NULL'lama) ve
     `DELETE` (365 günden eski kayıtların saklama silmesi). İkisinin de
     sınırı `audit_logs_append_only` trigger'ındadır; `core_app` onu
     kapatamaz.
   - `domain_events` (core): `UPDATE (tayfa_id, actor_id)` (outbox'ın
     ardından tayfa_id'nin yazılması, hesap silmede actor_id). `DELETE` yok.
   - `domain_events` (identity): `UPDATE (actor_id)` ve `DELETE`; DELETE'i
     `domain_events_delete_guard` trigger'ı yalnızca `user.profile_updated`
     olaylarıyla sınırlar (hesap silmede görünen adı taşıyan olaylar).
   - `domain_events` (chat) ve `consent_records`: yalnızca `SELECT, INSERT`.
   - `platform_audit_logs` (identity): `SELECT, INSERT` ve 1 yıllık saklama
     silmesi için `DELETE`; sınırı `platform_audit_logs_append_only`
     trigger'ındadır (güncelleme ve 365 günden yeni satırın silinmesi
     reddedilir).
   - `platform_admins` (identity): yalnızca `SELECT`. Yönetici kaydını
     uygulama ekleyemez, değiştiremez, silemez; platform sahibi migration
     kullanıcısıyla ekler (`docs/runbooks/platform-yoneticisi.md`).
   - Kolon düzeyinde `UPDATE`: değişmemesi gereken alanları olan tablolarda
     yalnızca sorguların yazdığı kolonlar güncellenebilir —
     `platform_sanctions` (`lifted_at, lifted_by`), `user_platform_bans`
     (`banned_at, event_id`), `job_runs` (iş adı hariç), `dm_messages` (silme ve hesap silme kolonları;
     mesaj düzenlenemez), `dm_conversations` (kimlik ve `created_at` hariç),
     `dm_conversation_reads` (`last_read_message_id, read_at`).
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
- Bir arka plan işi yeni bir yetki isterse en dar biçimde verilir:
  kolon düzeyinde `UPDATE`, ya da kolon düzeyinde verilemeyen `DELETE` için
  satırları sınırlayan bir trigger (ya da sahibe ait `SECURITY DEFINER`
  bir fonksiyon).
- Kalıcı silme işleri için içerik tablolarında `DELETE` vardır; servisi ele
  geçiren biri bu tablolardaki satırları (tayfanın cascade ile tüm verisi
  dahil) silebilir. Bunu "soft delete'ten 30 gün geçmiş" koşuluna bağlayan
  trigger'lar ayrı bir iştir.
- Sahip kullanıcının adında `_owner` eki yoktur; sahip olduğu servis adını
  taşır.
- Platform sahibi ortam başına bir kez `scripts/new-db-secrets.sh` ile
  çalışma zamanı secret'larını üretir (`identity-app-db`, `core-app-db`,
  `chat-app-db`).

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
