package event

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Attendance values (event_rsvps.attendance, attendance_method).
const (
	AttendanceUnknown  = "unknown"
	AttendanceAttended = "attended"
	AttendanceNoShow   = "no_show"

	MethodLocation  = "location"
	MethodOnline    = "online"
	MethodHikeStart = "hike_start"
	MethodOrganizer = "organizer"
)

// Outbox event types and audit actions of attendance (issue #50). Payloads
// and audit metadata hold IDs and the method only, never a location.
const (
	AttendanceRecorded = "attendance.recorded"
	AttendanceRemoved  = "attendance.removed"
	// AttendanceCorrected is the audit action of an organizer's correction.
	AttendanceCorrected = "attendance.corrected"
	HikeStarted         = "hike.started"
	HikeReturned        = "hike.returned"
)

const (
	// checkInLead is how long before the start check-in opens; it closes at
	// the end (data model v14 "Konumla check-in").
	checkInLead = 30 * time.Minute
	// correctionWindow is how long after the end the organizer may correct
	// attendance; activity records follow event edits until then and are
	// frozen afterwards (v14 "Organizatör düzeltmesi", "Kalıcı katılım
	// geçmişi").
	correctionWindow = 7 * 24 * time.Hour
	// clientSkew and clientMaxAge bound client times (hike start and
	// return): not in the future beyond a little clock skew, not older than
	// 48 hours (v14 "Çevrimdışı yazma"). The database allows 5 minutes.
	clientSkew   = 2 * time.Minute
	clientMaxAge = 48 * time.Hour
	// activityPerDay caps the recorded duration per started day of a
	// multi-day event; maxActivity is the cap of a 30-day event and the
	// user_activity_records_duration CHECK.
	activityPerDay = 12 * time.Hour
	maxActivity    = 30 * activityPerDay
)

// Attendance is a member's verified attendance of an event.
type Attendance struct {
	// Status is AttendanceUnknown, AttendanceAttended or AttendanceNoShow.
	Status string
	// Method is how it was verified; "" while unknown.
	Method string
}

// HikeCheckin is a participant's hike roll call: the client's start and
// return times. ReturnedAt is zero while out.
type HikeCheckin struct {
	StartedAt  time.Time
	ReturnedAt time.Time
}

// checkInOpen refuses a cancelled event and times outside the check-in
// window [start − 30 min, end).
func checkInOpen(ev store.GetEventRow, now time.Time) error {
	if ev.Status == StatusCancelled {
		return apperr.ErrEventCancelled
	}
	if now.Before(ev.StartsAt.Time.Add(-checkInLead)) || !now.Before(ev.EndsAt.Time) {
		return apperr.ErrCheckInClosed
	}
	return nil
}

// correctionOpen refuses a cancelled event and times outside the
// correction window [end, end + 7 days).
func correctionOpen(ev store.GetEventRow, now time.Time) error {
	if ev.Status == StatusCancelled {
		return apperr.ErrEventCancelled
	}
	if !inCorrectionWindow(ev, now) || now.Before(ev.EndsAt.Time) {
		return apperr.ErrCorrectionClosed
	}
	return nil
}

// inCorrectionWindow reports whether activity records of ev still follow
// changes: until seven days after the end.
func inCorrectionWindow(ev store.GetEventRow, now time.Time) bool {
	return now.Before(ev.EndsAt.Time.Add(correctionWindow))
}

