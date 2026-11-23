-- Günün içeriği (issue #269; ADR-0027 §5, ADR-0040; Notion "Faz 3 —
-- Katalog ayrıntıları ve şema ekleri (2026-11-18)").
--
--   tayfa_daily_items  tayfanın kendi havuzu: söz, dize, bilgi notu. Tayfam
--                      havuza hiçbir zaman içerik koymaz (dış içerik yok
--                      kararı); içeriği yönetici ve moderatör ekler.
--                      `body` 1–500 karakter, satır sonu (\n) olabilir
--                      (dizeler için), başka denetim karakteri olamaz;
--                      `attribution` isteğe bağlı tek satır, en fazla 120
--                      ("Atatürk", "Yunus Emre"…). `position` havuzun
--                      sırasıdır (sürükle-bırak). Bir içerik bir güne
--                      sabitlenebilir: `pin_month` + `pin_day`, `pin_year`
--                      NULL ise her yıl. İçerik tablosudur: soft delete,
--                      kalıcı silme 30 gün sonra arka plan işiyle.
--   tayfa_daily_state  tayfa başına sıranın durumu: sıradaki dönüşün
--                      başlayacağı `next_position`, son seçimin günü
--                      (`shown_on`, Europe/Istanbul) ve içeriği.
--
-- Seçim (ADR-0027 §5) sunucuda, istek anında, Europe/Istanbul gününe göre:
-- o güne sabitlenmiş canlı içerik varsa `position`'ı en küçük olanı; yoksa
-- sabitlenmemiş içerikler arasından sıradaki (gün başına bir adım, sona
-- gelince başa). Sabitlenmiş içerik sıradaki dönüşe girmez. Kimsenin
-- açmadığı gün sıra ilerlemez.
--
-- Modül: `daily` tayfa_modules'a yeni modül olarak eklenir (CHECK
-- genişler; ana sayfa bileşeni `daily` bu modüle bağlıdır). Var olan
-- standart tayfalarda açık satır eklenir (yeni tayfalarda da açık gelir);
-- anlık tayfada kapalıdır (var olanlarda satır yok = kapalı, yenilerde
-- kapalı satır).
--
-- Şikâyet: yeni hedef türü `daily_item` (tayfa moderasyonuna gider).
--
-- Kişisel veri: içerik tayfanındır; ekleyen (`created_by`) diğer
-- içeriklerdeki gibi tutulur. Ekleyenin içerikleri dışa aktarmaya girer
-- (duyurular gibi); hesap silinince içerik kalır, ekleyen ve silen boşalır.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları kaldırır, `daily` modül satırlarını ve
-- `daily_item` hedefli şikâyetleri siler (kısıtlara sığmadıkları için),
-- CHECK'leri eski haline getirir. Havuzdaki içerik kaybolur. Uygulama kodu
-- da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE tayfa_daily_items (
  id                uuid PRIMARY KEY,
  tayfa_id          uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Satırlar kırpılmış, satır içi boşluklar teke indirilmiş; boş satır
  -- art arda gelmez. \n dışında denetim karakteri yok.
  body              text NOT NULL,
  -- NULL: söyleyen/kaynak yok. Tek satır.
  attribution       text,
  position          integer NOT NULL,
  pin_month         smallint,
  pin_day           smallint,
  -- NULL: her yıl tekrarlanır.
  pin_year          smallint,
  -- Ekleyen; hesap silinince NULL ("Silinmiş kullanıcı"), içerik kalır.
  created_by        uuid,
  client_request_id uuid NOT NULL,
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL,
  deleted_at        timestamptz,
  deleted_by        uuid,

  CONSTRAINT tayfa_daily_items_body CHECK (
    char_length(body) BETWEEN 1 AND 500
    AND body = btrim(body, E' \n')
    AND body !~ '[\u0001-\u0009\u000b-\u001f\u007f-\u009f]'
    AND body !~ ' \n|\n | {2}|\n{3}'),
  CONSTRAINT tayfa_daily_items_attribution CHECK (
    attribution IS NULL OR (
      char_length(attribution) BETWEEN 1 AND 120
      AND attribution = btrim(attribution)
      AND attribution !~ '[[:cntrl:]]'
      AND attribution !~ ' {2}')),
  CONSTRAINT tayfa_daily_items_position CHECK (position >= 0),
  CONSTRAINT tayfa_daily_items_pin CHECK (
    (pin_month IS NULL AND pin_day IS NULL AND pin_year IS NULL)
    OR (pin_month IS NOT NULL AND pin_day IS NOT NULL
        AND pin_month BETWEEN 1 AND 12 AND pin_day BETWEEN 1 AND 31
        AND pin_day <= CASE WHEN pin_month = 2 THEN 29 WHEN pin_month IN (4, 6, 9, 11) THEN 30 ELSE 31 END
        AND (pin_year IS NULL OR pin_year BETWEEN 2000 AND 2100)
        -- 29 Şubat'a yıl verildiyse artık yıl olmalı; yılsız 29 Şubat
        -- yalnızca artık yıllarda gösterilir.
        AND (pin_year IS NULL OR pin_month <> 2 OR pin_day <> 29
             OR (pin_year % 4 = 0 AND (pin_year % 100 <> 0 OR pin_year % 400 = 0))))),
  CONSTRAINT tayfa_daily_items_deleted_by CHECK (deleted_by IS NULL OR deleted_at IS NOT NULL),
  CONSTRAINT tayfa_daily_items_client_request_key UNIQUE (tayfa_id, client_request_id)
);
-- Havuz sırası ve sıradaki seçimi.
CREATE INDEX tayfa_daily_items_tayfa_idx ON tayfa_daily_items (tayfa_id, position, id)
  WHERE deleted_at IS NULL;
