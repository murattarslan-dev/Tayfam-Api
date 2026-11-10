package keys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// ClaimHistoryKey records a new history key made by the caller's unpaired
// device when the user has no paired device (ADR-0018 item 4) and returns
// its version. A paired device gets the current version.
func (s *Service) ClaimHistoryKey(ctx context.Context, c Caller) (int, error) {
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return 0, apperr.ErrRateLimited
	}
	var version int16
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		st, err := lockState(ctx, q, c.User)
		if err != nil {
			return err
		}
		d, err := device(ctx, q, c)
		if err != nil {
			return err
		}
		if d.PairedAt.Valid {
			version = st.HistoryKeyVersion
			return nil
		}
		devices, err := q.ListUserDevices(ctx, c.User.PG())
		if err != nil {
			return fmt.Errorf("cihazlar: %w", err)
		}
		for _, o := range devices {
			if o.PairedAt.Valid {
				return apperr.ErrE2EPairedDeviceExists
			}
		}
		if version, err = q.BumpHistoryKeyVersion(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("geçmiş anahtarı: %w", err)
		}
		if _, err := q.PairDevice(ctx, store.PairDeviceParams{PairedAt: ts(s.now()), ID: d.ID}); err != nil {
			return fmt.Errorf("eşleştirme: %w", err)
		}
		if err := q.DeleteDeviceLinkRequest(ctx, d.ID); err != nil {
			return fmt.Errorf("eşleştirme isteği: %w", err)
		}
		return nil
	})
	return int(version), err
}

// LinkRequest is a pairing request (ADR-0018 item 5).
type LinkRequest struct {
	ID         id.ID
	ExpiresAt  time.Time
	Answered   bool
	Answer     []byte
	Version    int
	AnsweredBy id.ID
}

// CreateLink opens a pairing request for the caller's unpaired device,
// replacing its earlier one (CreateLinkRequest).
func (s *Service) CreateLink(ctx context.Context, c Caller) (LinkRequest, error) {
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return LinkRequest{}, apperr.ErrRateLimited
	}
	now := s.now()
	var out LinkRequest
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		d, err := device(ctx, q, c)
		if err != nil {
			return err
		}
		if d.PairedAt.Valid {
			return apperr.ErrE2EAlreadyPaired
		}
		if err := q.DeleteDeviceLinkRequest(ctx, d.ID); err != nil {
			return fmt.Errorf("eşleştirme isteği: %w", err)
		}
		r, err := q.CreateLinkRequest(ctx, store.CreateLinkRequestParams{
			ID: id.New().PG(), UserID: c.User.PG(), DeviceID: d.ID, ExpiresAt: ts(now.Add(LinkLifetime)), CreatedAt: ts(now),
		})
		if err != nil {
			return fmt.Errorf("eşleştirme isteği: %w", err)
		}
		out = LinkRequest{ID: uuidOf(r.ID), ExpiresAt: r.ExpiresAt.Time.UTC()}
		return nil
	})
	return out, err
}

