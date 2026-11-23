-- core'daki etkinlik türü kataloğunun identity'deki kopyası (issue #237;
-- ADR-0025 §10, ADR-0009 kopya modeli). Kopya yalnızca core'un
-- catalog.changed olayıyla değişir (issue #280, ADR-0042; catalogcopy
-- paketi): sürüm yalnızca ileri gider, ilgi alanı silinmez.

-- Profil ilgi alanlarının doğrulaması için: anahtar ve durum (emekliler
-- dahil).
-- name: ListCatalogInterests :many
SELECT key, status FROM catalog_interests;

-- Kopyanın yansıttığı katalog sürümü (core'daki catalog_version).
-- name: GetCatalogCopyVersion :one
SELECT catalog_version FROM catalog_copy;

-- Kopyanın sürümünü yalnızca daha yeni bir sürüme taşır; eşit ya da eski
-- sürümde satır dönmez (olay zaten uygulanmış ya da geç gelmiş).
-- name: AdvanceCatalogCopyVersion :execrows
UPDATE catalog_copy SET catalog_version = @catalog_version, updated_at = now()
WHERE catalog_version < @catalog_version;

-- Bir ilgi alanını ekler ya da durumunu günceller.
-- name: UpsertCatalogInterest :exec
INSERT INTO catalog_interests (key, status) VALUES (@key, @status)
ON CONFLICT (key) DO UPDATE SET status = EXCLUDED.status
WHERE catalog_interests.status <> EXCLUDED.status;

-- Kataloğun listesinde olmayan ilgi alanlarını emekliye alır (silmez).
-- name: RetireMissingCatalogInterests :execrows
UPDATE catalog_interests SET status = 'retired'
WHERE status <> 'retired' AND NOT (key = ANY(@keys::text[]));
