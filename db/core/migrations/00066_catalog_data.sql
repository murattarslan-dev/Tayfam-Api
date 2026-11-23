-- Tam katalog verisi (issue #260; ADR-0025 §2, ADR-0037, ADR-0039,
-- ADR-0040 §5, ADR-0042; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri
-- (2026-11-18)" §B ve §E.2, "Eğitim — Ders ve konu etiket listesi").
-- Platform sahibinin 2026-11-23 kararları issue #260'tadır.
--
--   catalog_interests            dokuz yeni ilgi alanı (toplam 15). Her ilgi
--                                alanının varsayılan ana sayfa bileşenleri
--                                yalnızca temel bölümlerdir (upcoming,
--                                announcements, polls, questions); modüller
--                                ve gösterim kartları kapalı başlar, tayfa
--                                yöneticisi açar (platform sahibi,
--                                2026-11-23).
--   catalog_event_types /        58 yeni tür (sürüm 1) ve Faz 1 türlerinden
--   catalog_event_type_versions  altısının ikinci sürümü (convoy, hike,
--                                camp, sports, gaming, general). Sürüm 1
--                                satırları değişmez: o sürümle açılmış
--                                etkinlikler onunla doğrulanır ve gösterilir
--                                (ADR-0025 §2; Notion "Mevcut etkinlikler
--                                eski tür sürümünde kalır"). Balıkçılığın
--                                tanımı Notion'la aynı olduğu için sürüm 1'de
--                                kalır. Türlerin sırası ilgi alanı sırasıyla
--                                yeniden verilir, Genel en sondadır.
--   catalog_study_tags           ders ve konu listesi (LGS, YKS TYT/AYT, lise,
--                                YDS, Diğer); her düzeyin ve dersin "Diğer"i
--                                <üst anahtar>_other'dır.
--   catalog_badges               ilk rozet listesi.
--   catalog_score_presets        değişmez (00045'teki sekiz ön ayar Notion'la
--                                aynıdır).
--
-- Ayrıntılar:
--   * Notion'daki "oylama (kaynak: rota durakları)" (Gastronomi turu)
--     serbest seçenekli oylamadır: route_stops kaynağı platform sahibinin
--     2026-11-23 kararıyla kalktı (ADR-0037 §3, #254).
--   * Notion'da "(yüz yüze/online)" yazan türler iki modludur, diğer yeni
--     türler yalnızca yüz yüze. Faz 1 türleri bugünkü iki modunu korur.
--   * "(ops)" bileşenler organizatörün açtığı, varsayılan kapalı
--     bileşenlerdir (optional default false); Genel'in araç kutusu da öyle.
--   * Ölçüm ve günlük anahtarları ortak metrik adlarıyla (events, attended,
--     participants, duration) çakışmaz: istatistik ve rozet metriği
--     <tür>.<anahtar>'dır (ADR-0039 uygulama notları).
--   * Tür düzeyindeki sözlük işaretli metin alanları (kitap, oyun, dal)
--     tanımda `vocabulary` taşır; sözlüğe bağlama bugün yalnızca günlük
--     kayıtlarında vardır (#244), tür alanı düz metin olarak saklanır.
--   * Eğitim türlerindeki ders/konu alanları tanımda yoktur: tanım şemasında
--     ders-konu listesine bağlı bir alan türü yoktur (PR'da açık soru).
--
-- Katalog bir kez değiştiği için catalog_meta.catalog_version bir artar;
-- catalog.changed olayını catalog-publish işi yayınlar (ADR-0042 §5). Eşlenik
-- identity migration'ı gerekmez (ADR-0042 §6).
--
-- Seed idempotenttir: eklemeler ON CONFLICT DO NOTHING, ikinci sürüme geçiş
-- yalnızca sürüm 1'deki türde yapılır; yeniden çalıştırmak yalnızca katalog
-- sürümünü artırır.
--
-- Veri taşınmaz ve silinmez.
--
-- Geri alma planı: Down eklenen rozetleri, etiketleri, türleri ve ilgi
-- alanlarını siler, altı türü sürüm 1'e ve eski sırasına döndürür, ikinci
-- sürüm satırlarını siler ve varsayılan ana sayfa bileşenlerini boşaltır;
-- catalog_version yine bir artar. Bu kayıtlara bağlanmış kullanıcı verisi
-- varsa (yeni türle ya da ikinci sürümle açılmış etkinlik, katılım kaydı,
-- yeni ilgi alanını seçmiş tayfa, etiketli soru/materyal, kazanılmış rozet)
-- geri alma foreign key'de durur; o veri önce elle ele alınmalıdır.

-- +goose Up

-- ---------------------------------------------------------------------------
-- İlgi alanları: dokuz yeni ilgi alanı; her ilgi alanında varsayılan açık
-- gelen ana sayfa bileşenleri yalnızca temel bölümlerdir.

INSERT INTO catalog_interests (key, position, labels, icon) VALUES
  ('environment', 60, '{"tr": "Çevre ve gönüllülük"}', 'eco'),
  ('animals', 70, '{"tr": "Hayvanlar"}', 'pets'),
  ('books', 80, '{"tr": "Kitap"}', 'menu_book'),
  ('arts', 90, '{"tr": "Sanat ve el işi"}', 'palette'),
  ('learning', 100, '{"tr": "Öğrenme"}', 'lightbulb'),
  ('travel', 110, '{"tr": "Gezi ve kültür"}', 'tour'),
  ('food', 120, '{"tr": "Yemek"}', 'restaurant'),
  ('faith', 130, '{"tr": "İnanç ve maneviyat"}', 'auto_awesome'),
  ('education', 140, '{"tr": "Eğitim"}', 'school')
ON CONFLICT (key) DO NOTHING;

UPDATE catalog_interests SET default_widgets = ARRAY['upcoming', 'announcements', 'polls', 'questions']
WHERE key IN ('automotive', 'nature', 'water', 'sports', 'games', 'general', 'environment', 'animals', 'books', 'arts', 'learning', 'travel', 'food', 'faith', 'education');

-- ---------------------------------------------------------------------------
-- Faz 1 türlerinin ikinci sürümü (balıkçılık hariç: tanımı Notion'la aynı).
-- Sürüm 1 satırı değişmez; o sürümle açılmış etkinlikler onunla kalır.

INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition) VALUES
  ('convoy', 2, '{"tr": "Konvoy"}', 'directions_car', ARRAY['in_person', 'online'],
   '{"components": [{"kind": "meeting_point"}, {"kind": "route"}, {"kind": "vehicles", "params": {"plate": true}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 10000, "decimals": 1, "showcase": true}}]}'),
  ('hike', 2, '{"tr": "Doğa yürüyüşü"}', 'hiking', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "difficulty", "kind": "integer", "min": 1, "max": 5, "labels": {"tr": "Zorluk"}}, {"key": "planned_distance", "kind": "decimal", "min": 0.01, "max": 1000, "decimals": 2, "labels": {"tr": "Planlanan mesafe"}, "unit": "km"}, {"key": "planned_elevation", "kind": "integer", "min": 0, "max": 9000, "labels": {"tr": "Planlanan irtifa"}, "unit": "m"}], "components": [{"kind": "route"}, {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 1000, "decimals": 2, "showcase": true, "prefill_from": "planned_distance"}}, {"kind": "measure", "key": "elevation_gain", "labels": {"tr": "İrtifa kazancı"}, "params": {"unit": "m", "scope": "total", "entered_by": "organizer", "min": 0, "max": 9000, "decimals": 0, "showcase": true, "prefill_from": "planned_elevation"}}]}'),
  ('camp', 2, '{"tr": "Kamp"}', 'camping', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "site_name", "kind": "text", "max_length": 120, "labels": {"tr": "Kamp alanı"}}, {"key": "has_water", "kind": "boolean", "labels": {"tr": "Su var"}}, {"key": "has_toilet", "kind": "boolean", "labels": {"tr": "Tuvalet var"}}, {"key": "notes", "kind": "long_text", "max_length": 1000, "labels": {"tr": "Notlar"}}], "components": [{"kind": "checklist", "key": "gear", "labels": {"tr": "Malzeme listesi"}, "params": {"label": "gear", "assignment": true, "quantity": true}}, {"kind": "checklist", "key": "duty", "labels": {"tr": "Nöbet listesi"}, "params": {"label": "duty", "assignment": true}}, {"kind": "measure", "key": "nights", "labels": {"tr": "Gece"}, "params": {"unit": "night", "scope": "total", "entered_by": "organizer", "min": 0, "max": 60, "decimals": 0, "showcase": true}}, {"kind": "vehicles", "optional": {"default": false}, "params": {"seats": true}}]}'),
  ('sports', 2, '{"tr": "Spor (diğer)"}', 'sports', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "sport", "kind": "text", "max_length": 60, "labels": {"tr": "Dal"}, "required": true, "vocabulary": {"namespace": "sport"}}, {"key": "venue_name", "kind": "text", "max_length": 120, "labels": {"tr": "Saha adı"}}], "components": [{"kind": "teams", "params": {"mode": {"organizer_choice": ["random", "balanced", "manual"], "default": "balanced"}}}, {"kind": "scoreboard", "params": {"side": "team", "tables": true}}, {"kind": "celebration", "params": {"source": "scoreboard"}}]}'),
  ('gaming', 2, '{"tr": "Video oyunu"}', 'sports_esports', ARRAY['in_person', 'online'],
   '{"fields": [{"key": "game", "kind": "text", "max_length": 60, "labels": {"tr": "Oyun"}, "required": true}, {"key": "platform", "kind": "choice", "options": ["pc", "playstation", "xbox", "switch", "mobile", "other"], "option_labels": {"pc": {"tr": "PC"}, "playstation": {"tr": "PlayStation"}, "xbox": {"tr": "Xbox"}, "switch": {"tr": "Switch"}, "mobile": {"tr": "Mobil"}, "other": {"tr": "Diğer"}}, "labels": {"tr": "Platform"}, "required": true}], "components": [{"kind": "teams", "params": {"mode": {"organizer_choice": ["random", "manual"], "default": "random"}}}, {"kind": "scoreboard", "optional": {"default": false}, "params": {"tables": true, "guests": true}}, {"kind": "celebration"}]}'),
  ('general', 2, '{"tr": "Genel"}', 'event', ARRAY['in_person', 'online'],
   '{"components": [{"kind": "checklist", "key": "checklist", "labels": {"tr": "Liste"}, "optional": {"default": false}, "params": {"label": "item", "assignment": true, "quantity": true}}, {"kind": "vote", "optional": {"default": false}, "params": {"source": "free"}}, {"kind": "teams", "optional": {"default": false}, "params": {"mode": {"organizer_choice": ["random", "balanced", "manual"], "default": "random"}}}, {"kind": "scoreboard", "optional": {"default": false}, "params": {"tables": true, "guests": true}}, {"kind": "timer", "optional": {"default": false}, "params": {"mode": {"organizer_choice": ["countdown", "stopwatch"], "default": "countdown"}}}]}')
ON CONFLICT (type_key, version) DO NOTHING;

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 2 AND t.version = 1
  AND t.key IN ('convoy', 'hike', 'camp', 'sports', 'gaming', 'general');

