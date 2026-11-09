-- Arka plan işlerinin son çalışması (issue #110; Notion "Platform Yönetim
-- Paneli → Pano": "temizlik işinin son çalışması").
--
-- job_runs: her iş için tek satır; internal/core/app/jobs her turun sonunda
--   yazar (bitiş zamanı, başarılı mı, değişen satır sayısı). Hata metni
--   yazılmaz (yalnızca servis log'unda). Birden fazla pod aynı satırı
--   yazabilir; daha eski bir bitiş daha yenisini ezmez (sorgudaki WHERE).
--   Kişisel veri yok: iş adı, zaman ve sayı.
--   Çalışma zamanı kullanıcısı (#45) SELECT, INSERT, UPDATE alır.
--
-- Geri alma planı: Down tabloyu siler; yalnızca izleme bilgisi kaybolur,
-- işlerin kendisi veritabanı durumuna bakarak çalışmaya devam eder.

-- +goose Up
CREATE TABLE job_runs (
  job               text PRIMARY KEY,
  finished_at       timestamptz NOT NULL,
  succeeded         boolean NOT NULL,
  changed           bigint NOT NULL DEFAULT 0,
  last_succeeded_at timestamptz,

  CONSTRAINT job_runs_job_format CHECK (job ~ '^[a-z][a-z-]{0,39}$'),
  CONSTRAINT job_runs_changed CHECK (changed >= 0),
  CONSTRAINT job_runs_success CHECK (NOT succeeded OR last_succeeded_at = finished_at)
);

-- +goose Down
DROP TABLE job_runs;
