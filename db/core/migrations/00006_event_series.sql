-- Tekrarlayan etkinlik üretimi (issue #51; veri modeli v14 "Tekrarlayan
-- etkinlik", "Arka plan işleri").
--
-- Örnekler gerçek events satırlarıdır. Arka plan işi önümüzdeki 8 haftanın
-- örneklerini üretir ve generated_until'ı ilerletir. Aynı örnek iki kez
-- üretilemez: (series_id, starts_at) tekildir ve üretim INSERT … ON
-- CONFLICT DO NOTHING ile yapılır; ayrıca iş tayfa ve seri satırlarını
-- kilitleyerek çalışır, birden fazla pod aynı seriyi sırayla işler.
--
-- dtstart (veri modelinde yok, bu issue'da eklendi): serinin ilk örneğinin
-- başlangıcı (RFC 5545 DTSTART). Şablon etkinlik sonradan başka bir örneğe
-- geçebildiği için (tek örnek düzenlemesi, seri güncellemesi) kuralın
-- başlangıcı şablondan ayrı tutulur; COUNT ve INTERVAL hep aynı noktadan
-- sayılır. Yerel saat, timezone'daki IANA adıyla bu zamandan çıkarılır.

-- +goose Up

ALTER TABLE event_series ADD COLUMN dtstart timestamptz;
UPDATE event_series s
SET dtstart = COALESCE((SELECT e.starts_at FROM events e WHERE e.id = s.template_event_id), s.created_at);
ALTER TABLE event_series ALTER COLUMN dtstart SET NOT NULL;

-- Kural ve saat dilimi biçimi; uygulama aynı kuralları Go'da da uygular
-- (internal/core/app/event/rrule).
ALTER TABLE event_series ADD CONSTRAINT event_series_rrule_format
  CHECK (rrule ~ '^FREQ=(WEEKLY|MONTHLY)(;[A-Z]+=[A-Z0-9,+-]+)*$');
ALTER TABLE event_series ADD CONSTRAINT event_series_timezone_format
  CHECK (timezone ~ '^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$');

-- Aynı seride aynı başlangıç zamanında tek örnek. Eski events_series_id_idx
-- bu indeksin önekiyle aynıdır.
DROP INDEX events_series_id_idx;
CREATE UNIQUE INDEX events_series_starts_at_key ON events (series_id, starts_at) WHERE series_id IS NOT NULL;

-- Üretim işinin taradığı seriler.
CREATE INDEX event_series_due_idx ON event_series (generated_until NULLS FIRST, id) WHERE ended_at IS NULL;

-- +goose Down
DROP INDEX event_series_due_idx;
DROP INDEX events_series_starts_at_key;
CREATE INDEX events_series_id_idx ON events (series_id) WHERE series_id IS NOT NULL;
ALTER TABLE event_series DROP CONSTRAINT event_series_timezone_format;
ALTER TABLE event_series DROP CONSTRAINT event_series_rrule_format;
ALTER TABLE event_series DROP COLUMN dtstart;
