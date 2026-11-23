// Package garage is the member garage — üye garajı (issue #272; ADR-0040
// §4). A garage belongs to the person, not to a tayfa: one garage per
// person, at most MaxVehicles vehicles, each with a make, a model, a model
// year, a colour, a short note and at most MaxPhotos photos. There is no
// plate field: a plate is never kept in the garage. There is no sale or
// listing field.
//
// The garage shows in a tayfa when the owner chose so for that tayfa
// (garage_tayfa_visibility); without a choice it shows in tayfas whose
// interests include "automotive". The SQL function garage_visible_in is the
// one place this rule is read. In a tayfa it is the `garage` module (its
// home widget "garage" is the module's showcase): only the tayfa's active
// members see it, and only the vehicles of owners who are active members.
// A vehicle's photos are seen by the same people (package media,
// garage_vehicle_seen_by).
//
// Who may do what:
//
//	own garage (list, add, edit, delete)   the person; no tayfa involved
//	read a tayfa's garage                  every active member (TayfaView)
//	show one's garage in a tayfa           a member, not muted (GarageShow)
//	hide one's garage in a tayfa           every active member (TayfaView)
//	remove a vehicle shown in a tayfa      moderators and admins (GarageRemoveAny)
//
// The vehicles component copies a garage vehicle into an event (ForEvent);
// the plate is written on the event's record only.
//
// Deleting is a soft delete; the retention job deletes for good 30 days
// later. The owner's own delete drops the photos at once; a moderator's or
// the platform's removal only hides the vehicle. On account deletion the
// vehicles and the visibility choices are deleted (KVKK). Texts are
// content: they never reach a log, an audit record or an event; IDs do.
// Export lists the vehicles and the visibility choices (package export).
package garage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (ADR-0040 §4; the same CHECKs are in the database).
const (
	MakeMax  = 40
	ModelMax = 40
	ColorMax = 30
	NoteMax  = 200
	// ModelYearMin is the earliest model year; the latest is next year.
	ModelYearMin = 1900
	// MaxPhotos is how many photos a vehicle has at most.
	MaxPhotos = 3
	// MaxVehicles bounds the live vehicles of one person's garage.
	MaxVehicles = 10
	// DefaultPageSize and MaxPageSize bound a tayfa garage page.
	DefaultPageSize = 20
	MaxPageSize     = 50
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
	// maxPageToken bounds a page token before decoding it.
	maxPageToken = 512
)

// Validation fields.
const (
	FieldMake            = "make"
	FieldModel           = "model"
	FieldModelYear       = "model_year"
	FieldColor           = "color"
	FieldNote            = "note"
	FieldPhotoIDs        = "photo_upload_ids"
	FieldVehicleID       = "vehicle_id"
	FieldOwnerID         = "owner_id"
	FieldPageSize        = "page_size"
	FieldPageToken       = "page_token"
	FieldClientRequestID = "client_request_id"
)

// AuditRemoved is the audit action of a moderator's removal. The record
// names the vehicle by ID only.
const AuditRemoved = "garage.vehicle_removed"

// targetType is the audit record's subject type (and the report target).
const targetType = "garage_vehicle"

// Vehicle is a garage vehicle as the caller sees it.
type Vehicle struct {
	ID        id.ID
	Owner     id.ID
	Make      string
	Model     string
	ModelYear int32
	Color     string
	Note      string
	// Photos are in order.
	Photos    []id.ID
	CreatedAt time.Time
	UpdatedAt time.Time
	// CallerCanEdit: the caller is the owner.
	CallerCanEdit bool
	// CallerCanRemove: the caller may remove it from the tayfa's garage now
	// (List only).
	CallerCanRemove bool
}

// Input is a vehicle as its owner writes it.
type Input struct {
	Make      string
	Model     string
	ModelYear int32
	Color     string
	Note      string
	Photos    []id.ID
}

// Mine is the caller's own garage.
type Mine struct {
	// Vehicles are in the order they were added.
	Vehicles []Vehicle
	// CallerCanAdd: fewer than MaxVehicles vehicles.
	CallerCanAdd bool
}

// Filter narrows a tayfa garage list. Zero values are no filter.
type Filter struct {
	Owner     id.ID
	PageSize  int32
	PageToken string
}

// Page is a page of a tayfa's garage.
type Page struct {
	// Vehicles are the newest first.
	Vehicles []Vehicle
	// NextPageToken is "" on the last page.
	NextPageToken string
	// CallerVisible: the caller's own garage shows in this tayfa.
	CallerVisible bool
}