-- Günün sabitlemesi.
CREATE INDEX tayfa_daily_items_pin_idx ON tayfa_daily_items (tayfa_id, pin_month, pin_day)
  WHERE deleted_at IS NULL AND pin_month IS NOT NULL;
CREATE INDEX tayfa_daily_items_deleted_at_idx ON tayfa_daily_items (deleted_at) WHERE deleted_at IS NOT NULL;
-- Hesap silme ve dışa aktarma.
CREATE INDEX tayfa_daily_items_created_by_idx ON tayfa_daily_items (created_by) WHERE created_by IS NOT NULL;
CREATE INDEX tayfa_daily_items_deleted_by_idx ON tayfa_daily_items (deleted_by) WHERE deleted_by IS NOT NULL;

CREATE TABLE tayfa_daily_state (
  tayfa_id      uuid PRIMARY KEY REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Sıradaki dönüş position'ı bu değer ya da büyük olan ilk sabitlenmemiş
  -- canlı içerikten başlar; yoksa baştan.
  next_position integer NOT NULL DEFAULT 0,
  -- Son sıradaki seçiminin günü (Europe/Istanbul) ve içeriği; o gün
  -- boyunca aynı içerik gösterilir.
  shown_on      date,
  shown_item_id uuid REFERENCES tayfa_daily_items (id) ON DELETE SET NULL,
  updated_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT tayfa_daily_state_next_position CHECK (next_position >= 0)
);
CREATE INDEX tayfa_daily_state_shown_item_idx ON tayfa_daily_state (shown_item_id) WHERE shown_item_id IS NOT NULL;

-- core_app (ADR-0015; varsayılan yetki SELECT ve INSERT'tir, 00016): içerik
-- başka tayfaya taşınamaz, istek kimliği değişmez. Metin, sıra ve
-- sabitleme düzenlenir; soft delete ve geri alma; hesap silme ekleyeni ve
-- sileni boşaltır; arka plan işi kalıcı siler. Durum satırı yalnızca
-- ilerler. SELECT ve INSERT burada da açıkça verilir.
GRANT SELECT, INSERT, DELETE ON tayfa_daily_items TO core_app;
GRANT UPDATE (body, attribution, position, pin_month, pin_day, pin_year, created_by, updated_at, deleted_at, deleted_by)
  ON tayfa_daily_items TO core_app;
GRANT SELECT, INSERT ON tayfa_daily_state TO core_app;
GRANT UPDATE (next_position, shown_on, shown_item_id, updated_at) ON tayfa_daily_state TO core_app;

-- Modül.
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN ('announcements', 'events', 'qa', 'polls', 'daily'));

INSERT INTO tayfa_modules (tayfa_id, module, enabled)
SELECT t.id, 'daily', true FROM tayfas t WHERE t.kind = 'standard'
ON CONFLICT (tayfa_id, module) DO NOTHING;

-- Şikâyet hedefi.
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question', 'daily_item'));

-- +goose Down

DELETE FROM reports WHERE target_type = 'daily_item';
ALTER TABLE reports DROP CONSTRAINT reports_target_type;
ALTER TABLE reports ADD CONSTRAINT reports_target_type CHECK (target_type IN
  ('user', 'tayfa', 'announcement', 'event', 'question', 'answer', 'poll', 'log_entry', 'dm_message', 'event_photo',
   'channel_message', 'event_question'));

DELETE FROM tayfa_modules WHERE module = 'daily';
ALTER TABLE tayfa_modules DROP CONSTRAINT tayfa_modules_module;
ALTER TABLE tayfa_modules ADD CONSTRAINT tayfa_modules_module CHECK (module IN ('announcements', 'events', 'qa', 'polls'));

DROP TABLE tayfa_daily_state;
DROP TABLE tayfa_daily_items;
