package keys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// RegisterInput is a device registration.
type RegisterInput struct {
	RegistrationID  uint32
	IdentityKey     []byte
	SignedPreKey    SignedPreKey
	KyberLastResort SignedPreKey
	OneTime         []PreKey
	Kyber           []SignedPreKey
}

// UploadInput adds pre-keys and, when set, replaces the signed and
// last-resort Kyber pre-keys.
type UploadInput struct {
	SignedPreKey    *SignedPreKey
	KyberLastResort *SignedPreKey
	OneTime         []PreKey
	Kyber           []SignedPreKey
}

func validOneTime(ec []PreKey, kyber []SignedPreKey) error {
	if len(ec) > MaxPreKeysPerCall {
		return invalid("one_time_prekeys")
	}
	if len(kyber) > MaxPreKeysPerCall {
		return invalid("kyber_prekeys")
	}
	seen := map[uint32]bool{}
	for _, k := range ec {
		if k.ID > MaxPreKeyID || !validEC(k.PublicKey) || seen[k.ID] {
			return invalid("one_time_prekeys")
		}
		seen[k.ID] = true
	}
	clear(seen)
	for _, k := range kyber {
		if !validSigned(k, KyberKeyLen, false) || seen[k.ID] {
			return invalid("kyber_prekeys")
		}
		seen[k.ID] = true
	}
	return nil
}

// addOneTime stores one-time pre-keys of device within the stored limit
// and returns the counts.
func addOneTime(ctx context.Context, q *store.Queries, device id.ID, ec []PreKey, kyber []SignedPreKey) (Counts, error) {
	c, err := counts(ctx, q, device)
	if err != nil {
		return Counts{}, err
	}
	if c.EC+len(ec) > MaxPreKeysStored {
		return Counts{}, invalid("one_time_prekeys")
	}
	if c.Kyber+len(kyber) > MaxPreKeysStored {
		return Counts{}, invalid("kyber_prekeys")
	}
	if len(ec) > 0 {
		p := store.InsertECPreKeysParams{DeviceID: device.PG()}
		for _, k := range ec {
			p.KeyIds = append(p.KeyIds, int32(k.ID)) //nolint:gosec // at most MaxPreKeyID
			p.PublicKeys = append(p.PublicKeys, k.PublicKey)
		}
		if _, err := q.InsertECPreKeys(ctx, p); err != nil {
			return Counts{}, fmt.Errorf("ön anahtarlar: %w", err)
		}
	}
	if len(kyber) > 0 {
		p := store.InsertKyberPreKeysParams{DeviceID: device.PG()}
		for _, k := range kyber {
			p.KeyIds = append(p.KeyIds, int32(k.ID)) //nolint:gosec // at most MaxPreKeyID
			p.PublicKeys = append(p.PublicKeys, k.PublicKey)
			p.Signatures = append(p.Signatures, k.Signature)
		}
		if _, err := q.InsertKyberPreKeys(ctx, p); err != nil {
			return Counts{}, fmt.Errorf("kyber ön anahtarları: %w", err)
		}
	}
	return counts(ctx, q, device)
}

func counts(ctx context.Context, q *store.Queries, device id.ID) (Counts, error) {
	r, err := q.CountOneTimePreKeys(ctx, device.PG())
	if err != nil {
		return Counts{}, fmt.Errorf("ön anahtar sayısı: %w", err)
	}
	return Counts{EC: int(r.Ec), Kyber: int(r.Kyber)}, nil
}

// nextNumber is the device number after last that no device of the user
// has, wrapping to the smallest free one after 127 (ADR-0018 item 1).
func nextNumber(last int16, devices []deviceRow) int16 {
	used := map[int16]bool{}
	for _, d := range devices {
		used[d.DeviceNumber] = true
	}
	for i := range int16(maxDeviceNumber) {
		n := (last+i)%maxDeviceNumber + 1
		if !used[n] {
			return n
		}
	}
	return 0 // unreachable: at most MaxDevices are used
}

