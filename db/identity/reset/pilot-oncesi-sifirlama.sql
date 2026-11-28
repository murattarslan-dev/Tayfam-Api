-- Pilot öncesi sıfırlama: identity veritabanı (issue #369, ADR-0046,
-- docs/runbooks/pilot-oncesi-sifirlama.md).
--
-- Bütün kullanıcıları ve kullanıcıya bağlı veriyi siler; şemayı, migration
-- geçmişini ve katalog kopyasını korur. Migration değildir: goose bu dizini
-- okumaz, servis ikilisine gömülmez. Platform sahibi Postgres pod'unda,
-- servisler durdurulmuşken psql ile bir kez çalıştırır:
--
--   kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- \
--     env PGOPTIONS='-c tayfam.mod=kuru' \
--     psql -X -q -v ON_ERROR_STOP=1 -d identity < db/identity/reset/pilot-oncesi-sifirlama.sql
--
-- Ayarlar (PGOPTIONS ile; değer verilmezse hiçbir şey yapmaz):
--   tayfam.mod    kuru: yalnızca sayar | sil: siler
--   tayfam.ortam  sil için zorunlu: dev | prod
--   tayfam.onay   sil için zorunlu: ortamın büyük harfle adı + "-SIFIRLA"
--                 (PROD-SIFIRLA, DEV-SIFIRLA)
--
-- Kilitler (sil): mod, ortam ve onay ifadesi birebir; veritabanının sahibi
-- identity olmalı (başka veritabanında çalışmaz); her tablo aşağıdaki iki
-- listeden birinde olmalı (yeni bir tablo betik güncellenmeden silinmez ya da
-- unutulmaz); gerçek hesap sayısı eşiği geçmemeli (ilk gerçek kullanıcılardan
-- sonra betik kendini kapatır); identity ya da identity_app ile bağlı oturum
-- olmamalı (servis ölçeklenmiş olmalı).
--
-- Tek DO bloğu, tek transaction: herhangi bir adım hata verirse hiçbir şey
-- silinmez. Çıktı yalnızca tablo adları ve satır sayılarıdır; kişisel veri,
-- kimlik ya da e-posta yazılmaz.

