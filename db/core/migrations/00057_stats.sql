-- Tayfa ve kişisel istatistik (issue #266; ADR-0026 §1–§2, ADR-0037 §7,
-- ADR-0039 §2).
--
--   tayfa_stats       Tayfanın donmuş etkinliklerinden hesaplanan toplamlar:
--                     yıl (Europe/Istanbul; 0 = tüm zamanlar) ve metrik
--                     başına bir satır. value toplam ya da farklı değer
--                     sayısıdır; event_count metriğe katkı veren etkinlik
--                     sayısı. source: common (ortak metrikler: events,
--                     attended, participants, duration ve <tür>.<metrik>),
--                     measure (<tür>.<ölçüm>), log (<tür>.<günlük>.<stat>).
--   user_stats        Kişinin aynı yapıdaki istatistiği, tayfa başına
--                     (tayfa içindeki kişisel katkı; profil toplamı
--                     tayfaların toplamıdır). Yalnızca kişinin kendi
--                     katılımı, kişisel ölçümleri ve günlük kayıtları.
--   event_stat_runs   İstatistiğe girmiş (counted) ya da sonradan düşmüş
--                     (silinen, iptal edilen) etkinlikler: arka plan işi
--                     yeni donan ve düşen etkinliği bir kez görür.
--   tayfa_stat_queue  Yeniden hesaplanacak tayfalar. İş, donan ya da düşen
--                     etkinliğin tayfasını, sözlük birleştirme olayının
--                     (tayfa.vocabulary_merged / _unmerged) tüketicisi o
--                     tayfayı ekler.
--
-- Kurallar:
--   * Tablolar türetilmiştir (ADR-0026 §2): kaynak bileşen tablolarıdır,
--     hesaplama tayfanın bütün satırlarını silip yeniden yazar
--     (idempotent). Kaynakla çelişirse kaynak kazanır.
--   * Hassas veri saklanmaz: sensitive ölçüm hiçbir tabloya yazılmaz;
--     sensitive_attendance türündeki etkinlik yalnızca tayfanın events ve
--     attended toplamına girer, kişisel satır ve farklı katılımcı yoktur
--     (ADR-0037 §7). Konum ve zaman örüntüsü (ay, gün, saat) yoktur.
--   * Hesap silinince kişinin user_stats satırları aynı transaction'da
--     silinir; tayfa silinince ikisi de tayfayla gider.
--
-- Veri taşınmaz ve silinmez; katalog değişmez.
--
-- Geri alma planı: Down tabloları kaldırır; hesaplanmış istatistik gider
-- (kaynaktan yeniden kurulur). Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE tayfa_stats (
  tayfa_id    uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  -- Europe/Istanbul takvim yılı; 0 tüm zamanlar.
  year        integer NOT NULL,
  metric_key  text NOT NULL,
  source      text NOT NULL,
  value       numeric(18, 3) NOT NULL,
  event_count integer NOT NULL,
  computed_at timestamptz NOT NULL,

  PRIMARY KEY (tayfa_id, year, metric_key),
  CONSTRAINT tayfa_stats_year CHECK (year = 0 OR year BETWEEN 2000 AND 2999),
  CONSTRAINT tayfa_stats_metric_key CHECK (
    metric_key ~ '^[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,48})?)?$'),
  CONSTRAINT tayfa_stats_source CHECK (source IN ('common', 'measure', 'log')),
  CONSTRAINT tayfa_stats_event_count CHECK (event_count >= 0)
);

CREATE TABLE user_stats (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id     uuid NOT NULL,
  tayfa_id    uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  year        integer NOT NULL,
  metric_key  text NOT NULL,
  source      text NOT NULL,
  value       numeric(18, 3) NOT NULL,
  event_count integer NOT NULL,
  computed_at timestamptz NOT NULL,

  CONSTRAINT user_stats_key UNIQUE (tayfa_id, year, metric_key, user_id),
  CONSTRAINT user_stats_year CHECK (year = 0 OR year BETWEEN 2000 AND 2999),
  CONSTRAINT user_stats_metric_key CHECK (
    metric_key ~ '^[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,39}(\.[a-z][a-z0-9_]{1,48})?)?$'),
  CONSTRAINT user_stats_source CHECK (source IN ('common', 'measure', 'log')),
  CONSTRAINT user_stats_event_count CHECK (event_count >= 0)
);

CREATE INDEX user_stats_user_idx ON user_stats (user_id, year);

CREATE TABLE event_stat_runs (
  event_id   uuid PRIMARY KEY REFERENCES events (id) ON DELETE CASCADE,
  tayfa_id   uuid NOT NULL,
  -- Etkinlik şu an istatistikte mi (donmuş, silinmemiş, iptal edilmemiş).
  counted    boolean NOT NULL,
  changed_at timestamptz NOT NULL
);

CREATE TABLE tayfa_stat_queue (
  tayfa_id  uuid PRIMARY KEY REFERENCES tayfas (id) ON DELETE CASCADE,
  queued_at timestamptz NOT NULL
);

-- Tablolar yeniden yazılır (silinip eklenir), satır değiştirilmez; işaret
-- yalnızca counted ve changed_at'i değiştirir. Kuyruk satırı FOR UPDATE ile
-- alınır (bu yüzden queued_at üzerinde UPDATE) ve silinir.
GRANT SELECT, INSERT, DELETE ON tayfa_stats TO core_app;
GRANT SELECT, INSERT, DELETE ON user_stats TO core_app;
GRANT SELECT, INSERT, UPDATE (counted, changed_at) ON event_stat_runs TO core_app;
GRANT SELECT, INSERT, DELETE, UPDATE (queued_at) ON tayfa_stat_queue TO core_app;

-- +goose Down

DROP TABLE tayfa_stat_queue;
DROP TABLE event_stat_runs;
DROP TABLE user_stats;
DROP TABLE tayfa_stats;
