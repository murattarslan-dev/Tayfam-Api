-- Test verisi: B'nin kurduğu tayfa (A ve B üye), etkinlik, denetim kaydı,
-- dosyalar, garaj ve olaylar.
BEGIN;
-- Koruma trigger'ları yalnızca test verisi yazılırken kapalı.
SET LOCAL session_replication_role = replica;
INSERT INTO user_profiles (user_id, display_name, source_event_id) VALUES
  ('00000000-0000-7000-8000-00000000000a', 'Yönetici', '00000000-0000-7000-8000-0000000e0002'),
  ('00000000-0000-7000-8000-00000000000b', 'Bir', '00000000-0000-7000-8000-0000000e0001');
INSERT INTO social_settings (user_id) VALUES
  ('00000000-0000-7000-8000-00000000000a'), ('00000000-0000-7000-8000-00000000000b');
INSERT INTO tayfas (id, name, name_normalized, join_code, created_by) VALUES
  ('00000000-0000-7000-8000-0000000000f1', 'Deneme', 'deneme', 'ABCD2345', '00000000-0000-7000-8000-00000000000b');
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES
  ('00000000-0000-7000-8000-0000000000f1', '00000000-0000-7000-8000-00000000000b', 'admin', 'active', 'creator', now()),
  ('00000000-0000-7000-8000-0000000000f1', '00000000-0000-7000-8000-00000000000a', 'member', 'active', 'code', now());
INSERT INTO audit_logs (id, tayfa_id, actor_id, action) VALUES
  ('00000000-0000-7000-8000-0000000a0001', '00000000-0000-7000-8000-0000000000f1', '00000000-0000-7000-8000-00000000000b', 'tayfa.created');
INSERT INTO uploads (id, user_id, tayfa_id, purpose, status, r2_key, preview_type, preview_bytes, full_type, full_bytes, width, height, attached_type, attached_id, attached_at, client_request_id, ready_at) VALUES
  ('00000000-0000-7000-8000-000000000a01', '00000000-0000-7000-8000-00000000000a', NULL, 'avatar', 'ready', repeat('a1', 16), 'image/webp', 1, 'image/webp', 1, 10, 10, 'user', '00000000-0000-7000-8000-00000000000a', now(), gen_random_uuid(), now()),
  ('00000000-0000-7000-8000-000000000b01', '00000000-0000-7000-8000-00000000000b', NULL, 'avatar', 'ready', repeat('b1', 16), 'image/webp', 1, 'image/webp', 1, 10, 10, 'user', '00000000-0000-7000-8000-00000000000b', now(), gen_random_uuid(), now()),
  ('00000000-0000-7000-8000-000000000a02', '00000000-0000-7000-8000-00000000000a', '00000000-0000-7000-8000-0000000000f1', 'cover', 'ready', repeat('a2', 16), 'image/webp', 1, 'image/webp', 1, 10, 10, 'tayfa', '00000000-0000-7000-8000-0000000000f1', now(), gen_random_uuid(), now());
UPDATE tayfas SET cover_upload_id = '00000000-0000-7000-8000-000000000a02';
INSERT INTO garage_vehicles (id, owner_id, make, client_request_id, created_at, updated_at) VALUES
  ('00000000-0000-7000-8000-0000000c0a01', '00000000-0000-7000-8000-00000000000a', 'Murat', gen_random_uuid(), now(), now()),
  ('00000000-0000-7000-8000-0000000c0b01', '00000000-0000-7000-8000-00000000000b', 'Murat', gen_random_uuid(), now(), now());
INSERT INTO domain_events (id, type, actor_id, tayfa_id, subject_type, subject_id, payload) VALUES
  ('00000000-0000-7000-8000-0000000e1001', 'tayfa.created', '00000000-0000-7000-8000-00000000000b', '00000000-0000-7000-8000-0000000000f1', 'tayfa', '00000000-0000-7000-8000-0000000000f1', '{}'),
  ('00000000-0000-7000-8000-0000000e1002', 'catalog.changed', '00000000-0000-7000-8000-00000000000a', NULL, NULL, NULL, '{"catalog_version": 1}');
INSERT INTO events (id, tayfa_id, type, type_version, mode, title, starts_at, ends_at, online_url, created_by) VALUES
  ('00000000-0000-7000-8000-0000000000e1', '00000000-0000-7000-8000-0000000000f1', 'sports', 1, 'online', 'Maç',
   now() + interval '1 day', now() + interval '1 day 2 hours', 'https://example.com', '00000000-0000-7000-8000-00000000000b');
INSERT INTO event_rsvps (event_id, user_id, status)
  VALUES ('00000000-0000-7000-8000-0000000000e1', '00000000-0000-7000-8000-00000000000a', 'going');
COMMIT;