-- Listeleme sırası: ilgi alanı sırasıyla, Genel en sonda.
UPDATE catalog_event_types AS t SET position = p.position
FROM (VALUES ('convoy', 110), ('hike', 210), ('camp', 220), ('fishing', 310), ('sports', 480), ('gaming', 550), ('general', 9000)) AS p (key, position)
WHERE t.key = p.key;

-- ---------------------------------------------------------------------------
-- Yeni türler, sürüm 1.

INSERT INTO catalog_event_types (key, interest_keys, position, labels, icon, modes, version, definition) VALUES
  ('moto_tour', ARRAY['automotive'], 120, '{"tr": "Motosiklet turu"}', 'two_wheeler', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "route"}, {"kind": "vehicles", "params": {"plate": true}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 10000, "decimals": 1, "showcase": true}}, {"kind": "roll_call", "optional": {"default": false}, "params": {"alarm": true, "return_grace_minutes": 30}}]}'),
  ('car_meet', ARRAY['automotive'], 130, '{"tr": "Araç buluşması"}', 'garage', ARRAY['in_person'], 1,
   '{"components": [{"kind": "vehicles"}, {"kind": "vote", "key": "best_vehicle", "labels": {"tr": "En güzel araç"}, "params": {"source": "vehicles"}}, {"kind": "celebration", "params": {"source": "best_vehicle"}}]}'),
  ('auto_show', ARRAY['automotive'], 140, '{"tr": "Fuar / müze gezisi"}', 'museum', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "schedule"}]}'),
  ('maintenance_workshop', ARRAY['automotive'], 150, '{"tr": "Bakım atölyesi"}', 'build', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "parts", "labels": {"tr": "Parça ve malzeme"}, "params": {"label": "item", "assignment": true, "quantity": true}}, {"kind": "schedule"}]}'),
  ('summit', ARRAY['nature'], 230, '{"tr": "Zirve tırmanışı"}', 'landscape', ARRAY['in_person'], 1,
   '{"fields": [{"key": "difficulty", "kind": "integer", "min": 1, "max": 5, "labels": {"tr": "Zorluk"}}, {"key": "summit_name", "kind": "text", "max_length": 120, "labels": {"tr": "Zirve adı"}}, {"key": "summit_elevation", "kind": "integer", "min": 0, "max": 9000, "labels": {"tr": "Zirve irtifası"}, "unit": "m"}], "components": [{"kind": "route"}, {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}, {"kind": "measure", "key": "elevation_gain", "labels": {"tr": "İrtifa kazancı"}, "params": {"unit": "m", "scope": "total", "entered_by": "organizer", "min": 0, "max": 9000, "decimals": 0}}, {"kind": "log", "key": "summit", "labels": {"tr": "Zirve kaydı"}, "params": {"fields": [{"key": "note", "kind": "text", "max_length": 120, "labels": {"tr": "Not"}}], "stat": ["count"], "max_entries": 1}}]}'),
  ('climbing', ARRAY['nature'], 240, '{"tr": "Kaya tırmanışı"}', 'terrain', ARRAY['in_person'], 1,
   '{"fields": [{"key": "region", "kind": "text", "max_length": 120, "labels": {"tr": "Bölge"}}, {"key": "discipline", "kind": "choice", "options": ["sport", "trad", "bouldering"], "option_labels": {"sport": {"tr": "Spor"}, "trad": {"tr": "Geleneksel"}, "bouldering": {"tr": "Bouldering"}}, "labels": {"tr": "Disiplin"}}], "components": [{"kind": "checklist", "key": "gear", "labels": {"tr": "Malzeme listesi"}, "params": {"label": "gear", "assignment": true, "quantity": true}}, {"kind": "roll_call", "params": {"alarm": false}}, {"kind": "log", "key": "routes", "labels": {"tr": "Tırmanılan rotalar"}, "params": {"fields": [{"key": "route_name", "kind": "text", "max_length": 60, "labels": {"tr": "Rota adı"}, "required": true, "vocabulary": {"namespace": "climbing_route"}}, {"key": "grade", "kind": "text", "max_length": 10, "labels": {"tr": "Derece"}}, {"key": "style", "kind": "choice", "options": ["flash", "onsight", "redpoint"], "option_labels": {"flash": {"tr": "Flash"}, "onsight": {"tr": "Onsight"}, "redpoint": {"tr": "Redpoint"}}, "labels": {"tr": "Stil"}}]}}]}'),
  ('birding', ARRAY['nature'], 250, '{"tr": "Kuş gözlemi"}', 'park', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "log", "key": "sightings", "labels": {"tr": "Gözlemler"}, "params": {"fields": [{"key": "species", "kind": "text", "max_length": 60, "labels": {"tr": "Tür"}, "required": true, "vocabulary": {"namespace": "bird_species"}}, {"key": "count", "kind": "integer", "min": 1, "max": 10000, "labels": {"tr": "Adet"}, "unit": "piece"}], "location": true, "location_default": "participants", "stat": ["distinct:species"]}}]}'),
  ('stargazing', ARRAY['nature'], 260, '{"tr": "Gökyüzü gözlemi"}', 'nightlight', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "checklist", "key": "gear", "labels": {"tr": "Ekipman"}, "params": {"label": "gear", "assignment": true}}, {"kind": "log", "key": "objects", "labels": {"tr": "Gözlenen cisimler"}, "params": {"fields": [{"key": "object", "kind": "text", "max_length": 60, "labels": {"tr": "Cisim"}, "required": true, "vocabulary": {"namespace": "sky_object"}}], "stat": ["distinct:object"]}}]}'),
  ('diving', ARRAY['water'], 320, '{"tr": "Dalış"}', 'scuba_diving', ARRAY['in_person'], 1,
   '{"fields": [{"key": "dive_site", "kind": "text", "max_length": 120, "labels": {"tr": "Dalış noktası"}}, {"key": "required_level", "kind": "choice", "options": ["beginner", "advanced", "expert"], "option_labels": {"beginner": {"tr": "Başlangıç"}, "advanced": {"tr": "İleri"}, "expert": {"tr": "Uzman"}}, "labels": {"tr": "Gereken seviye"}}], "components": [{"kind": "teams", "labels": {"tr": "Eş dalıcılar"}, "params": {"mode": {"organizer_choice": ["manual", "random"], "default": "manual"}, "team_size": 2}}, {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}, {"kind": "checklist", "key": "gear", "labels": {"tr": "Ekipman"}, "params": {"label": "gear", "assignment": true}}, {"kind": "log", "key": "dives", "labels": {"tr": "Dalış kayıtları"}, "params": {"fields": [{"key": "depth", "kind": "decimal", "min": 0.1, "max": 350, "decimals": 1, "labels": {"tr": "Derinlik"}, "unit": "m", "required": true}, {"key": "duration", "kind": "duration", "required": true, "min": 1, "max": 600, "labels": {"tr": "Süre"}}], "max_entries": 10}}]}'),
  ('paddling', ARRAY['water'], 330, '{"tr": "Kano / kürek"}', 'kayaking', ARRAY['in_person'], 1,
   '{"components": [{"kind": "route"}, {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 1000, "decimals": 2}}]}'),
  ('sailing', ARRAY['water'], 340, '{"tr": "Yelken"}', 'sailing', ARRAY['in_person'], 1,
   '{"components": [{"kind": "teams", "labels": {"tr": "Mürettebat"}, "params": {"mode": "manual"}}, {"kind": "roll_call", "params": {"alarm": true, "return_grace_minutes": 30}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "nm", "scope": "total", "entered_by": "organizer", "min": 0, "max": 5000, "decimals": 1}}]}'),
  ('team_sport', ARRAY['sports'], 410, '{"tr": "Takım sporu"}', 'sports_soccer', ARRAY['in_person'], 1,
   '{"fields": [{"key": "sport", "kind": "choice", "options": ["football", "basketball", "volleyball", "handball"], "option_labels": {"football": {"tr": "Futbol / halısaha"}, "basketball": {"tr": "Basketbol"}, "volleyball": {"tr": "Voleybol"}, "handball": {"tr": "Hentbol"}}, "labels": {"tr": "Dal"}, "required": true}, {"key": "venue_name", "kind": "text", "max_length": 120, "labels": {"tr": "Saha adı"}}, {"key": "squad_size", "kind": "integer", "min": 1, "max": 15, "labels": {"tr": "Kadro (takım başı oyuncu)"}}], "components": [{"kind": "teams", "params": {"mode": "balanced"}}, {"kind": "scoreboard", "params": {"side": "team", "rounds": "single", "winner": "high", "result": "points"}}, {"kind": "celebration", "params": {"source": "scoreboard"}}, {"kind": "measure", "key": "goals", "labels": {"tr": "Gol"}, "params": {"unit": "goal", "scope": "personal", "entered_by": "organizer", "min": 0, "max": 100, "decimals": 0, "showcase": true}}]}'),
  ('running', ARRAY['sports'], 420, '{"tr": "Koşu"}', 'directions_run', ARRAY['in_person'], 1,
   '{"fields": [{"key": "run_kind", "kind": "choice", "options": ["training", "race", "parkrun"], "option_labels": {"training": {"tr": "Antrenman"}, "race": {"tr": "Yarış"}, "parkrun": {"tr": "Park koşusu"}}, "labels": {"tr": "Tür"}}], "components": [{"kind": "route"}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "personal", "entered_by": "self", "min": 0, "max": 500, "decimals": 2}}, {"kind": "measure", "key": "run_time", "labels": {"tr": "Süre"}, "params": {"unit": "minute", "scope": "personal", "entered_by": "self", "min": 0, "max": 3000, "decimals": 2}}, {"kind": "ranking", "labels": {"tr": "Derece"}, "optional": {"default": false}, "params": {"source": "run_time", "direction": "asc"}}]}'),
  ('cycling', ARRAY['sports'], 430, '{"tr": "Bisiklet turu"}', 'directions_bike', ARRAY['in_person'], 1,
   '{"fields": [{"key": "bike", "kind": "choice", "options": ["road", "mountain", "city"], "option_labels": {"road": {"tr": "Yol"}, "mountain": {"tr": "Dağ"}, "city": {"tr": "Şehir"}}, "labels": {"tr": "Bisiklet"}}, {"key": "difficulty", "kind": "integer", "min": 1, "max": 5, "labels": {"tr": "Zorluk"}}], "components": [{"kind": "route"}, {"kind": "roll_call", "params": {"alarm": false}}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 2000, "decimals": 1}}]}'),
  ('racket', ARRAY['sports'], 440, '{"tr": "Raket sporları"}', 'sports_tennis', ARRAY['in_person'], 1,
   '{"fields": [{"key": "sport", "kind": "choice", "options": ["tennis", "table_tennis", "badminton", "padel", "squash"], "option_labels": {"tennis": {"tr": "Tenis"}, "table_tennis": {"tr": "Masa tenisi"}, "badminton": {"tr": "Badminton"}, "padel": {"tr": "Padel"}, "squash": {"tr": "Squash"}}, "labels": {"tr": "Dal"}, "required": true}, {"key": "venue_name", "kind": "text", "max_length": 120, "labels": {"tr": "Saha adı"}}], "components": [{"kind": "draw"}, {"kind": "bracket", "params": {"format": {"organizer_choice": ["elimination", "league", "swiss"], "default": "elimination"}, "best_of": {"organizer_choice": [1, 3, 5], "default": 3}}}, {"kind": "scoreboard", "params": {"side": "player", "rounds": "single", "winner": "high", "result": "points"}}, {"kind": "celebration", "params": {"source": "bracket"}}]}'),
  ('swimming', ARRAY['sports'], 450, '{"tr": "Yüzme"}', 'pool', ARRAY['in_person'], 1,
   '{"fields": [{"key": "place", "kind": "choice", "options": ["pool", "open_water"], "option_labels": {"pool": {"tr": "Havuz"}, "open_water": {"tr": "Açık su"}}, "labels": {"tr": "Yer"}}], "components": [{"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "m", "scope": "personal", "entered_by": "self", "min": 0, "max": 100000, "decimals": 0}}, {"kind": "ranking", "labels": {"tr": "Derece"}, "optional": {"default": false}, "params": {"metric": "duration", "direction": "asc"}}, {"kind": "roll_call", "optional": {"default": false}, "params": {"alarm": true, "return_grace_minutes": 30}}]}'),
  ('training', ARRAY['sports'], 460, '{"tr": "Antrenman"}', 'fitness_center', ARRAY['in_person', 'online'], 1,
   '{"components": [{"kind": "schedule", "labels": {"tr": "Hareketler"}}, {"kind": "measure", "key": "training_time", "labels": {"tr": "Süre"}, "params": {"unit": "minute", "scope": "personal", "entered_by": "self", "min": 0, "max": 1440, "decimals": 0, "showcase": true}}, {"kind": "timer", "params": {"mode": {"organizer_choice": ["countdown", "stopwatch"], "default": "countdown"}}}]}'),
  ('yoga', ARRAY['sports'], 470, '{"tr": "Yoga"}', 'self_improvement', ARRAY['in_person', 'online'], 1,
   '{}'),
  ('board_game_night', ARRAY['games'], 510, '{"tr": "Masa oyunu gecesi"}', 'casino', ARRAY['in_person'], 1,
   '{"components": [{"kind": "scoreboard", "params": {"tables": true, "presets": ["batak", "okey_101", "okey", "backgammon", "pisti", "foosball", "free"], "guests": true}}, {"kind": "timer", "params": {"mode": {"organizer_choice": ["countdown", "stopwatch"], "default": "countdown"}}}, {"kind": "celebration", "params": {"source": "scoreboard"}}]}'),
  ('quiz_night', ARRAY['games'], 520, '{"tr": "Bilgi yarışması gecesi"}', 'quiz', ARRAY['in_person', 'online'], 1,
   '{"components": [{"kind": "teams", "params": {"mode": {"organizer_choice": ["manual", "random"], "default": "random"}}}, {"kind": "scoreboard", "params": {"side": "team", "rounds": "round", "winner": "high", "result": "points"}}, {"kind": "timer", "params": {"mode": "countdown"}}, {"kind": "celebration", "params": {"source": "scoreboard"}}]}'),
  ('chess', ARRAY['games'], 530, '{"tr": "Satranç"}', 'extension', ARRAY['in_person', 'online'], 1,
   '{"fields": [{"key": "tempo", "kind": "choice", "options": ["classical", "rapid", "blitz"], "option_labels": {"classical": {"tr": "Klasik"}, "rapid": {"tr": "Rapid"}, "blitz": {"tr": "Blitz"}}, "labels": {"tr": "Tempo"}}], "components": [{"kind": "draw"}, {"kind": "bracket", "params": {"format": {"organizer_choice": ["swiss", "league", "elimination"], "default": "swiss"}}}, {"kind": "scoreboard", "params": {"side": "player", "tables": true, "rounds": "single", "result": "wdl"}}, {"kind": "timer", "params": {"mode": "chess_clock", "presets": [180, 300, 600, 900, 1800, 5400]}}, {"kind": "celebration", "params": {"source": "bracket"}}]}'),
  ('esports', ARRAY['games'], 540, '{"tr": "Espor turnuvası"}', 'emoji_events', ARRAY['in_person'], 1,
   '{"fields": [{"key": "game", "kind": "text", "max_length": 60, "labels": {"tr": "Oyun"}, "required": true, "vocabulary": {"namespace": "game"}}, {"key": "platform", "kind": "choice", "options": ["pc", "playstation", "xbox", "switch", "mobile", "other"], "option_labels": {"pc": {"tr": "PC"}, "playstation": {"tr": "PlayStation"}, "xbox": {"tr": "Xbox"}, "switch": {"tr": "Switch"}, "mobile": {"tr": "Mobil"}, "other": {"tr": "Diğer"}}, "labels": {"tr": "Platform"}, "required": true}], "components": [{"kind": "teams", "params": {"mode": "manual"}}, {"kind": "draw"}, {"kind": "bracket", "params": {"format": {"organizer_choice": ["elimination", "league", "swiss"], "default": "elimination"}, "best_of": {"organizer_choice": [1, 3, 5], "default": 1}}}, {"kind": "scoreboard", "params": {"side": "team", "rounds": "single", "winner": "high", "result": "points"}}, {"kind": "celebration", "params": {"source": "bracket"}}]}'),
  ('games_other', ARRAY['games'], 560, '{"tr": "Oyun (diğer)"}', 'stars', ARRAY['in_person'], 1,
   '{"fields": [{"key": "game", "kind": "text", "max_length": 60, "labels": {"tr": "Oyun adı"}, "required": true, "vocabulary": {"namespace": "game"}}], "components": [{"kind": "teams", "params": {"mode": {"organizer_choice": ["random", "manual"], "default": "random"}}}, {"kind": "scoreboard", "params": {"tables": true, "guests": true}}, {"kind": "celebration", "params": {"source": "scoreboard"}}]}'),
  ('trash_pickup', ARRAY['environment'], 610, '{"tr": "Çöp toplama"}', 'cleaning_services', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "checklist", "key": "gear", "labels": {"tr": "Malzeme listesi"}, "params": {"label": "gear", "assignment": true, "quantity": true}}, {"kind": "measure", "key": "weight", "labels": {"tr": "Ağırlık"}, "params": {"unit": "kg", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 1, "showcase": true}}, {"kind": "measure", "key": "bags", "labels": {"tr": "Poşet"}, "params": {"unit": "bag", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 0}}, {"kind": "goal", "params": {"measure": "weight"}}]}'),
  ('tree_planting', ARRAY['environment'], 620, '{"tr": "Fidan dikimi"}', 'grass', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "checklist", "key": "gear", "labels": {"tr": "Malzeme listesi"}, "params": {"label": "gear", "assignment": true, "quantity": true}}, {"kind": "measure", "key": "saplings", "labels": {"tr": "Fidan"}, "params": {"unit": "piece", "scope": "total", "entered_by": "organizer", "min": 0, "max": 1000000, "decimals": 0, "showcase": true}}, {"kind": "goal", "params": {"measure": "saplings"}}]}'),
  ('blood_drive', ARRAY['environment'], 630, '{"tr": "Kan bağışı"}', 'bloodtype', ARRAY['in_person'], 1,
   '{"components": [{"kind": "measure", "key": "units", "labels": {"tr": "Ünite"}, "params": {"unit": "unit", "scope": "total", "entered_by": "organizer", "min": 0, "max": 10000, "decimals": 0}}], "flags": {"sensitive_attendance": true, "personal_scope_forbidden": true}}'),
  ('aid_drive', ARRAY['environment'], 640, '{"tr": "Yardım toplama"}', 'volunteer_activism', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "needs", "labels": {"tr": "İhtiyaç listesi"}, "params": {"label": "item", "assignment": true, "quantity": true}}, {"kind": "measure", "key": "boxes", "labels": {"tr": "Koli"}, "params": {"unit": "box", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 0}}, {"kind": "goal", "params": {"measure": "boxes"}}]}'),
  ('visit', ARRAY['environment'], 650, '{"tr": "Ziyaret"}', 'handshake', ARRAY['in_person'], 1,
   '{"components": [{"kind": "schedule"}, {"kind": "checklist", "key": "checklist", "labels": {"tr": "Liste"}, "params": {"label": "item", "assignment": true, "quantity": true}}], "flags": {"album_notice": true}}'),
  ('shelter_volunteering', ARRAY['animals'], 710, '{"tr": "Barınak gönüllülüğü"}', 'cruelty_free', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "tasks", "labels": {"tr": "Görevler"}, "params": {"label": "task", "assignment": true}}, {"kind": "measure", "key": "food", "labels": {"tr": "Mama"}, "params": {"unit": "kg", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 1, "showcase": true}}]}'),
  ('dog_walk', ARRAY['animals', 'nature'], 720, '{"tr": "Köpek yürüyüşü"}', 'pets', ARRAY['in_person'], 1,
   '{"components": [{"kind": "route"}, {"kind": "measure", "key": "distance", "labels": {"tr": "Mesafe"}, "params": {"unit": "km", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100, "decimals": 2}}]}'),
  ('stray_feeding', ARRAY['animals'], 730, '{"tr": "Sokak hayvanı besleme"}', 'favorite', ARRAY['in_person'], 1,
   '{"components": [{"kind": "measure", "key": "food", "labels": {"tr": "Mama"}, "params": {"unit": "kg", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 1, "showcase": true}}, {"kind": "goal", "params": {"measure": "food"}}]}'),
  ('book_club', ARRAY['books'], 810, '{"tr": "Kitap buluşması"}', 'menu_book', ARRAY['in_person'], 1,
   '{"fields": [{"key": "book_title", "kind": "text", "max_length": 60, "labels": {"tr": "Kitap"}, "vocabulary": {"namespace": "book"}}, {"key": "author", "kind": "text", "max_length": 120, "labels": {"tr": "Yazar"}}], "components": [{"kind": "log", "key": "read", "labels": {"tr": "Okunanlar"}, "params": {"fields": [{"key": "book", "kind": "text", "max_length": 60, "labels": {"tr": "Kitap"}, "required": true, "vocabulary": {"namespace": "book"}}, {"key": "pages", "kind": "integer", "min": 1, "max": 20000, "labels": {"tr": "Sayfa"}, "unit": "page"}], "stat": ["distinct:book", "sum:pages"]}}, {"kind": "vote", "key": "next_book", "labels": {"tr": "Sıradaki kitap"}, "params": {"source": "free"}}]}'),
  ('reading_marathon', ARRAY['books'], 820, '{"tr": "Okuma maratonu"}', 'auto_stories', ARRAY['in_person'], 1,
   '{"components": [{"kind": "measure", "key": "pages", "labels": {"tr": "Sayfa"}, "params": {"unit": "page", "scope": "personal", "entered_by": "self", "min": 0, "max": 100000, "decimals": 0, "showcase": true}}, {"kind": "goal", "params": {"measure": "pages"}}, {"kind": "timer", "optional": {"default": false}, "params": {"mode": {"organizer_choice": ["countdown", "stopwatch"], "default": "countdown"}}}]}'),
  ('book_swap', ARRAY['books'], 830, '{"tr": "Kitap takası"}', 'recycling', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "books", "labels": {"tr": "Getirilecek kitaplar"}, "params": {"label": "item", "assignment": true}}]}'),
  ('writing_workshop', ARRAY['books', 'arts'], 840, '{"tr": "Yazı atölyesi"}', 'edit', ARRAY['in_person'], 1,
   '{"components": [{"kind": "schedule"}, {"kind": "timer", "params": {"mode": "countdown"}}]}'),
  ('art_workshop', ARRAY['arts'], 910, '{"tr": "Atölye"}', 'brush', ARRAY['in_person'], 1,
   '{"fields": [{"key": "craft", "kind": "choice", "options": ["painting", "ceramics", "knitting", "woodwork", "jewelry", "other"], "option_labels": {"painting": {"tr": "Resim"}, "ceramics": {"tr": "Seramik"}, "knitting": {"tr": "Örgü"}, "woodwork": {"tr": "Ahşap"}, "jewelry": {"tr": "Takı"}, "other": {"tr": "Diğer"}}, "labels": {"tr": "Dal"}}], "components": [{"kind": "checklist", "key": "materials", "labels": {"tr": "Malzeme listesi"}, "params": {"label": "gear", "assignment": true, "quantity": true}}, {"kind": "schedule"}]}'),
  ('photo_walk', ARRAY['arts', 'travel'], 920, '{"tr": "Fotoğraf yürüyüşü"}', 'photo_camera', ARRAY['in_person'], 1,
   '{"components": [{"kind": "route"}, {"kind": "vote", "key": "best_photo", "labels": {"tr": "En iyi kare"}, "params": {"source": "album"}}, {"kind": "celebration", "params": {"source": "best_photo"}}]}'),
  ('museum_visit', ARRAY['arts', 'travel'], 930, '{"tr": "Müze / sergi"}', 'museum', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "schedule"}]}'),
  ('music_rehearsal', ARRAY['arts'], 940, '{"tr": "Müzik provası"}', 'music_note', ARRAY['in_person'], 1,
   '{"components": [{"kind": "schedule", "labels": {"tr": "Repertuvar"}}, {"kind": "checklist", "key": "equipment", "labels": {"tr": "Ekipman"}, "params": {"label": "gear", "assignment": true}}]}'),
  ('choir', ARRAY['arts'], 950, '{"tr": "Koro"}', 'groups', ARRAY['in_person'], 1,
   '{"components": [{"kind": "schedule", "labels": {"tr": "Repertuvar"}}]}'),
  ('show_outing', ARRAY['arts', 'travel'], 960, '{"tr": "Konser / tiyatro çıkışı"}', 'theater_comedy', ARRAY['in_person'], 1,
   '{"fields": [{"key": "show_name", "kind": "text", "max_length": 120, "labels": {"tr": "Gösteri adı"}}, {"key": "venue", "kind": "text", "max_length": 120, "labels": {"tr": "Mekân"}}], "components": [{"kind": "meeting_point"}]}'),
  ('talk', ARRAY['learning'], 1010, '{"tr": "Söyleşi / seminer"}', 'mic', ARRAY['in_person', 'online'], 1,
   '{"fields": [{"key": "topic", "kind": "text", "max_length": 120, "labels": {"tr": "Konu"}}], "components": [{"kind": "schedule"}, {"kind": "questions", "params": {"anonymous": true}}, {"kind": "timer", "optional": {"default": false}, "params": {"mode": "countdown"}}]}'),
  ('language_exchange', ARRAY['learning'], 1020, '{"tr": "Dil pratiği"}', 'translate', ARRAY['in_person', 'online'], 1,
   '{"fields": [{"key": "languages", "kind": "multi_choice", "options": ["turkish", "english", "german", "french", "spanish", "italian", "russian", "arabic", "japanese", "chinese", "korean", "other"], "option_labels": {"turkish": {"tr": "Türkçe"}, "english": {"tr": "İngilizce"}, "german": {"tr": "Almanca"}, "french": {"tr": "Fransızca"}, "spanish": {"tr": "İspanyolca"}, "italian": {"tr": "İtalyanca"}, "russian": {"tr": "Rusça"}, "arabic": {"tr": "Arapça"}, "japanese": {"tr": "Japonca"}, "chinese": {"tr": "Çince"}, "korean": {"tr": "Korece"}, "other": {"tr": "Diğer"}}, "labels": {"tr": "Diller"}}, {"key": "level", "kind": "choice", "options": ["beginner", "intermediate", "advanced", "mixed"], "option_labels": {"beginner": {"tr": "Başlangıç"}, "intermediate": {"tr": "Orta"}, "advanced": {"tr": "İleri"}, "mixed": {"tr": "Karışık"}}, "labels": {"tr": "Seviye"}}], "components": [{"kind": "teams", "labels": {"tr": "Masalar"}, "params": {"mode": "random"}}, {"kind": "timer", "optional": {"default": false}, "params": {"mode": "countdown"}}]}'),
  ('hackathon', ARRAY['learning'], 1030, '{"tr": "Hackathon"}', 'code', ARRAY['in_person'], 1,
   '{"components": [{"kind": "teams", "params": {"mode": {"organizer_choice": ["manual", "random"], "default": "manual"}}}, {"kind": "schedule"}, {"kind": "vote", "labels": {"tr": "Proje oylaması"}, "params": {"source": "free"}}, {"kind": "celebration", "params": {"source": "vote"}}, {"kind": "timer", "params": {"mode": "countdown"}}]}'),
  ('city_tour', ARRAY['travel'], 1110, '{"tr": "Şehir turu"}', 'map', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "route"}, {"kind": "schedule"}]}'),
  ('day_trip', ARRAY['travel'], 1120, '{"tr": "Günübirlik gezi"}', 'calendar_month', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "schedule"}, {"kind": "roll_call", "params": {"alarm": false}}, {"kind": "vehicles", "optional": {"default": false}, "params": {"seats": true}}]}'),
  ('heritage_site', ARRAY['travel'], 1130, '{"tr": "Ören yeri gezisi"}', 'account_balance', ARRAY['in_person'], 1,
   '{"components": [{"kind": "route"}, {"kind": "schedule"}]}'),
  ('festival', ARRAY['travel'], 1140, '{"tr": "Festival"}', 'festival', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "schedule"}, {"kind": "vehicles", "optional": {"default": false}, "params": {"seats": true}}]}'),
  ('potluck', ARRAY['food'], 1210, '{"tr": "Ortak yemek"}', 'local_dining', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "dishes", "labels": {"tr": "Kim ne getiriyor"}, "params": {"label": "item", "assignment": true, "quantity": true}}]}'),
  ('cooking_class', ARRAY['food'], 1220, '{"tr": "Mutfak atölyesi"}', 'soup_kitchen', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "ingredients", "labels": {"tr": "Malzemeler"}, "params": {"label": "item", "assignment": true, "quantity": true}}, {"kind": "schedule"}, {"kind": "timer", "params": {"mode": "countdown"}}]}'),
  ('food_tour', ARRAY['food'], 1230, '{"tr": "Gastronomi turu"}', 'bakery_dining', ARRAY['in_person'], 1,
   '{"components": [{"kind": "route"}, {"kind": "vote", "key": "best_stop", "labels": {"tr": "En iyi durak"}, "params": {"source": "free"}}]}'),
  ('study_circle', ARRAY['faith'], 1310, '{"tr": "Sohbet / ders halkası"}', 'auto_stories', ARRAY['in_person'], 1,
   '{"components": [{"kind": "schedule"}]}'),
  ('group_reading', ARRAY['faith'], 1320, '{"tr": "Ortak okuma"}', 'history_edu', ARRAY['in_person'], 1,
   '{"components": [{"kind": "checklist", "key": "sections", "labels": {"tr": "Bölüm paylaştırma"}, "params": {"label": "section", "assignment": true}}, {"kind": "measure", "key": "sections_read", "labels": {"tr": "Okunan bölüm"}, "params": {"unit": "section", "scope": "total", "entered_by": "organizer", "min": 0, "max": 100000, "decimals": 0}}, {"kind": "goal", "params": {"measure": "sections_read"}}]}'),
  ('group_visit', ARRAY['faith'], 1330, '{"tr": "Toplu ziyaret"}', 'diversity_3', ARRAY['in_person'], 1,
   '{"components": [{"kind": "meeting_point"}, {"kind": "schedule"}]}'),
  ('study_group', ARRAY['education'], 1410, '{"tr": "Çalışma grubu / etüt"}', 'school', ARRAY['in_person', 'online'], 1,
   '{"components": [{"kind": "schedule", "labels": {"tr": "Konu planı"}}, {"kind": "measure", "key": "study_time", "labels": {"tr": "Çalışma süresi"}, "params": {"unit": "hour", "scope": "personal", "entered_by": "self", "min": 0, "max": 24, "decimals": 2, "showcase": true}}, {"kind": "timer", "params": {"mode": "countdown", "presets": [1500, 300, 900]}}]}'),
  ('problem_session', ARRAY['education'], 1420, '{"tr": "Soru çözüm oturumu"}', 'calculate', ARRAY['in_person', 'online'], 1,
   '{"components": [{"kind": "schedule"}, {"kind": "pool_link"}, {"kind": "measure", "key": "solved", "labels": {"tr": "Çözülen soru"}, "params": {"unit": "piece", "scope": "total", "entered_by": "organizer", "min": 0, "max": 10000, "decimals": 0, "showcase": true}}]}'),
  ('peer_teaching', ARRAY['education'], 1430, '{"tr": "Akran konu anlatımı"}', 'lightbulb', ARRAY['in_person', 'online'], 1,
   '{"components": [{"kind": "schedule"}, {"kind": "questions", "optional": {"default": false}}]}'),
  ('mock_exam', ARRAY['education'], 1440, '{"tr": "Deneme sınavı"}', 'timer', ARRAY['in_person'], 1,
   '{"components": [{"kind": "measure", "key": "net", "labels": {"tr": "Net"}, "params": {"unit": "net", "scope": "personal", "entered_by": "self", "min": -50, "max": 200, "decimals": 2, "sensitive": true}}, {"kind": "timer", "params": {"mode": "countdown"}}]}')
