package channel

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeEvent is an event as the fake core knows it.
type fakeEvent struct {
	organizer id.ID
	going     []id.ID
	managers  []id.ID // may manage the event (besides staff)
	end       time.Time
	cancelled bool
}

// fakeAudience stands in for core's event channel answers (the rules
// themselves are tested in core, internal/core/app/channelaccess).
type fakeAudience struct {
	mu      sync.Mutex
	members *fakeMembers
	tayfa   id.ID
	events  map[id.ID]*fakeEvent
}

func (f *fakeAudience) EventChannels(_ context.Context, caller, tayfa id.ID, refs []EventRef) (map[EventRef]EventAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.members.m[[2]id.ID{tayfa, caller}].Active {
		return nil, apperr.ErrTayfaNotFound
	}
	out := map[EventRef]EventAccess{}
	for _, r := range refs {
		ev, ok := f.events[r.Event]
		if !ok || tayfa != f.tayfa {
			continue
		}
		staff := f.members.m[[2]id.ID{tayfa, caller}].Role >= RoleModerator
		audience := append([]id.ID{ev.organizer}, ev.going...)
		if r.Opener != (id.ID{}) && !slices.Contains(audience, r.Opener) {
			audience = append(audience, r.Opener)
		}
		out[r] = EventAccess{
			Member:  slices.Contains(audience, caller),
			CanOpen: staff || slices.Contains(ev.managers, caller) || caller == ev.organizer,
			End:     ev.end, Cancelled: ev.cancelled, MemberCount: int32(len(audience)),
		}
	}
	return out, nil
}

func (f *fakeAudience) EventMembers(_ context.Context, _, _ id.ID, ref EventRef, _ int32, _ string) ([]id.ID, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev, ok := f.events[ref.Event]
	if !ok {
		return nil, "", apperr.ErrEventNotFound
	}
	return append([]id.ID{ev.organizer}, ev.going...), "", nil
}