// Register registers the caller's session as a device (RegisterDevice).
func (s *Service) Register(ctx context.Context, c Caller, in RegisterInput) (Device, Counts, error) {
	switch {
	case in.RegistrationID < 1 || in.RegistrationID > MaxRegistrationID:
		return Device{}, Counts{}, invalid("registration_id")
	case !validEC(in.IdentityKey):
		return Device{}, Counts{}, invalid("identity_key")
	case !validSigned(in.SignedPreKey, ECKeyLen, true):
		return Device{}, Counts{}, invalid("signed_prekey")
	case !validSigned(in.KyberLastResort, KyberKeyLen, false):
		return Device{}, Counts{}, invalid("kyber_last_resort_prekey")
	}
	if err := validOneTime(in.OneTime, in.Kyber); err != nil {
		return Device{}, Counts{}, err
	}
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return Device{}, Counts{}, apperr.ErrRateLimited
	}
	now := s.now()
	var (
		dev   deviceRow
		count Counts
	)
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		ended, err := q.IsSessionEnded(ctx, c.Session.PG())
		if err != nil {
			return fmt.Errorf("oturum: %w", err)
		}
		st, err := lockState(ctx, q, c.User)
		if err != nil {
			return err
		}
		if ended || (st.SessionsEndedAt.Valid && !c.IssuedAt.After(st.SessionsEndedAt.Time)) {
			return apperr.ErrSessionEnded
		}
		existing, err := q.GetDeviceBySession(ctx, store.GetDeviceBySessionParams{SessionID: c.Session.PG(), UserID: c.User.PG()})
		switch {
		case err == nil:
			// A retry: the same keys return the device; other keys are a
			// new identity and need UnregisterDevice first.
			if !bytes.Equal(existing.IdentityKey, in.IdentityKey) || existing.RegistrationID != int32(in.RegistrationID) { //nolint:gosec // checked above
				return apperr.ErrE2EDeviceAlreadyRegistered
			}
			dev = deviceRow(existing)
			count, err = addOneTime(ctx, q, uuidOf(dev.ID), in.OneTime, in.Kyber)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("cihaz: %w", err)
		}
		devices, err := q.ListUserDevices(ctx, c.User.PG())
		if err != nil {
			return fmt.Errorf("cihazlar: %w", err)
		}
		if len(devices) >= MaxDevices {
			return apperr.ErrE2EDeviceLimit
		}
		number := nextNumber(st.LastDeviceNumber, devices)
		r, err := q.CreateDevice(ctx, store.CreateDeviceParams{
			ID: id.New().PG(), UserID: c.User.PG(), SessionID: c.Session.PG(), DeviceNumber: number,
			RegistrationID: int32(in.RegistrationID), IdentityKey: in.IdentityKey, //nolint:gosec // checked above
			SignedPrekeyID: int32(in.SignedPreKey.ID), SignedPrekey: in.SignedPreKey.PublicKey, SignedPrekeySignature: in.SignedPreKey.Signature, //nolint:gosec // checked above
			KyberPrekeyID: int32(in.KyberLastResort.ID), KyberPrekey: in.KyberLastResort.PublicKey, KyberPrekeySignature: in.KyberLastResort.Signature, //nolint:gosec // checked above
			CreatedAt: ts(now),
		})
		if err != nil {
			return fmt.Errorf("cihaz kaydı: %w", err)
		}
		dev = deviceRow(r)
		if err := q.SetLastDeviceNumber(ctx, store.SetLastDeviceNumberParams{LastDeviceNumber: number, UserID: c.User.PG()}); err != nil {
			return fmt.Errorf("cihaz numarası: %w", err)
		}
		if count, err = addOneTime(ctx, q, uuidOf(dev.ID), in.OneTime, in.Kyber); err != nil {
			return err
		}
		return keyEvent(ctx, q, c.User, uuidOf(dev.ID), number, EventDeviceAdded, now)
	})
	if err != nil {
		return Device{}, Counts{}, err
	}
	return deviceOf(dev), count, nil
}

