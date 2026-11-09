package report

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/poll"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

type env struct {
	svc    *Service
	tayfas *tayfa.Service
	ann    *announcement.Service
	pool   *pgxpool.Pool
	shift  time.Duration
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := dbtest.New(t, coredb.Migrations())
	e := &env{pool: pool}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	e.svc, err = New(Config{Pool: pool, Now: now})
	must(t, err)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	e.ann, err = announcement.New(announcement.Config{Pool: pool, Now: now})
	must(t, err)
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: %v, %v bekleniyordu", what, err, want)
	}
}

func isValidation(t *testing.T, what string, err error, field string) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Field != field {
		t.Fatalf("%s: %v, %q alan hatası bekleniyordu", what, err, field)
	}
}

// crew is a tayfa with one member of each kind; everyone is a known adult
// unless a test says otherwise.
type crew struct {
	tayfa                             id.ID
	admin, mod, mod2, member, member2 id.ID
	mutedMember, pending, banned      id.ID
	nonMember                         id.ID
}

func (e *env) crew(t *testing.T) crew {
	t.Helper()
	c := crew{admin: id.New(), nonMember: id.New()}
	v, err := e.tayfas.Create(ctx, c.admin, tayfa.CreateInput{Name: "Kamp Tayfası " + id.New().String()[:8], JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	must(t, err)
	c.tayfa = v.ID
	c.mod = e.member(t, c, authz.Moderator)
	c.mod2 = e.member(t, c, authz.Moderator)
	c.member = e.member(t, c, authz.Member)
	c.member2 = e.member(t, c, authz.Member)
	c.mutedMember = e.member(t, c, authz.Member)
	e.mute(t, c, c.mutedMember)
	c.pending = id.New()
	e.join(t, c.tayfa, c.pending, authz.StatusPending)
	c.banned = e.member(t, c, authz.Member)
	e.ban(t, c, c.banned)
	for _, u := range []id.ID{c.admin, c.mod, c.mod2, c.member, c.member2, c.mutedMember, c.pending, c.banned, c.nonMember} {
		e.profile(t, u, false)
	}
	return c
}

func (e *env) as(t *testing.T, actor id.ID, fn func(q *store.Queries) error) {
	t.Helper()
	must(t, txn.WithActor(ctx, e.pool, actor, func(_ pgx.Tx, q *store.Queries) error { return fn(q) }))
}

func (e *env) join(t *testing.T, tf, user id.ID, st authz.Status) {
	t.Helper()
	joined := pgtype.Timestamptz{}
	if st == authz.StatusActive {
		joined = ts(time.Now())
	}
	e.as(t, user, func(q *store.Queries) error {
		return q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tf.PG(), UserID: user.PG(), Role: "member", Status: string(st), JoinedVia: "code", JoinedAt: joined,
		})
	})
}

func (e *env) member(t *testing.T, c crew, role authz.Role) id.ID {
	t.Helper()
	u := id.New()
	e.join(t, c.tayfa, u, authz.StatusActive)
	if role != authz.Member {
		e.as(t, c.admin, func(q *store.Queries) error {
			_, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), Role: role.String()})
			return err
		})
	}
	return u
}

func (e *env) mute(t *testing.T, c crew, u id.ID) {
	t.Helper()
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: c.tayfa.PG(), UserID: u.PG(), MutedUntil: ts(time.Now().Add(time.Hour)), MutedBy: c.admin.PG()})
		return err
	})
}

func (e *env) ban(t *testing.T, c crew, u id.ID) {
	t.Helper()
	e.as(t, c.admin, func(q *store.Queries) error {
		_, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: c.tayfa.PG(), UserID: u.PG()})
		return err
	})
}

