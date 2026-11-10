package keys

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// SessionEnded removes the device of an ended session (identity's
// session.revoked; ADR-0018 item 1) and keeps the session from registering
// a new one while its access token may still be valid. Idempotent: a
// repeated event finds nothing to remove.
func (s *Service) SessionEnded(ctx context.Context, session id.ID) error {
	now := s.now()
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.RecordEndedSession(ctx, store.RecordEndedSessionParams{SessionID: session.PG(), EndedAt: ts(now)}); err != nil {
			return fmt.Errorf("biten oturum: %w", err)
		}
		d, err := q.DeleteDeviceBySession(ctx, session.PG())
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("cihaz silme: %w", err)
		}
		user := uuidOf(d.UserID)
		if _, err := lockState(ctx, q, user); err != nil {
			return err
		}
		return removed(ctx, q, user, uuidOf(d.ID), d.DeviceNumber, d.PairedAt.Valid, now)
	})
}

// UserSanctioned removes every device of a sanctioned user (all their
// sessions were closed) and refuses tokens issued before at. Idempotent.
func (s *Service) UserSanctioned(ctx context.Context, user id.ID, at time.Time) error {
	now := s.now()
	if at.IsZero() || at.After(now) {
		at = now
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.EndUserSessions(ctx, store.EndUserSessionsParams{UserID: user.PG(), EndedAt: ts(at.UTC())}); err != nil {
			return fmt.Errorf("oturumlar: %w", err)
		}
		if _, err := lockState(ctx, q, user); err != nil {
			return err
		}
		gone, err := q.DeleteUserDevices(ctx, user.PG())
		if err != nil {
			return fmt.Errorf("cihaz silme: %w", err)
		}
		for _, d := range gone {
			if err := removed(ctx, q, user, uuidOf(d.ID), d.DeviceNumber, d.PairedAt.Valid, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// Cleanup removes a deleted user's end-to-end records inside the
// deletion's transaction (userevents.Cleanup; ADR-0009): devices (with
// their pre-keys, envelopes, shares and pairing requests), envelopes the
// user sent that wait, their archive, key events and state. Tokens the
// user may still hold cannot register a device afterwards. Idempotent.
func Cleanup(ctx context.Context, q *store.Queries, user id.ID) error {
	if _, err := q.DeleteUserDevices(ctx, user.PG()); err != nil {
		return fmt.Errorf("e2e cihazları: %w", err)
	}
	if err := q.DeleteEnvelopesFromSender(ctx, user.PG()); err != nil {
		return fmt.Errorf("e2e zarfları: %w", err)
	}
	if _, err := q.DeleteUserArchive(ctx, user.PG()); err != nil {
		return fmt.Errorf("dm arşivi: %w", err)
	}
	if err := q.DeleteUserKeyEvents(ctx, user.PG()); err != nil {
		return fmt.Errorf("anahtar olayları: %w", err)
	}
	if err := q.DeleteE2EState(ctx, user.PG()); err != nil {
		return fmt.Errorf("e2e durumu: %w", err)
	}
	if err := q.EndUserSessions(ctx, store.EndUserSessionsParams{UserID: user.PG(), EndedAt: ts(time.Now().UTC())}); err != nil {
		return fmt.Errorf("oturumlar: %w", err)
	}
	return nil
}

// RetentionResult counts what one retention pass deleted.
type RetentionResult struct {
	Envelopes, Shares, LinkRequests, EndedSessions, KeyEvents, Devices, States int64
}

// Retain runs one retention pass (ADR-0018 item 9).
func (s *Service) Retain(ctx context.Context) (RetentionResult, error) {
	var r RetentionResult
	now := s.now()
	q := store.New(s.cfg.Pool)
	for {
		n, err := q.PurgeEnvelopes(ctx, store.PurgeEnvelopesParams{Before: ts(now.Add(-EnvelopeRetention)), N: retentionBatch})
		if err != nil {
			return r, fmt.Errorf("zarf saklama: %w", err)
		}
		r.Envelopes += n
		if n < retentionBatch {
			break
		}
	}
	var err error
	if r.Shares, err = q.PurgeHistoryKeyShares(ctx, ts(now.Add(-ShareRetention))); err != nil {
		return r, fmt.Errorf("paylaşım saklama: %w", err)
	}
	if r.LinkRequests, err = q.PurgeLinkRequests(ctx, ts(now)); err != nil {
		return r, fmt.Errorf("eşleştirme saklama: %w", err)
	}
	if r.EndedSessions, err = q.PurgeEndedSessions(ctx, ts(now.Add(-EndedSessionRetention))); err != nil {
		return r, fmt.Errorf("oturum saklama: %w", err)
	}
	if r.KeyEvents, err = q.PurgeKeyEvents(ctx, ts(now.Add(-KeyEventRetention))); err != nil {
		return r, fmt.Errorf("anahtar olayı saklama: %w", err)
	}
	unseen, err := q.ListUnseenDevices(ctx, store.ListUnseenDevicesParams{Before: ts(now.Add(-DeviceRetention)), N: retentionBatch})
	if err != nil {
		return r, fmt.Errorf("görünmeyen cihazlar: %w", err)
	}
	for _, d := range unseen {
		err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			user := uuidOf(d.UserID)
			if _, err := lockState(ctx, q, user); err != nil {
				return err
			}
			g, err := q.DeleteDevice(ctx, store.DeleteDeviceParams(d))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("cihaz silme: %w", err)
			}
			r.Devices++
			return removed(ctx, q, user, uuidOf(g.ID), g.DeviceNumber, g.PairedAt.Valid, now)
		})
		if err != nil {
			return r, err
		}
	}
	if r.States, err = q.PurgeEndedE2EStates(ctx, ts(now.Add(-EndedSessionRetention))); err != nil {
		return r, fmt.Errorf("e2e durumu saklama: %w", err)
	}
	return r, nil
}

// RunRetention runs Retain every hour until ctx is done. Logs carry counts
// only.
func (s *Service) RunRetention(ctx context.Context) error {
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		r, err := s.Retain(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.cfg.Log.WarnContext(ctx, "e2e saklama işi başarısız", slog.String("hata", err.Error()))
		case r != (RetentionResult{}):
			s.cfg.Log.InfoContext(ctx, "e2e saklama işi",
				slog.Int64("zarf", r.Envelopes), slog.Int64("paylasim", r.Shares), slog.Int64("eslestirme", r.LinkRequests),
				slog.Int64("oturum", r.EndedSessions), slog.Int64("anahtar_olayi", r.KeyEvents), slog.Int64("cihaz", r.Devices),
				slog.Int64("durum", r.States))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
