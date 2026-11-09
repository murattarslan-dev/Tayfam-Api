-- Tayfa moderasyon kuyruğunda öncelik (issue #76 sonrası; platform
-- sahibinin 2026-11-09 kararı, ADR-0012 güncellendi; Notion v14 "Şikâyet
-- önceliği": "minor_involved şikâyetler tayfa kuyruğunda ... en üstte
-- gösterilir").
--
-- Kuyruk artık minor_involved ve çocuk/genç güvenliği (child_safety)
-- şikâyetlerini önce, sonra eskiden yeniye sıralar ve önceliği gösterir
-- (ADR-0012 ve Notion: ikisi de "kuyruğun en önüne geçer"). Yaş, doğum tarihi ve tarafların hangisinin
-- 18 altı olduğu yine hiçbir yerde tutulmaz ve gösterilmez. Platform
-- tarafının (priority, status, created_at) indeksi 00011'de vardır.
--
-- Sıralama ifadesi bir boolean'dır: metin olarak 'normal' > 'minor_involved'
-- olduğu için düz priority DESC yanlış sıralar.
-- Bu migration yalnızca indeksi değiştirir, veri değiştirmez.

-- +goose Up
DROP INDEX reports_tayfa_open_idx;
CREATE INDEX reports_tayfa_open_idx ON reports (tayfa_id, (priority = 'minor_involved' OR reason = 'child_safety') DESC, created_at, id)
  WHERE status = 'open' AND NOT platform_only;

-- +goose Down
DROP INDEX reports_tayfa_open_idx;
CREATE INDEX reports_tayfa_open_idx ON reports (tayfa_id, created_at, id)
  WHERE status = 'open' AND NOT platform_only;
