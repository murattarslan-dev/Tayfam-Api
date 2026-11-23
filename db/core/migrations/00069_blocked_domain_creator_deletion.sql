-- Hesap silmede engelli alan adını ekleyen boşalır (issue #334; ADR-0021,
-- ADR-0040 §3, Notion "Veri Modeli (v15)" hesap silme: anonimleştirme).
--
-- blocked_domains.created_by, alan adını panelden ekleyen platform
-- yöneticisidir (#262). Yönetici hesabını silince alan adı listede kalır
-- (platformun kuralıdır), ekleyen boşalır: hesap silme bu kolonu NULL'a
-- çeker (account_deletion.sql, ClearBlockedDomainCreator). Bunun için
-- core_app'e yalnızca bu kolonda UPDATE verilir; alan adı yine
-- değiştirilemez (ADR-0015, en az yetki).
--
-- Panel "başlangıç listesinden mi" bilgisini şimdiye kadar
-- created_by IS NULL'dan çıkarıyordu; silinen yöneticinin eklediği alan adı
-- da NULL olacağı için bu bilgi kendi kolonuna (initial) taşınır. Mevcut
-- satırlarda initial = (created_by IS NULL) ile doldurulur; yeni satırlar
-- panelden gelir (varsayılan false).
--
-- Veri silinmez; yalnızca yeni kolon doldurulur.
--
-- Geri alma planı: Down yetkiyi geri alır ve initial kolonunu siler. Bu
-- migration'dan sonra hesabı silinen yöneticilerin eklediği alan adları
-- geri alındıktan sonra panelde başlangıç listesinden görünür (created_by
-- NULL). Uygulama kodu da birlikte geri alınmalıdır.

-- +goose Up

ALTER TABLE blocked_domains ADD COLUMN initial boolean NOT NULL DEFAULT false;
UPDATE blocked_domains SET initial = (created_by IS NULL);

GRANT UPDATE (created_by) ON blocked_domains TO core_app;

-- +goose Down
REVOKE UPDATE (created_by) ON blocked_domains FROM core_app;
ALTER TABLE blocked_domains DROP COLUMN initial;
