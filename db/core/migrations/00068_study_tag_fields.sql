-- Eğitim türlerine ders/konu alanları (issue #333; ADR-0043, ADR-0040 §5,
-- ADR-0025 §2, ADR-0042; Notion "Faz 3 — Katalog ayrıntıları ve şema
-- ekleri (2026-11-18)" §B.14).
--
-- Tür tanımının yeni alan türü study_tags, ders/konu listesinden
-- (catalog_study_tags) seçimdir. Dört Eğitim türü sürüm 2 ile bu alanı alır;
-- bileşenleri sürüm 1 ile aynıdır:
--
--   study_group      subjects  "Dersler"        ders ya da konu, en fazla 5
--   problem_session  subjects  "Dersler"        ders ya da konu, en fazla 5
--   peer_teaching    subject   "Ders ve konu"   ders ya da konu, tek, zorunlu
--   mock_exam        exam      "Sınav / ders"   düzey ya da ders, tek
--
-- Sürüm 1 satırları değişmez: o sürümle açılmış etkinlikler onunla
-- doğrulanır ve gösterilir (ADR-0025 §2). Türün sürümü yalnızca ileri
-- gider (00067 tetikleyicisi).
--
-- Katalog bir kez değiştiği için catalog_meta.catalog_version bir artar;
-- catalog.changed olayını catalog-publish işi yayınlar (ADR-0042 §5).
-- Eşlenik identity migration'ı gerekmez (ADR-0042 §6).
--
-- Up idempotenttir: sürüm satırı ON CONFLICT DO NOTHING ile eklenir, tür
-- yalnızca sürüm 1'deyse sürüm 2'ye geçer; yeniden çalıştırmak yalnızca
-- katalog sürümünü artırır.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down, sürümün geri gitmesini engelleyen tetikleyiciyi
-- (00067) yalnızca kendi transaction'ında kapatır, dört türü sürüm 1'e
-- döndürür, tetikleyiciyi yeniden açar ve ikinci sürüm satırlarını siler;
-- catalog_version yine bir artar. Sürüm 2 ile açılmış etkinlik varsa geri
-- alma foreign key'de durur; o etkinlikler önce elle ele alınmalıdır.
-- Uygulama kodu da birlikte geri alınmalıdır (eski doğrulayıcı study_tags
-- alan türünü tanımaz).

-- +goose Up

INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition) VALUES
  ('study_group', 2, '{"tr": "Çalışma grubu / etüt"}', 'school', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "subjects", "kind": "study_tags", "labels": {"tr": "Dersler"}, "max_selected": 5}], "components": [{"kind": "schedule", "labels": {"tr": "Konu planı"}}, {"kind": "measure", "key": "study_time", "labels": {"tr": "Çalışma süresi"}, "params": {"unit": "hour", "scope": "personal", "entered_by": "self", "min": 0, "max": 24, "decimals": 2, "showcase": true}}, {"kind": "timer", "params": {"mode": "countdown", "presets": [1500, 300, 900]}}]}'),
  ('problem_session', 2, '{"tr": "Soru çözüm oturumu"}', 'calculate', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "subjects", "kind": "study_tags", "labels": {"tr": "Dersler"}, "max_selected": 5}], "components": [{"kind": "schedule"}, {"kind": "pool_link"}, {"kind": "measure", "key": "solved", "labels": {"tr": "Çözülen soru"}, "params": {"unit": "piece", "scope": "total", "entered_by": "organizer", "min": 0, "max": 10000, "decimals": 0, "showcase": true}}]}'),
  ('peer_teaching', 2, '{"tr": "Akran konu anlatımı"}', 'lightbulb', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "subject", "kind": "study_tags", "labels": {"tr": "Ders ve konu"}, "required": true}], "components": [{"kind": "schedule"}, {"kind": "questions", "optional": {"default": false}}]}'),
  ('mock_exam', 2, '{"tr": "Deneme sınavı"}', 'timer', ARRAY['in_person'],
   '{"fields": [{"key": "exam", "kind": "study_tags", "labels": {"tr": "Sınav / ders"}, "min_depth": "level", "max_depth": "subject"}], "components": [{"kind": "measure", "key": "net", "labels": {"tr": "Net"}, "params": {"unit": "net", "scope": "personal", "entered_by": "self", "min": -50, "max": 200, "decimals": 2, "sensitive": true}}, {"kind": "timer", "params": {"mode": "countdown"}}]}')
ON CONFLICT (type_key, version) DO NOTHING;

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 2 AND t.version = 1
  AND t.key IN ('study_group', 'problem_session', 'peer_teaching', 'mock_exam');

UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- +goose Down

-- Sürüm yalnızca bu geri almada, bu transaction içinde geri gider.
ALTER TABLE catalog_event_types DISABLE TRIGGER catalog_event_types_version_forward;

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 1 AND t.version = 2
  AND t.key IN ('study_group', 'problem_session', 'peer_teaching', 'mock_exam');

ALTER TABLE catalog_event_types ENABLE TRIGGER catalog_event_types_version_forward;

DELETE FROM catalog_event_type_versions
WHERE version = 2 AND type_key IN ('study_group', 'problem_session', 'peer_teaching', 'mock_exam');

UPDATE catalog_meta SET catalog_version = catalog_version + 1;
