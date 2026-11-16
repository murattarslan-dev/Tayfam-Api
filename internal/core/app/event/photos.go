package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Event album (issue #206, ADR-0028; Notion "Faz 2 (v2)" §2).
const (
	// PhotosPerUser and PhotosPerAlbum bound the undeleted photos of one
	// person on one event and of one event (the event_photos_guard trigger
	// checks them again).
	PhotosPerUser  = 30
	PhotosPerAlbum = 500
	// albumAfterEnd is how long after the end the album takes photos.
	albumAfterEnd = 7 * 24 * time.Hour
	captionMax    = 200
)

// PhotoRemoved is the audit action of removing someone else's photo.
const PhotoRemoved = "event.photo_removed"

// Photo is a photo of an event's album.
type Photo struct {
	ID         id.ID
	EventID    id.ID
	UploaderID id.ID
	UploadID   id.ID
	// Caption is "" when there is none.
	Caption   string
	CreatedAt time.Time
}

// PhotoInput is a new album photo.
type PhotoInput struct {
	// UploadID is the caller's own READY event_photo upload opened for the
	// tayfa.
	UploadID        id.ID
	Caption         string
	ClientRequestID string
}

// cleanCaption trims the caption; empty means none.
func cleanCaption(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	return cleanText(s, "caption", 1, captionMax, false)
}

// albumOpen reports whether the album of ev takes photos at now: from the
// start until seven days after the end.
func albumOpen(ev store.GetEventRow, now time.Time) bool {
	return !now.Before(ev.StartsAt.Time) && now.Before(ev.EndsAt.Time.Add(albumAfterEnd))
}

// AddPhoto adds the caller's photo to the event's album. The caller must
// have verified attendance; the album is open from the start until seven
// days after the end, not on a cancelled event. At most PhotosPerUser per
// person and PhotosPerAlbum per album. A repeat with the same
// ClientRequestID returns the first photo.
func (s *Service) AddPhoto(ctx context.Context, user, tayfa, event id.ID, in PhotoInput) (Photo, error) {
	caption, err := cleanCaption(in.Caption)
	if err != nil {
		return Photo{}, err
	}
	if in.UploadID == (id.ID{}) {
		return Photo{}, apperr.Validation("upload_id")
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Photo{}, apperr.Validation("client_request_id")
	}
	var out Photo
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller, _ *batch) error {
		if err := c.authorize(authz.EventPhotoAdd); err != nil {
			return err
		}
		// Locked: concurrent additions to one event queue up, so the
		// limits hold.
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		prev, err := q.GetEventPhotoByClientRequest(ctx, store.GetEventPhotoByClientRequestParams{UploaderID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid || id.ID(prev.EventID.Bytes) != event {
				return apperr.Validation("client_request_id")
			}
			r, err := q.GetEventPhoto(ctx, store.GetEventPhotoParams{EventID: event.PG(), ID: prev.ID})
			if err != nil {
				return fmt.Errorf("albüm fotoğrafı: %w", err)
			}
			out = photoOf(r)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		if ev.Status == StatusCancelled {
			return apperr.ErrEventCancelled
		}
		if !albumOpen(ev, c.now) {
			return apperr.ErrAlbumClosed
		}
		r, ok, err := rsvpRow(ctx, q, event, user)
		if err != nil {
			return err
		}
		if !ok || r.Attendance != AttendanceAttended {
			return apperr.ErrNotAttended
		}
		n, err := q.CountEventPhotos(ctx, store.CountEventPhotosParams{EventID: event.PG(), UploaderID: user.PG()})
		if err != nil {
			return fmt.Errorf("albüm: %w", err)
		}
		if n.Own >= PhotosPerUser || n.Total >= PhotosPerAlbum {
			return apperr.ErrEventLimit
		}
		pid := id.New()
		// Attached first; the column's trigger checks it again.
		if err := media.Attach(ctx, q, user, in.UploadID, media.PurposeEventPhoto, tayfa,
			media.Target{Type: media.TargetEventPhoto, ID: pid}, c.now); err != nil {
			return err
		}
		p := store.CreateEventPhotoParams{
			ID: pid.PG(), EventID: event.PG(), UploaderID: user.PG(), UploadID: in.UploadID.PG(),
			ClientRequestID: crid.PG(), CreatedAt: ts(c.now),
		}
		if caption != "" {
			p.Caption = pgtype.Text{String: caption, Valid: true}
		}
		if err := q.CreateEventPhoto(ctx, p); err != nil {
			return photoInsertErr(err)
		}
		out = Photo{ID: pid, EventID: event, UploaderID: user, UploadID: in.UploadID, Caption: caption, CreatedAt: c.now}
		return nil
	})
	if err != nil {
		return Photo{}, err
	}
	return out, nil
}

// ListPhotos lists the event's album, oldest first. Every active member
// reads it, also while muted and on a cancelled event.
func (s *Service) ListPhotos(ctx context.Context, user, tayfa, event id.ID, size int32, token string) ([]Photo, string, error) {
	size, err := pageSize(size)
	if err != nil {
		return nil, "", err
	}
	after, at, err := decodeCursor(token, "ListEventPhotos", tayfa, event.String())
	if err != nil {
		return nil, "", err
	}
	c, err := s.load(ctx, s.q, user, tayfa, false)
	if err != nil {
		return nil, "", err
	}
	if err := c.authorize(authz.EventView); err != nil {
		return nil, "", err
	}
	if _, err := getEvent(ctx, s.q, tayfa, event); err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListEventPhotos(ctx, store.ListEventPhotosParams{EventID: event.PG(), AfterTime: ts(at), AfterID: after.PG(), PageSize: size + 1})
	if err != nil {
		return nil, "", fmt.Errorf("albüm: %w", err)
	}
	next := ""
	if len(rows) > int(size) {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = encodeCursor("ListEventPhotos", tayfa, event.String(), last.CreatedAt.Time, id.ID(last.ID.Bytes))
	}
	out := make([]Photo, len(rows))
	for i, r := range rows {
		out[i] = photoOf(store.GetEventPhotoRow(r))
	}
	return out, next, nil
}

// RemovePhoto removes an album photo (soft delete; the file goes with the
// row after the retention period): one's own, any photo of one's own event
// for the organizer, any photo for moderators and admins. No role
// hierarchy applies ("her fotoğrafı", ADR-0028). Works on cancelled and
// ended events too.
func (s *Service) RemovePhoto(ctx context.Context, user, tayfa, event, photo id.ID) error {
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller, _ *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		r, err := q.GetEventPhoto(ctx, store.GetEventPhotoParams{EventID: event.PG(), ID: photo.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrEventItemNotFound
		}
		if err != nil {
			return fmt.Errorf("albüm fotoğrafı: %w", err)
		}
		own := id.ID(r.UploaderID.Bytes) == user
		if err := c.removePhoto(ev, own); err != nil {
			return err
		}
		n, err := q.DeleteEventPhoto(ctx, store.DeleteEventPhotoParams{DeletedAt: ts(c.now), DeletedBy: user.PG(), EventID: event.PG(), ID: photo.PG()})
		if err != nil {
			return fmt.Errorf("albüm fotoğrafı: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		if own {
			return nil
		}
		// Someone else's photo: the tayfa's audit log records who removed
		// it (not the caption).
		return audit(ctx, q, c, PhotoRemoved, event, map[string]any{
			"photo_id": photo.String(), "uploader_id": id.ID(r.UploaderID.Bytes).String(),
		})
	})
}

// removePhoto checks the removal of a photo of ev: the uploader's own
// (EventPhotoDeleteOwn, allowed while muted), the organizer's on their own
// event (EventPhotoDeleteOwnEvent), anyone's (EventPhotoDeleteAny).
func (c caller) removePhoto(ev store.GetEventRow, own bool) error {
	if own {
		return c.authorize(authz.EventPhotoDeleteOwn)
	}
	if c.authorize(authz.EventPhotoDeleteAny) == nil {
		return nil
	}
	if ev.CreatedBy.Valid && id.ID(ev.CreatedBy.Bytes) == c.user {
		return c.authorize(authz.EventPhotoDeleteOwnEvent)
	}
	return c.authorize(authz.EventPhotoDeleteAny)
}

// photoInsertErr maps a refusal of event_photos_guard (the database's
// second line, ADR-0028 §2) to the API error the app check would have
// given; anything else stays an internal error.
func photoInsertErr(err error) error {
	switch store.SQLState(err) {
	case store.CodePhotoLimit:
		return apperr.ErrEventLimit
	case store.CodePhotoNotAttended:
		return apperr.ErrNotAttended
	case store.CodePhotoEventClosed:
		return apperr.ErrEventCancelled
	}
	return fmt.Errorf("albüm fotoğrafı: %w", err)
}

func photoOf(r store.GetEventPhotoRow) Photo {
	return Photo{
		ID: id.ID(r.ID.Bytes), EventID: id.ID(r.EventID.Bytes), UploaderID: id.ID(r.UploaderID.Bytes),
		UploadID: id.ID(r.UploadID.Bytes), Caption: r.Caption.String, CreatedAt: r.CreatedAt.Time.UTC(),
	}
}