ON CONFLICT (key) DO NOTHING;

INSERT INTO catalog_event_type_versions (type_key, version, labels, icon, modes, definition)
SELECT key, version, labels, icon, modes, definition FROM catalog_event_types
WHERE version = 1 AND key IN ('moto_tour', 'car_meet', 'auto_show', 'maintenance_workshop', 'summit', 'climbing', 'birding', 'stargazing', 'diving', 'paddling', 'sailing', 'team_sport', 'running', 'cycling', 'racket', 'swimming', 'training', 'yoga', 'board_game_night', 'quiz_night', 'chess', 'esports', 'games_other', 'trash_pickup', 'tree_planting', 'blood_drive', 'aid_drive', 'visit', 'shelter_volunteering', 'dog_walk', 'stray_feeding', 'book_club', 'reading_marathon', 'book_swap', 'writing_workshop', 'art_workshop', 'photo_walk', 'museum_visit', 'music_rehearsal', 'choir', 'show_outing', 'talk', 'language_exchange', 'hackathon', 'city_tour', 'day_trip', 'heritage_site', 'festival', 'potluck', 'cooking_class', 'food_tour', 'study_circle', 'group_reading', 'group_visit', 'study_group', 'problem_session', 'peer_teaching', 'mock_exam')
ON CONFLICT (type_key, version) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Ders ve konu listesi: önce düzeyler, sonra dersler, sonra konular.