// State is the caller's device and the user's end-to-end state.
type State struct {
	Device            Device
	Counts            Counts
	HistoryKeyVersion int
	RotationNeeded    bool
	OtherPaired       bool
}

// MyDevice returns the caller's device and state (GetMyDevice).
func (s *Service) MyDevice(ctx context.Context, c Caller) (State, error) {
	q := store.New(s.cfg.Pool)
	d, err := device(ctx, q, c)
	if err != nil {
		return State{}, err
	}
	out := State{Device: deviceOf(d)}
	if out.Counts, err = counts(ctx, q, uuidOf(d.ID)); err != nil {
		return State{}, err
	}
	st, err := q.GetE2EState(ctx, c.User.PG())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return State{}, fmt.Errorf("e2e durumu: %w", err)
	}
	out.HistoryKeyVersion, out.RotationNeeded = int(st.HistoryKeyVersion), st.RotationNeeded
	devices, err := q.ListUserDevices(ctx, c.User.PG())
	if err != nil {
		return State{}, fmt.Errorf("cihazlar: %w", err)
	}
	for _, o := range devices {
		if o.ID != d.ID && o.PairedAt.Valid {
			out.OtherPaired = true
		}
	}
	return out, nil
}

// Unregister removes the caller's device (UnregisterDevice).
func (s *Service) Unregister(ctx context.Context, c Caller) error {
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := lockState(ctx, q, c.User); err != nil {
			return err
		}
		r, err := q.GetDeviceBySession(ctx, store.GetDeviceBySessionParams{SessionID: c.Session.PG(), UserID: c.User.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("cihaz: %w", err)
		}
		d, err := q.DeleteDevice(ctx, store.DeleteDeviceParams{ID: r.ID, UserID: c.User.PG()})
		if err != nil {
			return fmt.Errorf("cihaz silme: %w", err)
		}
		return removed(ctx, q, c.User, uuidOf(d.ID), d.DeviceNumber, d.PairedAt.Valid, s.now())
	})
}

// Upload stores pre-keys of the caller's device (UploadPreKeys).
func (s *Service) Upload(ctx context.Context, c Caller, in UploadInput) (Counts, error) {
	switch {
	case in.SignedPreKey != nil && !validSigned(*in.SignedPreKey, ECKeyLen, true):
		return Counts{}, invalid("signed_prekey")
	case in.KyberLastResort != nil && !validSigned(*in.KyberLastResort, KyberKeyLen, false):
		return Counts{}, invalid("kyber_last_resort_prekey")
	}
	if err := validOneTime(in.OneTime, in.Kyber); err != nil {
		return Counts{}, err
	}
	if !s.cfg.KeyOps.Allow(c.User.String()) {
		return Counts{}, apperr.ErrRateLimited
	}
	now := s.now()
	var out Counts
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		r, err := q.LockDeviceBySession(ctx, store.LockDeviceBySessionParams{SessionID: c.Session.PG(), UserID: c.User.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrE2EDeviceNotRegistered
		case err != nil:
			return fmt.Errorf("cihaz: %w", err)
		}
		if k := in.SignedPreKey; k != nil {
			if err := q.UpdateSignedPreKey(ctx, store.UpdateSignedPreKeyParams{
				SignedPrekeyID: int32(k.ID), SignedPrekey: k.PublicKey, SignedPrekeySignature: k.Signature, At: ts(now), ID: r.ID, //nolint:gosec // checked above
			}); err != nil {
				return fmt.Errorf("imzalı ön anahtar: %w", err)
			}
		}
		if k := in.KyberLastResort; k != nil {
			if err := q.UpdateKyberLastResortPreKey(ctx, store.UpdateKyberLastResortPreKeyParams{
				KyberPrekeyID: int32(k.ID), KyberPrekey: k.PublicKey, KyberPrekeySignature: k.Signature, At: ts(now), ID: r.ID, //nolint:gosec // checked above
			}); err != nil {
				return fmt.Errorf("son çare kyber ön anahtarı: %w", err)
			}
		}
		if out, err = addOneTime(ctx, q, uuidOf(r.ID), in.OneTime, in.Kyber); err != nil {
			return err
		}
		s.touch(ctx, q, uuidOf(r.ID))
		return nil
	})
	return out, err
}