// rsvpRow loads and locks the caller's answer; ok is false without one.
func rsvpRow(ctx context.Context, q *store.Queries, event, user id.ID) (store.GetRsvpForCheckInRow, bool, error) {
	r, err := q.GetRsvpForCheckIn(ctx, store.GetRsvpForCheckInParams{EventID: event.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetRsvpForCheckInRow{}, false, nil
	}
	if err != nil {
		return store.GetRsvpForCheckInRow{}, false, fmt.Errorf("katılım: %w", err)
	}
	return r, true, nil
}

func attendanceOf(r store.GetRsvpForCheckInRow) Attendance {
	return Attendance{Status: r.Attendance, Method: r.AttendanceMethod.String}
}

// CheckIn verifies the caller's attendance (data model v14 "Konumla
// check-in", "Online etkinlik"). In person, at is the caller's current
// location and must be within 300 m of the meeting point; at most five
// attempts per event count, failed ones included. Online, at must be nil
// and the caller must have answered going before the start. Either way the
// caller must be going now, and the window is from 30 minutes before the
// start until the end.
//
// at is used for the distance check only: it is never stored, logged,
// audited, put in an event or in an error.
func (s *Service) CheckIn(ctx context.Context, user, tayfa, event id.ID, at *Point) (Attendance, error) {
	if at != nil && !at.valid() {
		return Attendance{}, apperr.Validation("location")
	}
	var (
		out Attendance
		// refused is returned after the commit, so a failed attempt still
		// counts.
		refused error
	)
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCheckIn); err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := checkInOpen(ev, c.now); err != nil {
			return err
		}
		online := ev.Mode == ModeOnline
		if online != (at == nil) {
			return apperr.Validation("location")
		}
		r, ok, err := rsvpRow(ctx, q, event, user)
		if err != nil {
			return err
		}
		if ok && r.Attendance == AttendanceAttended {
			out = attendanceOf(r)
			return nil
		}
		if !ok || r.Status != RSVPGoing {
			return apperr.ErrEventNotGoing
		}
		method := MethodOnline
		if online {
			// "Katıldım" is for those who said going before the start.
			if !r.StatusChangedAt.Time.Before(ev.StartsAt.Time) {
				return apperr.ErrEventNotGoing
			}
		} else {
			if !ev.MeetingLat.Valid || !ev.MeetingLng.Valid {
				return errors.New("yüz yüze etkinliğin buluşma noktası yok")
			}
			n, err := q.CountCheckInAttempt(ctx, store.CountCheckInAttemptParams{EventID: event.PG(), UserID: user.PG()})
			if err != nil {
				return fmt.Errorf("check-in denemesi: %w", err)
			}
			if n == 0 {
				refused = apperr.ErrCheckInAttempts
				return nil
			}
			near := withinCheckIn(Point{Lat: ev.MeetingLat.Float64, Lng: ev.MeetingLng.Float64}, *at)
			if !near {
				refused = apperr.ErrCheckInTooFar
				return nil
			}
			method = MethodLocation
		}
		if err := markAttended(ctx, q, c, b, ev, user, method, false); err != nil {
			return err
		}
		out = Attendance{Status: AttendanceAttended, Method: method}
		return txn.TouchTayfa(ctx, q, tayfa, c.now)
	})
	if err != nil {
		return Attendance{}, err
	}
	if refused != nil {
		return Attendance{}, refused
	}
	return out, nil
}

// markAttended records the attendance of user (the caller, or the target of
// an organizer correction) with its activity record, audit row and event.
func markAttended(ctx context.Context, q *store.Queries, c caller, b *batch, ev store.GetEventRow, user id.ID, method string, insert bool) error {
	event := id.ID(ev.ID.Bytes)
	var err error
	switch {
	case insert:
		err = q.InsertAttendedRsvp(ctx, store.InsertAttendedRsvpParams{
			EventID: event.PG(), UserID: user.PG(), MarkedBy: c.user.PG(), MarkedAt: ts(c.now),
		})
	case method == MethodOrganizer:
		err = q.MarkAttended(ctx, store.MarkAttendedParams{
			EventID: event.PG(), UserID: user.PG(), Method: text(method),
			MarkedBy: c.user.PG(), MarkedAt: ts(c.now),
		})
	default:
		err = q.MarkAttended(ctx, store.MarkAttendedParams{
			EventID: event.PG(), UserID: user.PG(), Method: text(method), CheckedInAt: ts(c.now),
		})
	}
	if err != nil {
		return fmt.Errorf("yoklama: %w", err)
	}
	if err := recordActivity(ctx, q, ev, user, method); err != nil {
		return err
	}
	meta := map[string]any{"user_id": user.String(), "method": method}
	if method == MethodOrganizer {
		meta["attended"] = true
		err = audit(ctx, q, c, AttendanceCorrected, event, meta)
	} else {
		err = audit(ctx, q, c, AttendanceRecorded, event, meta)
	}
	if err != nil {
		return err
	}
	b.add(AttendanceRecorded, event, map[string]any{"event_id": event.String(), "user_id": user.String(), "method": method})
	return nil
}

