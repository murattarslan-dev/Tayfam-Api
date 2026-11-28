-- Pilot öncesi sıfırlama: core veritabanı (issue #369, ADR-0046,
-- docs/runbooks/pilot-oncesi-sifirlama.md).
--
-- Bütün tayfaları, içeriği, sosyal ilişkileri, şikâyetleri ve yükleme
-- kayıtlarını siler; şemayı, migration geçmişini, kataloğu, engelli alan
-- adlarını ve iş çalışma kayıtlarını korur. Migration değildir: goose bu
-- dizini okumaz, servis ikilisine gömülmez. Platform sahibi Postgres
-- pod'unda psql ile çalıştırır:
--
--   kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- \
--     env PGOPTIONS='-c tayfam.mod=kuru' \
--     psql -X -q -v ON_ERROR_STOP=1 -d core < db/core/reset/pilot-oncesi-sifirlama.sql
--
-- Ayarlar (PGOPTIONS ile; değer verilmezse hiçbir şey yapmaz):
--   tayfam.mod    kuru:  yalnızca sayar
--                 medya: bütün yüklemeleri silme kuyruğuna alır (status
--                        'deleting'); R2'deki nesneleri çalışan core'un medya
--                        temizliği siler (ADR-0007, media.Cleanup). core
--                        ayakta olmalı, gateway durdurulmuş olmalı.
--                 sil:   siler; uploads boş olmalı (medya adımı bitmiş)
--   tayfam.ortam  medya ve sil için zorunlu: dev | prod
--   tayfam.onay   medya ve sil için zorunlu: PROD-SIFIRLA ya da DEV-SIFIRLA
--   tayfam.r2     yalnızca R2'si yapılandırılmamış ortam için: atla (uploads
--                 boş değilken sil'e izin verir; nesneler R2'de kalırsa
--                 silinmez)
--
-- Kilitler: identity betiğindekiler (mod, ortam ve onay ifadesi, veritabanı
-- sahibi core, tablo sınıflandırması, eşik, sil'de bağlı servis yok) ve
-- sil'de boş uploads. Eşik: adında "Demo" geçmeyen tayfalarda üyeliği olan
-- farklı kullanıcı sayısı (ADR-0023 §2: demo tayfalarının adında "Demo"
-- geçer). Demo hesapları yalnızca demo tayfalarındadır.
--
-- Tek DO bloğu, tek transaction. Çıktı yalnızca tablo adları ve sayılardır.

DO $sifirla$
DECLARE
  servis constant text := 'core';
  -- Demo dışı tayfalardaki farklı kullanıcı: platform sahibi + en çok 5 hesap.
  esik constant bigint := 6;
  -- Outbox: katalog olayları korunur (identity'nin katalog kopyası; olay
  -- sürüm taşır, tekrar teslim zararsızdır), geri kalanı silinir.
  korunan_olay constant text := 'catalog.changed';

  -- Tamamı silinen tablolar: tayfalar ve içerikleri, etkinlikler ve
  -- bileşenleri, soru-cevap, anketler, sosyal (arkadaşlık, engel, puan,
  -- öneri), rozet ve istatistik sonuçları, kütüphane, materyal, garaj,
  -- şikâyetler, platform yasakları, profil kopyaları, yüklemeler, denetim
  -- kaydı (audit_logs) ve outbox tüketici konumları.
  silinen constant text[] := ARRAY[
    'announcements', 'answer_votes', 'answers', 'audit_logs',
    'discover_joins', 'event_badge_runs', 'event_bracket_games',
    'event_bracket_matches', 'event_brackets', 'event_celebration_winners',
    'event_celebrations', 'event_checklist_items', 'event_components',
    'event_consumers', 'event_draw_entries', 'event_draws', 'event_feedback',
    'event_field_values', 'event_goals', 'event_guest_players',
    'event_join_qr_counts', 'event_join_qr_tokens', 'event_log_entries',
    'event_log_entry_vocabulary', 'event_measurements', 'event_photos',
    'event_pool_links', 'event_question_votes', 'event_questions',
    'event_ranking_entries', 'event_referees', 'event_roll_call_checkins',
    'event_roll_calls', 'event_routes', 'event_rsvps',
    'event_safety_contacts', 'event_schedule_items', 'event_score_players',
    'event_score_tables', 'event_scores', 'event_series', 'event_stat_runs',
    'event_team_members', 'event_teams', 'event_vehicle_seat_requests',
    'event_vehicles', 'event_vote_closures', 'event_vote_options',
    'event_votes', 'events', 'friend_request_outcomes', 'friend_scores',
    'friend_suggestions', 'friendships', 'garage_tayfa_visibility',
    'garage_vehicles', 'invitation_outcomes', 'invites', 'library_items',
    'library_loans', 'library_queue', 'materials', 'memberships',
    'now_notification_counters', 'poll_options', 'poll_votes', 'polls',
    'question_tags', 'questions', 'reports', 'social_settings', 'tags',
    'tayfa_badges', 'tayfa_daily_items', 'tayfa_daily_state',
    'tayfa_discover', 'tayfa_home_widgets', 'tayfa_interests',
    'tayfa_modules', 'tayfa_stat_queue', 'tayfa_stats', 'tayfa_vocabulary',
    'tayfas', 'uploads', 'user_activity_records', 'user_availability',
    'user_badges', 'user_blocks', 'user_platform_bans', 'user_profiles',
    'user_stats'
  ];
  -- Dokunulmayan tablolar (domain_events ve blocked_domains aşağıda ayrıca).
  korunan constant text[] := ARRAY[
    'catalog_badges', 'catalog_event_type_versions', 'catalog_event_types',
    'catalog_interests', 'catalog_meta', 'catalog_score_presets',
    'catalog_study_tags',  -- katalog (ADR-0024, ADR-0025, ADR-0042)
    'blocked_domains',     -- engelli alan adları (platform kuralı); ekleyen boşalır
    'job_runs',            -- arka plan işlerinin son çalışması; kişisel veri yok
    'domain_events',       -- yalnızca catalog.changed olayları kalır
    'goose_db_version'     -- migration geçmişi
  ];

  mod    text := coalesce(current_setting('tayfam.mod', true), '');
  ortam  text := coalesce(current_setting('tayfam.ortam', true), '');
  onay   text := coalesce(current_setting('tayfam.onay', true), '');
  r2     text := coalesce(current_setting('tayfam.r2', true), '');
  sahip  text;
  tablolar text[];
  fark   text[];
  t      text;
  n      bigint;
  toplam bigint := 0;
  gercek bigint;
  bagli  bigint;
  yukleme bigint;
  olay   bigint;
  tr     record;
  kapali text[] := '{}';
BEGIN
  IF mod NOT IN ('kuru', 'medya', 'sil') THEN
    RAISE EXCEPTION 'tayfam.mod kuru, medya ya da sil olmalı (PGOPTIONS=''-c tayfam.mod=kuru'')'
      USING ERRCODE = 'TS001';
  END IF;

  SELECT pg_get_userbyid(datdba) INTO sahip FROM pg_database WHERE datname = current_database();
  IF sahip IS DISTINCT FROM servis THEN
    RAISE EXCEPTION 'bu betik % veritabanı içindir; bu veritabanının sahibi %', servis, sahip
      USING ERRCODE = 'TS003';
  END IF;

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

  SELECT count(DISTINCT m.user_id) INTO gercek
  FROM memberships m JOIN tayfas tf ON tf.id = m.tayfa_id
  WHERE position('Demo' IN tf.name) = 0;
  SELECT count(*) INTO yukleme FROM uploads;
  SELECT count(*) INTO olay FROM domain_events WHERE type <> korunan_olay;

  RAISE NOTICE '% (%): %', servis, current_database(), CASE mod
    WHEN 'kuru' THEN 'kuru çalışma, hiçbir şey silinmez'
    WHEN 'medya' THEN 'medya adımı, ortam ' || ortam
    ELSE 'sıfırlama, ortam ' || ortam END;
  FOREACH t IN ARRAY silinen LOOP
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    toplam := toplam + n;
    RAISE NOTICE 'silinecek  %  %', rpad(t, 34), n;
  END LOOP;
  RAISE NOTICE 'silinecek  %  %', rpad('domain_events (diğer olaylar)', 34), olay;
  toplam := toplam + olay;
  FOREACH t IN ARRAY korunan LOOP
    IF t = 'domain_events' THEN
      SELECT count(*) INTO n FROM domain_events WHERE type = korunan_olay;
      RAISE NOTICE 'korunur    %  %', rpad('domain_events (' || korunan_olay || ')', 34), n;
      CONTINUE;
    END IF;
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    RAISE NOTICE 'korunur    %  %', rpad(t, 34), n;
  END LOOP;
  SELECT count(*) INTO n FROM blocked_domains WHERE created_by IS NOT NULL;
  RAISE NOTICE 'blocked_domains: ekleyeni boşalacak satır: %', n;
  RAISE NOTICE 'yükleme kaydı (R2 nesnesi olan): %, silme kuyruğunda: %', yukleme,
    (SELECT count(*) FROM uploads WHERE status = 'deleting');
  RAISE NOTICE 'toplam silinecek satır: %', toplam;
  RAISE NOTICE 'gerçek kullanıcı (Demo olmayan tayfalardaki üyeler): % (eşik %)%', gercek, esik,
    CASE WHEN gercek > esik THEN ': EŞİK AŞILDI, medya ve sil reddedilir' ELSE '' END;

  IF mod = 'kuru' THEN
    RETURN;
  END IF;

  IF ortam NOT IN ('dev', 'prod') OR onay <> upper(ortam) || '-SIFIRLA' THEN
    RAISE EXCEPTION '% için tayfam.ortam (dev|prod) ve tayfam.onay (ör. PROD-SIFIRLA) birebir verilmeli', mod
      USING ERRCODE = 'TS002';
  END IF;
  IF gercek > esik THEN
    RAISE EXCEPTION 'gerçek kullanıcı sayısı (%) eşiği (%) geçiyor: pilot başlamış sayılır, sıfırlama reddedildi', gercek, esik
      USING ERRCODE = 'TS005';
  END IF;

  PERFORM set_config('lock_timeout', '5s', true);
  EXECUTE format('SET LOCAL ROLE %I', servis);

  IF mod = 'medya' THEN
    -- core'un medya temizliği (media.Cleanup) her geçişte R2 nesnelerini ve
    -- ardından satırı siler; pod yeniden başlayınca hemen bir geçiş yapar.
    UPDATE uploads SET status = 'deleting' WHERE status <> 'deleting';
    GET DIAGNOSTICS n = ROW_COUNT;
    RAISE NOTICE 'silme kuyruğuna alındı: % yükleme (toplam kuyruk %)', n, yukleme;
    RETURN;
  END IF;

  -- sil.
  RESET ROLE;
  SELECT count(*) INTO bagli FROM pg_stat_activity
  WHERE datname = current_database() AND pid <> pg_backend_pid()
    AND usename IN (servis, servis || '_app');
  IF bagli > 0 THEN
    RAISE EXCEPTION '% veritabanına bağlı % oturum var: önce servisi 0''a ölçekle', servis, bagli
      USING ERRCODE = 'TS006';
  END IF;
  IF yukleme > 0 AND r2 <> 'atla' THEN
    RAISE EXCEPTION 'uploads boş değil (% kayıt): önce medya adımı ve R2 temizliğinin bitmesi gerekir', yukleme
      USING ERRCODE = 'TS007';
  END IF;
  EXECUTE format('SET LOCAL ROLE %I', servis);

  -- Yalnızca eklenen tabloların TRUNCATE yasağı (audit_logs) bu transaction
  -- içinde kapatılır ve sonunda yeniden açılır.
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
  DELETE FROM domain_events WHERE type <> korunan_olay;
  -- Hesap silmenin yaptığı gibi (ClearBlockedDomainCreator): alan adı kalır,
  -- ekleyen boşalır.
  UPDATE blocked_domains SET created_by = NULL WHERE created_by IS NOT NULL;

  FOR i IN 1 .. cardinality(kapali) / 2 LOOP
    EXECUTE format('ALTER TABLE %I ENABLE TRIGGER %I', kapali[2 * i - 1], kapali[2 * i]);
  END LOOP;

  FOREACH t IN ARRAY silinen LOOP
    EXECUTE format('SELECT count(*) FROM %I', t) INTO n;
    IF n <> 0 THEN
      RAISE EXCEPTION '% boşalmadı (% satır); hiçbir şey silinmedi', t, n USING ERRCODE = 'TS008';
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM domain_events WHERE type <> korunan_olay)
     OR EXISTS (SELECT 1 FROM blocked_domains WHERE created_by IS NOT NULL) THEN
    RAISE EXCEPTION 'outbox ya da blocked_domains temizlenmedi; hiçbir şey silinmedi' USING ERRCODE = 'TS008';
  END IF;
  RAISE NOTICE 'sıfırlandı: % satır silindi', toplam;
END
$sifirla$;