-- Düzeyler
INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES
  ('lgs', NULL, 10, '{"tr": "LGS (8. sınıf)"}'),
  ('tyt', NULL, 20, '{"tr": "YKS — TYT"}'),
  ('ayt', NULL, 30, '{"tr": "YKS — AYT"}'),
  ('high_school', NULL, 40, '{"tr": "Lise (okul dersleri)"}'),
  ('yds', NULL, 50, '{"tr": "YDS / İngilizce"}'),
  ('other_exams', NULL, 60, '{"tr": "Diğer"}')
ON CONFLICT (key) DO NOTHING;

-- Dersler ve düzeylerin Diğer'i
INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES
  ('lgs_turkish', 'lgs', 10, '{"tr": "Türkçe"}'),
  ('lgs_math', 'lgs', 20, '{"tr": "Matematik"}'),
  ('lgs_science', 'lgs', 30, '{"tr": "Fen Bilimleri"}'),
  ('lgs_history', 'lgs', 40, '{"tr": "T.C. İnkılap Tarihi ve Atatürkçülük"}'),
  ('lgs_religion', 'lgs', 50, '{"tr": "Din Kültürü ve Ahlak Bilgisi"}'),
  ('lgs_english', 'lgs', 60, '{"tr": "İngilizce"}'),
  ('lgs_other', 'lgs', 999, '{"tr": "Diğer"}'),
  ('tyt_turkish', 'tyt', 10, '{"tr": "Türkçe"}'),
  ('tyt_math', 'tyt', 20, '{"tr": "Matematik"}'),
  ('tyt_geometry', 'tyt', 30, '{"tr": "Geometri"}'),
  ('tyt_physics', 'tyt', 40, '{"tr": "Fizik"}'),
  ('tyt_chemistry', 'tyt', 50, '{"tr": "Kimya"}'),
  ('tyt_biology', 'tyt', 60, '{"tr": "Biyoloji"}'),
  ('tyt_history', 'tyt', 70, '{"tr": "Tarih"}'),
  ('tyt_geography', 'tyt', 80, '{"tr": "Coğrafya"}'),
  ('tyt_philosophy', 'tyt', 90, '{"tr": "Felsefe"}'),
  ('tyt_religion', 'tyt', 100, '{"tr": "Din Kültürü"}'),
  ('tyt_other', 'tyt', 999, '{"tr": "Diğer"}'),
  ('ayt_math', 'ayt', 10, '{"tr": "Matematik"}'),
  ('ayt_physics', 'ayt', 20, '{"tr": "Fizik"}'),
  ('ayt_chemistry', 'ayt', 30, '{"tr": "Kimya"}'),
  ('ayt_biology', 'ayt', 40, '{"tr": "Biyoloji"}'),
  ('ayt_literature', 'ayt', 50, '{"tr": "Türk Dili ve Edebiyatı"}'),
  ('ayt_history', 'ayt', 60, '{"tr": "Tarih"}'),
  ('ayt_geography', 'ayt', 70, '{"tr": "Coğrafya"}'),
  ('ayt_philosophy', 'ayt', 80, '{"tr": "Felsefe grubu (Mantık, Psikoloji, Sosyoloji)"}'),
  ('ayt_other', 'ayt', 999, '{"tr": "Diğer"}'),
  ('hs_math', 'high_school', 10, '{"tr": "Matematik"}'),
  ('hs_physics', 'high_school', 20, '{"tr": "Fizik"}'),
  ('hs_chemistry', 'high_school', 30, '{"tr": "Kimya"}'),
  ('hs_biology', 'high_school', 40, '{"tr": "Biyoloji"}'),
  ('hs_literature', 'high_school', 50, '{"tr": "Türk Dili ve Edebiyatı"}'),
  ('hs_history', 'high_school', 60, '{"tr": "Tarih"}'),
  ('hs_geography', 'high_school', 70, '{"tr": "Coğrafya"}'),
  ('hs_philosophy', 'high_school', 80, '{"tr": "Felsefe"}'),
  ('hs_english', 'high_school', 90, '{"tr": "İngilizce"}'),
  ('hs_german', 'high_school', 100, '{"tr": "Almanca"}'),
  ('hs_religion', 'high_school', 110, '{"tr": "Din Kültürü"}'),
  ('high_school_other', 'high_school', 999, '{"tr": "Diğer"}'),
  ('yds_english', 'yds', 10, '{"tr": "İngilizce"}'),
  ('yds_other', 'yds', 999, '{"tr": "Diğer"}'),
  ('other_exams_other', 'other_exams', 999, '{"tr": "Diğer"}')
