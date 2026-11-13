package channelaccess

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// event writes an online event organized by organizer, ending at end.
func (f fixture) event(t *testing.T, organizer id.ID, end time.Time) id.ID {
	t.Helper()
	e := id.New()
	f.as(organizer, func(q *store.Queries) error {
		_, err := f.pool.Exec(ctx, `INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by)
			VALUES ($1, $2, 'gaming', 'online', 'Oyun gecesi', $3, $4, 'https://example.org/x', $5)`,
			e.PG(), f.tayfa.PG(), ts(end.Add(-2*time.Hour)), ts(end), organizer.PG())
		return err
	})
	return e
}

func (f fixture) rsvp(t *testing.T, event, user id.ID, status string) {
	t.Helper()
	_, err := f.pool.Exec(ctx, `INSERT INTO event_rsvps (event_id, user_id, status) VALUES ($1, $2, $3)
		ON CONFLICT (event_id, user_id) DO UPDATE SET status = EXCLUDED.status`, event.PG(), user.PG(), status)
	must(t, err)
}

// TestEventChannelAudience (issue #183): going members, the organizer and
// the opener are in; waitlist, maybe and not_going are not; staff are not
// in by role.
func TestEventChannelAudience(t *testing.T) {
	f := setup(t)
	end := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	ev := f.event(t, f.member, end)
	going, maybe, waitlist, notGoing := f.join(t), f.join(t), f.join(t), f.join(t)
	f.rsvp(t, ev, going, "going")
	f.rsvp(t, ev, maybe, "maybe")
	f.rsvp(t, ev, waitlist, "waitlist")
	f.rsvp(t, ev, notGoing, "not_going")
	f.rsvp(t, ev, f.banned, "going") // not an active member: never counted
	ch := EventChannel{Event: ev, Opener: f.mod}

	for name, tc := range map[string]struct {
		caller          id.ID
		member, canOpen bool
	}{
		"gidecek":              {going, true, false},
		"organizatör":          {f.member, true, true},
		"kanalı açan":          {f.mod, true, true},
		"belki":                {maybe, false, false},
		"bekleme listesi":      {waitlist, false, false},
		"gelmeyecek":           {notGoing, false, false},
		"yönetici (gitmiyor)":  {f.admin, false, true},
		"moderatör (gitmiyor)": {f.mod2, false, true},
		"susturulmuş":          {f.muted, false, false},
	} {
		got, err := f.svc.EventChannels(ctx, tc.caller, f.tayfa, []EventChannel{ch})
		must(t, err)
		if len(got) != 1 || got[0].Member != tc.member || got[0].CanOpen != tc.canOpen {
			t.Errorf("%s: %+v", name, got)
			continue
		}
		// going + organizer + opener (the moderator).
		if got[0].MemberCount != 3 || !got[0].End.Equal(end) || got[0].Cancelled {
			t.Errorf("%s: sayı/bitiş %+v", name, got[0])
		}
	}
	// An opener who is also going is counted once.
	got, err := f.svc.EventChannels(ctx, going, f.tayfa, []EventChannel{{Event: ev, Opener: going}})
	must(t, err)
	if got[0].MemberCount != 2 {
		t.Fatalf("kanalı açan gidiyor: %d", got[0].MemberCount)
	}

	// Cancelled: the end is the moment of cancellation.
	at := time.Now().UTC().Truncate(time.Microsecond)
	_, err = f.pool.Exec(ctx, `UPDATE events SET status = 'cancelled', cancelled_at = $2 WHERE id = $1`, ev.PG(), ts(at))
	must(t, err)
	got, err = f.svc.EventChannels(ctx, going, f.tayfa, []EventChannel{ch})
	must(t, err)
	if !got[0].Cancelled || !got[0].End.Equal(at) {
		t.Fatalf("iptal: %+v", got[0])
	}

	// Unknown events and another tayfa's are left out; non-members get
	// tayfa-not-found.
	got, err = f.svc.EventChannels(ctx, going, f.tayfa, []EventChannel{{Event: id.New()}})
	if err != nil || len(got) != 0 {
		t.Fatalf("bilinmeyen etkinlik: %+v %v", got, err)
	}
	if _, err := f.svc.EventChannels(ctx, f.nonMember, f.tayfa, []EventChannel{ch}); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olmayan: %v", err)
	}
	if _, err := f.svc.EventChannels(ctx, going, f.tayfa, nil); err == nil {
		t.Fatal("boş liste kabul edildi")
	}
}

func TestEventAudienceList(t *testing.T) {
	f := setup(t)
	ev := f.event(t, f.member, time.Now().Add(time.Hour))
	var want []id.ID
	for range 5 {
		u := f.join(t)
		f.rsvp(t, ev, u, "going")
		want = append(want, u)
	}
	f.rsvp(t, ev, f.join(t), "maybe")
	want = append(want, f.member, f.mod)
	slices.SortFunc(want, compareID)

	var got []id.ID
	token := ""
	for range 10 {
		page, next, err := f.svc.EventAudience(ctx, f.member, f.tayfa, EventChannel{Event: ev, Opener: f.mod}, 2, token)
		must(t, err)
		if len(page) > 2 {
			t.Fatalf("sayfa = %d", len(page))
		}
		got = append(got, page...)
		if next == "" {
			break
		}
		token = next
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kitle = %v, %v bekleniyordu", got, want)
	}
	if _, _, err := f.svc.EventAudience(ctx, f.member, f.tayfa, EventChannel{Event: id.New()}, 0, ""); !errors.Is(err, apperr.ErrEventNotFound) {
		t.Fatalf("bilinmeyen: %v", err)
	}
	if _, _, err := f.svc.EventAudience(ctx, f.member, f.tayfa, EventChannel{Event: ev}, 0, "bozuk!"); err == nil {
		t.Fatal("bozuk token kabul edildi")
	}
}

func TestActiveMembers(t *testing.T) {
	f := setup(t)
	got, err := f.svc.ActiveMembers(ctx, f.member, f.tayfa, []id.ID{f.mod, f.pending, f.banned, f.nonMember, f.mod, f.admin})
	if err != nil || !slices.Equal(got, []id.ID{f.mod, f.admin}) {
		t.Fatalf("etkin üyeler = %v %v", got, err)
	}
	if _, err := f.svc.ActiveMembers(ctx, f.nonMember, f.tayfa, []id.ID{f.mod}); !errors.Is(err, apperr.ErrTayfaNotFound) {
		t.Fatalf("üye olmayan: %v", err)
	}
	if _, err := f.svc.ActiveMembers(ctx, f.member, f.tayfa, make([]id.ID, MaxBatch+1)); err == nil {
		t.Fatal("101 kimlik kabul edildi")
	}
}
