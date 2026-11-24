-- Test verisi: A yönetici, B ve C sıradan kullanıcı; A'yı B davet etmiş.
BEGIN;
-- Koruma trigger'ları yalnızca test verisi yazılırken kapalı.
SET LOCAL session_replication_role = replica;
INSERT INTO users (id, display_name, ref_code) VALUES
  ('00000000-0000-7000-8000-00000000000a', 'Yönetici', 'TYF-AAAAAA'),
  ('00000000-0000-7000-8000-00000000000b', 'Bir', 'TYF-BBBBBB'),
  ('00000000-0000-7000-8000-00000000000c', 'İki', 'TYF-CCCCCC');
UPDATE users SET referred_by_user_id = '00000000-0000-7000-8000-00000000000b', referred_at = now()
WHERE id = '00000000-0000-7000-8000-00000000000a';
INSERT INTO platform_admins (user_id) VALUES ('00000000-0000-7000-8000-00000000000a');
INSERT INTO devices (id, user_id, platform) VALUES
  ('00000000-0000-7000-8000-0000000000da', '00000000-0000-7000-8000-00000000000a', 'android'),
  ('00000000-0000-7000-8000-0000000000db', '00000000-0000-7000-8000-00000000000b', 'android');
INSERT INTO sessions (id, user_id, device_id, expires_at) VALUES
  ('00000000-0000-7000-8000-0000000005aa', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-0000000000da', now() + interval '1 day'),
  ('00000000-0000-7000-8000-0000000005bb', '00000000-0000-7000-8000-00000000000b', '00000000-0000-7000-8000-0000000000db', now() + interval '1 day');
INSERT INTO refresh_tokens (id, session_id, token_hash) VALUES
  ('00000000-0000-7000-8000-0000000007aa', '00000000-0000-7000-8000-0000000005aa', decode(repeat('aa', 32), 'hex')),
  ('00000000-0000-7000-8000-0000000007bb', '00000000-0000-7000-8000-0000000005bb', decode(repeat('bb', 32), 'hex'));
INSERT INTO domain_events (id, type, actor_id, subject_type, subject_id, payload) VALUES
  ('00000000-0000-7000-8000-0000000e0001', 'user.profile_updated', '00000000-0000-7000-8000-00000000000b', 'user', '00000000-0000-7000-8000-00000000000b', '{}'),
  ('00000000-0000-7000-8000-0000000e0002', 'user.profile_updated', '00000000-0000-7000-8000-00000000000a', 'user', '00000000-0000-7000-8000-00000000000a', '{}');
COMMIT;
