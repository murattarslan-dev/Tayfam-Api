-- Outbox (ADR-0002): olaylar iş verisiyle aynı transaction'da
-- internal/platform/outbox.Write ile yazılır (sıralama kilidi, outbox paket
-- belgesi). payload'a kişisel içerik yazılmaz. outbox ortak kolonları yazar;
-- core'a özgü tayfa_id kolonu aynı transaction'da hemen ardından doldurulur.
-- name: SetDomainEventTayfa :execrows
UPDATE domain_events SET tayfa_id = @tayfa_id WHERE id = @id AND tayfa_id IS NULL;
