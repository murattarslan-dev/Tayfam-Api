-- Duyurunun düzenlenme zamanı (issue #191; platform sahibi 2026-11-14).
--
-- updated_at sabitleme ve sabitlemeyi kaldırmada da değişir; istemcinin
-- "düzenlendi" etiketi bu yüzden yanlış görünüyordu. edited_at yalnızca
-- başlık ya da gövde değiştiğinde yazılır. Mevcut duyurularda boş kalır:
-- geçmiş tahmin edilmez.
--
-- Geri alma planı: Down kolonu düşürür (yalnızca bu migration'la oluşan
-- bilgi kaybolur).

-- +goose Up
ALTER TABLE announcements ADD COLUMN edited_at timestamptz;
ALTER TABLE announcements ADD CONSTRAINT announcements_edited_at CHECK (edited_at IS NULL OR edited_at >= created_at);

-- +goose Down
ALTER TABLE announcements DROP CONSTRAINT announcements_edited_at;
ALTER TABLE announcements DROP COLUMN edited_at;
