// Package keys is the server side of end-to-end encrypted direct messages
// (E2EService, proto/tayfam/chat/v1/e2e.proto; issue #127; ADR-0018,
// ADR-0005 "Direkt mesajlar").
//
// The protocol is Signal (X3DH/PQXDH and the Double Ratchet) and runs on
// the clients. This package does no cryptography: it checks formats and
// sizes, decides who may fetch or write what, and stores and relays public
// keys, opaque envelopes, archive entries and pairing messages. It never
// decrypts, signs or verifies anything (CLAUDE.md rule 4).
//
// A device is a session (ADR-0018 item 1): the device registered with the
// session (sid) of the caller's token. When the session ends (identity's
// session.revoked, user.sanctioned, user.deleted) the device goes with its
// pre-keys, waiting envelopes, key shares and pairing request, and a
// device_removed key event is written.
//
// Key material, envelopes, archive entries and pairing messages never reach
// logs, events, metrics or error messages (CLAUDE.md rule 3): errors carry
// field names and device IDs only, and logs carry counts.
package keys

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (ADR-0018 item 8; the same CHECKs are in the database).
const (
	MaxDevices        = 5
	maxDeviceNumber   = 127
	MaxRegistrationID = 16380
	MaxPreKeyID       = 1<<24 - 1
	ECKeyLen          = 33
	KyberKeyLen       = 1569
	SignatureLen      = 64
	// ecKeyType is Signal's DJB (Curve25519) key type byte.
	ecKeyType = 0x05
	// MaxPreKeysPerCall and MaxPreKeysStored count one kind (ec or kyber).
	MaxPreKeysPerCall = 100
	MaxPreKeysStored  = 200
	// LowPreKeys: below this many of a kind the device is told to upload.
	LowPreKeys = 10

	MaxEnvelopeBytes = 64 << 10
	MaxEnvelopes     = 16
	// Envelope types: Signal's CiphertextMessageType.
	EnvelopeWhisper = 2
	EnvelopePreKey  = 3

	MaxArchiveBytes   = 32 << 10
	MaxArchivePerCall = 50
	MaxShareBytes     = 16 << 10
	MaxAckIDs         = 100
	maxHistoryVersion = 32767

	LinkLifetime = 10 * time.Minute

	EnvelopeRetention     = 30 * 24 * time.Hour
	ShareRetention        = 30 * 24 * time.Hour
	KeyEventRetention     = 90 * 24 * time.Hour
	DeviceRetention       = 90 * 24 * time.Hour
	EndedSessionRetention = 24 * time.Hour
	// touchEvery bounds writes of a device's last_seen_at.
	touchEvery = time.Hour

	defaultPageSize = 50
	maxPageSize     = 100
	retentionBatch  = 1000
	retentionEvery  = time.Hour
)

// Key event types (user_key_events.event_type).
const (
	EventDeviceAdded   = "device_added"
	EventDeviceRemoved = "device_removed"
)

// Caller is the verified caller: the user, the session of the token and
// when the token was issued.
type Caller struct {
	User     id.ID
	Session  id.ID
	IssuedAt time.Time
}

// Contacts answers, with dm's rules, whether caller may reach user. ctx
// carries the caller's token (core is asked with it).
type Contacts interface {
	// MayWrite returns nil when caller could send recipient a message
	// now, else the reason (apperr.ErrDirectMessageNotAllowed,
	// ErrMessageRequestPending, ErrMessageRequestLimit) or an error.
	MayWrite(ctx context.Context, caller, recipient id.ID) error
	// HasConversation reports whether caller sees a conversation with
	// other.
	HasConversation(ctx context.Context, caller, other id.ID) (bool, error)
}

// Publisher tells live connections of a session (a device) about changes.
// Methods must not block.
type Publisher interface {
	PreKeysLow(session id.ID, c Counts)
	LinkAnswered(session, request id.ID)
	HistoryKeyShared(session id.ID)
}

type nopPublisher struct{}

func (nopPublisher) PreKeysLow(id.ID, Counts)  {}
func (nopPublisher) LinkAnswered(id.ID, id.ID) {}
func (nopPublisher) HistoryKeyShared(id.ID)    {}

// Limit is a per-user rate (ratelimit.Limiter).
type Limit interface {
	Allow(key string) bool
}

// Config wires a Service.
type Config struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Contacts Contacts
	// Bundles limits pre-key bundle fetches, KeyOps registration, uploads,
	// pairing and rotation, Archive archive writes; all per user.
	Bundles   Limit
	KeyOps    Limit
	Archive   Limit
	Publisher Publisher        // optional
	Now       func() time.Time // optional, for tests
}

// Service implements E2EService's business logic.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Contacts == nil || cfg.Bundles == nil || cfg.KeyOps == nil || cfg.Archive == nil {
		return nil, errors.New("keys: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Publisher == nil {
		cfg.Publisher = nopPublisher{}
	}
	return &Service{cfg: cfg}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// SignedPreKey is a signed EC pre-key or a Kyber pre-key.
type SignedPreKey struct {
	ID        uint32
	PublicKey []byte
	Signature []byte
}

// PreKey is a one-time EC pre-key.
type PreKey struct {
	ID        uint32
	PublicKey []byte
}