// recordActivity writes the user's activity record of ev: a snapshot of the
// type, mode, title, city and district, the start and the duration. Hikes
// use the roll call's start and, once back, the real duration. Never a
// coordinate.
func recordActivity(ctx context.Context, q *store.Queries, ev store.GetEventRow, user id.ID, method string) error {
	started := ev.StartsAt.Time
	dur := ev.EndsAt.Time.Sub(started)
	if ev.Type == TypeHike {
		h, err := q.GetHikeCheckin(ctx, store.GetHikeCheckinParams{EventID: ev.ID, UserID: user.PG()})
		switch {
		case err == nil:
			started = h.StartedAt.Time
			if h.ReturnedAt.Valid {
				dur = h.ReturnedAt.Time.Sub(started)
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("yürüyüş yoklaması: %w", err)
		}
	}
	if err := q.UpsertActivityRecord(ctx, store.UpsertActivityRecordParams{
		ID: id.New().PG(), UserID: user.PG(), EventID: ev.ID, TayfaID: ev.TayfaID,
		EventType: ev.Type, Mode: ev.Mode, EventTitle: ev.Title, City: ev.City, District: ev.District,
		StartedAt: ts(started), DurationMinutes: activityMinutes(dur), AttendanceMethod: method,
	}); err != nil {
		return fmt.Errorf("katılım geçmişi: %w", err)
	}
	return nil
}

// activityMinutes is the recorded duration: at most activityPerDay for each
// started day, at most maxActivity in all.
func activityMinutes(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	days := (d + 24*time.Hour - 1) / (24 * time.Hour)
	d = min(d, days*activityPerDay, maxActivity)
	return int32(d / time.Minute) //nolint:gosec // at most maxActivity
}

// refreshActivity re-snapshots every activity record of a just edited
// event while the correction window is open.
func refreshActivity(ctx context.Context, q *store.Queries, tayfa, event id.ID, now time.Time) error {
	ev, err := lockEvent(ctx, q, tayfa, event)
	if err != nil {
		return err
	}
	if !inCorrectionWindow(ev, now) {
		return nil
	}
	rows, err := q.ListActivityRecordsOfEvent(ctx, ev.ID)
	if err != nil {
		return fmt.Errorf("katılım geçmişi: %w", err)
	}
	for _, r := range rows {
		if err := recordActivity(ctx, q, ev, id.ID(r.UserID.Bytes), r.AttendanceMethod); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Hike roll call

// clientTime checks a client-reported time against now; zero means now.
func clientTime(t, now time.Time, field string) (time.Time, error) {
	if t.IsZero() {
		return now, nil
	}
	t = t.UTC().Truncate(time.Microsecond)
	if t.After(now.Add(clientSkew)) || t.Before(now.Add(-clientMaxAge)) {
		return time.Time{}, apperr.Validation(field)
	}
	return t, nil
}

// StartHike records that the caller set out on a hike (data model v14
// "Yürüyüşte beklenen dönüş"). It counts as attendance (hike_start). The
// caller must be going, and the server's time must be in the check-in
// window; startedAt is the client's time (zero: now). A repeat returns the
// first start.
func (s *Service) StartHike(ctx context.Context, user, tayfa, event id.ID, startedAt time.Time) (HikeCheckin, error) {
	var out HikeCheckin
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCheckIn); err != nil {
			return err
		}
		start, err := clientTime(startedAt, c.now, "start_time")
		if err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeHike {
			return apperr.ErrEventTypeMismatch
		}
		if err := checkInOpen(ev, c.now); err != nil {
			return err
		}
		r, ok, err := rsvpRow(ctx, q, event, user)
		if err != nil {
			return err
		}
		if !ok || r.Status != RSVPGoing {
			return apperr.ErrEventNotGoing
		}
		n, err := q.StartHikeCheckin(ctx, store.StartHikeCheckinParams{
			EventID: event.PG(), UserID: user.PG(), StartedAt: ts(start), ReceivedAt: ts(c.now),
		})
		if err != nil {
			return fmt.Errorf("yürüyüş yoklaması: %w", err)
		}
		if n == 0 {
			out, err = hikeCheckin(ctx, q, event, user)
			return err
		}
		out = HikeCheckin{StartedAt: start}
		if r.Attendance == AttendanceAttended {
			// Already checked in: the record now starts with the hike.
			if err := recordActivity(ctx, q, ev, user, r.AttendanceMethod.String); err != nil {
				return err
			}
		} else if err := markAttended(ctx, q, c, b, ev, user, MethodHikeStart, false); err != nil {
			return err
		}
		if err := audit(ctx, q, c, HikeStarted, event, map[string]any{"user_id": user.String()}); err != nil {
			return err
		}
		b.add(HikeStarted, event, map[string]any{"event_id": event.String(), "user_id": user.String()})
		return txn.TouchTayfa(ctx, q, tayfa, c.now)
	})
	if err != nil {
		return HikeCheckin{}, err
	}
	return out, nil
}

