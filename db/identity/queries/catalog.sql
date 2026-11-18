-- core'daki etkinlik türü kataloğunun identity'deki salt okunur kopyası
-- (issue #237; ADR-0025 §10, ADR-0009 kopya modeli). identity_app yalnızca
-- okur; kopya migration'la gelir.

-- Profil ilgi alanlarının doğrulaması için: anahtar ve durum (emekliler
-- dahil).
-- name: ListCatalogInterests :many
SELECT key, status FROM catalog_interests;

-- Kopyanın yansıttığı katalog sürümü (core'daki catalog_version).
-- name: GetCatalogCopyVersion :one
SELECT catalog_version FROM catalog_copy;