// AnswerLink answers a pairing request of the caller's user from the
// caller's paired device with the history keys encrypted to the new
// device (AnswerLinkRequest).
func (s *Service) AnswerLink(ctx context.Context, c Caller, request id.ID, version uint32, answer []byte) error {
	switch {
	case request == (id.ID{}):
		return invalid("link_request_id")
	case version < 1 || version > maxHistoryVersion:
		return invalid("history_key_version")
	case len(answer) < 1 || len(answer) > MaxShareBytes:
		return invalid("answer")
	}
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return apperr.ErrRateLimited
	}
	now := s.now()
	var target id.ID // the new device's session
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		st, err := lockState(ctx, q, c.User)
		if err != nil {
			return err
		}
		d, err := paired(ctx, q, c)
		if err != nil {
			return err
		}
		if uint32(st.HistoryKeyVersion) != version { //nolint:gosec // 0..32767
			return apperr.ErrE2EHistoryKeyStale
		}
		r, err := q.GetLinkRequest(ctx, store.GetLinkRequestParams{ID: request.PG(), UserID: c.User.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrE2ELinkRequestNotFound
		case err != nil:
			return fmt.Errorf("eşleştirme isteği: %w", err)
		}
		n, err := q.AnswerLinkRequest(ctx, store.AnswerLinkRequestParams{
			Answer: answer, HistoryKeyVersion: pgtype.Int2{Int16: int16(version), Valid: true}, //nolint:gosec // checked above
			AnsweredByDeviceID: d.ID, AnsweredAt: ts(now), ID: request.PG(), UserID: c.User.PG(),
		})
		if err != nil {
			return fmt.Errorf("eşleştirme cevabı: %w", err)
		}
		if n == 0 {
			return apperr.ErrE2ELinkRequestNotFound // expired or answered
		}
		devices, err := q.ListUserDevices(ctx, c.User.PG())
		if err != nil {
			return fmt.Errorf("cihazlar: %w", err)
		}
		for _, o := range devices {
			if o.ID == r.DeviceID {
				target = uuidOf(o.SessionID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if target != (id.ID{}) {
		s.cfg.Publisher.LinkAnswered(target, request)
	}
	return nil
}

// ownLink returns the caller's device's own pairing request.
func (s *Service) ownLink(ctx context.Context, q *store.Queries, c Caller, request id.ID) (deviceRow, store.GetLinkRequestRow, error) {
	if request == (id.ID{}) {
		return deviceRow{}, store.GetLinkRequestRow{}, invalid("link_request_id")
	}
	d, err := device(ctx, q, c)
	if err != nil {
		return deviceRow{}, store.GetLinkRequestRow{}, err
	}
	r, err := q.GetLinkRequest(ctx, store.GetLinkRequestParams{ID: request.PG(), UserID: c.User.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return deviceRow{}, store.GetLinkRequestRow{}, apperr.ErrE2ELinkRequestNotFound
	case err != nil:
		return deviceRow{}, store.GetLinkRequestRow{}, fmt.Errorf("eşleştirme isteği: %w", err)
	}
	if r.DeviceID != d.ID || (!r.AnsweredAt.Valid && !r.ExpiresAt.Time.After(s.now())) {
		return deviceRow{}, store.GetLinkRequestRow{}, apperr.ErrE2ELinkRequestNotFound
	}
	return d, r, nil
}

// GetLink returns the caller's device's pairing request (GetLinkRequest).
func (s *Service) GetLink(ctx context.Context, c Caller, request id.ID) (LinkRequest, error) {
	_, r, err := s.ownLink(ctx, store.New(s.cfg.Pool), c, request)
	if err != nil {
		return LinkRequest{}, err
	}
	return LinkRequest{
		ID: uuidOf(r.ID), ExpiresAt: r.ExpiresAt.Time.UTC(), Answered: r.AnsweredAt.Valid, Answer: r.Answer,
		Version: int(r.HistoryKeyVersion.Int16), AnsweredBy: uuidOf(r.AnsweredByDeviceID),
	}, nil
}

// CompleteLink pairs the caller's device after it read the answer and
// deletes the request (CompleteLinkRequest).
func (s *Service) CompleteLink(ctx context.Context, c Caller, request id.ID) (Device, error) {
	var out Device
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		st, err := lockState(ctx, q, c.User)
		if err != nil {
			return err
		}
		d, r, err := s.ownLink(ctx, q, c, request)
		if err != nil {
			return err
		}
		if !r.AnsweredAt.Valid {
			return apperr.ErrE2ELinkRequestNotFound
		}
		if r.HistoryKeyVersion.Int16 != st.HistoryKeyVersion {
			return apperr.ErrE2EHistoryKeyStale
		}
		now := s.now()
		if _, err := q.PairDevice(ctx, store.PairDeviceParams{PairedAt: ts(now), ID: d.ID}); err != nil {
			return fmt.Errorf("eşleştirme: %w", err)
		}
		if err := q.DeleteLinkRequest(ctx, store.DeleteLinkRequestParams{ID: r.ID, UserID: c.User.PG()}); err != nil {
			return fmt.Errorf("eşleştirme isteği: %w", err)
		}
		out = deviceOf(d)
		out.Paired = true
		return nil
	})
	return out, err
}

// ShareInput is a new history key for one paired device.
type ShareInput struct {
	Device id.ID
	Bundle []byte
}

// Rotate records a new history key version made by the caller's paired
// device and its shares for every other paired device (RotateHistoryKey).
func (s *Service) Rotate(ctx context.Context, c Caller, version uint32, shares []ShareInput) error {
	if version < 1 || version > maxHistoryVersion {
		return invalid("history_key_version")
	}
	if len(shares) > MaxDevices {
		return invalid("shares")
	}
	for _, sh := range shares {
		if sh.Device == (id.ID{}) || len(sh.Bundle) < 1 || len(sh.Bundle) > MaxShareBytes {
			return invalid("shares")
		}
	}
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return apperr.ErrRateLimited
	}
	now := s.now()
	var notify []id.ID
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		st, err := lockState(ctx, q, c.User)
		if err != nil {
			return err
		}
		d, err := paired(ctx, q, c)
		if err != nil {
			return err
		}
		if uint32(st.HistoryKeyVersion)+1 != version { //nolint:gosec // 0..32767
			return apperr.ErrE2EHistoryKeyStale
		}
		devices, err := q.ListUserDevices(ctx, c.User.PG())
		if err != nil {
			return fmt.Errorf("cihazlar: %w", err)
		}
		var want []DeviceInfo
		for _, o := range devices {
			if o.PairedAt.Valid && o.ID != d.ID {
				want = append(want, infoOf(o))
			}
		}
		given := make([]Target, 0, len(shares))
		reg := map[id.ID]uint32{}
		for _, o := range want {
			reg[o.ID] = uint32(o.RegistrationID) //nolint:gosec // 1..16380
		}
		for _, sh := range shares {
			given = append(given, Target{Device: sh.Device, RegistrationID: reg[sh.Device]})
		}
		if err := Match(want, given); err != nil {
			return err
		}
		if _, err := q.BumpHistoryKeyVersion(ctx, c.User.PG()); err != nil {
			return fmt.Errorf("geçmiş anahtarı: %w", err)
		}
		for _, sh := range shares {
			if err := q.InsertHistoryKeyShare(ctx, store.InsertHistoryKeyShareParams{
				ID: id.New().PG(), UserID: c.User.PG(), RecipientDeviceID: sh.Device.PG(), SenderDeviceID: d.ID,
				HistoryKeyVersion: int16(version), Bundle: sh.Bundle, CreatedAt: ts(now), //nolint:gosec // checked above
			}); err != nil {
				return fmt.Errorf("anahtar paylaşımı: %w", err)
			}
		}
		for _, o := range want {
			notify = append(notify, o.Session)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, session := range notify {
		s.cfg.Publisher.HistoryKeyShared(session)
	}
	return nil
}

// Share is a history key share waiting for the caller's device.
type Share struct {
	ID           id.ID
	SenderDevice id.ID
	Version      int
	Bundle       []byte
	CreatedAt    time.Time
}

// Shares returns the shares waiting for the caller's device, oldest first
// (ListHistoryKeyShares).
func (s *Service) Shares(ctx context.Context, c Caller) ([]Share, error) {
	q := store.New(s.cfg.Pool)
	d, err := device(ctx, q, c)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListHistoryKeyShares(ctx, store.ListHistoryKeySharesParams{DeviceID: d.ID, N: maxPageSize})
	if err != nil {
		return nil, fmt.Errorf("anahtar paylaşımları: %w", err)
	}
	out := make([]Share, 0, len(rows))
	for _, r := range rows {
		out = append(out, Share{ID: uuidOf(r.ID), SenderDevice: uuidOf(r.SenderDeviceID), Version: int(r.HistoryKeyVersion), Bundle: r.Bundle, CreatedAt: r.CreatedAt.Time.UTC()})
	}
	return out, nil
}

// AckShares deletes received shares of the caller's device
// (AckHistoryKeyShares).
func (s *Service) AckShares(ctx context.Context, c Caller, ids []id.ID) error {
	if err := validIDs(ids, "share_ids"); err != nil {
		return err
	}
	q := store.New(s.cfg.Pool)
	d, err := device(ctx, q, c)
	if err != nil {
		return err
	}
	p := store.AckHistoryKeySharesParams{DeviceID: d.ID}
	for _, v := range ids {
		p.Ids = append(p.Ids, v.PG())
	}
	if _, err := q.AckHistoryKeyShares(ctx, p); err != nil {
		return fmt.Errorf("paylaşım onayı: %w", err)
	}
	return nil
}
