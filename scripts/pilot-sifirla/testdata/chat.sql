-- Test verisi: tayfa kanalı ve mesajı, A–B birebir sohbeti, iki kullanıcının
-- cihaz anahtarı, push token'ı ve bildirimi.
BEGIN;
-- Koruma trigger'ları yalnızca test verisi yazılırken kapalı.
SET LOCAL session_replication_role = replica;
INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES
  ('00000000-0000-7000-8000-00000000000a', 'Yönetici', '00000000-0000-7000-8000-0000000e0002'),
  ('00000000-0000-7000-8000-00000000000b', 'Bir', '00000000-0000-7000-8000-0000000e0001');
INSERT INTO channels (id, tayfa_id, name, name_normalized, created_by) VALUES
  ('00000000-0000-7000-8000-0000000000c1', '00000000-0000-7000-8000-0000000000f1', 'genel', 'genel', '00000000-0000-7000-8000-00000000000b');
INSERT INTO channel_messages (id, channel_id, author_id, body_encrypted, key_version, client_request_id) VALUES
  ('00000000-0000-7000-8000-0000000c1001', '00000000-0000-7000-8000-0000000000c1', '00000000-0000-7000-8000-00000000000b', decode(repeat('00', 29), 'hex'), 1, gen_random_uuid());
INSERT INTO dm_conversations (id, user_low_id, user_high_id, status, requested_by, last_message_at) VALUES
  ('00000000-0000-7000-8000-0000000000d1', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-00000000000b', 'request', '00000000-0000-7000-8000-00000000000b', now());
INSERT INTO device_keys (id, user_id, session_id, device_number, registration_id, identity_key, signed_prekey_id, signed_prekey, signed_prekey_signature, kyber_prekey_id, kyber_prekey, kyber_prekey_signature)
SELECT u.id, u.uid, u.sid, 1, 1, '\x05' || decode(repeat(u.b, 32), 'hex'), 1, '\x05' || decode(repeat(u.b, 32), 'hex'), decode(repeat(u.b, 64), 'hex'), 1, '\x08' || decode(repeat(u.b, 1568), 'hex'), decode(repeat(u.b, 64), 'hex')
FROM (VALUES
  ('00000000-0000-7000-8000-000000000ea1'::uuid, '00000000-0000-7000-8000-00000000000a'::uuid, '00000000-0000-7000-8000-0000000005aa'::uuid, 'aa'),
  ('00000000-0000-7000-8000-000000000eb1'::uuid, '00000000-0000-7000-8000-00000000000b'::uuid, '00000000-0000-7000-8000-0000000005bb'::uuid, 'bb')) AS u(id, uid, sid, b);
INSERT INTO push_tokens (session_id, user_id, platform, token) VALUES
  ('00000000-0000-7000-8000-0000000005aa', '00000000-0000-7000-8000-00000000000a', 'android', 'token-a'),
  ('00000000-0000-7000-8000-0000000005bb', '00000000-0000-7000-8000-00000000000b', 'android', 'token-b');
INSERT INTO notifications (id, user_id, tayfa_id, type, actor_id, target_type, target_id, source_event_id) VALUES
  ('00000000-0000-7000-8000-0000000b0001', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-0000000000f1', 'event_created', '00000000-0000-7000-8000-00000000000b', 'event', '00000000-0000-7000-8000-0000000000e1', '00000000-0000-7000-8000-0000000e1001');
COMMIT;