ON CONFLICT (key) DO NOTHING;

-- Konular ve derslerin Diğer'i
INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES
  ('lgs_tr_word_meaning', 'lgs_turkish', 10, '{"tr": "Sözcükte anlam"}'),
  ('lgs_tr_sentence_meaning', 'lgs_turkish', 20, '{"tr": "Cümlede anlam"}'),
  ('lgs_tr_paragraph', 'lgs_turkish', 30, '{"tr": "Paragraf"}'),
  ('lgs_tr_verbals', 'lgs_turkish', 40, '{"tr": "Fiilimsiler"}'),
  ('lgs_tr_sentence_elements', 'lgs_turkish', 50, '{"tr": "Cümlenin öğeleri"}'),
  ('lgs_tr_sentence_types', 'lgs_turkish', 60, '{"tr": "Cümle türleri"}'),
  ('lgs_tr_spelling', 'lgs_turkish', 70, '{"tr": "Yazım kuralları"}'),
  ('lgs_tr_punctuation', 'lgs_turkish', 80, '{"tr": "Noktalama"}'),
  ('lgs_tr_figures_of_speech', 'lgs_turkish', 90, '{"tr": "Söz sanatları"}'),
  ('lgs_tr_text_types', 'lgs_turkish', 100, '{"tr": "Metin türleri"}'),
  ('lgs_turkish_other', 'lgs_turkish', 999, '{"tr": "Diğer"}'),
  ('lgs_math_factors_multiples', 'lgs_math', 10, '{"tr": "Çarpanlar ve katlar"}'),
  ('lgs_math_exponents', 'lgs_math', 20, '{"tr": "Üslü ifadeler"}'),
  ('lgs_math_square_roots', 'lgs_math', 30, '{"tr": "Kareköklü ifadeler"}'),
  ('lgs_math_data_analysis', 'lgs_math', 40, '{"tr": "Veri analizi"}'),
  ('lgs_math_probability', 'lgs_math', 50, '{"tr": "Olasılık"}'),
  ('lgs_math_algebraic_expressions', 'lgs_math', 60, '{"tr": "Cebirsel ifadeler ve özdeşlikler"}'),
  ('lgs_math_linear_equations', 'lgs_math', 70, '{"tr": "Doğrusal denklemler"}'),
  ('lgs_math_inequalities', 'lgs_math', 80, '{"tr": "Eşitsizlikler"}'),
  ('lgs_math_triangles', 'lgs_math', 90, '{"tr": "Üçgenler"}'),
  ('lgs_math_similarity', 'lgs_math', 100, '{"tr": "Eşlik ve benzerlik"}'),
  ('lgs_math_transformations', 'lgs_math', 110, '{"tr": "Dönüşüm geometrisi"}'),
  ('lgs_math_solids', 'lgs_math', 120, '{"tr": "Geometrik cisimler"}'),
  ('lgs_math_other', 'lgs_math', 999, '{"tr": "Diğer"}'),
  ('lgs_sci_seasons_climate', 'lgs_science', 10, '{"tr": "Mevsimler ve iklim"}'),
  ('lgs_sci_dna_genetic_code', 'lgs_science', 20, '{"tr": "DNA ve genetik kod"}'),
  ('lgs_sci_pressure', 'lgs_science', 30, '{"tr": "Basınç"}'),
  ('lgs_sci_matter_industry', 'lgs_science', 40, '{"tr": "Madde ve endüstri"}'),
  ('lgs_sci_simple_machines', 'lgs_science', 50, '{"tr": "Basit makineler"}'),
  ('lgs_sci_energy_environment', 'lgs_science', 60, '{"tr": "Enerji dönüşümleri ve çevre bilimi"}'),
  ('lgs_sci_electric_charges', 'lgs_science', 70, '{"tr": "Elektrik yükleri ve elektrik enerjisi"}'),
  ('lgs_science_other', 'lgs_science', 999, '{"tr": "Diğer"}'),
  ('lgs_hist_hero_born', 'lgs_history', 10, '{"tr": "Bir kahraman doğuyor"}'),
  ('lgs_hist_national_awakening', 'lgs_history', 20, '{"tr": "Millî uyanış"}'),
  ('lgs_hist_national_epic', 'lgs_history', 30, '{"tr": "Millî bir destan"}'),
  ('lgs_hist_modernizing_turkey', 'lgs_history', 40, '{"tr": "Atatürkçülük ve çağdaşlaşan Türkiye"}'),
  ('lgs_hist_democratization', 'lgs_history', 50, '{"tr": "Demokratikleşme çabaları"}'),
  ('lgs_hist_foreign_policy', 'lgs_history', 60, '{"tr": "Atatürk dönemi dış politika"}'),
  ('lgs_hist_after_ataturk', 'lgs_history', 70, '{"tr": "Atatürk''ün ölümü ve sonrası"}'),
  ('lgs_history_other', 'lgs_history', 999, '{"tr": "Diğer"}'),
  ('lgs_rel_destiny', 'lgs_religion', 10, '{"tr": "Kader inancı"}'),
  ('lgs_rel_zakat_charity', 'lgs_religion', 20, '{"tr": "Zekât ve sadaka"}'),
  ('lgs_rel_religion_life', 'lgs_religion', 30, '{"tr": "Din ve hayat"}'),
  ('lgs_rel_prophet_example', 'lgs_religion', 40, '{"tr": "Hz. Muhammed''in örnekliği"}'),
  ('lgs_rel_quran', 'lgs_religion', 50, '{"tr": "Kur''an-ı Kerim ve özellikleri"}'),
  ('lgs_religion_other', 'lgs_religion', 999, '{"tr": "Diğer"}'),
  ('lgs_en_friendship', 'lgs_english', 10, '{"tr": "Friendship"}'),
  ('lgs_en_teen_life', 'lgs_english', 20, '{"tr": "Teen life"}'),
  ('lgs_en_kitchen', 'lgs_english', 30, '{"tr": "In the kitchen"}'),
  ('lgs_en_phone', 'lgs_english', 40, '{"tr": "On the phone"}'),
  ('lgs_en_internet', 'lgs_english', 50, '{"tr": "The Internet"}'),
  ('lgs_en_adventures', 'lgs_english', 60, '{"tr": "Adventures"}'),
  ('lgs_en_tourism', 'lgs_english', 70, '{"tr": "Tourism"}'),
  ('lgs_en_chores', 'lgs_english', 80, '{"tr": "Chores"}'),
  ('lgs_en_science', 'lgs_english', 90, '{"tr": "Science"}'),
  ('lgs_en_natural_forces', 'lgs_english', 100, '{"tr": "Natural forces"}'),
  ('lgs_english_other', 'lgs_english', 999, '{"tr": "Diğer"}'),
  ('tyt_tr_word_meaning', 'tyt_turkish', 10, '{"tr": "Sözcükte anlam"}'),
  ('tyt_tr_sentence_meaning', 'tyt_turkish', 20, '{"tr": "Cümlede anlam"}'),
  ('tyt_tr_paragraph', 'tyt_turkish', 30, '{"tr": "Paragraf"}'),
  ('tyt_tr_phonetics', 'tyt_turkish', 40, '{"tr": "Ses bilgisi"}'),
  ('tyt_tr_spelling', 'tyt_turkish', 50, '{"tr": "Yazım kuralları"}'),
  ('tyt_tr_punctuation', 'tyt_turkish', 60, '{"tr": "Noktalama"}'),
  ('tyt_tr_word_types', 'tyt_turkish', 70, '{"tr": "Sözcük türleri"}'),
  ('tyt_tr_sentence_elements', 'tyt_turkish', 80, '{"tr": "Cümlenin öğeleri"}'),
  ('tyt_tr_expression_errors', 'tyt_turkish', 90, '{"tr": "Anlatım bozuklukları"}'),
  ('tyt_turkish_other', 'tyt_turkish', 999, '{"tr": "Diğer"}'),
  ('tyt_math_basic_concepts', 'tyt_math', 10, '{"tr": "Temel kavramlar"}'),
  ('tyt_math_digits', 'tyt_math', 20, '{"tr": "Sayı basamakları"}'),
  ('tyt_math_divisibility', 'tyt_math', 30, '{"tr": "Bölme ve bölünebilme"}'),
  ('tyt_math_gcd_lcm', 'tyt_math', 40, '{"tr": "EBOB-EKOK"}'),
  ('tyt_math_rationals', 'tyt_math', 50, '{"tr": "Rasyonel sayılar"}'),
  ('tyt_math_exponents_roots', 'tyt_math', 60, '{"tr": "Üslü ve köklü sayılar"}'),
  ('tyt_math_factoring', 'tyt_math', 70, '{"tr": "Çarpanlara ayırma"}'),
  ('tyt_math_ratio_proportion', 'tyt_math', 80, '{"tr": "Oran-orantı"}'),
  ('tyt_math_word_problems', 'tyt_math', 90, '{"tr": "Problemler"}'),
  ('tyt_math_sets', 'tyt_math', 100, '{"tr": "Kümeler"}'),
  ('tyt_math_functions', 'tyt_math', 110, '{"tr": "Fonksiyonlar"}'),
  ('tyt_math_combinatorics', 'tyt_math', 120, '{"tr": "Permütasyon, kombinasyon, olasılık"}'),
  ('tyt_math_statistics', 'tyt_math', 130, '{"tr": "Veri ve istatistik"}'),
  ('tyt_math_other', 'tyt_math', 999, '{"tr": "Diğer"}'),
  ('tyt_geo_angles', 'tyt_geometry', 10, '{"tr": "Açılar"}'),
  ('tyt_geo_triangles', 'tyt_geometry', 20, '{"tr": "Üçgenler"}'),
  ('tyt_geo_polygons', 'tyt_geometry', 30, '{"tr": "Çokgenler ve dörtgenler"}'),
  ('tyt_geo_circles', 'tyt_geometry', 40, '{"tr": "Çember ve daire"}'),
  ('tyt_geo_solids', 'tyt_geometry', 50, '{"tr": "Katı cisimler"}'),
  ('tyt_geo_analytic', 'tyt_geometry', 60, '{"tr": "Analitik geometri"}'),
  ('tyt_geometry_other', 'tyt_geometry', 999, '{"tr": "Diğer"}'),
  ('tyt_phy_intro', 'tyt_physics', 10, '{"tr": "Fizik bilimine giriş"}'),
  ('tyt_phy_matter', 'tyt_physics', 20, '{"tr": "Madde ve özellikleri"}'),
  ('tyt_phy_motion_force', 'tyt_physics', 30, '{"tr": "Hareket ve kuvvet"}'),
  ('tyt_phy_energy', 'tyt_physics', 40, '{"tr": "Enerji"}'),
  ('tyt_phy_heat', 'tyt_physics', 50, '{"tr": "Isı ve sıcaklık"}'),
  ('tyt_phy_electrostatics', 'tyt_physics', 60, '{"tr": "Elektrostatik"}'),
  ('tyt_phy_electromagnetism', 'tyt_physics', 70, '{"tr": "Elektrik ve manyetizma"}'),
  ('tyt_phy_pressure_buoyancy', 'tyt_physics', 80, '{"tr": "Basınç ve kaldırma kuvveti"}'),
  ('tyt_phy_waves', 'tyt_physics', 90, '{"tr": "Dalgalar"}'),
  ('tyt_phy_optics', 'tyt_physics', 100, '{"tr": "Optik"}'),
  ('tyt_physics_other', 'tyt_physics', 999, '{"tr": "Diğer"}'),
  ('tyt_chem_science', 'tyt_chemistry', 10, '{"tr": "Kimya bilimi"}'),
  ('tyt_chem_atom_periodic', 'tyt_chemistry', 20, '{"tr": "Atom ve periyodik sistem"}'),
  ('tyt_chem_interactions', 'tyt_chemistry', 30, '{"tr": "Kimyasal türler arası etkileşimler"}'),
  ('tyt_chem_states', 'tyt_chemistry', 40, '{"tr": "Maddenin halleri"}'),
  ('tyt_chem_nature', 'tyt_chemistry', 50, '{"tr": "Doğa ve kimya"}'),
  ('tyt_chem_laws', 'tyt_chemistry', 60, '{"tr": "Kimyanın temel kanunları"}'),
  ('tyt_chem_mixtures', 'tyt_chemistry', 70, '{"tr": "Karışımlar"}'),
  ('tyt_chem_acids_bases', 'tyt_chemistry', 80, '{"tr": "Asitler, bazlar ve tuzlar"}'),
  ('tyt_chem_everywhere', 'tyt_chemistry', 90, '{"tr": "Kimya her yerde"}'),
  ('tyt_chemistry_other', 'tyt_chemistry', 999, '{"tr": "Diğer"}'),
  ('tyt_bio_living_things', 'tyt_biology', 10, '{"tr": "Canlıların ortak özellikleri"}'),
  ('tyt_bio_basic_components', 'tyt_biology', 20, '{"tr": "Temel bileşenler"}'),
  ('tyt_bio_cell', 'tyt_biology', 30, '{"tr": "Hücre"}'),
  ('tyt_bio_classification', 'tyt_biology', 40, '{"tr": "Sınıflandırma"}'),
  ('tyt_bio_cell_division', 'tyt_biology', 50, '{"tr": "Hücre bölünmeleri"}'),
  ('tyt_bio_heredity', 'tyt_biology', 60, '{"tr": "Kalıtım"}'),
  ('tyt_bio_ecosystem', 'tyt_biology', 70, '{"tr": "Ekosistem ekolojisi"}'),
  ('tyt_biology_other', 'tyt_biology', 999, '{"tr": "Diğer"}'),
  ('tyt_history_other', 'tyt_history', 999, '{"tr": "Diğer"}'),
  ('tyt_geography_other', 'tyt_geography', 999, '{"tr": "Diğer"}'),
  ('tyt_philosophy_other', 'tyt_philosophy', 999, '{"tr": "Diğer"}'),
  ('tyt_religion_other', 'tyt_religion', 999, '{"tr": "Diğer"}'),
  ('ayt_math_polynomials', 'ayt_math', 10, '{"tr": "Polinomlar"}'),
  ('ayt_math_quadratics', 'ayt_math', 20, '{"tr": "İkinci dereceden denklemler"}'),
  ('ayt_math_parabola', 'ayt_math', 30, '{"tr": "Parabol"}'),
  ('ayt_math_complex_numbers', 'ayt_math', 40, '{"tr": "Karmaşık sayılar"}'),
  ('ayt_math_logarithm', 'ayt_math', 50, '{"tr": "Logaritma"}'),
  ('ayt_math_sequences', 'ayt_math', 60, '{"tr": "Diziler"}'),
  ('ayt_math_trigonometry', 'ayt_math', 70, '{"tr": "Trigonometri"}'),
  ('ayt_math_limits', 'ayt_math', 80, '{"tr": "Limit ve süreklilik"}'),
  ('ayt_math_derivative', 'ayt_math', 90, '{"tr": "Türev"}'),
  ('ayt_math_integral', 'ayt_math', 100, '{"tr": "İntegral"}'),
  ('ayt_math_other', 'ayt_math', 999, '{"tr": "Diğer"}'),
  ('ayt_phy_vectors', 'ayt_physics', 10, '{"tr": "Vektörler"}'),
  ('ayt_phy_force_motion', 'ayt_physics', 20, '{"tr": "Kuvvet ve hareket"}'),
  ('ayt_phy_projectiles', 'ayt_physics', 30, '{"tr": "Atışlar"}'),
  ('ayt_phy_work_energy', 'ayt_physics', 40, '{"tr": "İş, güç, enerji"}'),
  ('ayt_phy_momentum', 'ayt_physics', 50, '{"tr": "İtme ve momentum"}'),
  ('ayt_phy_torque', 'ayt_physics', 60, '{"tr": "Tork ve denge"}'),
  ('ayt_phy_circular_motion', 'ayt_physics', 70, '{"tr": "Çembersel hareket"}'),
  ('ayt_phy_harmonic_motion', 'ayt_physics', 80, '{"tr": "Basit harmonik hareket"}'),
  ('ayt_phy_electromagnetism', 'ayt_physics', 90, '{"tr": "Elektrik ve manyetizma"}'),
  ('ayt_phy_wave_mechanics', 'ayt_physics', 100, '{"tr": "Dalga mekaniği"}'),
  ('ayt_phy_modern_physics', 'ayt_physics', 110, '{"tr": "Modern fizik"}'),
  ('ayt_physics_other', 'ayt_physics', 999, '{"tr": "Diğer"}'),
  ('ayt_chem_atom_theory', 'ayt_chemistry', 10, '{"tr": "Modern atom teorisi"}'),
  ('ayt_chem_gases', 'ayt_chemistry', 20, '{"tr": "Gazlar"}'),
  ('ayt_chem_solutions', 'ayt_chemistry', 30, '{"tr": "Sıvı çözeltiler"}'),
  ('ayt_chem_reaction_energy', 'ayt_chemistry', 40, '{"tr": "Tepkimelerde enerji"}'),
  ('ayt_chem_reaction_rates', 'ayt_chemistry', 50, '{"tr": "Tepkime hızları"}'),
  ('ayt_chem_equilibrium', 'ayt_chemistry', 60, '{"tr": "Kimyasal denge"}'),
  ('ayt_chem_acid_base_equilibrium', 'ayt_chemistry', 70, '{"tr": "Asit-baz dengesi"}'),
  ('ayt_chem_solubility', 'ayt_chemistry', 80, '{"tr": "Çözünürlük dengesi"}'),
  ('ayt_chem_electrochemistry', 'ayt_chemistry', 90, '{"tr": "Elektrokimya"}'),
  ('ayt_chem_organic', 'ayt_chemistry', 100, '{"tr": "Organik kimya"}'),
  ('ayt_chemistry_other', 'ayt_chemistry', 999, '{"tr": "Diğer"}'),
  ('ayt_bio_nervous', 'ayt_biology', 10, '{"tr": "Sinir sistemi"}'),
  ('ayt_bio_endocrine', 'ayt_biology', 20, '{"tr": "Endokrin sistem"}'),
  ('ayt_bio_senses', 'ayt_biology', 30, '{"tr": "Duyu organları"}'),
  ('ayt_bio_support_movement', 'ayt_biology', 40, '{"tr": "Destek ve hareket"}'),
  ('ayt_bio_digestion', 'ayt_biology', 50, '{"tr": "Sindirim"}'),
  ('ayt_bio_circulation', 'ayt_biology', 60, '{"tr": "Dolaşım"}'),
  ('ayt_bio_respiration', 'ayt_biology', 70, '{"tr": "Solunum"}'),
  ('ayt_bio_excretion', 'ayt_biology', 80, '{"tr": "Boşaltım"}'),
  ('ayt_bio_reproduction', 'ayt_biology', 90, '{"tr": "Üreme ve gelişme"}'),
  ('ayt_bio_gene_protein', 'ayt_biology', 100, '{"tr": "Genden proteine"}'),
  ('ayt_bio_photosynthesis', 'ayt_biology', 110, '{"tr": "Fotosentez ve solunum"}'),
  ('ayt_bio_plants', 'ayt_biology', 120, '{"tr": "Bitki biyolojisi"}'),
  ('ayt_bio_community_ecology', 'ayt_biology', 130, '{"tr": "Komünite ve popülasyon ekolojisi"}'),
  ('ayt_biology_other', 'ayt_biology', 999, '{"tr": "Diğer"}'),
  ('ayt_lit_poetry', 'ayt_literature', 10, '{"tr": "Şiir bilgisi"}'),
  ('ayt_lit_literary_arts', 'ayt_literature', 20, '{"tr": "Edebî sanatlar"}'),
  ('ayt_lit_divan', 'ayt_literature', 30, '{"tr": "Divan edebiyatı"}'),
  ('ayt_lit_folk', 'ayt_literature', 40, '{"tr": "Halk edebiyatı"}'),
  ('ayt_lit_tanzimat', 'ayt_literature', 50, '{"tr": "Tanzimat"}'),
  ('ayt_lit_servet_i_funun', 'ayt_literature', 60, '{"tr": "Servet-i Fünun"}'),
  ('ayt_lit_national', 'ayt_literature', 70, '{"tr": "Millî edebiyat"}'),
  ('ayt_lit_republic', 'ayt_literature', 80, '{"tr": "Cumhuriyet dönemi"}'),
  ('ayt_lit_western_movements', 'ayt_literature', 90, '{"tr": "Batı edebiyatı akımları"}'),
  ('ayt_literature_other', 'ayt_literature', 999, '{"tr": "Diğer"}'),
  ('ayt_history_other', 'ayt_history', 999, '{"tr": "Diğer"}'),
  ('ayt_geography_other', 'ayt_geography', 999, '{"tr": "Diğer"}'),
  ('ayt_philosophy_other', 'ayt_philosophy', 999, '{"tr": "Diğer"}'),
  ('hs_math_9', 'hs_math', 10, '{"tr": "9. sınıf"}'),
  ('hs_math_10', 'hs_math', 20, '{"tr": "10. sınıf"}'),
  ('hs_math_11', 'hs_math', 30, '{"tr": "11. sınıf"}'),
  ('hs_math_12', 'hs_math', 40, '{"tr": "12. sınıf"}'),
  ('hs_math_other', 'hs_math', 999, '{"tr": "Diğer"}'),
  ('hs_physics_9', 'hs_physics', 10, '{"tr": "9. sınıf"}'),
  ('hs_physics_10', 'hs_physics', 20, '{"tr": "10. sınıf"}'),
  ('hs_physics_11', 'hs_physics', 30, '{"tr": "11. sınıf"}'),
  ('hs_physics_12', 'hs_physics', 40, '{"tr": "12. sınıf"}'),
  ('hs_physics_other', 'hs_physics', 999, '{"tr": "Diğer"}'),
  ('hs_chemistry_9', 'hs_chemistry', 10, '{"tr": "9. sınıf"}'),
  ('hs_chemistry_10', 'hs_chemistry', 20, '{"tr": "10. sınıf"}'),
  ('hs_chemistry_11', 'hs_chemistry', 30, '{"tr": "11. sınıf"}'),
  ('hs_chemistry_12', 'hs_chemistry', 40, '{"tr": "12. sınıf"}'),
  ('hs_chemistry_other', 'hs_chemistry', 999, '{"tr": "Diğer"}'),
  ('hs_biology_9', 'hs_biology', 10, '{"tr": "9. sınıf"}'),
  ('hs_biology_10', 'hs_biology', 20, '{"tr": "10. sınıf"}'),
  ('hs_biology_11', 'hs_biology', 30, '{"tr": "11. sınıf"}'),
  ('hs_biology_12', 'hs_biology', 40, '{"tr": "12. sınıf"}'),
  ('hs_biology_other', 'hs_biology', 999, '{"tr": "Diğer"}'),
  ('hs_literature_9', 'hs_literature', 10, '{"tr": "9. sınıf"}'),
  ('hs_literature_10', 'hs_literature', 20, '{"tr": "10. sınıf"}'),
  ('hs_literature_11', 'hs_literature', 30, '{"tr": "11. sınıf"}'),
  ('hs_literature_12', 'hs_literature', 40, '{"tr": "12. sınıf"}'),
  ('hs_literature_other', 'hs_literature', 999, '{"tr": "Diğer"}'),
  ('hs_history_9', 'hs_history', 10, '{"tr": "9. sınıf"}'),
  ('hs_history_10', 'hs_history', 20, '{"tr": "10. sınıf"}'),
  ('hs_history_11', 'hs_history', 30, '{"tr": "11. sınıf"}'),
  ('hs_history_12', 'hs_history', 40, '{"tr": "12. sınıf"}'),
  ('hs_history_other', 'hs_history', 999, '{"tr": "Diğer"}'),
  ('hs_geography_9', 'hs_geography', 10, '{"tr": "9. sınıf"}'),
  ('hs_geography_10', 'hs_geography', 20, '{"tr": "10. sınıf"}'),
  ('hs_geography_11', 'hs_geography', 30, '{"tr": "11. sınıf"}'),
  ('hs_geography_12', 'hs_geography', 40, '{"tr": "12. sınıf"}'),
  ('hs_geography_other', 'hs_geography', 999, '{"tr": "Diğer"}'),
  ('hs_philosophy_9', 'hs_philosophy', 10, '{"tr": "9. sınıf"}'),
  ('hs_philosophy_10', 'hs_philosophy', 20, '{"tr": "10. sınıf"}'),
  ('hs_philosophy_11', 'hs_philosophy', 30, '{"tr": "11. sınıf"}'),
  ('hs_philosophy_12', 'hs_philosophy', 40, '{"tr": "12. sınıf"}'),
  ('hs_philosophy_other', 'hs_philosophy', 999, '{"tr": "Diğer"}'),
  ('hs_english_9', 'hs_english', 10, '{"tr": "9. sınıf"}'),
  ('hs_english_10', 'hs_english', 20, '{"tr": "10. sınıf"}'),
  ('hs_english_11', 'hs_english', 30, '{"tr": "11. sınıf"}'),
  ('hs_english_12', 'hs_english', 40, '{"tr": "12. sınıf"}'),
  ('hs_english_other', 'hs_english', 999, '{"tr": "Diğer"}'),
  ('hs_german_9', 'hs_german', 10, '{"tr": "9. sınıf"}'),
  ('hs_german_10', 'hs_german', 20, '{"tr": "10. sınıf"}'),
  ('hs_german_11', 'hs_german', 30, '{"tr": "11. sınıf"}'),
  ('hs_german_12', 'hs_german', 40, '{"tr": "12. sınıf"}'),
  ('hs_german_other', 'hs_german', 999, '{"tr": "Diğer"}'),
  ('hs_religion_9', 'hs_religion', 10, '{"tr": "9. sınıf"}'),
  ('hs_religion_10', 'hs_religion', 20, '{"tr": "10. sınıf"}'),
  ('hs_religion_11', 'hs_religion', 30, '{"tr": "11. sınıf"}'),
  ('hs_religion_12', 'hs_religion', 40, '{"tr": "12. sınıf"}'),
  ('hs_religion_other', 'hs_religion', 999, '{"tr": "Diğer"}'),
  ('yds_en_grammar', 'yds_english', 10, '{"tr": "Grammar"}'),
  ('yds_en_vocabulary', 'yds_english', 20, '{"tr": "Vocabulary"}'),
  ('yds_en_reading', 'yds_english', 30, '{"tr": "Reading"}'),
  ('yds_en_cloze_test', 'yds_english', 40, '{"tr": "Cloze test"}'),
  ('yds_en_sentence_completion', 'yds_english', 50, '{"tr": "Sentence completion"}'),
  ('yds_en_translation', 'yds_english', 60, '{"tr": "Translation"}'),
  ('yds_en_paragraph_completion', 'yds_english', 70, '{"tr": "Paragraph completion"}'),
  ('yds_en_dialogue', 'yds_english', 80, '{"tr": "Dialogue"}'),
  ('yds_english_other', 'yds_english', 999, '{"tr": "Diğer"}')
