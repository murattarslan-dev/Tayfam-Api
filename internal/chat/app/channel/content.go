package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Message kinds (channel_messages.kind; issue #205, ADR-0031).
const (
	KindText  = "text"
	KindPhoto = "photo"
	KindPlace = "place"
)

// PlaceNameMax is the longest place name (the same limit as an event's
// meeting point, data model v15).
const PlaceNameMax = 120

// Place is a point a user chose to share: content, not the device's
// location (ADR-0008). It is encrypted like a body and never logged.
type Place struct {
	Latitude, Longitude float64
	// Name is optional.
	Name string
}

// Photos attaches a channel message's photo in core with the sender's own
// token (core MediaService.AttachChannelPhoto; ADR-0031). It returns
// apperr.ErrUploadNotFound, apperr.ErrUploadNotReady, apperr.ErrUploadInUse
// or apperr.ErrUnavailable.
type Photos interface {
	AttachChannelPhoto(ctx context.Context, upload, tayfa, message id.ID) error
}

// isUniqueViolation reports whether err is a unique violation of the
// named constraint (index).
func isUniqueViolation(err error, constraint string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == constraint
}

// validPlace checks a place; the error names the field.
func validPlace(p *Place) error {
	switch {
	case p == nil:
		return invalid("place")
	case math.IsNaN(p.Latitude) || p.Latitude < -90 || p.Latitude > 90:
		return invalid("place.latitude")
	case math.IsNaN(p.Longitude) || p.Longitude < -180 || p.Longitude > 180:
		return invalid("place.longitude")
	}
	if p.Name == "" {
		return nil
	}
	n := strings.TrimSpace(p.Name)
	if n == "" || !utf8.ValidString(n) || utf8.RuneCountInString(n) > PlaceNameMax {
		return invalid("place.name")
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return invalid("place.name")
		}
	}
	p.Name = n
	return nil
}

// placeData is the encrypted form of a place. Field names are short and
// fixed: the data never leaves chat in this form.
type placeData struct {
	Lat  float64 `json:"lat"`
	Lng  float64 `json:"lng"`
	Name string  `json:"name,omitempty"`
}

// maxPlaceBytes bounds an encoded place: two numbers and a name of at
// most PlaceNameMax characters (4 bytes each, JSON escaping up to 6 per
// byte) fit well within it, and it stays under the stored body's limit
// (channel_messages_body_len).
const maxPlaceBytes = 4096

func encodePlace(p Place) (string, error) {
	b, err := json.Marshal(placeData{Lat: p.Latitude, Lng: p.Longitude, Name: p.Name})
	if err != nil {
		return "", fmt.Errorf("yer: %w", err)
	}
	if len(b) > maxPlaceBytes {
		return "", invalid("place.name")
	}
	return string(b), nil
}

// DecodePlace reads a decrypted place (also for the KVKK export).
func DecodePlace(s string) (*Place, error) {
	var d placeData
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		// Never the value: it is a location.
		return nil, errors.New("yer çözülemedi")
	}
	return &Place{Latitude: d.Lat, Longitude: d.Lng, Name: d.Name}, nil
}

// normalizeSend checks a message to send by its kind and returns the text to
// encrypt (the body, or the encoded place; empty for a photo).
func normalizeSend(in *SendInput) (string, error) {
	switch in.Kind {
	case "", KindText:
		in.Kind = KindText
		switch {
		case in.PhotoUploadID != (id.ID{}):
			return "", invalid("photo_upload_id")
		case in.Place != nil:
			return "", invalid("place")
		case !validBody(in.Body):
			return "", invalid("body")
		}
		return in.Body, nil
	case KindPhoto:
		switch {
		case in.Body != "":
			return "", invalid("body")
		case in.Place != nil:
			return "", invalid("place")
		case in.PhotoUploadID == (id.ID{}):
			return "", invalid("photo_upload_id")
		}
		return "", nil
	case KindPlace:
		switch {
		case in.Body != "":
			return "", invalid("body")
		case in.PhotoUploadID != (id.ID{}):
			return "", invalid("photo_upload_id")
		}
		if err := validPlace(in.Place); err != nil {
			return "", err
		}
		return encodePlace(*in.Place)
	}
	return "", invalid("kind")
}

// MediaItem asks whether a photo attached to a channel message is
// visible (MessageMediaService; ADR-0031).
type MediaItem struct {
	Upload, Message id.ID
}

// VisiblePhotos returns the uploads among items that caller may see: the
// message is in a channel the caller may read (the same rule as Messages),
// it is not deleted and its photo is the upload. Anything else is left out
// without a reason. core asks this before it signs a link.
func (s *Service) VisiblePhotos(ctx context.Context, caller id.ID, items []MediaItem) (map[id.ID]bool, error) {
	out := make(map[id.ID]bool, len(items))
	if s.cfg.Cipher == nil {
		return out, nil
	}
	q := store.New(s.cfg.Pool)
	// One access answer per tayfa and one channel check per channel.
	decisions := map[id.ID]*Decision{}
	channels := map[id.ID]bool{}
	for _, it := range items {
		r, err := q.GetChannelPhotoMessage(ctx, it.Message.PG())
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			continue
		case err != nil:
			return nil, fmt.Errorf("fotoğraf mesajı: %w", err)
		}
		if r.DeletedAt.Valid || !r.PhotoUploadID.Valid || id.ID(r.PhotoUploadID.Bytes) != it.Upload {
			continue
		}
		tayfa, ch := id.ID(r.TayfaID.Bytes), id.ID(r.ChannelID.Bytes)
		seen, ok := channels[ch]
		if !ok {
			d, ok := decisions[tayfa]
			if !ok {
				dv, err := s.access(ctx, caller, tayfa)
				switch {
				case errors.Is(err, apperr.ErrTayfaNotFound):
					d = nil
				case err != nil:
					return nil, err
				default:
					d = &dv
				}
				decisions[tayfa] = d
			}
			seen = false
			if d != nil && authorize(*d, ActionView) == nil {
				_, err := s.loadChannel(ctx, q, *d, tayfa, ch, false)
				switch {
				case err == nil:
					seen = true
				case errors.Is(err, apperr.ErrChannelNotFound):
				default:
					return nil, err
				}
			}
			channels[ch] = seen
		}
		if seen {
			out[it.Upload] = true
		}
	}
	return out, nil
}

// PhotoHolders returns, for each of uploads some channel message holds,
// that message's ID (core's reconciliation, MediaHolderService; ADR-0031).
// A hidden message still holding its photo counts. It needs no key and no
// user: only core's service token reaches it.
func (s *Service) PhotoHolders(ctx context.Context, uploads []id.ID) (map[id.ID]id.ID, error) {
	pg := make([]pgtype.UUID, len(uploads))
	for i, u := range uploads {
		pg[i] = u.PG()
	}
	rows, err := store.New(s.cfg.Pool).FindChannelPhotoHolders(ctx, pg)
	if err != nil {
		return nil, fmt.Errorf("fotoğraf sahipleri: %w", err)
	}
	out := make(map[id.ID]id.ID, len(rows))
	for _, r := range rows {
		out[id.ID(r.UploadID.Bytes)] = id.ID(r.ID.Bytes)
	}
	return out, nil
}
