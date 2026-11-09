-- core arka plan işlerinin indeksleri (issue #66; veri modeli v14 "Arka plan
-- işleri", "Tayfa yönetimi", "Tayfa silmede bekleme", "Yürüyüşte beklenen
-- dönüş", "Konvoy aracı", "Saklama süreleri").
--
-- İşler internal/core/app/jobs'tadır. İş kuyruğu yoktur: her iş Go
-- zamanlayıcısıyla çalışır ve veritabanındaki duruma bakarak tarar; kesintiden
-- sonra bir sonraki çalışmada kaldığı yerden devam eder. Bu migration yalnızca
-- indeks ekler, veri değiştirmez ya da silmez. Taramalar şunları kullanır:
--
--   Pasif tayfa:       tayfas_status_activity_idx (00001) ve aşağıdaki
--                      tayfas_new_active_idx.
--   72 saatlik silme:  tayfas_deletion_scheduled_idx (00001).
--   30 günlük silme:   *_deleted_at_idx (00001, 00003, 00004, 00005, 00007,
--                      00008).
--   Yürüyüş alarmı:    event_hike_checkins_alert_idx.
--   Plaka temizliği:   event_convoy_vehicles_plate_idx.

-- +goose Up

-- İlk haftasını dolduran ve hâlâ aktif olan tayfalar (üye sayısı kuralı).
CREATE INDEX tayfas_new_active_idx ON tayfas (created_at) WHERE deleted_at IS NULL AND status = 'active';

-- Dönmemiş ve henüz alarm gönderilmemiş yürüyüşçüler. Alarm gönderilince ya
-- da kişi dönünce satır indeksten çıkar; tarama hep küçük kalır.
CREATE INDEX event_hike_checkins_alert_idx ON event_hike_checkins (event_id) WHERE returned_at IS NULL AND alert_sent_at IS NULL;

-- Plakası temizlenmemiş araçlar.
CREATE INDEX event_convoy_vehicles_plate_idx ON event_convoy_vehicles (event_id) WHERE plate IS NOT NULL;

-- +goose Down
DROP INDEX event_convoy_vehicles_plate_idx;
DROP INDEX event_hike_checkins_alert_idx;
DROP INDEX tayfas_new_active_idx;