// Copy is what the vehicles component copies from a garage vehicle.
type Copy struct {
	Make      string
	Model     string
	ModelYear int32
	Color     string
}

// Service serves the member garage, built on the component framework
// (component.Service.Personal for the garage, Tayfa for a tayfa's view).
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// CleanText returns a text as stored: one line, trimmed, whitespace (line
// breaks and tabs too) folded to single spaces. "" is returned for an
// empty text. Refused: invalid UTF-8, any other control or format
// character, more than max characters.
func CleanText(s string, max int) (string, bool) {
	if len(s) > (max+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, func(r rune) bool {
		if r == '\n' || r == '\r' || r == '\t' {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(t) > max {
		return "", false
	}
	return t, true
}

// checkTerms is the one place a garage vehicle meets the abuse term check
// (ADR-0012; ADR-0040 ortak kurallar: "serbest metin kötüye kullanım
// filtresinden geçer"). The term list and its check are not in core yet;
// when they come, the texts are checked here and nowhere else.
func checkTerms(...string) error { return nil }

// cleanPhotos checks photos: 0–MaxPhotos, no repeats; the order is kept.
func cleanPhotos(photos []id.ID) ([]id.ID, error) {
	if len(photos) > MaxPhotos {
		return nil, apperr.Validation(FieldPhotoIDs)
	}
	out := make([]id.ID, 0, len(photos))
	for _, p := range photos {
		if p == (id.ID{}) || slices.Contains(out, p) {
			return nil, apperr.Validation(FieldPhotoIDs)
		}
		out = append(out, p)
	}
	return out, nil
}

// clean checks an input at now (the upper bound of the model year); the
// error names the field.
func clean(in Input, now time.Time) (Input, error) {
	var (
		out Input
		ok  bool
	)
	if out.Make, ok = CleanText(in.Make, MakeMax); !ok || out.Make == "" {
		return Input{}, apperr.Validation(FieldMake)
	}
	if out.Model, ok = CleanText(in.Model, ModelMax); !ok {
		return Input{}, apperr.Validation(FieldModel)
	}
	if out.Color, ok = CleanText(in.Color, ColorMax); !ok {
		return Input{}, apperr.Validation(FieldColor)
	}
	if out.Note, ok = CleanText(in.Note, NoteMax); !ok {
		return Input{}, apperr.Validation(FieldNote)
	}
	if in.ModelYear != 0 && (in.ModelYear < ModelYearMin || int(in.ModelYear) > now.Year()+1) {
		return Input{}, apperr.Validation(FieldModelYear)
	}
	out.ModelYear = in.ModelYear
	photos, err := cleanPhotos(in.Photos)
	if err != nil {
		return Input{}, err
	}
	out.Photos = photos
	if err := checkTerms(out.Make, out.Model, out.Color, out.Note); err != nil {
		return Input{}, err
	}
	return out, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func int4(n int32) pgtype.Int4 { return pgtype.Int4{Int32: n, Valid: n != 0} }

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

// photoColumns spreads photos over the three columns.
func photoColumns(photos []id.ID) (p [MaxPhotos]pgtype.UUID) {
	for i, x := range photos {
		if i < MaxPhotos {
			p[i] = x.PG()
		}
	}
	return p
}

// photosOf reads the three columns back, in order.
func photosOf(cols ...pgtype.UUID) []id.ID {
	out := []id.ID{}
	for _, c := range cols {
		if c.Valid {
			out = append(out, id.ID(c.Bytes))
		}
	}
	return out
}

// view is a stored vehicle as user sees it.
func view(r store.ListOwnGarageVehiclesRow, user id.ID) Vehicle {
	v := Vehicle{
		ID: id.ID(r.ID.Bytes), Owner: id.ID(r.OwnerID.Bytes), Make: r.Make, Model: r.Model.String,
		ModelYear: r.ModelYear.Int32, Color: r.Color.String, Note: r.Note.String,
		Photos:    photosOf(r.Photo1UploadID, r.Photo2UploadID, r.Photo3UploadID),
		CreatedAt: timeOf(r.CreatedAt), UpdatedAt: timeOf(r.UpdatedAt),
	}
	v.CallerCanEdit = v.Owner == user
	return v
}

// attach attaches the owner's uploads to the vehicle before its row is
// written (the photo columns' triggers check it again); the photos it
// already has are left as they are.
func attach(ctx context.Context, q *store.Queries, owner id.ID, photos, have []id.ID, vehicle id.ID, now time.Time) error {
	for _, p := range photos {
		if slices.Contains(have, p) {
			continue
		}
		if err := media.Attach(ctx, q, owner, p, media.PurposeGarageVehicle, id.ID{},
			media.Target{Type: media.TargetGarageVehicle, ID: vehicle}, now); err != nil {
			return err
		}
	}
	return nil
}

// own reads the caller's live vehicles; with vehicle set, only that one.
func own(ctx context.Context, q *store.Queries, user, vehicle id.ID) ([]Vehicle, error) {
	p := store.ListOwnGarageVehiclesParams{OwnerID: user.PG()}
	if vehicle != (id.ID{}) {
		p.VehicleID = vehicle.PG()
	}
	rows, err := q.ListOwnGarageVehicles(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("garaj: %w", err)
	}
	out := make([]Vehicle, 0, len(rows))
	for _, r := range rows {
		out = append(out, view(r, user))
	}
	return out, nil
}

// one reads one live vehicle of the caller's.
func one(ctx context.Context, q *store.Queries, user, vehicle id.ID) (Vehicle, error) {
	vs, err := own(ctx, q, user, vehicle)
	if err != nil {
		return Vehicle{}, err
	}
	if len(vs) != 1 {
		return Vehicle{}, apperr.ErrGarageVehicleNotFound
	}
	return vs[0], nil
}

// Mine returns the caller's own garage.
func (s *Service) Mine(ctx context.Context, user id.ID) (Mine, error) {
	var out Mine
	err := s.fw.Personal(ctx, user, func(q *store.Queries, _ time.Time) error {
		vs, err := own(ctx, q, user, id.ID{})
		if err != nil {
			return err
		}
		out = Mine{Vehicles: vs, CallerCanAdd: len(vs) < MaxVehicles}
		return nil
	})
	if err != nil {
		return Mine{}, err
	}
	return out, nil
}

// Create adds a vehicle to the caller's garage. A repeated client request
// ID returns the vehicle it created. Refused: a bad input (validation error
// on make, model, model_year, color, note, photo_upload_ids,
// client_request_id), a photo that is not the caller's ready
// garage_vehicle upload (the media errors), a full garage
// (apperr.ErrGarageLimit).
func (s *Service) Create(ctx context.Context, user id.ID, in Input, clientRequestID string) (Vehicle, error) {
	crid, ok := id.Parse(clientRequestID)
	if !ok {
		return Vehicle{}, apperr.Validation(FieldClientRequestID)
	}
	var out Vehicle
	err := s.fw.Personal(ctx, user, func(q *store.Queries, now time.Time) error {
		c, err := clean(in, now)
		if err != nil {
			return err
		}
		if err := q.LockGarage(ctx, user.PG()); err != nil {
			return fmt.Errorf("garaj kilidi: %w", err)
		}
		prev, err := q.GetGarageVehicleByClientRequest(ctx, store.GetGarageVehicleByClientRequestParams{OwnerID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			// A deleted one stays deleted: the retry finds nothing to show.
			out, err = one(ctx, q, user, id.ID(prev.Bytes))
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("garaj aracı: %w", err)
		}
		n, err := q.CountOwnGarageVehicles(ctx, user.PG())
		if err != nil {
			return fmt.Errorf("garaj: %w", err)
		}
		if n >= MaxVehicles {
			return apperr.ErrGarageLimit
		}
		vehicle := id.New()
		if err := attach(ctx, q, user, c.Photos, nil, vehicle, now); err != nil {
			return err
		}
		photos := photoColumns(c.Photos)
		if err := q.CreateGarageVehicle(ctx, store.CreateGarageVehicleParams{
			ID: vehicle.PG(), OwnerID: user.PG(), Make: c.Make, Model: text(c.Model), ModelYear: int4(c.ModelYear),
			Color: text(c.Color), Note: text(c.Note),
			Photo1UploadID: photos[0], Photo2UploadID: photos[1], Photo3UploadID: photos[2],
			ClientRequestID: crid.PG(), Now: ts(now),
		}); err != nil {
			return fmt.Errorf("garaj aracı ekleme: %w", err)
		}
		out, err = one(ctx, q, user, vehicle)
		return err
	})
	if err != nil {
		return Vehicle{}, err
	}
	return out, nil
}

// Update replaces a vehicle of the caller's garage as a whole. Photos left
// out are released (their files are deleted). Refused: a bad input, a
// vehicle that is not in the caller's garage
// (apperr.ErrGarageVehicleNotFound), the media errors.
func (s *Service) Update(ctx context.Context, user, vehicle id.ID, in Input) (Vehicle, error) {
	var out Vehicle
	err := s.fw.Personal(ctx, user, func(q *store.Queries, now time.Time) error {
		c, err := clean(in, now)
		if err != nil {
			return err
		}
		r, err := q.GetOwnGarageVehicleForUpdate(ctx, store.GetOwnGarageVehicleForUpdateParams{OwnerID: user.PG(), ID: vehicle.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrGarageVehicleNotFound
		case err != nil:
			return fmt.Errorf("garaj aracı: %w", err)
		}
		if err := attach(ctx, q, user, c.Photos, photosOf(r.Photo1UploadID, r.Photo2UploadID, r.Photo3UploadID), vehicle, now); err != nil {
			return err
		}
		photos := photoColumns(c.Photos)
		n, err := q.UpdateGarageVehicle(ctx, store.UpdateGarageVehicleParams{
			Make: c.Make, Model: text(c.Model), ModelYear: int4(c.ModelYear), Color: text(c.Color), Note: text(c.Note),
			Photo1UploadID: photos[0], Photo2UploadID: photos[1], Photo3UploadID: photos[2],
			Now: ts(now), OwnerID: user.PG(), ID: vehicle.PG(),
		})
		if err != nil {
			return fmt.Errorf("garaj aracı düzenleme: %w", err)
		}
		if n != 1 {
			return apperr.ErrGarageVehicleNotFound
		}
		out, err = one(ctx, q, user, vehicle)
		return err
	})
	if err != nil {
		return Vehicle{}, err
	}
	return out, nil
}

// Delete deletes a vehicle of the caller's garage; its photos are released
// at once. Refused: a vehicle that is not in the caller's garage
// (apperr.ErrGarageVehicleNotFound).
func (s *Service) Delete(ctx context.Context, user, vehicle id.ID) error {
	return s.fw.Personal(ctx, user, func(q *store.Queries, now time.Time) error {
		n, err := q.DeleteOwnGarageVehicle(ctx, store.DeleteOwnGarageVehicleParams{Now: ts(now), OwnerID: user.PG(), ID: vehicle.PG()})
		if err != nil {
			return fmt.Errorf("garaj aracı silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrGarageVehicleNotFound
		}
		return nil
	})
}

// ForEvent returns what the vehicles component copies from a vehicle of
// owner's garage (issue #272; ADR-0040 §4), in the component's transaction.
// Refused: a vehicle that is not in owner's garage
// (apperr.ErrGarageVehicleNotFound).
func ForEvent(ctx context.Context, q *store.Queries, owner, vehicle id.ID) (Copy, error) {
	r, err := q.GetOwnGarageVehicleForEvent(ctx, store.GetOwnGarageVehicleForEventParams{OwnerID: owner.PG(), ID: vehicle.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Copy{}, apperr.ErrGarageVehicleNotFound
	case err != nil:
		return Copy{}, fmt.Errorf("garaj aracı: %w", err)
	}
	return Copy{Make: r.Make, Model: r.Model.String, ModelYear: r.ModelYear.Int32, Color: r.Color.String}, nil
}

// tayfaScope runs fn with the caller's standing in the tayfa: an active
// member (anyone else gets apperr.ErrTayfaNotFound), the module on. With
// write, in a transaction with the tayfa locked and a read-only tayfa
// refused. fn asks for its own permission.
func (s *Service) tayfaScope(ctx context.Context, user, tayfa id.ID, write bool, fn func(sc *component.TayfaScope) error) error {
	return s.fw.Tayfa(ctx, user, tayfa, write, func(sc *component.TayfaScope) error {
		if err := access.RequireModule(ctx, sc.Q(), tayfa, access.ModuleGarage); err != nil {
			return err
		}
		return fn(sc)
	})
}

// cursor is a page token's content: the list it belongs to and the last
// vehicle it returned. Clients treat it as opaque.
type cursor struct {
	Tayfa string    `json:"t"`
	Owner string    `json:"o"`
	At    time.Time `json:"a"`
	ID    string    `json:"i"`
}

func encodeCursor(tayfa id.ID, f Filter, at time.Time, last id.ID) string {
	b, _ := json.Marshal(cursor{Tayfa: tayfa.String(), Owner: f.Owner.String(), At: at, ID: last.String()})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(token string, tayfa id.ID, f Filter) (time.Time, id.ID, bool) {
	if len(token) > maxPageToken {
		return time.Time{}, id.ID{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return time.Time{}, id.ID{}, false
	}
	last, ok := id.Parse(c.ID)
	if !ok || c.Tayfa != tayfa.String() || c.Owner != f.Owner.String() || c.At.IsZero() {
		return time.Time{}, id.ID{}, false
	}
	return c.At, last, true
}

// List returns a page of the vehicles that show in the tayfa, the newest
// first. Refused: a bad filter (validation error on page_size,
// page_token), anyone but an active member, the module off.
func (s *Service) List(ctx context.Context, user, tayfa id.ID, f Filter) (Page, error) {
	size := f.PageSize
	switch {
	case size == 0:
		size = DefaultPageSize
	case size < 0 || size > MaxPageSize:
		return Page{}, apperr.Validation(FieldPageSize)
	}
	p := store.ListTayfaGarageVehiclesParams{TayfaID: tayfa.PG(), PageSize: size + 1}
	if f.Owner != (id.ID{}) {
		p.OwnerID = f.Owner.PG()
	}
	if f.PageToken != "" {
		at, last, ok := decodeCursor(f.PageToken, tayfa, f)
		if !ok {
			return Page{}, apperr.Validation(FieldPageToken)
		}
		p.HasCursor, p.AfterCreatedAt, p.AfterID = true, ts(at), last.PG()
	}
	var out Page
	err := s.tayfaScope(ctx, user, tayfa, false, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.TayfaView); err != nil {
			return err
		}
		rows, err := sc.Q().ListTayfaGarageVehicles(ctx, p)
		if err != nil {
			return fmt.Errorf("tayfa garajı: %w", err)
		}
		more := len(rows) > int(size)
		if more {
			rows = rows[:size]
		}
		canRemove := !sc.ReadOnly() && sc.Authorize(authz.GarageRemoveAny) == nil
		out.Vehicles = make([]Vehicle, 0, len(rows))
		for _, r := range rows {
			v := view(store.ListOwnGarageVehiclesRow(r), user)
			v.CallerCanRemove = canRemove && v.Owner != user
			out.Vehicles = append(out.Vehicles, v)
		}
		if more {
			last := rows[len(rows)-1]
			out.NextPageToken = encodeCursor(tayfa, f, timeOf(last.CreatedAt), id.ID(last.ID.Bytes))
		}
		out.CallerVisible, err = sc.Q().GarageVisibleIn(ctx, store.GarageVisibleInParams{OwnerID: user.PG(), TayfaID: tayfa.PG()})
		if err != nil {
			return fmt.Errorf("garaj görünürlüğü: %w", err)
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return out, nil
}

// SetVisibility shows or hides the caller's garage in the tayfa and
// returns the choice. Showing needs GarageShow (a member, not muted);
// hiding TayfaView. Refused: anyone but an active member, the module off,
// a read-only tayfa.
func (s *Service) SetVisibility(ctx context.Context, user, tayfa id.ID, visible bool) (bool, error) {
	err := s.tayfaScope(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		action := authz.TayfaView
		if visible {
			action = authz.GarageShow
		}
		if err := sc.Authorize(action); err != nil {
			return err
		}
		if err := sc.Q().SetGarageVisibility(ctx, store.SetGarageVisibilityParams{
			UserID: user.PG(), TayfaID: tayfa.PG(), Visible: visible, Now: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("garaj görünürlüğü: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return visible, nil
}

// Remove removes a vehicle that shows in the tayfa (GarageRemoveAny;
// moderation): it is hidden everywhere, its photos stay until the
// retention job. Refused: a vehicle that does not show in the tayfa
// (apperr.ErrGarageVehicleNotFound), the permission, a read-only tayfa.
func (s *Service) Remove(ctx context.Context, user, tayfa, vehicle id.ID) error {
	return s.tayfaScope(ctx, user, tayfa, true, func(sc *component.TayfaScope) error {
		if err := sc.Authorize(authz.GarageRemoveAny); err != nil {
			return err
		}
		n, err := sc.Q().RemoveTayfaGarageVehicle(ctx, store.RemoveTayfaGarageVehicleParams{
			Now: ts(sc.Now), DeletedBy: user.PG(), ID: vehicle.PG(), TayfaID: tayfa.PG(),
		})
		if err != nil {
			return fmt.Errorf("garaj aracı kaldırma: %w", err)
		}
		if n != 1 {
			return apperr.ErrGarageVehicleNotFound
		}
		return sc.Audit(ctx, AuditRemoved, txn.Target{Type: targetType, ID: vehicle}, nil)
	})
}
