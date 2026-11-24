-- Katalog kopyası core'un catalog.changed olayıyla güncellenir (issue #280,
-- ADR-0042; ADR-0025 §10, ADR-0009 "Katalog kopyası").
--
-- 00014'te kopya (catalog_copy, catalog_interests) migration'la gelen, salt
-- okunur bir tablo çiftiydi ve kataloğu değiştiren her core migration'ı
-- eşlenik bir identity migration'ı gerektiriyordu. Artık identity kopyayı
-- CatalogCopyService.ApplyCatalogChange ile kendisi günceller; bu geçici kural
-- bu migration'la kalkar.
--
-- Yetki (ADR-0015, en az yetki): identity_app yalnızca gerekeni yazar:
--
--   catalog_copy       UPDATE (catalog_version, updated_at)
--   catalog_interests  INSERT, UPDATE (status)
--
-- Silme ve anahtar değiştirme yetkisi verilmez: bir ilgi alanı kopyadan
-- silinmez, emekliye alınır (profiller onu göstermeye devam eder).
-- catalog_copy_forward_only tetikleyicisi sürümün geri gitmesini reddeder:
-- servis ele geçirilse bile kopya eski bir sürüme döndürülemez (ADR-0011).
--
-- Veri taşınmaz ve silinmez. Kopya ilk catalog.changed olayıyla (core'un
-- catalog-publish işi) core'un güncel sürümüne gelir.
--
-- Geri alma planı: Down yetkileri ve tetikleyiciyi kaldırır; kopya o anki
-- haliyle kalır. Uygulama koduyla birlikte geri alınmalıdır (aksi halde
-- ApplyCatalogChange yetki hatası alır ve olay outbox'ta bekler).

-- +goose Up

GRANT UPDATE (catalog_version, updated_at) ON catalog_copy TO identity_app;
GRANT INSERT, UPDATE (status) ON catalog_interests TO identity_app;

-- +goose StatementBegin
CREATE FUNCTION catalog_copy_forward_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.catalog_version < OLD.catalog_version THEN
    RAISE EXCEPTION 'catalog_copy: sürüm geri gidemez'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'catalog_copy_forward_only';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_copy_forward_only BEFORE UPDATE ON catalog_copy
  FOR EACH ROW EXECUTE FUNCTION catalog_copy_forward_only();

-- +goose Down

DROP TRIGGER catalog_copy_forward_only ON catalog_copy;
DROP FUNCTION catalog_copy_forward_only();
REVOKE INSERT, UPDATE (status) ON catalog_interests FROM identity_app;
REVOKE UPDATE (catalog_version, updated_at) ON catalog_copy FROM identity_app;