func (f *fakeAudience) ActiveMembers(_ context.Context, caller, tayfa id.ID, users []id.ID) ([]id.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.members.m[[2]id.ID{tayfa, caller}].Active {
		return nil, apperr.ErrTayfaNotFound
	}
	var out []id.ID
	for _, u := range users {
		if f.members.m[[2]id.ID{tayfa, u}].Active {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeAudience) setGoing(ev id.ID, users ...id.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[ev].going = users
}

func (e *env) audience(t *testing.T) *fakeAudience {
	t.Helper()
	a := &fakeAudience{members: e.members, tayfa: e.tayfa, events: map[id.ID]*fakeEvent{}}
	e.svc.cfg.Audience = a
	return a
}

func visibleTo(t *testing.T, e *env, user id.ID, ch id.ID) bool {
	t.Helper()
	list, err := e.svc.List(ctx, user, e.tayfa)
	must(t, err)
	inList := slices.ContainsFunc(list, func(c Channel) bool { return c.ID == ch })
	_, err = e.svc.Get(ctx, user, e.tayfa, ch)
	switch {
	case err == nil:
	case errors.Is(err, apperr.ErrChannelNotFound):
	default:
		t.Fatalf("Get: %v", err)
	}
	if inList != (err == nil) {
		t.Fatalf("liste ve Get uyuşmuyor: liste %v, Get %v", inList, err)
	}
	return inList
}

func isValidation(t *testing.T, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Fatalf("hata = %v, %q alan hatası bekleniyordu", err, field)
	}
}

// TestEventChannel (issue #183): the audience is the opener, the organizer
// and who goes; staff are not in by role; entering later shows earlier
// messages; leaving shows none.
func TestEventChannel(t *testing.T) {
	e := setup(t)
	a := e.audience(t)
	organizer, going, manager := id.New(), id.New(), id.New()
	for _, u := range []id.ID{organizer, going, manager} {
		e.members.set(e.tayfa, u, Member{Active: true, Role: RoleMember})
	}
	ev := id.New()
	a.events[ev] = &fakeEvent{organizer: organizer, going: []id.ID{going}, managers: []id.ID{manager}, end: time.Now().Add(48 * time.Hour)}

	// Who may open it: staff, the organizer and whoever manages the event.
	_, err := e.svc.Create(ctx, e.member, e.tayfa, ChannelInput{Name: "Etkinlik", Visibility: VisibilityEvent, Event: ev})
	is(t, err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, manager, e.tayfa, ChannelInput{Name: "Etkinlik", Visibility: VisibilityEvent, Event: id.New()})
	is(t, err, apperr.ErrEventNotFound)
	_, err = e.svc.Create(ctx, manager, e.tayfa, ChannelInput{Name: "Etkinlik", Visibility: VisibilityEvent})
	isValidation(t, err, "event_id")
	_, err = e.svc.Create(ctx, manager, e.tayfa, ChannelInput{Name: "Etkinlik", Event: ev})
	isValidation(t, err, "event_id")
	c, err := e.svc.Create(ctx, manager, e.tayfa, ChannelInput{Name: "Etkinlik", Visibility: VisibilityEvent, Event: ev})
	must(t, err)
	if c.EventID != ev || c.MemberCount != 3 || !c.PlannedArchiveAt.Equal(a.events[ev].end.Add(ArchiveAfter).Truncate(time.Microsecond)) {
		t.Fatalf("kanal = %+v", c)
	}
	// One open channel per event.
	_, err = e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "İkinci", Visibility: VisibilityEvent, Event: ev})
	is(t, err, apperr.ErrEventChannelExists)

	for name, tc := range map[string]struct {
		user id.ID
		want bool
	}{
		"kanalı açan": {manager, true}, "organizatör": {organizer, true}, "gidecek": {going, true},
		"gitmeyen üye": {e.member, false}, "yönetici": {e.admin, false}, "moderatör": {e.mod, false},
	} {
		if got := visibleTo(t, e, tc.user, c.ID); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	_, err = e.svc.Send(ctx, e.admin, e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelNotFound)

	first := e.send(t, going, c.ID, "Saat kaçta?")
	// Someone who says "going" later reads the earlier messages.
	a.setGoing(ev, going, e.member)
	msgs, _, err := e.svc.Messages(ctx, e.member, e.tayfa, c.ID, ListInput{})
	if err != nil || len(msgs) != 1 || msgs[0].ID != first.ID {
		t.Fatalf("sonradan giren: %v %v", msgs, err)
	}
	// Changing the answer takes them out: nothing is readable.
	a.setGoing(ev)
	_, _, err = e.svc.Messages(ctx, going, e.tayfa, c.ID, ListInput{})
	is(t, err, apperr.ErrChannelNotFound)
	// The opener stays in whatever they answer.
	if !visibleTo(t, e, manager, c.ID) {
		t.Fatal("kanalı açan çıktı")
	}

	// Members come from core.
	page, err := e.svc.Members(ctx, organizer, e.tayfa, c.ID, 0, "")
	if err != nil || len(page.Users) != 1 || page.Users[0] != organizer {
		t.Fatalf("üyeler = %+v %v", page, err)
	}
	// The audience cannot be changed by hand, nor the kind.
	_, err = e.svc.AddMembers(ctx, manager, e.tayfa, c.ID, []id.ID{e.member})
	isValidation(t, err, "channel_id")
	vis := VisibilityMembers
	_, err = e.svc.Update(ctx, e.mod, e.tayfa, c.ID, ChannelUpdate{Visibility: &vis})
	is(t, err, apperr.ErrChannelNotFound) // the moderator is not in it
}

