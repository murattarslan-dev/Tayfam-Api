-- Çalışma zamanı kullanıcısı core_app (ADR-0015, ADR-0011 madde 8).
--
-- Migration'ları veritabanının ve tüm nesnelerin sahibi olan core kullanıcısı
-- çalıştırır. Servis core_app ile bağlanır; sahip olmadığı için DDL yapamaz,
-- trigger kapatamaz (audit_logs, memberships, polls ve reports korumaları
-- dahil), tabloları boşaltamaz veya silemez. Tablolarda yalnızca
-- sorgularının (db/core/queries) ve arka plan işlerinin
-- (internal/core/app/jobs: kalıcı silme, saklama; hesap silme tüketicisi)
-- gerektirdiği DML'e sahiptir. TRUNCATE, REFERENCES ve TRIGGER hiçbir
-- tabloda verilmez; goose_db_version'a hiçbir yetkisi yoktur.
--
-- Satır kilitleri (FOR UPDATE, FOR NO KEY UPDATE; memberships trigger'ı
-- tayfas satırını kilitler) kilitlenen tabloda en az bir kolonda UPDATE
-- yetkisi ister. Foreign key kontrolleri ve ON DELETE CASCADE / SET NULL
-- tablo sahibinin yetkisiyle çalışır: tayfanın, etkinliğin, sorunun kalıcı
-- silinmesi bağlı tablolarda (audit_logs dahil) ayrıca DELETE gerektirmez.
--
-- core_app rolünü cluster'da CloudNativePG oluşturur
-- (deploy/base/postgres.yaml), yerelde scripts/postgres-init.sql, testlerde
-- dbtest. Rol yoksa bu migration hata verir ve servis başlamaz.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('GRANT CONNECT ON DATABASE %I TO core_app', current_database());
END
$$;
-- +goose StatementEnd
GRANT USAGE ON SCHEMA public TO core_app;

-- Soft delete edilen ve 30 gün sonra kalıcı silinen (jobs/retention) ya da
-- hesap silmede satırı silinen tablolar.
GRANT SELECT, INSERT, UPDATE, DELETE ON
  tayfas, memberships,
  announcements, questions, answers, polls,
  events, event_rsvps, event_camp_items, event_convoy_vehicles,
  event_hike_checkins, user_activity_records, fishing_catches,
  friendships, reports
  TO core_app;

-- Güncellenmeyen, yalnızca eklenip silinen bağ ve oy tabloları.
GRANT SELECT, INSERT, DELETE ON
  tayfa_interests, answer_votes, question_tags, tags, poll_votes, user_blocks
  TO core_app;

-- Silinmeyen (yalnızca cascade ile giden) ve güncellenen ya da ON CONFLICT
-- DO UPDATE ile yazılan tablolar.
GRANT SELECT, INSERT, UPDATE ON
  invites, tayfa_modules, user_profiles, event_series,
  event_camp, event_convoy, event_hike, event_fishing, event_gaming, event_sports
  TO core_app;

-- Anket seçenekleri yalnızca eklenir (polls_guard / poll_options_guard).
GRANT SELECT, INSERT ON poll_options TO core_app;

-- Outbox relay'inin hedef başına konumu (ADR-0002); satır silinmez.
GRANT SELECT, INSERT, UPDATE ON event_consumers TO core_app;

-- Outbox yalnızca eklenir ve okunur. Kolon düzeyinde iki dar istisna:
--   tayfa_id: outbox.Write ortak kolonları yazar, core'a özgü tayfa_id aynı
--             transaction'da hemen ardından doldurulur (SetDomainEventTayfa).
--   actor_id: hesap silmede NULL yapılır (veri modeli v14).
-- Tür, konu ve payload değiştirilemez; DELETE yoktur.
GRANT SELECT, INSERT ON domain_events TO core_app;
GRANT UPDATE (tayfa_id, actor_id) ON domain_events TO core_app;

-- Denetim kaydı yalnızca eklenir ve okunur. İki dar istisna, ikisi de
-- audit_logs_append_only trigger'ının (00001, 00011) izin verdiği
-- değişikliklerle sınırlıdır:
--   UPDATE (actor_id): hesap silmede actor_id NULL yapılır; trigger yalnızca
--     app.system_job = 'account_deletion' altında ve yalnızca bu değişikliğe
--     izin verir. Kolon yetkisi, saklama işinin FOR UPDATE SKIP LOCKED
--     kilidini de karşılar.
--   DELETE: saklama işi (jobs/retention) 365 günden eski kayıtları siler;
--     trigger yalnızca app.retention_job = 'audit_logs' altında ve 365
--     günden eski satırlar için izin verir. DELETE kolon düzeyinde
--     verilemez; sınır trigger'dadır ve trigger'ı core_app kapatamaz.
GRANT SELECT, INSERT ON audit_logs TO core_app;
GRANT UPDATE (actor_id) ON audit_logs TO core_app;
GRANT DELETE ON audit_logs TO core_app;

-- Platform banları (00014): identity'nin user.banned / user.unbanned
-- olaylarıyla yazılır (ApplyUserBan, ON CONFLICT DO UPDATE yalnızca
-- banned_at ve event_id'yi değiştirir), hesap silmede satır silinir.
GRANT SELECT, INSERT, DELETE ON user_platform_bans TO core_app;
GRANT UPDATE (banned_at, event_id) ON user_platform_bans TO core_app;

-- Arka plan işlerinin son çalışması (00015): her iş için tek satır,
-- RecordJobRun ON CONFLICT DO UPDATE ile yazar; satır silinmez. İş adı
-- (anahtar) değiştirilemez.
GRANT SELECT, INSERT ON job_runs TO core_app;
GRANT UPDATE (finished_at, succeeded, changed, last_succeeded_at) ON job_runs TO core_app;

-- Sonraki migration'larda açılan tablolar varsayılan olarak yalnızca okunur
-- ve eklenir; UPDATE ve DELETE gereken tablo bunu kendi migration'ında açıkça
-- verir. Sıra (sequence) ve fonksiyonlar için varsayılan yetki verilmez.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT ON TABLES TO core_app;

-- +goose Down
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT ON TABLES FROM core_app;
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM core_app;
REVOKE USAGE ON SCHEMA public FROM core_app;
-- +goose StatementBegin
DO $$
BEGIN
  EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM core_app', current_database());
END
$$;
-- +goose StatementEnd
