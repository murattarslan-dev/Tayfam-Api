// Package export returns a user's own chat data for the KVKK data export
// (issue #144; ADR-0008, ADR-0021; fields in docs/data-export-format.md):
// the channel messages they wrote, decrypted, and their muting
// preferences. Direct messages are not part of it (ADR-0008: end-to-end
// encrypted; the client exports them on the device).
//
// identity calls ExportService.ExportMyData with the user's own access
// token; the user is always the verified caller. The package logs nothing:
// message text never leaves it except in the stream to the caller.
package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Section names; the archive file is chat/<name>.json.
const (
	SectionChannelMessages         = "channel_messages"
	SectionNotificationPreferences = "notification_preferences"
)

// Defaults as in core's export.
const (
	DefaultPageSize   = 500
	DefaultChunkBytes = 1 << 20
)

// Chunk is part of one section.
type Chunk struct {
	Section string
	Records [][]byte
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Cipher opens channel messages; nil (no key configured) makes Export
	// unavailable.
	Cipher     *channelcrypt.Cipher
	PageSize   int32
	ChunkBytes int
}

// Service reads exports.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("export: eksik yapılandırma")
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = DefaultChunkBytes
	}
	return &Service{cfg: cfg}, nil
}

// message is one record of channel_messages.json.
type message struct {
	ID        string     `json:"id"`
	ChannelID string     `json:"channel_id"`
	TayfaID   string     `json:"tayfa_id"`
	Body      *string    `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// chunker collects records of one section and sends them in chunks.
type chunker struct {
	limit int
	send  func(Chunk) error
	c     Chunk
	size  int
}

func (k *chunker) add(r []byte) error {
	if len(k.c.Records) > 0 && k.size+len(r) > k.limit {
		if err := k.flush(); err != nil {
			return err
		}
	}
	k.c.Records = append(k.c.Records, r)
	k.size += len(r)
	return nil
}

func (k *chunker) flush() error {
	if len(k.c.Records) == 0 {
		return nil
	}
	err := k.send(k.c)
	k.c, k.size = Chunk{Section: k.c.Section}, 0
	return err
}

// Export sends user's records to send. A send error stops it and is
// returned as it is.
func (s *Service) Export(ctx context.Context, user id.ID, send func(Chunk) error) error {
	if s.cfg.Cipher == nil {
		return apperr.ErrChannelsUnavailable
	}
	if user == (id.ID{}) {
		return errors.New("export: kullanıcı yok")
	}
	tx, err := s.cfg.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("dışa aktarma: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL TimeZone = 'UTC'"); err != nil {
		return fmt.Errorf("dışa aktarma: %w", err)
	}
	q := store.New(tx)
	if err := s.messages(ctx, q, user, send); err != nil {
		return err
	}
	return s.preferences(ctx, q, user, send)
}

func (s *Service) messages(ctx context.Context, q *store.Queries, user id.ID, send func(Chunk) error) error {
	k := &chunker{limit: s.cfg.ChunkBytes, send: send, c: Chunk{Section: SectionChannelMessages}}
	after := pgtype.UUID{Valid: true}
	for {
		page, err := q.ExportChannelMessages(ctx, store.ExportChannelMessagesParams{UserID: user.PG(), After: after, N: s.cfg.PageSize})
		if err != nil {
			return fmt.Errorf("dışa aktarma %s: %w", SectionChannelMessages, err)
		}
		for _, r := range page {
			m := message{
				ID: id.ID(r.ID.Bytes).String(), ChannelID: id.ID(r.ChannelID.Bytes).String(), TayfaID: id.ID(r.TayfaID.Bytes).String(),
				CreatedAt: r.CreatedAt.Time.UTC(),
			}
			if r.DeletedAt.Valid {
				// The author deleted it (or a moderator did): its text is not
				// exported, only that it existed.
				d := r.DeletedAt.Time.UTC()
				m.DeletedAt = &d
			} else {
				body, err := s.cfg.Cipher.Decrypt(id.ID(r.ID.Bytes), id.ID(r.ChannelID.Bytes), channelcrypt.Sealed{Version: r.KeyVersion.Int16, Data: r.BodyEncrypted})
				if err != nil {
					// The message ID only; never the text.
					return fmt.Errorf("dışa aktarma: mesaj %s açılamadı: %w", m.ID, err)
				}
				m.Body = &body
			}
			b, err := json.Marshal(m)
			if err != nil {
				return fmt.Errorf("dışa aktarma: %w", err)
			}
			if err := k.add(b); err != nil {
				return err
			}
		}
		if int32(len(page)) < s.cfg.PageSize { //nolint:gosec // at most PageSize rows
			break
		}
		after = page[len(page)-1].ID
	}
	return k.flush()
}

func (s *Service) preferences(ctx context.Context, q *store.Queries, user id.ID, send func(Chunk) error) error {
	k := &chunker{limit: s.cfg.ChunkBytes, send: send, c: Chunk{Section: SectionNotificationPreferences}}
	after := pgtype.UUID{Valid: true}
	for {
		page, err := q.ExportNotificationPreferences(ctx, store.ExportNotificationPreferencesParams{UserID: user.PG(), After: after, N: s.cfg.PageSize})
		if err != nil {
			return fmt.Errorf("dışa aktarma %s: %w", SectionNotificationPreferences, err)
		}
		for _, r := range page {
			if err := k.add(r.Data); err != nil {
				return err
			}
		}
		if int32(len(page)) < s.cfg.PageSize { //nolint:gosec // at most PageSize rows
			break
		}
		after = page[len(page)-1].K
	}
	return k.flush()
}