// profile writes the profile copy with identity's minor flag.
func (e *env) profile(t *testing.T, u id.ID, minor bool) {
	t.Helper()
	e.exec(t, `INSERT INTO user_profiles (user_id, display_name, source_event_id, minor) VALUES ($1, 'Deniz', $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET minor = EXCLUDED.minor`, u.PG(), id.New().PG(), minor)
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.pool.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

func (e *env) publish(t *testing.T, c crew, author id.ID) id.ID {
	t.Helper()
	v, err := e.ann.Create(ctx, author, c.tayfa, announcement.CreateInput{Title: "Pazar konvoyu", ClientRequestID: id.New().String()})
	must(t, err)
	return v.ID
}

func (e *env) report(t *testing.T, c crew, reporter id.ID, tt TargetType, target id.ID, r Reason) MyView {
	t.Helper()
	v, created, err := e.svc.Create(ctx, reporter, c.tayfa, CreateInput{TargetType: tt, TargetID: target.String(), Reason: r, Description: "Ayrıntı"})
	must(t, err)
	if !created {
		t.Fatal("şikâyet yeni değil")
	}
	return v
}

func (e *env) queue(t *testing.T, c crew, viewer id.ID, st Status) []ModView {
	t.Helper()
	vs, _, err := e.svc.ListQueue(ctx, viewer, c.tayfa, st, 100, "")
	must(t, err)
	return vs
}

func ids(vs []ModView) []id.ID {
	out := make([]id.ID, len(vs))
	for i, v := range vs {
		out[i] = v.ID
	}
	return out
}

func TestCreateAndRepeat(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	a := e.publish(t, c, c.mod)
	v, created, err := e.svc.Create(ctx, c.member, c.tayfa, CreateInput{
		TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam, Description: "  Reklam\nlinki  ",
	})
	must(t, err)
	if !created || v.TayfaID != c.tayfa || v.TargetType != TargetAnnouncement || v.TargetID != a || v.Reason != ReasonSpam ||
		v.Description != "Reklam\nlinki" || v.Status != StatusOpen || v.Outcome != "" || v.CreatedAt.IsZero() || !v.ResolvedAt.IsZero() {
		t.Fatalf("şikâyet = %+v %v", v, created)
	}
	// v14 "Şikâyet tekrarı": the second report returns the first.
	again, created, err := e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonHarassment})
	must(t, err)
	if created || again.ID != v.ID || again.Reason != ReasonSpam {
		t.Fatalf("tekrar = %+v %v", again, created)
	}
	if n := e.count(t, `SELECT count(*) FROM reports`); n != 1 {
		t.Fatalf("şikâyet sayısı %d", n)
	}
	// Someone else's report about the same content is a report of its own.
	e.report(t, c, c.member2, TargetAnnouncement, a, ReasonSpam)

	got, err := e.svc.GetMine(ctx, c.member, v.ID)
	if err != nil || got.ID != v.ID {
		t.Fatalf("GetMine = %+v %v", got, err)
	}
	_, err = e.svc.GetMine(ctx, c.member2, v.ID)
	is(t, "başkasının şikâyeti", err, apperr.ErrReportNotFound)
}