ON CONFLICT (key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Rozetler.

INSERT INTO catalog_badges (key, scope, position, labels, icon, rule) VALUES
  ('first_attendance', 'user', 10, '{"tr": "İlk etkinlik"}', 'flag', '{"kind": "first"}'),
  ('attended_10', 'user', 20, '{"tr": "10 etkinlik"}', 'military_tech', '{"kind": "threshold", "metric": "attended", "value": 10, "period": "all_time"}'),
  ('attended_50', 'user', 30, '{"tr": "50 etkinlik"}', 'workspace_premium', '{"kind": "threshold", "metric": "attended", "value": 50, "period": "all_time"}'),
  ('attended_100', 'user', 40, '{"tr": "100 etkinlik"}', 'stars', '{"kind": "threshold", "metric": "attended", "value": 100, "period": "all_time"}'),
  ('championship', 'user', 50, '{"tr": "Şampiyon"}', 'emoji_events', '{"kind": "champion"}')
ON CONFLICT (key) DO NOTHING;

UPDATE catalog_meta SET catalog_version = catalog_version + 1;

-- +goose Down

DELETE FROM catalog_badges WHERE key IN ('first_attendance', 'attended_10', 'attended_50', 'attended_100', 'championship');

DELETE FROM catalog_study_tags WHERE key IN (
  'lgs', 'tyt', 'ayt', 'high_school', 'yds', 'other_exams',
  'lgs_turkish', 'lgs_math', 'lgs_science', 'lgs_history', 'lgs_religion', 'lgs_english',
  'lgs_other', 'tyt_turkish', 'tyt_math', 'tyt_geometry', 'tyt_physics', 'tyt_chemistry',
  'tyt_biology', 'tyt_history', 'tyt_geography', 'tyt_philosophy', 'tyt_religion', 'tyt_other',
  'ayt_math', 'ayt_physics', 'ayt_chemistry', 'ayt_biology', 'ayt_literature', 'ayt_history',
  'ayt_geography', 'ayt_philosophy', 'ayt_other', 'hs_math', 'hs_physics', 'hs_chemistry',
  'hs_biology', 'hs_literature', 'hs_history', 'hs_geography', 'hs_philosophy', 'hs_english',
  'hs_german', 'hs_religion', 'high_school_other', 'yds_english', 'yds_other', 'other_exams_other',
  'lgs_tr_word_meaning', 'lgs_tr_sentence_meaning', 'lgs_tr_paragraph', 'lgs_tr_verbals', 'lgs_tr_sentence_elements', 'lgs_tr_sentence_types',
  'lgs_tr_spelling', 'lgs_tr_punctuation', 'lgs_tr_figures_of_speech', 'lgs_tr_text_types', 'lgs_turkish_other', 'lgs_math_factors_multiples',
  'lgs_math_exponents', 'lgs_math_square_roots', 'lgs_math_data_analysis', 'lgs_math_probability', 'lgs_math_algebraic_expressions', 'lgs_math_linear_equations',
  'lgs_math_inequalities', 'lgs_math_triangles', 'lgs_math_similarity', 'lgs_math_transformations', 'lgs_math_solids', 'lgs_math_other',
  'lgs_sci_seasons_climate', 'lgs_sci_dna_genetic_code', 'lgs_sci_pressure', 'lgs_sci_matter_industry', 'lgs_sci_simple_machines', 'lgs_sci_energy_environment',
  'lgs_sci_electric_charges', 'lgs_science_other', 'lgs_hist_hero_born', 'lgs_hist_national_awakening', 'lgs_hist_national_epic', 'lgs_hist_modernizing_turkey',
  'lgs_hist_democratization', 'lgs_hist_foreign_policy', 'lgs_hist_after_ataturk', 'lgs_history_other', 'lgs_rel_destiny', 'lgs_rel_zakat_charity',
  'lgs_rel_religion_life', 'lgs_rel_prophet_example', 'lgs_rel_quran', 'lgs_religion_other', 'lgs_en_friendship', 'lgs_en_teen_life',
  'lgs_en_kitchen', 'lgs_en_phone', 'lgs_en_internet', 'lgs_en_adventures', 'lgs_en_tourism', 'lgs_en_chores',
  'lgs_en_science', 'lgs_en_natural_forces', 'lgs_english_other', 'tyt_tr_word_meaning', 'tyt_tr_sentence_meaning', 'tyt_tr_paragraph',
  'tyt_tr_phonetics', 'tyt_tr_spelling', 'tyt_tr_punctuation', 'tyt_tr_word_types', 'tyt_tr_sentence_elements', 'tyt_tr_expression_errors',
  'tyt_turkish_other', 'tyt_math_basic_concepts', 'tyt_math_digits', 'tyt_math_divisibility', 'tyt_math_gcd_lcm', 'tyt_math_rationals',
  'tyt_math_exponents_roots', 'tyt_math_factoring', 'tyt_math_ratio_proportion', 'tyt_math_word_problems', 'tyt_math_sets', 'tyt_math_functions',
  'tyt_math_combinatorics', 'tyt_math_statistics', 'tyt_math_other', 'tyt_geo_angles', 'tyt_geo_triangles', 'tyt_geo_polygons',
  'tyt_geo_circles', 'tyt_geo_solids', 'tyt_geo_analytic', 'tyt_geometry_other', 'tyt_phy_intro', 'tyt_phy_matter',
  'tyt_phy_motion_force', 'tyt_phy_energy', 'tyt_phy_heat', 'tyt_phy_electrostatics', 'tyt_phy_electromagnetism', 'tyt_phy_pressure_buoyancy',
  'tyt_phy_waves', 'tyt_phy_optics', 'tyt_physics_other', 'tyt_chem_science', 'tyt_chem_atom_periodic', 'tyt_chem_interactions',
  'tyt_chem_states', 'tyt_chem_nature', 'tyt_chem_laws', 'tyt_chem_mixtures', 'tyt_chem_acids_bases', 'tyt_chem_everywhere',
  'tyt_chemistry_other', 'tyt_bio_living_things', 'tyt_bio_basic_components', 'tyt_bio_cell', 'tyt_bio_classification', 'tyt_bio_cell_division',
  'tyt_bio_heredity', 'tyt_bio_ecosystem', 'tyt_biology_other', 'tyt_history_other', 'tyt_geography_other', 'tyt_philosophy_other',
  'tyt_religion_other', 'ayt_math_polynomials', 'ayt_math_quadratics', 'ayt_math_parabola', 'ayt_math_complex_numbers', 'ayt_math_logarithm',
  'ayt_math_sequences', 'ayt_math_trigonometry', 'ayt_math_limits', 'ayt_math_derivative', 'ayt_math_integral', 'ayt_math_other',
  'ayt_phy_vectors', 'ayt_phy_force_motion', 'ayt_phy_projectiles', 'ayt_phy_work_energy', 'ayt_phy_momentum', 'ayt_phy_torque',
  'ayt_phy_circular_motion', 'ayt_phy_harmonic_motion', 'ayt_phy_electromagnetism', 'ayt_phy_wave_mechanics', 'ayt_phy_modern_physics', 'ayt_physics_other',
  'ayt_chem_atom_theory', 'ayt_chem_gases', 'ayt_chem_solutions', 'ayt_chem_reaction_energy', 'ayt_chem_reaction_rates', 'ayt_chem_equilibrium',
  'ayt_chem_acid_base_equilibrium', 'ayt_chem_solubility', 'ayt_chem_electrochemistry', 'ayt_chem_organic', 'ayt_chemistry_other', 'ayt_bio_nervous',
  'ayt_bio_endocrine', 'ayt_bio_senses', 'ayt_bio_support_movement', 'ayt_bio_digestion', 'ayt_bio_circulation', 'ayt_bio_respiration',
  'ayt_bio_excretion', 'ayt_bio_reproduction', 'ayt_bio_gene_protein', 'ayt_bio_photosynthesis', 'ayt_bio_plants', 'ayt_bio_community_ecology',
  'ayt_biology_other', 'ayt_lit_poetry', 'ayt_lit_literary_arts', 'ayt_lit_divan', 'ayt_lit_folk', 'ayt_lit_tanzimat',
  'ayt_lit_servet_i_funun', 'ayt_lit_national', 'ayt_lit_republic', 'ayt_lit_western_movements', 'ayt_literature_other', 'ayt_history_other',
  'ayt_geography_other', 'ayt_philosophy_other', 'hs_math_9', 'hs_math_10', 'hs_math_11', 'hs_math_12',
  'hs_math_other', 'hs_physics_9', 'hs_physics_10', 'hs_physics_11', 'hs_physics_12', 'hs_physics_other',
  'hs_chemistry_9', 'hs_chemistry_10', 'hs_chemistry_11', 'hs_chemistry_12', 'hs_chemistry_other', 'hs_biology_9',
  'hs_biology_10', 'hs_biology_11', 'hs_biology_12', 'hs_biology_other', 'hs_literature_9', 'hs_literature_10',
  'hs_literature_11', 'hs_literature_12', 'hs_literature_other', 'hs_history_9', 'hs_history_10', 'hs_history_11',
  'hs_history_12', 'hs_history_other', 'hs_geography_9', 'hs_geography_10', 'hs_geography_11', 'hs_geography_12',
  'hs_geography_other', 'hs_philosophy_9', 'hs_philosophy_10', 'hs_philosophy_11', 'hs_philosophy_12', 'hs_philosophy_other',
  'hs_english_9', 'hs_english_10', 'hs_english_11', 'hs_english_12', 'hs_english_other', 'hs_german_9',
  'hs_german_10', 'hs_german_11', 'hs_german_12', 'hs_german_other', 'hs_religion_9', 'hs_religion_10',
  'hs_religion_11', 'hs_religion_12', 'hs_religion_other', 'yds_en_grammar', 'yds_en_vocabulary', 'yds_en_reading',
  'yds_en_cloze_test', 'yds_en_sentence_completion', 'yds_en_translation', 'yds_en_paragraph_completion', 'yds_en_dialogue', 'yds_english_other'
);

DELETE FROM catalog_event_type_versions
WHERE type_key IN ('moto_tour', 'car_meet', 'auto_show', 'maintenance_workshop', 'summit', 'climbing', 'birding', 'stargazing', 'diving', 'paddling', 'sailing', 'team_sport', 'running', 'cycling', 'racket', 'swimming', 'training', 'yoga', 'board_game_night', 'quiz_night', 'chess', 'esports', 'games_other', 'trash_pickup', 'tree_planting', 'blood_drive', 'aid_drive', 'visit', 'shelter_volunteering', 'dog_walk', 'stray_feeding', 'book_club', 'reading_marathon', 'book_swap', 'writing_workshop', 'art_workshop', 'photo_walk', 'museum_visit', 'music_rehearsal', 'choir', 'show_outing', 'talk', 'language_exchange', 'hackathon', 'city_tour', 'day_trip', 'heritage_site', 'festival', 'potluck', 'cooking_class', 'food_tour', 'study_circle', 'group_reading', 'group_visit', 'study_group', 'problem_session', 'peer_teaching', 'mock_exam');
DELETE FROM catalog_event_types WHERE key IN ('moto_tour', 'car_meet', 'auto_show', 'maintenance_workshop', 'summit', 'climbing', 'birding', 'stargazing', 'diving', 'paddling', 'sailing', 'team_sport', 'running', 'cycling', 'racket', 'swimming', 'training', 'yoga', 'board_game_night', 'quiz_night', 'chess', 'esports', 'games_other', 'trash_pickup', 'tree_planting', 'blood_drive', 'aid_drive', 'visit', 'shelter_volunteering', 'dog_walk', 'stray_feeding', 'book_club', 'reading_marathon', 'book_swap', 'writing_workshop', 'art_workshop', 'photo_walk', 'museum_visit', 'music_rehearsal', 'choir', 'show_outing', 'talk', 'language_exchange', 'hackathon', 'city_tour', 'day_trip', 'heritage_site', 'festival', 'potluck', 'cooking_class', 'food_tour', 'study_circle', 'group_reading', 'group_visit', 'study_group', 'problem_session', 'peer_teaching', 'mock_exam');

UPDATE catalog_event_types AS t
SET version = v.version, labels = v.labels, icon = v.icon, modes = v.modes, definition = v.definition
FROM catalog_event_type_versions AS v
WHERE v.type_key = t.key AND v.version = 1 AND t.version = 2
  AND t.key IN ('convoy', 'hike', 'camp', 'sports', 'gaming', 'general');
DELETE FROM catalog_event_type_versions WHERE version = 2 AND type_key IN ('convoy', 'hike', 'camp', 'sports', 'gaming', 'general');

UPDATE catalog_event_types AS t SET position = p.position
FROM (VALUES ('convoy', 10), ('hike', 20), ('camp', 30), ('fishing', 40), ('sports', 50), ('gaming', 60), ('general', 900)) AS p (key, position)
WHERE t.key = p.key;

DELETE FROM catalog_interests WHERE key IN ('environment', 'animals', 'books', 'arts', 'learning', 'travel', 'food', 'faith', 'education');
UPDATE catalog_interests SET default_widgets = '{}' WHERE key IN ('automotive', 'nature', 'water', 'sports', 'games', 'general');

UPDATE catalog_meta SET catalog_version = catalog_version + 1;