// FinishHike records the caller's return from a hike they started;
// returnedAt is the client's time (zero: now). It works after the end and
// on a cancelled event too: a late return is exactly what the overdue
// alarm waits for. A repeat returns the first return.
func (s *Service) FinishHike(ctx context.Context, user, tayfa, event id.ID, returnedAt time.Time) (HikeCheckin, error) {
	var out HikeCheckin
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		if err := c.authorize(authz.EventCheckIn); err != nil {
			return err
		}
		back, err := clientTime(returnedAt, c.now, "return_time")
		if err != nil {
			return err
		}
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if ev.Type != TypeHike {
			return apperr.ErrEventTypeMismatch
		}
		h, err := hikeCheckin(ctx, q, event, user)
		if err != nil {
			return err
		}
		if !h.ReturnedAt.IsZero() {
			out = h
			return nil
		}
		if back.Before(h.StartedAt) {
			return apperr.Validation("return_time")
		}
		if _, err := q.ReturnHikeCheckin(ctx, store.ReturnHikeCheckinParams{
			EventID: event.PG(), UserID: user.PG(), ReturnedAt: ts(back), ReceivedAt: ts(c.now),
		}); err != nil {
			return fmt.Errorf("yürüyüş yoklaması: %w", err)
		}
		out = HikeCheckin{StartedAt: h.StartedAt, ReturnedAt: back}
		// The real duration replaces the planned one while the record is
		// not frozen and the attendance still stands.
		r, ok, err := rsvpRow(ctx, q, event, user)
		if err != nil {
			return err
		}
		if ok && r.Attendance == AttendanceAttended && inCorrectionWindow(ev, c.now) {
			if err := recordActivity(ctx, q, ev, user, r.AttendanceMethod.String); err != nil {
				return err
			}
		}
		if err := audit(ctx, q, c, HikeReturned, event, map[string]any{"user_id": user.String()}); err != nil {
			return err
		}
		b.add(HikeReturned, event, map[string]any{"event_id": event.String(), "user_id": user.String()})
		return nil
	})
	if err != nil {
		return HikeCheckin{}, err
	}
	return out, nil
}

// hikeCheckin loads a roll call row; apperr.ErrHikeNotStarted without one.
func hikeCheckin(ctx context.Context, q *store.Queries, event, user id.ID) (HikeCheckin, error) {
	h, err := q.GetHikeCheckin(ctx, store.GetHikeCheckinParams{EventID: event.PG(), UserID: user.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return HikeCheckin{}, apperr.ErrHikeNotStarted
	}
	if err != nil {
		return HikeCheckin{}, fmt.Errorf("yürüyüş yoklaması: %w", err)
	}
	out := HikeCheckin{StartedAt: h.StartedAt.Time.UTC()}
	if h.ReturnedAt.Valid {
		out.ReturnedAt = h.ReturnedAt.Time.UTC()
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Organizer correction

// SetAttendance is the organizer's correction (data model v14
// "Organizatör düzeltmesi"): the organizer of the event, moderators and
// admins add the attendance of a real participant whose GPS failed, or
// remove a fake check-in, within seven days after the end. Nobody corrects
// their own attendance. Adding needs the target to be an active member;
// someone without an answer gets a going answer. Removing deletes the
// activity record. Repeats change nothing.
func (s *Service) SetAttendance(ctx context.Context, user, tayfa, event, target id.ID, attended bool) error {
	if target == (id.ID{}) {
		return apperr.Validation("user_id")
	}
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller, b *batch) error {
		ev, err := lockEvent(ctx, q, tayfa, event)
		if err != nil {
			return err
		}
		if err := c.manage(ev, authz.EventAttendanceOwn, authz.EventAttendanceAny); err != nil {
			return err
		}
		if target == user {
			return apperr.ErrPermissionDenied
		}
		if err := correctionOpen(ev, c.now); err != nil {
			return err
		}
		r, ok, err := rsvpRow(ctx, q, event, target)
		if err != nil {
			return err
		}
		if attended {
			if ok && r.Attendance == AttendanceAttended {
				return nil
			}
			active, err := q.IsActiveMember(ctx, store.IsActiveMemberParams{TayfaID: tayfa.PG(), UserID: target.PG()})
			if err != nil {
				return fmt.Errorf("üyelik: %w", err)
			}
			if !active {
				return apperr.ErrMemberNotFound
			}
			return markAttended(ctx, q, c, b, ev, target, MethodOrganizer, !ok)
		}
		if !ok || r.Attendance != AttendanceAttended {
			return nil
		}
		if err := q.MarkNoShow(ctx, store.MarkNoShowParams{
			EventID: event.PG(), UserID: target.PG(), MarkedBy: user.PG(), MarkedAt: ts(c.now),
		}); err != nil {
			return fmt.Errorf("yoklama: %w", err)
		}
		if _, err := q.DeleteActivityRecord(ctx, store.DeleteActivityRecordParams{UserID: target.PG(), EventID: event.PG()}); err != nil {
			return fmt.Errorf("katılım geçmişi: %w", err)
		}
		if err := audit(ctx, q, c, AttendanceCorrected, event, map[string]any{
			"user_id": target.String(), "method": MethodOrganizer, "attended": false,
		}); err != nil {
			return err
		}
		b.add(AttendanceRemoved, event, map[string]any{"event_id": event.String(), "user_id": target.String(), "method": MethodOrganizer})
		return nil
	})
}