// TestCreateAccess: only active members report, and only what they can see
// in that tayfa (IDOR). A muted member still can.
func TestCreateAccess(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	other := e.crew(t)
	a := e.publish(t, c, c.mod)
	foreign := e.publish(t, other, other.mod)
	hidden := e.publish(t, c, c.mod2)
	must(t, e.ann.Delete(ctx, c.admin, c.tayfa, hidden))

	for _, tc := range []struct {
		name   string
		user   id.ID
		tt     TargetType
		target id.ID
		want   error
	}{
		{"üye değil", c.nonMember, TargetAnnouncement, a, apperr.ErrTayfaNotFound},
		{"onay bekleyen", c.pending, TargetAnnouncement, a, apperr.ErrTayfaNotFound},
		{"banlı", c.banned, TargetAnnouncement, a, apperr.ErrTayfaNotFound},
		{"bilinmeyen duyuru", c.member, TargetAnnouncement, id.New(), apperr.ErrReportTargetNotFound},
		{"başka tayfanın duyurusu", c.member, TargetAnnouncement, foreign, apperr.ErrReportTargetNotFound},
		{"silinmiş duyuru", c.member, TargetAnnouncement, hidden, apperr.ErrReportTargetNotFound},
		{"başka tür olarak", c.member, TargetQuestion, a, apperr.ErrReportTargetNotFound},
		{"üye olmayan kullanıcı", c.member, TargetUser, c.nonMember, apperr.ErrReportTargetNotFound},
		{"onay bekleyen kullanıcı", c.member, TargetUser, c.pending, apperr.ErrReportTargetNotFound},
		{"başka tayfanın üyesi", c.member, TargetUser, other.member, apperr.ErrReportTargetNotFound},
	} {
		_, _, err := e.svc.Create(ctx, tc.user, c.tayfa, CreateInput{TargetType: tc.tt, TargetID: tc.target.String(), Reason: ReasonSpam})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	// A module turned off hides its content.
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "announcements", false))
	_, _, err := e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam})
	is(t, "kapalı modül", err, apperr.ErrReportTargetNotFound)
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "announcements", true))

	for _, tc := range []struct {
		name  string
		in    CreateInput
		user  id.ID
		field string
	}{
		{"kendisi", CreateInput{TargetType: TargetUser, TargetID: c.member.String(), Reason: ReasonSpam}, c.member, "target_id"},
		{"kendi duyurusu", CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam}, c.mod, "target_id"},
		{"başka tayfa hedefi", CreateInput{TargetType: TargetTayfa, TargetID: other.tayfa.String(), Reason: ReasonSpam}, c.member, "target_id"},
		{"bozuk kimlik", CreateInput{TargetType: TargetUser, TargetID: "x", Reason: ReasonSpam}, c.member, "target_id"},
		{"tür", CreateInput{TargetType: "channel_message", TargetID: a.String(), Reason: ReasonSpam}, c.member, "target_type"},
		{"sebep", CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: "boring"}, c.member, "reason"},
		{"uzun açıklama", CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam, Description: strings.Repeat("ş", DescriptionMax+1)}, c.member, "description"},
		{"kontrol karakteri", CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam, Description: "a\x00b"}, c.member, "description"},
	} {
		_, _, err := e.svc.Create(ctx, tc.user, c.tayfa, tc.in)
		isValidation(t, tc.name, err, tc.field)
	}

	// A mute does not silence a safety report.
	e.report(t, c, c.mutedMember, TargetUser, c.member, ReasonHarassment)
	e.report(t, c, c.member, TargetAnnouncement, a, ReasonSpam)
	e.report(t, c, c.member, TargetTayfa, c.tayfa, ReasonGambling)
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnnouncement, TargetID: a.String(), Reason: ReasonSpam, Description: strings.Repeat("ş", DescriptionMax)})
	must(t, err)
}

func TestDailyLimit(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	for range DailyLimit {
		e.report(t, c, c.member, TargetAnnouncement, e.publish(t, c, c.mod), ReasonSpam)
	}
	last := e.publish(t, c, c.mod)
	_, _, err := e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnnouncement, TargetID: last.String(), Reason: ReasonSpam})
	is(t, "sınır", err, apperr.ErrReportLimit)
	e.shift = 25 * time.Hour
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnnouncement, TargetID: last.String(), Reason: ReasonSpam})
	must(t, err)
}

