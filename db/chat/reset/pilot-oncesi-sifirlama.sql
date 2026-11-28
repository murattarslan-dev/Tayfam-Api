-- Pilot öncesi sıfırlama: chat veritabanı (issue #369, ADR-0046,
-- docs/runbooks/pilot-oncesi-sifirlama.md).
--
-- Bütün kanalları, mesajları, birebir sohbetleri, E2E anahtarlarını ve
-- zarflarını, bildirimleri, push token'larını ve outbox'ı siler; şemayı ve
-- migration geçmişini korur. chat'te yapılandırma tablosu yoktur. Migration
-- değildir: goose bu dizini okumaz, servis ikilisine gömülmez. Platform
-- sahibi Postgres pod'unda, chat durdurulmuşken psql ile çalıştırır:
--
--   kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- \
--     env PGOPTIONS='-c tayfam.mod=kuru' \
--     psql -X -q -v ON_ERROR_STOP=1 -d chat < db/chat/reset/pilot-oncesi-sifirlama.sql
--
-- Ayarlar ve kilitler identity betiğindekilerle aynıdır (tayfam.mod kuru|sil,
-- tayfam.ortam, tayfam.onay; veritabanı sahibi chat; tablo sınıflandırması;
-- bağlı servis yok). Eşik: push token'ı ya da E2E cihaz anahtarı olan farklı
-- kullanıcı sayısı. Demo hesaplarının cihazı yoktur (ADR-0023 §3); bu sayı
-- yalnızca uygulamaya gerçekten giren kişileri sayar.
--
-- Tek DO bloğu, tek transaction. Çıktı yalnızca tablo adları ve sayılardır.

DO $sifirla$
DECLARE
  -- Bu betiğin ait olduğu servis: veritabanının sahibi bu rol olmalıdır.
  servis constant text := 'chat';
  -- Cihazı olan farklı kullanıcı: platform sahibi + en çok 5 hesap.
  esik constant bigint := 6;

  -- Tamamı silinen tablolar (TRUNCATE, aralarındaki FK'ler birlikte).
  silinen constant text[] := ARRAY[
    'channel_members', 'channel_messages', 'channel_reads', 'channels',
    'device_keys', 'device_link_requests', 'device_one_time_prekeys',
    'dm_archive_entries', 'dm_conversation_reads', 'dm_conversations',
    'dm_envelopes', 'dm_messages', 'domain_events', 'e2e_ended_sessions',
    'event_consumers', 'history_key_shares', 'notification_preferences',
    'notifications', 'push_queue', 'push_tokens', 'user_e2e_state',
    'user_key_events', 'user_profiles'
  ];
  -- Dokunulmayan tablolar.
  korunan constant text[] := ARRAY[
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

  SELECT count(*) INTO gercek FROM (
    SELECT user_id FROM push_tokens UNION SELECT user_id FROM device_keys) x;

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
  RAISE NOTICE 'gerçek kullanıcı (push token ya da cihaz anahtarı olan): % (eşik %)%', gercek, esik,
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
    RAISE EXCEPTION 'gerçek kullanıcı sayısı (%) eşiği (%) geçiyor: pilot başlamış sayılır, sıfırlama reddedildi', gercek, esik
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

  -- chat'te TRUNCATE yasağı olan tablo yoktur; ileride eklenirse bu
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
