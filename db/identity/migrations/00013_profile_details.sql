-- Profil tamamlama formu (issue #186; Notion Proje Planı §4 madde 8,
-- platform sahibinin kararı 2026-11-13).
--
-- profile_details: kullanıcının kendini tanıttığı isteğe bağlı alanlar.
--   Satır yalnızca kullanıcı bir alanı doldurduğunda, reddettiğinde ya da
--   formu ertelediğinde açılır; hesap silinince hemen silinir. Değerler
--   core ve chat'e gönderilmez (profil kopyalarına girmez); başka
--   kullanıcılara yalnızca identity'nin ProfileService'i görünürlüğe göre
--   gösterir. Log'a, metriğe ve trace'e yazılmaz.
--
--   interests        etkinlik türlerinden (EventType) tekrarsız çoklu seçim
--   about            hakkımda, 1–160 karakter
--   occupation       meslek / uğraş, 1–60 karakter
--   team             tutulan takım, 1–60 karakter
--   languages        ISO 639-1 kodları, en fazla 5, tekrarsız
--   declined_fields  "Paylaşmak istemiyorum" denen alanlar; cevaplanmış
--                    sayılır. Bir alan hem dolu hem reddedilmiş olamaz.
--   about_visibility friends_and_mutual (varsayılan) / friends / none.
--                    Sosyal medya hesabı her durumda en fazla arkadaşlara
--                    görünür (Go'da uygulanır).
--   prompt_snoozed_until    "Daha sonra doldur": 7 gün
--   prompt_fields_version   ertelemenin yapıldığı alan sürümü; sunucudaki
--                           sürüm büyükse erteleme geçersizdir
--
-- user_social_accounts: platform başına bir kullanıcı adı. Yalnızca kullanıcı
--   adı saklanır, bağlantı istemcide platformun bilinen adresinden kurulur
--   (profile oltalama bağlantısı konamaz).

-- +goose Up
CREATE TABLE profile_details (
  user_id               uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  interests             text[] NOT NULL DEFAULT '{}',
  about                 text,
  occupation            text,
  team                  text,
  languages             text[] NOT NULL DEFAULT '{}',
  declined_fields       text[] NOT NULL DEFAULT '{}',
  about_visibility      text NOT NULL DEFAULT 'friends_and_mutual',
  prompt_snoozed_until  timestamptz,
  prompt_fields_version integer NOT NULL DEFAULT 0,
  updated_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT profile_details_interests CHECK (
    interests <@ ARRAY['convoy', 'camp', 'hike', 'fishing', 'gaming', 'sports', 'general']::text[]
    AND cardinality(interests) <= 7),
  CONSTRAINT profile_details_about_len CHECK (about IS NULL OR char_length(about) BETWEEN 1 AND 160),
  CONSTRAINT profile_details_occupation_len CHECK (occupation IS NULL OR char_length(occupation) BETWEEN 1 AND 60),
  CONSTRAINT profile_details_team_len CHECK (team IS NULL OR char_length(team) BETWEEN 1 AND 60),
  CONSTRAINT profile_details_languages CHECK (
    cardinality(languages) <= 5
    AND array_to_string(languages, ',') ~ '^([a-z]{2}(,[a-z]{2})*)?$'),
  CONSTRAINT profile_details_declined CHECK (
    declined_fields <@ ARRAY['interests', 'about', 'occupation', 'team', 'languages', 'social_accounts']::text[]),
  CONSTRAINT profile_details_declined_empty CHECK (
    (NOT 'interests' = ANY (declined_fields) OR cardinality(interests) = 0)
    AND (NOT 'about' = ANY (declined_fields) OR about IS NULL)
    AND (NOT 'occupation' = ANY (declined_fields) OR occupation IS NULL)
    AND (NOT 'team' = ANY (declined_fields) OR team IS NULL)
    AND (NOT 'languages' = ANY (declined_fields) OR cardinality(languages) = 0)),
  CONSTRAINT profile_details_about_visibility CHECK (about_visibility IN ('friends_and_mutual', 'friends', 'none')),
  CONSTRAINT profile_details_prompt_version CHECK (prompt_fields_version >= 0)
);

CREATE TABLE user_social_accounts (
  user_id  uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  platform text NOT NULL,
  username text NOT NULL,
  PRIMARY KEY (user_id, platform),
  CONSTRAINT user_social_accounts_platform CHECK (platform IN ('instagram', 'x', 'tiktok', 'youtube')),
  -- Yalnızca kullanıcı adı: harf, rakam, nokta, alt çizgi, tire; URL'ye
  -- gereken ":" ve "/" hiçbir platformda geçmez. Platforma özgü kural Go'da.
  CONSTRAINT user_social_accounts_username CHECK (username ~ '^[A-Za-z0-9._-]{1,30}$')
);

-- identity_app (ADR-0015): alanları günceller; sosyal hesapları silip yeniden
-- ekler (UPDATE gerekmez), hesap silmede ikisini de siler. Varsayılan yetki
-- (00008) SELECT ve INSERT'tir.
GRANT UPDATE, DELETE ON profile_details TO identity_app;
GRANT DELETE ON user_social_accounts TO identity_app;

-- +goose Down
DROP TABLE user_social_accounts;
DROP TABLE profile_details;
