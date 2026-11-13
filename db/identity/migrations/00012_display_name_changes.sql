-- Görünen ad değiştirme (issue #184; Notion Proje Planı §4.7). Kayıttan
-- sonra kullanıcı adını 24 saatte en fazla 3 kez değiştirebilir (platform
-- sahibinin kararı, 2026-11-13).
--
-- display_name_changes: yalnızca kimin ne zaman adını değiştirdiği. Eski ad
-- da yeni ad da saklanmaz (tutmadığın veri çalınamaz); sınır servis yeniden
-- başlasa da sıfırlanmasın diye veritabanından sayılır. 24 saatten eski
-- satırlar aynı kullanıcının bir sonraki değişikliğinde silinir, hesap
-- silinince hepsi gider.

-- +goose Up
CREATE TABLE display_name_changes (
  id         uuid PRIMARY KEY,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  changed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX display_name_changes_user_time_idx ON display_name_changes (user_id, changed_at);
-- Eski satırlar ve hesap silme için DELETE (00008: varsayılan yalnızca
-- SELECT ve INSERT).
GRANT DELETE ON display_name_changes TO identity_app;

-- +goose Down
DROP TABLE display_name_changes;