// PublicDevice is a device as others see it.
type PublicDevice struct {
	ID             id.ID
	Number         int
	RegistrationID int
	IdentityKey    []byte
	CreatedAt      time.Time
}

// mayList: the user themselves, a party of a conversation with them, or
// someone who may write to them (ADR-0018 item 2). Anything else is
// apperr.ErrDirectMessageNotAllowed; the reason is never said.
func (s *Service) mayList(ctx context.Context, caller, user id.ID) error {
	if user == (id.ID{}) {
		return invalid("user_id")
	}
	if caller == user {
		return nil
	}
	ok, err := s.cfg.Contacts.HasConversation(ctx, caller, user)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	err = s.cfg.Contacts.MayWrite(ctx, caller, user)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, apperr.ErrDirectMessageNotAllowed), errors.Is(err, apperr.ErrMessageRequestPending),
		errors.Is(err, apperr.ErrMessageRequestLimit):
		return apperr.ErrDirectMessageNotAllowed
	}
	return err
}

// Devices lists user's devices (ListDevices).
func (s *Service) Devices(ctx context.Context, caller, user id.ID) ([]PublicDevice, error) {
	if err := s.mayList(ctx, caller, user); err != nil {
		return nil, err
	}
	rows, err := store.New(s.cfg.Pool).ListUserDevices(ctx, user.PG())
	if err != nil {
		return nil, fmt.Errorf("cihazlar: %w", err)
	}
	out := make([]PublicDevice, 0, len(rows))
	for _, r := range rows {
		out = append(out, PublicDevice{
			ID: uuidOf(r.ID), Number: int(r.DeviceNumber), RegistrationID: int(r.RegistrationID),
			IdentityKey: r.IdentityKey, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, nil
}

// KeyEvent is a change of a user's devices.
type KeyEvent struct {
	ID        id.ID
	DeviceID  id.ID
	Number    int
	Type      string
	CreatedAt time.Time
}

// KeyEvents lists user's key events, newest first (ListKeyEvents).
func (s *Service) KeyEvents(ctx context.Context, caller, user id.ID, size int32, token string) ([]KeyEvent, string, error) {
	n, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	before, err := pageCursor(token)
	if err != nil {
		return nil, "", err
	}
	if err := s.mayList(ctx, caller, user); err != nil {
		return nil, "", err
	}
	rows, err := store.New(s.cfg.Pool).ListKeyEvents(ctx, store.ListKeyEventsParams{UserID: user.PG(), BeforeID: before, N: n + 1})
	if err != nil {
		return nil, "", fmt.Errorf("anahtar olayları: %w", err)
	}
	next := ""
	if int32(len(rows)) > n { //nolint:gosec // at most n+1 rows
		rows = rows[:n]
		next = uuidOf(rows[n-1].ID).String()
	}
	out := make([]KeyEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, KeyEvent{ID: uuidOf(r.ID), DeviceID: uuidOf(r.DeviceID), Number: int(r.DeviceNumber), Type: r.EventType, CreatedAt: r.CreatedAt.Time.UTC()})
	}
	return out, next, nil
}

// Bundle is a pre-key bundle of one device.
type Bundle struct {
	DeviceID        id.ID
	Number          int
	RegistrationID  int
	IdentityKey     []byte
	SignedPreKey    SignedPreKey
	OneTime         *PreKey // nil when none is left
	Kyber           SignedPreKey
	KyberLastResort bool
}

// Bundles returns a bundle per device of user, claiming one-time pre-keys
// (GetPreKeyBundles). Only the user themselves (their other devices) or
// someone who could send them a message now may fetch them.
func (s *Service) Bundles(ctx context.Context, c Caller, user id.ID, only []id.ID) ([]Bundle, error) {
	if user == (id.ID{}) {
		return nil, invalid("user_id")
	}
	if len(only) > MaxDevices {
		return nil, invalid("device_ids")
	}
	if !s.cfg.Bundles.Allow(c.User.String()) {
		return nil, apperr.ErrRateLimited
	}
	if user != c.User {
		if err := s.cfg.Contacts.MayWrite(ctx, c.User, user); err != nil {
			return nil, err
		}
	}
	q := store.New(s.cfg.Pool)
	rows, err := q.ListUserDevices(ctx, user.PG())
	if err != nil {
		return nil, fmt.Errorf("cihazlar: %w", err)
	}
	out := []Bundle{}
	for _, r := range rows {
		d := uuidOf(r.ID)
		if uuidOf(r.SessionID) == c.Session || (len(only) > 0 && !slices.Contains(only, d)) {
			continue
		}
		b := Bundle{
			DeviceID: d, Number: int(r.DeviceNumber), RegistrationID: int(r.RegistrationID), IdentityKey: r.IdentityKey,
			SignedPreKey: SignedPreKey{ID: uint32(r.SignedPrekeyID), PublicKey: r.SignedPrekey, Signature: r.SignedPrekeySignature}, //nolint:gosec // 24-bit IDs
		}
		ec, err := claim(ctx, q, d, "ec")
		if err != nil {
			return nil, err
		}
		if ec != nil {
			b.OneTime = &PreKey{ID: uint32(ec.KeyID), PublicKey: ec.PublicKey} //nolint:gosec // 24-bit IDs
		}
		ky, err := claim(ctx, q, d, "kyber")
		if err != nil {
			return nil, err
		}
		if ky != nil {
			b.Kyber = SignedPreKey{ID: uint32(ky.KeyID), PublicKey: ky.PublicKey, Signature: ky.Signature} //nolint:gosec // 24-bit IDs
		} else {
			b.Kyber = SignedPreKey{ID: uint32(r.KyberPrekeyID), PublicKey: r.KyberPrekey, Signature: r.KyberPrekeySignature} //nolint:gosec // 24-bit IDs
			b.KyberLastResort = true
		}
		out = append(out, b)
		if ec != nil || ky != nil {
			if n, err := counts(ctx, q, d); err == nil && n.Low() {
				s.cfg.Publisher.PreKeysLow(uuidOf(r.SessionID), n)
			}
		}
	}
	return out, nil
}

// claim takes one one-time pre-key of kind from device; nil if none is
// left. Each key is handed out once (FOR UPDATE SKIP LOCKED and delete in
// one statement).
func claim(ctx context.Context, q *store.Queries, device id.ID, kind string) (*store.ClaimOneTimePreKeyRow, error) {
	r, err := q.ClaimOneTimePreKey(ctx, store.ClaimOneTimePreKeyParams{DeviceID: device.PG(), Kind: kind})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil //nolint:nilnil // none left is not an error
	case err != nil:
		return nil, fmt.Errorf("ön anahtar: %w", err)
	}
	return &r, nil
}

// DeviceInfo is what the send path needs of a device.
type DeviceInfo struct {
	ID             id.ID
	Session        id.ID
	Number         int16
	RegistrationID int32
}

func infoOf(d deviceRow) DeviceInfo {
	return DeviceInfo{ID: uuidOf(d.ID), Session: uuidOf(d.SessionID), Number: d.DeviceNumber, RegistrationID: d.RegistrationID}
}

// Recipients returns the caller's device and the devices an end-to-end
// message of sender to recipient must reach: every device of recipient
// and every other device of sender. dm's send path calls it inside its
// transaction.
func Recipients(ctx context.Context, q *store.Queries, sender Caller, recipient id.ID) (DeviceInfo, []DeviceInfo, error) {
	from, err := device(ctx, q, sender)
	if err != nil {
		return DeviceInfo{}, nil, err
	}
	theirs, err := q.ListUserDevices(ctx, recipient.PG())
	if err != nil {
		return DeviceInfo{}, nil, fmt.Errorf("alıcı cihazları: %w", err)
	}
	if len(theirs) == 0 {
		return DeviceInfo{}, nil, apperr.ErrE2ERecipientUnavailable
	}
	mine, err := q.ListUserDevices(ctx, sender.User.PG())
	if err != nil {
		return DeviceInfo{}, nil, fmt.Errorf("gönderen cihazları: %w", err)
	}
	targets := make([]DeviceInfo, 0, len(theirs)+len(mine))
	for _, d := range theirs {
		targets = append(targets, infoOf(d))
	}
	for _, d := range mine {
		if d.ID != from.ID {
			targets = append(targets, infoOf(d))
		}
	}
	return infoOf(from), targets, nil
}

// Target is one device a client addressed: its ID and the registration ID
// the client's session with it has.
type Target struct {
	Device         id.ID
	RegistrationID uint32
}

// Match checks that given covers exactly want: every device once, with
// its current registration ID. Otherwise a DevicesChangedError lists the
// missing and the stale (gone, duplicated or replaced) devices.
func Match(want []DeviceInfo, given []Target) error {
	byID := map[id.ID]DeviceInfo{}
	for _, d := range want {
		byID[d.ID] = d
	}
	var e apperr.DevicesChangedError
	seen := map[id.ID]bool{}
	for _, g := range given {
		d, ok := byID[g.Device]
		if !ok || seen[g.Device] || uint32(d.RegistrationID) != g.RegistrationID { //nolint:gosec // 1..16380
			e.Stale = append(e.Stale, g.Device.String())
		}
		seen[g.Device] = true
	}
	for _, d := range want {
		if !seen[d.ID] {
			e.Missing = append(e.Missing, d.ID.String())
		}
	}
	if len(e.Missing) > 0 || len(e.Stale) > 0 {
		return &e
	}
	return nil
}

// Outgoing is the opaque content of one envelope and its Signal type.
type Outgoing struct {
	Type    int
	Content []byte
}

// StoreEnvelopes writes one envelope per target (Match has checked the
// set) inside dm's send transaction and returns them for live delivery.
func StoreEnvelopes(ctx context.Context, q *store.Queries, message, conversation, sender id.ID, from DeviceInfo, targets []DeviceInfo, content map[id.ID]Outgoing, at time.Time) ([]Delivery, error) {
	out := make([]Delivery, 0, len(targets))
	prev := id.New()
	for _, t := range targets {
		o := content[t.ID]
		e := Envelope{
			ID: prev, MessageID: message, ConversationID: conversation, SenderID: sender, SenderDevice: from.ID,
			SenderNumber: int(from.Number), Type: o.Type, Content: o.Content, CreatedAt: at,
		}
		prev = id.After(prev)
		if err := q.InsertEnvelope(ctx, store.InsertEnvelopeParams{
			ID: e.ID.PG(), MessageID: message.PG(), RecipientDeviceID: t.ID.PG(), SenderID: sender.PG(), SenderDeviceID: from.ID.PG(),
			SenderDeviceNumber: from.Number, MessageType: int16(o.Type), Ciphertext: o.Content, CreatedAt: ts(at), //nolint:gosec // 2 or 3
		}); err != nil {
			return nil, fmt.Errorf("zarf: %w", err)
		}
		out = append(out, Delivery{Session: t.Session, Envelope: e})
	}
	return out, nil
}