// TestReporterNeverRevealed pins the rule of issue #76: the reporter's
// identity never reaches the reported person, and (decision in this PR,
// see the package comment) not the tayfa's moderators either.
func TestReporterNeverRevealed(t *testing.T) {
	// The moderator view has no field that could carry the reporter.
	typ := reflect.TypeFor[ModView]()
	for i := range typ.NumField() {
		if f := strings.ToLower(typ.Field(i).Name); strings.Contains(f, "reporter") {
			t.Fatalf("ModView.%s şikâyet edeni taşıyabilir", typ.Field(i).Name)
		}
	}

	e := setup(t)
	c := e.crew(t)
	a := e.publish(t, c, c.mod2)
	r := e.report(t, c, c.member, TargetAnnouncement, a, ReasonHarassment)
	u := e.report(t, c, c.member2, TargetUser, c.mod, ReasonHarassment)

	// The reported moderator sees neither report about themselves.
	for name, tc := range map[string]struct {
		viewer id.ID
		hidden id.ID
	}{"duyuru yazarı": {c.mod2, r.ID}, "şikâyet edilen moderatör": {c.mod, u.ID}} {
		for _, v := range e.queue(t, c, tc.viewer, StatusOpen) {
			if v.ID == tc.hidden {
				t.Errorf("%s kendisi hakkındaki şikâyeti gördü", name)
			}
		}
		_, err := e.svc.Resolve(ctx, tc.viewer, c.tayfa, tc.hidden, ResolutionDismissed)
		is(t, name+" çözüm", err, apperr.ErrReportNotFound)
	}
	// Others see them, with the reported person but without the reporter.
	q := e.queue(t, c, c.admin, StatusOpen)
	if len(q) != 2 {
		t.Fatalf("kuyruk = %d", len(q))
	}
	for _, v := range q {
		if v.ID == r.ID && (v.SubjectID != c.mod2 || v.Description != "Ayrıntı") {
			t.Errorf("duyuru şikâyeti = %+v", v)
		}
	}

	// Filing writes no audit record (admins read the audit log) and an
	// event without an actor.
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action LIKE 'report.%'`); n != 0 {
		t.Fatalf("oluşturma denetim kaydı = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'report.created' AND actor_id IS NULL`); n != 2 {
		t.Fatalf("report.created aktörsüz = %d", n)
	}

	// Resolving: the audit record, the events and the notification do not
	// name the reporter; the notification has no actor.
	must(t, e.ann.Delete(ctx, c.admin, c.tayfa, a))
	_, err := e.svc.Resolve(ctx, c.admin, c.tayfa, r.ID, ResolutionContentRemoved)
	must(t, err)
	for _, reporter := range []id.ID{c.member, c.member2} {
		s := reporter.String()
		if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE actor_id = $1 OR metadata::text LIKE '%' || $2 || '%'`, reporter.PG(), s); n != 0 {
			t.Errorf("denetim kaydında şikâyet eden: %d", n)
		}
		if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type LIKE 'report.%' AND (actor_id = $1 OR payload::text LIKE '%' || $2 || '%')`, reporter.PG(), s); n != 0 {
			t.Errorf("olayda şikâyet eden: %d", n)
		}
	}
	var payload string
	var actor pgtype.UUID
	must(t, e.pool.QueryRow(ctx, `SELECT payload::text, actor_id FROM domain_events WHERE type = 'notification.requested' AND payload->>'type' = 'report_resolved'`).Scan(&payload, &actor))
	if actor.Valid || !strings.Contains(payload, c.member.String()) || !strings.Contains(payload, r.ID.String()) || strings.Contains(payload, c.mod2.String()) {
		t.Fatalf("bildirim = %s aktör %v", payload, actor.Valid)
	}
	// The reporter learns the outcome, not the action or the handler.
	mine, err := e.svc.GetMine(ctx, c.member, r.ID)
	must(t, err)
	if mine.Status != StatusResolved || mine.Outcome != OutcomeActionTaken || mine.ResolvedAt.IsZero() {
		t.Fatalf("şikâyet edenin görünümü = %+v", mine)
	}
}

