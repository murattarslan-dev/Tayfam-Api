-- Outbox: iş verisiyle aynı transaction'da yazılır (ADR-0002). payload'a
-- kişisel içerik yazılmaz.
-- name: CreateDomainEvent :exec
INSERT INTO domain_events (id, type, actor_id, tayfa_id, subject_type, subject_id, payload)
VALUES (@id, @type, @actor_id, @tayfa_id, @subject_type, @subject_id, @payload);
