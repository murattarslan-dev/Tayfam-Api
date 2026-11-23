-- Platform panelinde katalog yönetimi (issue #262; ADR-0025 §7, ADR-0039
-- §1, ADR-0040 §3 ve §5, ADR-0042 §5).
--
-- Platform paneli ilgi alanlarını, türleri, rozetleri, ders/konu
-- etiketlerini, yazboz ön ayarlarını ve engelli alan adlarını kod
-- değiştirmeden yönetir. Panel core'a admin token'ıyla gRPC üzerinden yazar
-- (AdminService, internal/core/app/admin/catalog.go); core_app şimdiye kadar
-- bu tabloları yalnızca okuyordu (00030, 00045, 00052, 00053, 00063). Bu
-- migration panelin gerektirdiği yazma yetkisini en dar haliyle açar
-- (ADR-0015, en az yetki):
--
--   catalog_meta                 UPDATE (catalog_version, updated_at)
--   catalog_interests            INSERT, UPDATE (anahtar hariç)
--   catalog_event_types          INSERT, UPDATE (anahtar hariç)
--   catalog_event_type_versions  yalnızca INSERT: sürüm satırı değişmez ve
--                                silinmez (00030)
--   catalog_study_tags           INSERT, UPDATE (anahtar ve üst etiket hariç)
--   catalog_badges               INSERT, UPDATE (anahtar ve kapsam hariç;
--                                kural türü ve dönemi 00053'teki tetikleyici
--                                korur)
--   catalog_score_presets        INSERT, UPDATE (anahtar hariç)
--   blocked_domains              INSERT, DELETE (listeden çıkarma silmedir;
--                                materyal silinmez, yeniden görünür)
--
-- Hiçbir katalog tablosunda DELETE yoktur: anahtar değişmez, kayıt
-- silinmez, emekliye ayrılır (ADR-0025 §2).
--
-- İkinci savunma hattı (ADR-0011): servis ele geçirilse bile
--   * kataloğun sürümü (catalog_meta.catalog_version) ve bir türün güncel
--     sürümü (catalog_event_types.version) geri gidemez; bu yüzden eski bir
--     sürüme dönülüp etkinliklerin bağlı olduğu tanım değiştirilemez.
-- Ele geçirilmiş bir core'un katalogla yapabileceği en kötü şey anlamsız ya
-- da emekli bir kayıttır; eski etkinliklerin tanımı (sürüm satırları)
-- değişmez.
--
-- Veri taşınmaz ve silinmez; katalog değişmez (catalog_version artmaz).
--
-- Geri alma planı: Down yetkileri ve tetikleyicileri geri alır; panelden
-- yazılmış katalog kayıtları yerinde kalır. Uygulama kodu da birlikte geri
-- alınmalıdır (panel yazamaz hale gelir).

-- +goose Up

GRANT UPDATE (catalog_version, updated_at) ON catalog_meta TO core_app;
GRANT INSERT, UPDATE (position, labels, icon, status, default_widgets, updated_at) ON catalog_interests TO core_app;
GRANT INSERT, UPDATE (interest_keys, position, labels, icon, modes, status, version, definition, updated_at)
  ON catalog_event_types TO core_app;
GRANT INSERT ON catalog_event_type_versions TO core_app;
GRANT INSERT, UPDATE (position, labels, status, updated_at) ON catalog_study_tags TO core_app;
GRANT INSERT, UPDATE (position, labels, icon, rule, status, updated_at) ON catalog_badges TO core_app;
GRANT INSERT, UPDATE (position, labels, params, status, updated_at) ON catalog_score_presets TO core_app;
GRANT INSERT, DELETE ON blocked_domains TO core_app;

-- Sürümler yalnızca ileri gider.
-- +goose StatementBegin
CREATE FUNCTION catalog_meta_forward_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.catalog_version < OLD.catalog_version THEN
    RAISE EXCEPTION 'katalog sürümü geri gidemez'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_meta_forward_only BEFORE UPDATE ON catalog_meta
  FOR EACH ROW EXECUTE FUNCTION catalog_meta_forward_only();

-- +goose StatementBegin
CREATE FUNCTION catalog_event_types_version_forward() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.version < OLD.version THEN
    RAISE EXCEPTION 'türün sürümü geri gidemez'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER catalog_event_types_version_forward BEFORE UPDATE ON catalog_event_types
  FOR EACH ROW EXECUTE FUNCTION catalog_event_types_version_forward();

-- +goose Down
DROP TRIGGER catalog_event_types_version_forward ON catalog_event_types;
DROP FUNCTION catalog_event_types_version_forward();
DROP TRIGGER catalog_meta_forward_only ON catalog_meta;
DROP FUNCTION catalog_meta_forward_only();
REVOKE DELETE, INSERT ON blocked_domains FROM core_app;
REVOKE UPDATE (position, labels, params, status, updated_at) ON catalog_score_presets FROM core_app;
REVOKE INSERT ON catalog_score_presets FROM core_app;
REVOKE UPDATE (position, labels, icon, rule, status, updated_at) ON catalog_badges FROM core_app;
REVOKE INSERT ON catalog_badges FROM core_app;
REVOKE UPDATE (position, labels, status, updated_at) ON catalog_study_tags FROM core_app;
REVOKE INSERT ON catalog_study_tags FROM core_app;
REVOKE INSERT ON catalog_event_type_versions FROM core_app;
REVOKE UPDATE (interest_keys, position, labels, icon, modes, status, version, definition, updated_at)
  ON catalog_event_types FROM core_app;
REVOKE INSERT ON catalog_event_types FROM core_app;
REVOKE UPDATE (position, labels, icon, status, default_widgets, updated_at) ON catalog_interests FROM core_app;
REVOKE INSERT ON catalog_interests FROM core_app;
REVOKE UPDATE (catalog_version, updated_at) ON catalog_meta FROM core_app;