func TestPriorityAndEscalation(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	minor := e.member(t, c, authz.Member)
	e.profile(t, minor, true)
	unknown := e.member(t, c, authz.Member) // identity has not told core yet

	adults := e.report(t, c, c.member, TargetUser, c.member2, ReasonSpam)
	byMinor := e.report(t, c, minor, TargetUser, c.member2, ReasonSpam)
	aboutUnknown := e.report(t, c, c.member, TargetUser, unknown, ReasonSpam)
	child := e.report(t, c, c.member2, TargetUser, c.member, ReasonChildSafety)
	aboutTayfa := e.report(t, c, c.member2, TargetTayfa, c.tayfa, ReasonGambling)
	staff := e.report(t, c, c.member2, TargetUser, c.mod, ReasonStaffConduct)

	type row struct {
		priority, escalation string
		minorAt              bool
		platformOnly         bool
	}
	get := func(r id.ID) row {
		var x row
		var esc *string
		var at pgtype.Timestamptz
		must(t, e.pool.QueryRow(ctx, `SELECT priority, escalation_reason, minor_involved_at, platform_only FROM reports WHERE id = $1`, r.PG()).
			Scan(&x.priority, &esc, &at, &x.platformOnly))
		if esc != nil {
			x.escalation = *esc
		}
		x.minorAt = at.Valid
		return x
	}
	for name, tc := range map[string]struct {
		r    id.ID
		want row
	}{
		"yetişkinler":          {adults.ID, row{"normal", "", false, false}},
		"18 altı şikâyet eden": {byMinor.ID, row{"minor_involved", EscalationMinorInvolved, true, false}},
		"bilinmeyen yaş":       {aboutUnknown.ID, row{"minor_involved", EscalationMinorInvolved, true, false}},
		"çocuk güvenliği":      {child.ID, row{"normal", EscalationChildSafety, false, false}},
		"tayfa":                {aboutTayfa.ID, row{"normal", EscalationTayfaTarget, false, true}},
		"yönetici işlemi":      {staff.ID, row{"normal", EscalationStaffConduct, false, true}},
	} {
		if got := get(tc.r); got != tc.want {
			t.Errorf("%s: %+v, %+v bekleniyordu", name, got, tc.want)
		}
	}
	// report.created and report.escalated never carry an age or the flag
	// itself beyond the priority.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'report.escalated'`); n != 5 {
		t.Errorf("report.escalated = %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type LIKE 'report.%' AND (payload ? 'minor' OR payload ? 'age' OR payload ? 'birth_date')`); n != 0 {
		t.Error("olayda yaş bilgisi")
	}

	// Queue: platform-only reports are not listed; MinorInvolved first, then
	// oldest first; the priority is shown, nobody's age.
	got := ids(e.queue(t, c, c.admin, StatusOpen))
	want := []id.ID{byMinor.ID, aboutUnknown.ID, adults.ID, child.ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kuyruk sırası = %v, %v bekleniyordu", got, want)
	}

	// The priority never changes, not even directly in the database.
	if _, err := e.pool.Exec(ctx, `UPDATE reports SET priority = 'normal', minor_involved_at = NULL WHERE id = $1`, byMinor.ID.PG()); err == nil {
		t.Fatal("öncelik değişti")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE reports SET escalated_to_platform_at = NULL, escalation_reason = NULL, platform_status = NULL WHERE id = $1`, child.ID.PG()); err == nil {
		t.Fatal("yükselme geri alındı")
	}
}

func TestQueuePermissions(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	e.report(t, c, c.member, TargetUser, c.member2, ReasonSpam)
	for _, tc := range []struct {
		name string
		user id.ID
		want error
	}{
		{"yönetici", c.admin, nil},
		{"moderatör", c.mod, nil},
		{"üye", c.member, apperr.ErrPermissionDenied},
		{"susturulmuş üye", c.mutedMember, apperr.ErrPermissionDenied},
		{"banlı", c.banned, apperr.ErrTayfaNotFound},
		{"üye değil", c.nonMember, apperr.ErrTayfaNotFound},
	} {
		_, _, err := e.svc.ListQueue(ctx, tc.user, c.tayfa, StatusOpen, 0, "")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, %v bekleniyordu", tc.name, err, tc.want)
		}
	}
	_, _, err := e.svc.ListQueue(ctx, c.admin, c.tayfa, "closed", 0, "")
	isValidation(t, "durum", err, "status")
}

func TestResolve(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	a := e.publish(t, c, c.mod2)
	content := e.report(t, c, c.member, TargetAnnouncement, a, ReasonSpam)
	user := e.report(t, c, c.member, TargetUser, c.member2, ReasonHarassment)
	aboutMod := e.report(t, c, c.member2, TargetUser, c.mod2, ReasonHarassment)
	dismiss := e.report(t, c, c.member2, TargetUser, c.member, ReasonOther)

	// Who may resolve.
	_, err := e.svc.Resolve(ctx, c.member2, c.tayfa, dismiss.ID, ResolutionDismissed)
	is(t, "üye", err, apperr.ErrPermissionDenied)
	mutedMod := e.member(t, c, authz.Moderator)
	e.mute(t, c, mutedMod)
	_, err = e.svc.Resolve(ctx, mutedMod, c.tayfa, dismiss.ID, ResolutionDismissed)
	is(t, "susturulmuş moderatör", err, apperr.ErrMuted)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, id.New(), ResolutionDismissed)
	is(t, "bilinmeyen", err, apperr.ErrReportNotFound)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, dismiss.ID, "ignored")
	isValidation(t, "çözüm", err, "resolution")
	// Another tayfa's report is not found (IDOR).
	other := e.crew(t)
	_, err = e.svc.Resolve(ctx, other.admin, other.tayfa, dismiss.ID, ResolutionDismissed)
	is(t, "başka tayfa", err, apperr.ErrReportNotFound)

	// Hierarchy: a moderator cannot close a report about a moderator.
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, aboutMod.ID, ResolutionDismissed)
	is(t, "moderatör hakkında", err, apperr.ErrRoleHierarchy)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, content.ID, ResolutionDismissed)
	is(t, "moderatörün duyurusu", err, apperr.ErrRoleHierarchy)

	// The action must have been taken.
	_, err = e.svc.Resolve(ctx, c.admin, c.tayfa, content.ID, ResolutionContentRemoved)
	is(t, "görünür içerik", err, apperr.ErrReportActionMissing)
	_, err = e.svc.Resolve(ctx, c.admin, c.tayfa, content.ID, ResolutionMemberSanctioned)
	is(t, "susturulmamış yazar", err, apperr.ErrReportActionMissing)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, user.ID, ResolutionContentRemoved)
	isValidation(t, "kullanıcı için içerik", err, "resolution")
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, user.ID, ResolutionMemberSanctioned)
	is(t, "susturulmamış üye", err, apperr.ErrReportActionMissing)

	must(t, e.ann.Delete(ctx, c.admin, c.tayfa, a))
	v, err := e.svc.Resolve(ctx, c.admin, c.tayfa, content.ID, ResolutionContentRemoved)
	must(t, err)
	if v.Status != StatusResolved || v.Resolution != ResolutionContentRemoved || v.HandledBy != c.admin || v.ResolvedAt.IsZero() {
		t.Fatalf("çözüm = %+v", v)
	}
	_, err = e.svc.Resolve(ctx, c.admin, c.tayfa, content.ID, ResolutionDismissed)
	is(t, "ikinci çözüm", err, apperr.ErrReportAlreadyResolved)

	e.mute(t, c, c.member2)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, user.ID, ResolutionMemberSanctioned)
	must(t, err)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, dismiss.ID, ResolutionDismissed)
	must(t, err)
	if n := e.count(t, `SELECT count(*) FROM audit_logs WHERE action = 'report.resolved' AND tayfa_id = $1`, c.tayfa.PG()); n != 3 {
		t.Errorf("denetim = %d", n)
	}
	// Each reporter still active gets one notice.
	if n := e.count(t, `SELECT count(*) FROM domain_events WHERE type = 'notification.requested' AND payload->>'type' = 'report_resolved'`); n != 3 {
		t.Errorf("bildirim = %d", n)
	}
	mine, err := e.svc.GetMine(ctx, c.member2, dismiss.ID)
	must(t, err)
	if mine.Outcome != OutcomeDismissed {
		t.Errorf("sonuç = %q", mine.Outcome)
	}

	resolved := ids(e.queue(t, c, c.admin, StatusResolved))
	if len(resolved) != 3 || resolved[0] != dismiss.ID || resolved[2] != content.ID {
		t.Fatalf("çözülenler = %v", resolved)
	}
	if open := ids(e.queue(t, c, c.admin, StatusOpen)); len(open) != 1 || open[0] != aboutMod.ID {
		t.Fatalf("açıklar = %v", open)
	}
}

func TestPaging(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	var all []id.ID
	for range 5 {
		u := e.member(t, c, authz.Member)
		e.profile(t, u, false)
		all = append(all, e.report(t, c, c.member, TargetUser, u, ReasonSpam).ID)
	}
	minor := e.member(t, c, authz.Member)
	e.profile(t, minor, true)
	first := e.report(t, c, c.member2, TargetUser, minor, ReasonSpam).ID
	want := append([]id.ID{first}, all...)

	var got []id.ID
	token := ""
	for {
		vs, next, err := e.svc.ListQueue(ctx, c.admin, c.tayfa, StatusOpen, 2, token)
		must(t, err)
		got = append(got, ids(vs)...)
		if next == "" {
			break
		}
		token = next
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sayfalı kuyruk = %v, %v bekleniyordu", got, want)
	}
	_, _, err := e.svc.ListQueue(ctx, c.admin, c.tayfa, StatusResolved, 2, token)
	isValidation(t, "başka listenin token'ı", err, "page_token")
	_, _, err = e.svc.ListQueue(ctx, c.admin, c.tayfa, StatusOpen, -1, "")
	isValidation(t, "negatif sayfa", err, "page_size")

	var mine []id.ID
	token = ""
	for {
		vs, next, err := e.svc.ListMine(ctx, c.member, 2, token)
		must(t, err)
		for _, v := range vs {
			mine = append(mine, v.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if len(mine) != 5 || mine[0] != all[4] || mine[4] != all[0] {
		t.Fatalf("kendi şikâyetleri = %v", mine)
	}
	_, _, err = e.svc.ListMine(ctx, c.member2, 2, token)
	isValidation(t, "başkasının token'ı", err, "page_token")
}

// TestOtherContent: questions, answers and polls follow the same rules; an
// answer of a deleted question cannot be reported, and its removal resolves
// the report.
func TestOtherContent(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	qs, err := qa.New(qa.Config{Pool: e.pool})
	must(t, err)
	ps, err := poll.New(poll.Config{Pool: e.pool})
	must(t, err)
	q, err := qs.CreateQuestion(ctx, c.member2, c.tayfa, qa.CreateQuestionInput{Title: "Kamp yeri?", ClientRequestID: id.New().String()})
	must(t, err)
	ans, err := qs.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, qa.CreateAnswerInput{Body: "Göl kenarı", ClientRequestID: id.New().String()})
	must(t, err)
	p, err := ps.Create(ctx, c.member2, c.tayfa, poll.CreateInput{Question: "Hangi hafta?", Options: []string{"Bu", "Sonraki"}})
	must(t, err)

	rq := e.report(t, c, c.member, TargetQuestion, q.ID, ReasonSpam)
	e.report(t, c, c.member, TargetAnswer, ans.ID, ReasonSpam)
	rp := e.report(t, c, c.member, TargetPoll, p.ID, ReasonSpam)
	for _, v := range e.queue(t, c, c.mod, StatusOpen) {
		if v.SubjectID != c.member2 {
			t.Errorf("yazar = %v", v.SubjectID)
		}
	}

	must(t, qs.DeleteQuestion(ctx, c.mod, c.tayfa, q.ID))
	_, _, err = e.svc.Create(ctx, c.mod2, c.tayfa, CreateInput{TargetType: TargetAnswer, TargetID: ans.ID.String(), Reason: ReasonSpam})
	is(t, "silinmiş sorunun cevabı", err, apperr.ErrReportTargetNotFound)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, rq.ID, ResolutionContentRemoved)
	must(t, err)

	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "polls", false))
	_, _, err = e.svc.Create(ctx, c.mod2, c.tayfa, CreateInput{TargetType: TargetPoll, TargetID: p.ID.String(), Reason: ReasonSpam})
	is(t, "kapalı anket modülü", err, apperr.ErrReportTargetNotFound)
	_, err = e.svc.Resolve(ctx, c.mod, c.tayfa, rp.ID, ResolutionContentRemoved)
	is(t, "görünür anket", err, apperr.ErrReportActionMissing)
}
