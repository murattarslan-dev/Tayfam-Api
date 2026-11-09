-- Referans kodu (veri modeli v14 "Arkadaşlık ve Davet (v11)", issue #71).
--
-- ref_code_lookups: kodla kullanıcı arama denemeleri. Kod tahminiyle
--   kullanıcı taramaya karşı kullanıcı başına saatte 10 arama sınırı
--   veritabanından sayılır; servis yeniden başlasa da sıfırlanmaz. Kod,
--   sonuç veya aranan kişi saklanmaz; yalnızca kimin ne zaman aradığı.
--   Bir saatten eski satırlar aynı kullanıcının bir sonraki aramasında
--   silinir, hesap silinince hepsi gider.
--
-- users.referred_by_user_id bir kez yazılır ve sonra değiştirilemez
--   (Notion: "bir kez set edilir, sonradan değiştirilemez"). referred_at
--   bunun işaretidir: dolduktan sonra ikisi de değişmez. Tek istisna
--   referans veren hesabın kalıcı silinmesidir (ON DELETE SET NULL):
--   referred_by_user_id NULL olur, referred_at kalır ve yeni bir referans
--   yazılamaz.

-- +goose Up
CREATE TABLE ref_code_lookups (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  looked_up_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ref_code_lookups_user_time_idx ON ref_code_lookups (user_id, looked_up_at);

ALTER TABLE users ADD CONSTRAINT users_referred_at CHECK (referred_by_user_id IS NULL OR referred_at IS NOT NULL);

-- +goose StatementBegin
CREATE FUNCTION users_referral_once() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.referred_at IS NOT NULL AND (
       NEW.referred_at IS DISTINCT FROM OLD.referred_at
       OR (NEW.referred_by_user_id IS DISTINCT FROM OLD.referred_by_user_id
           AND NEW.referred_by_user_id IS NOT NULL)) THEN
    RAISE EXCEPTION 'referans bir kez yazılır' USING ERRCODE = 'TI001';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER users_referral_once BEFORE UPDATE OF referred_by_user_id, referred_at ON users
  FOR EACH ROW EXECUTE FUNCTION users_referral_once();

-- +goose Down
DROP TRIGGER users_referral_once ON users;
DROP FUNCTION users_referral_once();
ALTER TABLE users DROP CONSTRAINT users_referred_at;
DROP TABLE ref_code_lookups;