// Device is the caller's own device.
type Device struct {
	ID             id.ID
	Number         int
	RegistrationID int
	IdentityKey    []byte
	Paired         bool
	CreatedAt      time.Time
}

// Counts are the one-time pre-keys left on a device.
type Counts struct {
	EC, Kyber int
}

// Low reports whether fewer than LowPreKeys of a kind are left.
func (c Counts) Low() bool { return c.EC < LowPreKeys || c.Kyber < LowPreKeys }

// deviceRow is a device row; every device query returns these columns.
type deviceRow = store.ListUserDevicesRow

func uuidOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func invalid(field string) error { return apperr.Validation(field) }

func deviceOf(r deviceRow) Device {
	return Device{
		ID: uuidOf(r.ID), Number: int(r.DeviceNumber), RegistrationID: int(r.RegistrationID),
		IdentityKey: r.IdentityKey, Paired: r.PairedAt.Valid, CreatedAt: r.CreatedAt.Time.UTC(),
	}
}

func pageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, invalid("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

func pageCursor(token string) (pgtype.UUID, error) {
	if token == "" {
		return pgtype.UUID{}, nil
	}
	v, ok := id.Parse(token)
	if !ok {
		return pgtype.UUID{}, invalid("page_token")
	}
	return v.PG(), nil
}

// Format checks (ADR-0018 item 8). They check shape only, not keys.

func validEC(b []byte) bool { return len(b) == ECKeyLen && b[0] == ecKeyType }

func validSigned(k SignedPreKey, keyLen int, ec bool) bool {
	if k.ID > MaxPreKeyID || len(k.Signature) != SignatureLen {
		return false
	}
	if ec {
		return validEC(k.PublicKey)
	}
	return len(k.PublicKey) == keyLen
}

// ValidEnvelope checks one envelope of a send: its type and size.
func ValidEnvelope(typ int, content []byte) bool {
	return (typ == EnvelopeWhisper || typ == EnvelopePreKey) && len(content) >= 1 && len(content) <= MaxEnvelopeBytes
}

// device returns the caller's device, or ErrE2EDeviceNotRegistered.
func device(ctx context.Context, q *store.Queries, c Caller) (deviceRow, error) {
	r, err := q.GetDeviceBySession(ctx, store.GetDeviceBySessionParams{SessionID: c.Session.PG(), UserID: c.User.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return deviceRow{}, apperr.ErrE2EDeviceNotRegistered
	case err != nil:
		return deviceRow{}, fmt.Errorf("cihaz: %w", err)
	}
	return deviceRow(r), nil
}

// lockState locks the user's E2E state, creating it if needed.
func lockState(ctx context.Context, q *store.Queries, user id.ID) (store.LockE2EStateRow, error) {
	if err := q.EnsureE2EState(ctx, user.PG()); err != nil {
		return store.LockE2EStateRow{}, fmt.Errorf("e2e durumu: %w", err)
	}
	st, err := q.LockE2EState(ctx, user.PG())
	if err != nil {
		return store.LockE2EStateRow{}, fmt.Errorf("e2e durumu: %w", err)
	}
	return st, nil
}

// touch records that the device was seen, at most once per touchEvery.
func (s *Service) touch(ctx context.Context, q *store.Queries, d id.ID) {
	now := s.now()
	if err := q.TouchDevice(ctx, store.TouchDeviceParams{Now: ts(now), ID: d.PG(), StaleBefore: ts(now.Add(-touchEvery))}); err != nil && ctx.Err() == nil {
		s.cfg.Log.WarnContext(ctx, "cihaz görülme zamanı yazılamadı", slog.String("hata", err.Error()))
	}
}

// keyEvent writes a key event of user's device.
func keyEvent(ctx context.Context, q *store.Queries, user, device id.ID, number int16, typ string, at time.Time) error {
	if err := q.InsertKeyEvent(ctx, store.InsertKeyEventParams{
		ID: id.New().PG(), UserID: user.PG(), DeviceID: device.PG(), DeviceNumber: number, EventType: typ, CreatedAt: ts(at),
	}); err != nil {
		return fmt.Errorf("anahtar olayı: %w", err)
	}
	return nil
}

// removed records a removed device of user (its rows are gone already, by
// cascade): a device_removed key event and, when it was paired and paired
// devices remain, a needed history key rotation (ADR-0018 item 6). The
// caller holds the user's state lock.
func removed(ctx context.Context, q *store.Queries, user, device id.ID, number int16, paired bool, at time.Time) error {
	if err := keyEvent(ctx, q, user, device, number, EventDeviceRemoved, at); err != nil {
		return err
	}
	rest, err := q.ListUserDevices(ctx, user.PG())
	if err != nil {
		return fmt.Errorf("cihazlar: %w", err)
	}
	anyPaired := false
	for _, d := range rest {
		anyPaired = anyPaired || d.PairedAt.Valid
	}
	switch {
	case paired && anyPaired:
		err = q.SetRotationNeeded(ctx, store.SetRotationNeededParams{RotationNeeded: true, UserID: user.PG()})
	case !anyPaired:
		// Nobody left to rotate; the next device claims a new key.
		err = q.SetRotationNeeded(ctx, store.SetRotationNeededParams{RotationNeeded: false, UserID: user.PG()})
	}
	if err != nil {
		return fmt.Errorf("anahtar yenileme işareti: %w", err)
	}
	return nil
}
