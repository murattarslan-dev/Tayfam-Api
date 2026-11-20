-- Tayfa ana sayfa bileşenleri düzeni (issue #268; ADR-0027 §1–§4,
-- ADR-0040; Notion "Faz 3 — Katalog ayrıntıları ve şema ekleri
-- (2026-11-18)").
--
--   tayfa_home_widgets  Tayfa başına bileşen başına bir satır: açık mı,
--                       sırası, ayarları (config). "Sıradaki" kartı
--                       tabloda yoktur; istemci her zaman en üstte
--                       gösterir.
--
-- Kurallar (ADR-0027 §2–§4):
--   * Bileşen listesi koddadır (internal/core/app/home): anahtarın burada
--     yalnızca biçimi denetlenir, liste CHECK'te tutulmaz; böylece her
--     bileşen (günün içeriği, kütüphane, …) kendi iş kaydında yalnızca koda
--     bir satır ekler, migration gerekmez. Kodun tanımadığı anahtarın
--     satırı yok sayılır.
--   * config bileşene göre kapalı şemadır ve Go'da doğrulanır; burada
--     yalnızca nesne olması ve boyutu denetlenir.
--   * Kimin değiştirdiği satırda tutulmaz (ADR-0011, tutmadığın veri
--     çalınamaz): değişiklik denetim kaydına yazılır (tayfa.home_updated).
--     Satırda kişisel veri yoktur; KVKK dışa aktarmasına ve hesap silmeye
--     girmez, tayfayla gider.
--   * Varsayılanlar: tayfa oluşturulurken (ve anlık tayfa dönüştürülürken)
--     bugünkü sabit bölümler ile ilgi alanlarının varsayılan bileşenleri
--     (catalog_interests.default_widgets), ilgi alanı eklendiğinde yalnızca
--     onun varsayılanları, satırı yoksa açık olarak eklenir. Var olan
--     satıra dokunulmaz.
--
-- Veri taşıma: bugünkü sabit bölümler (Yaklaşan, Duyurular, Açık anket,
-- Son sorular; Tayfam-App ADR-0015) her standart tayfaya bugünkü sırasıyla
-- açık satır olarak eklenir. Anlık tayfanın ana sayfası yoktur; "Bu ekiple
-- tayfa kur" ile dönüştürülünce satırları eklenir. Veri silinmez; katalog
-- değişmez (catalog_version artmaz).
--
-- Geri alma planı: Down tabloyu kaldırır; yöneticilerin düzenlediği sıra
-- ve ayarlar gider (ana sayfa bugünkü sabit bölümlere döner). Uygulama
-- kodu da birlikte geri alınmalıdır.

-- +goose Up

CREATE TABLE tayfa_home_widgets (
  tayfa_id   uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  widget_key text NOT NULL,
  enabled    boolean NOT NULL,
  -- Küçükten büyüğe; "Sıradaki" kartından sonra.
  position   integer NOT NULL,
  -- Bileşene göre kapalı şema (ADR-0027 §4); ayarı olmayan bileşende NULL.
  config     jsonb,
  updated_at timestamptz NOT NULL,

  PRIMARY KEY (tayfa_id, widget_key),
  CONSTRAINT tayfa_home_widgets_key CHECK (widget_key ~ '^[a-z][a-z0-9_]{1,39}$'),
  CONSTRAINT tayfa_home_widgets_position CHECK (position BETWEEN 0 AND 999),
  CONSTRAINT tayfa_home_widgets_config CHECK (
    config IS NULL OR (jsonb_typeof(config) = 'object' AND octet_length(config::text) <= 2048))
);

-- Düzen bütün liste olarak yazılır (satır eklenir ya da güncellenir);
-- satır silinmez, tayfa silinince cascade ile gider.
GRANT SELECT, INSERT, UPDATE (enabled, position, config, updated_at) ON tayfa_home_widgets TO core_app;

INSERT INTO tayfa_home_widgets (tayfa_id, widget_key, enabled, position, updated_at)
SELECT t.id, s.widget_key, true, s.position, now()
FROM tayfas t
CROSS JOIN (VALUES ('upcoming', 0), ('announcements', 1), ('polls', 2), ('questions', 3)) AS s (widget_key, position)
WHERE t.kind = 'standard';

-- +goose Down

DROP TABLE tayfa_home_widgets;