// TestEventChannelArchive: archived 7 days after the end (or the
// cancellation); read-only from then on, before the job too; newer
// schedule information wins.
func TestEventChannelArchive(t *testing.T) {
	e := setup(t)
	a := e.audience(t)
	ev := id.New()
	end := time.Now().Add(time.Hour)
	a.events[ev] = &fakeEvent{organizer: e.mod, end: end}
	c, err := e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Etkinlik", Visibility: VisibilityEvent, Event: ev})
	must(t, err)

	// The event was cancelled: 7 days from the cancellation.
	cancelled := time.Now().UTC().Add(-time.Hour)
	q := store.New(e.pool)
	must(t, EventEnded(ctx, q, e.tayfa, ev, cancelled, time.Now().Add(time.Minute)))
	// An older piece of information does not undo it.
	must(t, EventEnded(ctx, q, e.tayfa, ev, end, time.Now().Add(-time.Hour)))
	got, err := e.svc.Get(ctx, e.mod, e.tayfa, c.ID)
	must(t, err)
	if !got.PlannedArchiveAt.Equal(cancelled.Add(ArchiveAfter).Truncate(time.Microsecond)) || !got.ArchivedAt.IsZero() {
		t.Fatalf("planlanan arşiv = %+v", got)
	}
	e.send(t, e.mod, c.ID, "Yine de görüşürüz")

	e.shift = ArchiveAfter
	_, err = e.svc.Send(ctx, e.mod, e.tayfa, c.ID, SendInput{Body: "x", ClientRequestID: id.New()})
	is(t, err, apperr.ErrChannelArchived)
	if got, _ := e.svc.Get(ctx, e.mod, e.tayfa, c.ID); got.ArchivedAt.IsZero() {
		t.Fatal("arşiv zamanı geçti ama arşivli görünmüyor")
	}
	n, err := e.svc.ArchiveDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("arşiv işi = %d %v", n, err)
	}
	if n, _ := e.svc.ArchiveDue(ctx); n != 0 {
		t.Fatalf("ikinci çalıştırma = %d", n)
	}
	_, err = e.svc.SetArchived(ctx, e.mod, e.tayfa, c.ID, false)
	is(t, err, apperr.ErrEventEnded)
	// Messages stay readable.
	if msgs, _, err := e.svc.Messages(ctx, e.mod, e.tayfa, c.ID, ListInput{}); err != nil || len(msgs) != 1 {
		t.Fatalf("arşivde okuma: %v %v", msgs, err)
	}
	// A channel for an event over long ago would be archived at once.
	old := id.New()
	a.events[old] = &fakeEvent{organizer: e.mod, end: time.Now().Add(-ArchiveAfter - time.Hour)}
	_, err = e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Eski", Visibility: VisibilityEvent, Event: old})
	is(t, err, apperr.ErrEventEnded)
}

