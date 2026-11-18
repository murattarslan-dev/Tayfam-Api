-- Tayfa ilgi alanları katalog ilgi alanı anahtarı tutar (issue #237;
-- ADR-0025 §2, §10; ADR-0041 §3).
--
-- tayfa_interests bugüne kadar etkinlik türü anahtarı tutuyordu (yedi türlük
-- CHECK listesi). İlgi alanı artık katalogdaki bir kayıttır
-- (catalog_interests); tablo interest_key tutar ve kataloğa foreign key ile
-- bağlanır. Katalogda olmayan ilgi alanı yazılamaz; ilgi alanı silinmediği
-- (yalnızca "retired" olduğu) için eski tayfalar onu göstermeye devam eder.
--
-- Veri taşıma (ADR-0025 §10; VERİ DEĞİŞTİREN migration): mevcut satırlar
-- türden ilgi alanına eşlenir:
--
--   convoy        → automotive
--   camp, hike    → nature
--   fishing       → water
--   sports        → sports
--   gaming        → games
--   general       → general
--
-- Aynı ilgi alanına düşen iki tür (camp + hike) tek satır olur; en eski
-- satırın created_at'i korunur. Tayfanın hangi türü seçtiği bilgisi bu
-- eşlemeyle kaybolur (ilgi alanı türden geniştir).
--
-- Yetki (ADR-0015): değişmez. core_app tabloda SELECT, INSERT, DELETE
-- yapar (00016); satır güncellenmez.
--
-- Geri alma planı: Down kolonu event_type'a geri çevirir: automotive →
-- convoy, nature → camp, water → fishing, sports → sports, games → gaming,
-- general → general. "nature" için camp / hike ayrımı geri gelmez (camp
-- yazılır). Altı Faz 1 ilgi alanı dışında bir ilgi alanı seçilmişse geri
-- alma CHECK'te durur (o satırlar önce elle ele alınmalıdır). Prod'daki veri
-- yalnızca demo ve platform sahibinin verisidir (ADR-0041 §3); yine de
-- uygulamadan önce veritabanı yedeği alınır (docs/runbooks).

-- +goose Up

ALTER TABLE tayfa_interests ADD COLUMN interest_key text;

UPDATE tayfa_interests SET interest_key = CASE event_type
  WHEN 'convoy'  THEN 'automotive'
  WHEN 'camp'    THEN 'nature'
  WHEN 'hike'    THEN 'nature'
  WHEN 'fishing' THEN 'water'
  WHEN 'sports'  THEN 'sports'
  WHEN 'gaming'  THEN 'games'
  WHEN 'general' THEN 'general'
END;

-- camp + hike aynı ilgi alanına düşer: en eski satır kalır.
DELETE FROM tayfa_interests a
USING tayfa_interests b
WHERE a.tayfa_id = b.tayfa_id
  AND a.interest_key = b.interest_key
  AND (a.created_at, a.event_type) > (b.created_at, b.event_type);

ALTER TABLE tayfa_interests DROP CONSTRAINT tayfa_interests_pkey;
ALTER TABLE tayfa_interests DROP CONSTRAINT tayfa_interests_event_type;
ALTER TABLE tayfa_interests DROP COLUMN event_type;
ALTER TABLE tayfa_interests ALTER COLUMN interest_key SET NOT NULL;
ALTER TABLE tayfa_interests ADD PRIMARY KEY (tayfa_id, interest_key);
ALTER TABLE tayfa_interests ADD CONSTRAINT tayfa_interests_interest_key_fkey
  FOREIGN KEY (interest_key) REFERENCES catalog_interests (key);

-- +goose Down

ALTER TABLE tayfa_interests ADD COLUMN event_type text;

UPDATE tayfa_interests SET event_type = CASE interest_key
  WHEN 'automotive' THEN 'convoy'
  WHEN 'nature'     THEN 'camp'
  WHEN 'water'      THEN 'fishing'
  WHEN 'sports'     THEN 'sports'
  WHEN 'games'      THEN 'gaming'
  WHEN 'general'    THEN 'general'
  -- Eşlemesi olmayan ilgi alanı aşağıdaki CHECK'te durur.
  ELSE interest_key
END;

ALTER TABLE tayfa_interests DROP CONSTRAINT tayfa_interests_interest_key_fkey;
ALTER TABLE tayfa_interests DROP CONSTRAINT tayfa_interests_pkey;
ALTER TABLE tayfa_interests DROP COLUMN interest_key;
ALTER TABLE tayfa_interests ALTER COLUMN event_type SET NOT NULL;
ALTER TABLE tayfa_interests ADD PRIMARY KEY (tayfa_id, event_type);
ALTER TABLE tayfa_interests ADD CONSTRAINT tayfa_interests_event_type
  CHECK (event_type IN ('convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general'));
