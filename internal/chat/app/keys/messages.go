package keys

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Envelope is an envelope waiting for a device.
type Envelope struct {
	ID             id.ID
	MessageID      id.ID
	ConversationID id.ID
	SenderID       id.ID
	SenderDevice   id.ID
	SenderNumber   int
	Type           int
	Content        []byte
	CreatedAt      time.Time
}

// Delivery is an envelope for the live connections of one session.
type Delivery struct {
	Session  id.ID
	Envelope Envelope
}

// Envelopes returns the envelopes waiting for the caller's device, oldest
// first (ListEnvelopes).
func (s *Service) Envelopes(ctx context.Context, c Caller, size int32, token string) ([]Envelope, string, error) {
	n, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	after, err := pageCursor(token)
	if err != nil {
		return nil, "", err
	}
	q := store.New(s.cfg.Pool)
	d, err := device(ctx, q, c)
	if err != nil {
		return nil, "", err
	}
	s.touch(ctx, q, uuidOf(d.ID))
	rows, err := q.ListEnvelopes(ctx, store.ListEnvelopesParams{DeviceID: d.ID, AfterID: after, N: n + 1})
	if err != nil {
		return nil, "", fmt.Errorf("zarflar: %w", err)
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		next = uuidOf(rows[n-1].ID).String()
	}
	out := make([]Envelope, 0, len(rows))
	for _, r := range rows {
		out = append(out, Envelope{
			ID: uuidOf(r.ID), MessageID: uuidOf(r.MessageID), ConversationID: uuidOf(r.ConversationID),
			SenderID: uuidOf(r.SenderID), SenderDevice: uuidOf(r.SenderDeviceID), SenderNumber: int(r.SenderDeviceNumber),
			Type: int(r.MessageType), Content: r.Ciphertext, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, next, nil
}

func validIDs(ids []id.ID, field string) error {
	if len(ids) == 0 || len(ids) > MaxAckIDs {
		return invalid(field)
	}
	for _, v := range ids {
		if v == (id.ID{}) {
			return invalid(field)
		}
	}
	return nil
}

// Ack deletes delivered envelopes of the caller's device (AckEnvelopes,
// WebSocket dm.envelope_ack). Other devices' envelopes are not touched.
func (s *Service) Ack(ctx context.Context, c Caller, ids []id.ID) error {
	if err := validIDs(ids, "envelope_ids"); err != nil {
		return err
	}
	q := store.New(s.cfg.Pool)
	d, err := device(ctx, q, c)
	if err != nil {
		return err
	}
	p := store.AckEnvelopesParams{DeviceID: d.ID}
	for _, v := range ids {
		p.Ids = append(p.Ids, v.PG())
	}
	if _, err := q.AckEnvelopes(ctx, p); err != nil {
		return fmt.Errorf("zarf onayı: %w", err)
	}
	return nil
}

// ArchiveInput is an archive entry to write.
type ArchiveInput struct {
	MessageID id.ID
	Content   []byte
}

// ArchiveEntry is an entry of the caller's archive.
type ArchiveEntry struct {
	MessageID      id.ID
	ConversationID id.ID
	KeyVersion     int
	Content        []byte
	CreatedAt      time.Time
}

// paired returns the caller's device if it is paired.
func paired(ctx context.Context, q *store.Queries, c Caller) (deviceRow, error) {
	d, err := device(ctx, q, c)
	if err != nil {
		return deviceRow{}, err
	}
	if !d.PairedAt.Valid {
		return deviceRow{}, apperr.ErrE2ENotPaired
	}
	return d, nil
}

// PutArchive writes the caller's archive entries (PutArchiveEntries) and
// returns the given messages that have an entry of the caller now.
func (s *Service) PutArchive(ctx context.Context, c Caller, version uint32, entries []ArchiveInput) ([]id.ID, error) {
	if version < 1 || version > maxHistoryVersion {
		return nil, invalid("history_key_version")
	}
	if len(entries) == 0 || len(entries) > MaxArchivePerCall {
		return nil, invalid("entries")
	}
	seen := map[id.ID]bool{}
	for _, e := range entries {
		if e.MessageID == (id.ID{}) || seen[e.MessageID] || len(e.Content) < 1 || len(e.Content) > MaxArchiveBytes {
			return nil, invalid("entries")
		}
		seen[e.MessageID] = true
	}
	if !s.cfg.Archive.Allow(c.User.String()) {
		return nil, apperr.ErrRateLimited
	}
	q := store.New(s.cfg.Pool)
	if _, err := paired(ctx, q, c); err != nil {
		return nil, err
	}
	st, err := q.GetE2EState(ctx, c.User.PG())
	if err != nil {
		return nil, fmt.Errorf("e2e durumu: %w", err)
	}
	if uint32(st.HistoryKeyVersion) != version { //nolint:gosec // 0..32767
		return nil, apperr.ErrE2EHistoryKeyStale
	}
	p := store.InsertArchiveEntriesParams{UserID: c.User.PG(), KeyVersion: int16(version), CreatedAt: ts(s.now())} //nolint:gosec // checked above
	for _, e := range entries {
		p.MessageIds = append(p.MessageIds, e.MessageID.PG())
		p.Ciphertexts = append(p.Ciphertexts, e.Content)
	}
	if err := q.InsertArchiveEntries(ctx, p); err != nil {
		return nil, fmt.Errorf("arşiv: %w", err)
	}
	got, err := q.ListArchivedMessageIDs(ctx, store.ListArchivedMessageIDsParams{UserID: c.User.PG(), MessageIds: p.MessageIds})
	if err != nil {
		return nil, fmt.Errorf("arşiv: %w", err)
	}
	out := make([]id.ID, 0, len(got))
	for _, m := range got {
		out = append(out, uuidOf(m))
	}
	return out, nil
}

// ArchiveListInput selects a page as dm.ListInput does.
type ArchiveListInput struct {
	PageSize  int32
	PageToken string
	After     id.ID
}

// Archive returns the caller's archive entries of conversation
// (ListArchiveEntries). Only the caller's own entries exist for them, so
// another user's conversation simply has none.
func (s *Service) Archive(ctx context.Context, c Caller, conversation id.ID, in ArchiveListInput) ([]ArchiveEntry, string, error) {
	if conversation == (id.ID{}) {
		return nil, "", invalid("conversation_id")
	}
	n, err := pageSize(in.PageSize)
	if err != nil {
		return nil, "", err
	}
	var cursor id.ID
	if in.PageToken != "" {
		v, ok := id.Parse(in.PageToken)
		if !ok {
			return nil, "", invalid("page_token")
		}
		cursor = v
	}
	q := store.New(s.cfg.Pool)
	if _, err := paired(ctx, q, c); err != nil {
		return nil, "", err
	}
	var rows []store.ListArchiveBeforeRow
	if in.After != (id.ID{}) {
		after := in.After
		if cursor != (id.ID{}) {
			if bytes.Compare(cursor[:], after[:]) < 0 {
				return nil, "", invalid("page_token")
			}
			after = cursor
		}
		rs, err := q.ListArchiveAfter(ctx, store.ListArchiveAfterParams{UserID: c.User.PG(), ConversationID: conversation.PG(), AfterID: after.PG(), N: n + 1})
		if err != nil {
			return nil, "", fmt.Errorf("arşiv: %w", err)
		}
		for _, r := range rs {
			rows = append(rows, store.ListArchiveBeforeRow(r))
		}
	} else {
		p := store.ListArchiveBeforeParams{UserID: c.User.PG(), ConversationID: conversation.PG(), N: n + 1}
		if cursor != (id.ID{}) {
			p.BeforeID = cursor.PG()
		}
		if rows, err = q.ListArchiveBefore(ctx, p); err != nil {
			return nil, "", fmt.Errorf("arşiv: %w", err)
		}
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		next = uuidOf(rows[n-1].MessageID).String()
	}
	out := make([]ArchiveEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, ArchiveEntry{
			MessageID: uuidOf(r.MessageID), ConversationID: uuidOf(r.ConversationID), KeyVersion: int(r.KeyVersion),
			Content: r.Ciphertext, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, next, nil
}

// DeleteArchive deletes the caller's archive entries of conversation, or
// all of them when conversation is zero (DeleteArchive).
func (s *Service) DeleteArchive(ctx context.Context, c Caller, conversation id.ID) error {
	q := store.New(s.cfg.Pool)
	var err error
	if conversation == (id.ID{}) {
		_, err = q.DeleteUserArchive(ctx, c.User.PG())
	} else {
		_, err = q.DeleteArchiveConversation(ctx, store.DeleteArchiveConversationParams{UserID: c.User.PG(), ConversationID: conversation.PG()})
	}
	if err != nil {
		return fmt.Errorf("arşiv silme: %w", err)
	}
	return nil
}

// MessageDeleted removes a deleted message's waiting envelopes and every
// archive entry of it (both parties'), inside dm's deletion transaction.
func MessageDeleted(ctx context.Context, q *store.Queries, message id.ID) error {
	if err := q.DeleteMessageEnvelopes(ctx, message.PG()); err != nil {
		return fmt.Errorf("mesaj zarfları: %w", err)
	}
	if err := q.DeleteMessageArchive(ctx, message.PG()); err != nil {
		return fmt.Errorf("mesaj arşivi: %w", err)
	}
	return nil
}