// TestSelectedChannel (issue #183): staff open it, only the opener changes
// who is in it, staff are not in by role, a removed member reads nothing
// and their subscriptions are checked again.
func TestSelectedChannel(t *testing.T) {
	e := setup(t)
	e.audience(t)
	stranger := id.New()

	_, err := e.svc.Create(ctx, e.member, e.tayfa, ChannelInput{Name: "Seçili", Visibility: VisibilitySelected})
	is(t, err, apperr.ErrPermissionDenied)
	_, err = e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Seçili", Visibility: VisibilitySelected, Members: []id.ID{e.member, stranger}})
	is(t, err, apperr.ErrNotTayfaMember)
	_, err = e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Genel", Members: []id.ID{e.member}})
	isValidation(t, err, "member_user_ids")
	c, err := e.svc.Create(ctx, e.mod, e.tayfa, ChannelInput{Name: "Seçili", Visibility: VisibilitySelected, Members: []id.ID{e.member, e.mod, e.member}})
	must(t, err)
	if c.MemberCount != 2 {
		t.Fatalf("üye sayısı = %d", c.MemberCount)
	}
	for name, tc := range map[string]struct {
		user id.ID
		want bool
	}{"kanalı açan": {e.mod, true}, "seçilen": {e.member, true}, "seçilmeyen": {e.other, false}, "yönetici": {e.admin, false}} {
		if got := visibleTo(t, e, tc.user, c.ID); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	e.send(t, e.member, c.ID, "Merhaba")

	// Only the opener changes the members.
	_, err = e.svc.AddMembers(ctx, e.member, e.tayfa, c.ID, []id.ID{e.other})
	is(t, err, apperr.ErrPermissionDenied)
	_, err = e.svc.AddMembers(ctx, e.admin, e.tayfa, c.ID, []id.ID{e.other})
	is(t, err, apperr.ErrChannelNotFound)
	_, err = e.svc.AddMembers(ctx, e.mod, e.tayfa, c.ID, []id.ID{stranger})
	is(t, err, apperr.ErrNotTayfaMember)
	got, err := e.svc.AddMembers(ctx, e.mod, e.tayfa, c.ID, []id.ID{e.other})
	if err != nil || got.MemberCount != 3 || !visibleTo(t, e, e.other, c.ID) {
		t.Fatalf("ekleme: %+v %v", got, err)
	}
	// The newcomer reads the earlier messages.
	if msgs, _, err := e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{}); err != nil || len(msgs) != 1 {
		t.Fatalf("sonradan giren: %v %v", msgs, err)
	}
	page, err := e.svc.Members(ctx, e.other, e.tayfa, c.ID, 2, "")
	if err != nil || len(page.Users) != 2 || page.Next == "" {
		t.Fatalf("üyeler sayfası = %+v %v", page, err)
	}
	rest, err := e.svc.Members(ctx, e.other, e.tayfa, c.ID, 2, page.Next)
	if err != nil || len(rest.Users) != 1 || rest.Next != "" {
		t.Fatalf("ikinci sayfa = %+v %v", rest, err)
	}

	e.pub.take()
	is(t, e.svc.RemoveMember(ctx, e.member, e.tayfa, c.ID, e.other), apperr.ErrPermissionDenied)
	isValidation(t, e.svc.RemoveMember(ctx, e.mod, e.tayfa, c.ID, e.mod), "user_id")
	must(t, e.svc.RemoveMember(ctx, e.mod, e.tayfa, c.ID, e.other))
	if ev, _ := e.pub.take(); !slices.Equal(ev, []string{"channel:" + c.ID.String()}) {
		t.Fatalf("yeniden denetim = %v", ev)
	}
	if visibleTo(t, e, e.other, c.ID) {
		t.Fatal("çıkarılan hâlâ görüyor")
	}
	_, _, err = e.svc.Messages(ctx, e.other, e.tayfa, c.ID, ListInput{})
	is(t, err, apperr.ErrChannelNotFound)
	// Removing again changes nothing and rechecks nothing.
	must(t, e.svc.RemoveMember(ctx, e.mod, e.tayfa, c.ID, e.other))
	if ev, _ := e.pub.take(); len(ev) != 0 {
		t.Fatalf("boş çıkarma yayınladı: %v", ev)
	}

	// The kind cannot change; the member channels list does not change.
	vis := VisibilityMembers
	_, err = e.svc.Update(ctx, e.mod, e.tayfa, c.ID, ChannelUpdate{Visibility: &vis})
	isValidation(t, err, "visibility")
	general := e.channel(t, ChannelInput{Name: "Genel"})
	sel := VisibilitySelected
	_, err = e.svc.Update(ctx, e.mod, e.tayfa, general.ID, ChannelUpdate{Visibility: &sel})
	isValidation(t, err, "visibility")
	_, err = e.svc.Members(ctx, e.mod, e.tayfa, general.ID, 0, "")
	isValidation(t, err, "channel_id")

	// Leaving the tayfa takes the member out for good.
	must(t, LeaveTayfa(ctx, store.New(e.pool), e.tayfa, e.member))
	if visibleTo(t, e, e.member, c.ID) {
		t.Fatal("tayfadan ayrılan hâlâ kanalda")
	}
	// Account deletion too.
	must(t, Cleanup(ctx, store.New(e.pool), e.mod))
	if n := e.count(t, `SELECT count(*) FROM channel_members WHERE user_id = $1`, e.mod.PG()); n != 0 {
		t.Fatalf("hesap silmede kalan üyelik = %d", n)
	}
}