DO $sifirla$
DECLARE
  -- Bu betiğin ait olduğu servis: veritabanının sahibi bu rol olmalıdır.
  servis constant text := 'identity';
  -- Gerçek hesap eşiği: demo hesabı ve platform yöneticisi olmayan, silinmemiş
  -- hesap sayısı bunu geçerse betik silmeyi reddeder.
  esik constant bigint := 5;

  -- Tamamı silinen tablolar (TRUNCATE, aralarındaki FK'ler birlikte).
  silinen constant text[] := ARRAY[
    'auth_identities',       -- Google/Apple/demo giriş kimlikleri
    'consent_records',       -- onay kayıtları
    'data_export_requests',  -- KVKK dışa aktarma talepleri
    'devices',               -- cihazlar ve bütünlük kayıtları
    'display_name_changes',  -- görünen ad değişiklik geçmişi
    'domain_events',         -- outbox (user.* olayları)
    'event_consumers',       -- outbox tüketici konumları
    'platform_admins',       -- users'a FK: kullanıcı silinince anlamsız; yeniden eklenir
    'platform_audit_logs',   -- panel denetim kaydı (silinen hesaplara bağlı)
    'platform_sanctions',    -- platform yaptırımları
    'profile_details',       -- profil ayrıntıları
    'ref_code_lookups',      -- referans kodu sorgu kayıtları
    'refresh_tokens',        -- yenileme token'ları
    'sessions',              -- oturumlar
    'user_social_accounts',  -- sosyal hesap bağlantıları
    'users'                  -- hesaplar
  ];
  -- Dokunulmayan tablolar.
  korunan constant text[] := ARRAY[
    'catalog_copy',          -- core kataloğunun sürüm kopyası (ADR-0042)
    'catalog_interests',     -- core kataloğundaki ilgi alanlarının kopyası
    'goose_db_version'       -- migration geçmişi
  ];

  mod    text := coalesce(current_setting('tayfam.mod', true), '');
  ortam  text := coalesce(current_setting('tayfam.ortam', true), '');
  onay   text := coalesce(current_setting('tayfam.onay', true), '');
  sahip  text;
  tablolar text[];
  fark   text[];
  t      text;
  n      bigint;
  toplam bigint := 0;
  gercek bigint;
  bagli  bigint;
  tr     record;
  kapali text[] := '{}';
BEGIN
  IF mod NOT IN ('kuru', 'sil') THEN
    RAISE EXCEPTION 'tayfam.mod kuru ya da sil olmalı (PGOPTIONS=''-c tayfam.mod=kuru'')'
      USING ERRCODE = 'TS001';
  END IF;

  SELECT pg_get_userbyid(datdba) INTO sahip FROM pg_database WHERE datname = current_database();
  IF sahip IS DISTINCT FROM servis THEN
    RAISE EXCEPTION 'bu betik % veritabanı içindir; bu veritabanının sahibi %', servis, sahip
      USING ERRCODE = 'TS003';
  END IF;

  -- Şemadaki her tablo tam olarak bir listede olmalı.
  SELECT coalesce(array_agg(c.relname::text ORDER BY c.relname), '{}') INTO tablolar
  FROM pg_class c
  JOIN pg_namespace ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
  WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition
    AND NOT EXISTS (SELECT 1 FROM pg_depend d
                    WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e');
  SELECT coalesce(array_agg(x ORDER BY x), '{}') INTO fark
  FROM unnest(tablolar) x WHERE NOT (x = ANY (silinen || korunan));
  IF cardinality(fark) > 0 THEN
    RAISE EXCEPTION 'sınıflandırılmamış tablo: %; betik güncellenmeden çalışmaz', array_to_string(fark, ', ')
      USING ERRCODE = 'TS004';
  END IF;
  SELECT coalesce(array_agg(x ORDER BY x), '{}') INTO fark
  FROM unnest(silinen || korunan) x WHERE NOT (x = ANY (tablolar));
  IF cardinality(fark) > 0 THEN
    RAISE EXCEPTION 'betikteki tablo şemada yok: %; betik güncellenmeden çalışmaz', array_to_string(fark, ', ')
      USING ERRCODE = 'TS004';
  END IF;

  -- Gerçek hesap: silinmemiş, demo kimliği olmayan, platform yöneticisi
  -- olmayan hesap.
  SELECT count(*) INTO gercek FROM users u
  WHERE u.deleted_at IS NULL
    AND NOT EXISTS (SELECT 1 FROM auth_identities a WHERE a.user_id = u.id AND a.provider = 'demo')
    AND NOT EXISTS (SELECT 1 FROM platform_admins p WHERE p.user_id = u.id);

  IF mod = 'kuru' THEN
    RAISE NOTICE '% (%): kuru çalışma, hiçbir şey silinmez', servis, current_database();
  ELSE
    RAISE NOTICE '% (%): sıfırlama, ortam %', servis, current_database(), ortam;
  END IF;
  FOREACH t IN ARRAY silinen LOOP
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    toplam := toplam + n;
    RAISE NOTICE 'silinecek  %  %', rpad(t, 34), n;
  END LOOP;
  FOREACH t IN ARRAY korunan LOOP
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    RAISE NOTICE 'korunur    %  %', rpad(t, 34), n;
  END LOOP;
  RAISE NOTICE 'toplam silinecek satır: %', toplam;
  RAISE NOTICE 'gerçek hesap (demo ve yönetici dışı, silinmemiş): % (eşik %)%', gercek, esik,
    CASE WHEN gercek > esik THEN ': EŞİK AŞILDI, sil reddedilir' ELSE '' END;

  IF mod = 'kuru' THEN
    RETURN;
  END IF;

  -- sil: kilitler.
  IF ortam NOT IN ('dev', 'prod') OR onay <> upper(ortam) || '-SIFIRLA' THEN
    RAISE EXCEPTION 'sil için tayfam.ortam (dev|prod) ve tayfam.onay (ör. PROD-SIFIRLA) birebir verilmeli'
      USING ERRCODE = 'TS002';
  END IF;
  IF gercek > esik THEN
    RAISE EXCEPTION 'gerçek hesap sayısı (%) eşiği (%) geçiyor: pilot başlamış sayılır, sıfırlama reddedildi', gercek, esik
      USING ERRCODE = 'TS005';
  END IF;
  SELECT count(*) INTO bagli FROM pg_stat_activity
  WHERE datname = current_database() AND pid <> pg_backend_pid()
    AND usename IN (servis, servis || '_app');
  IF bagli > 0 THEN
    RAISE EXCEPTION '% veritabanına bağlı % oturum var: önce servisi 0''a ölçekle', servis, bagli
      USING ERRCODE = 'TS006';
  END IF;

  -- Sahip rolüyle: süper kullanıcı yetkisi gerekmez.
  PERFORM set_config('lock_timeout', '5s', true);
  EXECUTE format('SET LOCAL ROLE %I', servis);

  -- Yalnızca eklenen tabloların TRUNCATE yasağı (platform_audit_logs) bu
  -- transaction içinde kapatılır ve sonunda yeniden açılır.
  FOR tr IN
    SELECT tg.tgname::text AS tg, c.relname::text AS tablo
    FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid
    JOIN pg_namespace ns ON ns.oid = c.relnamespace AND ns.nspname = 'public'
    WHERE NOT tg.tgisinternal AND tg.tgenabled <> 'D' AND (tg.tgtype & 32) <> 0
      AND c.relname = ANY (silinen)
  LOOP
    EXECUTE format('ALTER TABLE %I DISABLE TRIGGER %I', tr.tablo, tr.tg);
    kapali := kapali || ARRAY[tr.tablo, tr.tg];
  END LOOP;

  EXECUTE 'TRUNCATE TABLE ' || (SELECT string_agg(format('%I', x), ', ') FROM unnest(silinen) x);

  FOR i IN 1 .. cardinality(kapali) / 2 LOOP
    EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I', kapali[2 * i - 1], kapali[2 * i]);
  END LOOP;

  -- Doğrulama.
  FOREACH t IN ARRAY silinen LOOP
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    IF n <> 0 THEN
      RAISE EXCEPTION '% boşalmadı (% satır); hiçbir şey silinmedi', t, n USING ERRCODE = 'TS008';
    END IF;
  END LOOP;
  RAISE NOTICE 'sıfırlandı: % satır silindi', toplam;
END
$sifirla$;
