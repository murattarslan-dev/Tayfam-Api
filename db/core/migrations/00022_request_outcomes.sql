-- Arkadaşlık isteği ve tayfa davetinin sonucu (issue #192; platform sahibi
-- 2026-11-14).
--
-- Bildirimler sekmesi istek ve davet bildiriminde yanıt durumunu gösterir
-- (Notification.resolution). Durum bildirim listelenirken core'dan sorulur;
-- bildirim kopyasına yazılmaz. Bekleyen ve kabul edilen istek/davet zaten
-- friendships ve memberships'te görünür; reddedilen ve geri çekilen satır
-- ise silindiği için sonucu burada tutulur:
--   friend_request_outcomes  isteyen → alıcı: accepted / declined / withdrawn
--   invitation_outcomes      tayfa, davet edilen: accepted / declined / withdrawn
-- Yeni bir istek ya da davet o çiftin satırını siler. Yalnızca kimlik,
-- sonuç ve zaman tutulur; kimin geri çektiği tutulmaz. 90 gün sonra
-- (bildirimlerin saklama süresi) saklama işiyle, hesap silinince hemen
-- silinir; tayfa kalıcı silinince CASCADE.
--
-- Geri alma planı: Down tabloları kaldırır; sonuçlar "expired" görünür.

-- +goose Up
CREATE TABLE friend_request_outcomes (
  requester_id uuid NOT NULL,
  recipient_id uuid NOT NULL,
  outcome      text NOT NULL,
  resolved_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (requester_id, recipient_id),
  CONSTRAINT friend_request_outcomes_not_self CHECK (requester_id <> recipient_id),
  CONSTRAINT friend_request_outcomes_outcome CHECK (outcome IN ('accepted', 'declined', 'withdrawn'))
);
CREATE INDEX friend_request_outcomes_recipient_idx ON friend_request_outcomes (recipient_id);
CREATE INDEX friend_request_outcomes_resolved_idx ON friend_request_outcomes (resolved_at);

CREATE TABLE invitation_outcomes (
  tayfa_id    uuid NOT NULL REFERENCES tayfas (id) ON DELETE CASCADE,
  user_id     uuid NOT NULL,
  outcome     text NOT NULL,
  resolved_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tayfa_id, user_id),
  CONSTRAINT invitation_outcomes_outcome CHECK (outcome IN ('accepted', 'declined', 'withdrawn'))
);
CREATE INDEX invitation_outcomes_user_idx ON invitation_outcomes (user_id);
CREATE INDEX invitation_outcomes_resolved_idx ON invitation_outcomes (resolved_at);

-- core_app (ADR-0015): sonucu günceller (ON CONFLICT) ve siler. Varsayılan
-- yetki (00016) SELECT ve INSERT'tir.
GRANT UPDATE (outcome, resolved_at), DELETE ON friend_request_outcomes TO core_app;
GRANT UPDATE (outcome, resolved_at), DELETE ON invitation_outcomes TO core_app;

-- +goose Down
DROP TABLE invitation_outcomes;
DROP TABLE friend_request_outcomes;
