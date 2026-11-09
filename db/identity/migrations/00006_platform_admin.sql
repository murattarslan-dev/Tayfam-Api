-- Platform yönetim paneli erişim katmanı (issue #104 1. aşama; Notion
-- "Platform Yönetim Paneli", veri modeli v14 "Platform Yönetimi ve Abonelik
-- Altyapısı"; ADR-0008).
--
-- platform_admins: panele girebilecek kullanıcılar. Uygulama bu tabloya
--   hiçbir zaman yazmaz (db/identity/queries'de INSERT/UPDATE/DELETE yoktur;
--   store testi bunu sabitler). Kayıt yalnızca platform sahibinin migration
--   kullanıcısıyla çalıştırdığı tek SQL komutuyla eklenir
--   (docs/runbooks/platform-yoneticisi.md). Çalışma zamanı kullanıcısı
--   (identity_app, #45) yalnızca SELECT alır.
--   granted_by NULL ise kayıt platform sahibince doğrudan eklenmiştir.
--
-- platform_audit_logs: panelden yapılan her işlem gerekçesiyle buraya
--   yazılır. Yalnızca eklenir: güncelleme ve TRUNCATE trigger'la, 365 günden
--   yeni satırın silinmesi trigger'la reddedilir (ADR-0008 saklama: 1 yıl;
--   eski satırlar saklama işiyle silinir). actor_id bilerek foreign key
--   değildir: yöneticinin hesabı silinse de kayıt bir yıl kalır. Gerekçe
--   zorunludur. Kişisel veri (e-posta, mesaj metni, konum) metadata'ya
--   yazılmaz; yalnızca kimlikler ve kısa değerler.
--   identity_app (#45) SELECT, INSERT ve (saklama işi için) DELETE alır.

-- +goose Up
CREATE TABLE platform_admins (
  user_id    uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  granted_at timestamptz NOT NULL DEFAULT now(),
  granted_by uuid REFERENCES users (id) ON DELETE SET NULL,

  CONSTRAINT platform_admins_not_self_granted CHECK (granted_by IS NULL OR granted_by <> user_id)
);

CREATE TABLE platform_audit_logs (
  id          uuid PRIMARY KEY,
  actor_id    uuid NOT NULL,
  action      text NOT NULL,
  target_type text,
  target_id   uuid,
  reason      text NOT NULL,
  metadata    jsonb NOT NULL DEFAULT '{}',
  created_at  timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT platform_audit_logs_action_format CHECK (action ~ '^[a-z][a-z_]{0,30}(\.[a-z][a-z_]{0,30}){1,2}$'),
  CONSTRAINT platform_audit_logs_target_type CHECK (target_type IS NULL OR target_type IN ('user', 'tayfa', 'membership', 'report', 'moderation_term', 'subscription')),
  CONSTRAINT platform_audit_logs_target CHECK ((target_type IS NULL) = (target_id IS NULL)),
  CONSTRAINT platform_audit_logs_reason_len CHECK (char_length(btrim(reason)) BETWEEN 10 AND 1000 AND char_length(reason) <= 1000),
  CONSTRAINT platform_audit_logs_metadata CHECK (jsonb_typeof(metadata) = 'object' AND octet_length(metadata::text) <= 4096)
);
CREATE INDEX platform_audit_logs_created_at_idx ON platform_audit_logs (created_at);
CREATE INDEX platform_audit_logs_actor_idx ON platform_audit_logs (actor_id, created_at);
CREATE INDEX platform_audit_logs_target_idx ON platform_audit_logs (target_type, target_id, created_at) WHERE target_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION platform_audit_logs_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.created_at < now() - interval '365 days' THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION 'platform denetim kaydı değiştirilemez' USING ERRCODE = 'TI003';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER platform_audit_logs_append_only BEFORE UPDATE OR DELETE ON platform_audit_logs
  FOR EACH ROW EXECUTE FUNCTION platform_audit_logs_append_only();
CREATE TRIGGER platform_audit_logs_no_truncate BEFORE TRUNCATE ON platform_audit_logs
  FOR EACH STATEMENT EXECUTE FUNCTION platform_audit_logs_append_only();

-- +goose Down
DROP TABLE platform_audit_logs;
DROP FUNCTION platform_audit_logs_append_only();
DROP TABLE platform_admins;
