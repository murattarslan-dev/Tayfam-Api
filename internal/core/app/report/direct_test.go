package report

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakeChat stands in for chat's VerifyDirectMessageForReport: message →
// sender as seen by any party; anything else not found.
type fakeChat struct {
	mu      sync.Mutex
	senders map[id.ID]id.ID
	err     error
	calls   int
}

func (f *fakeChat) Sender(_ context.Context, message id.ID) (id.ID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return id.ID{}, f.err
	}
	s, ok := f.senders[message]
	if !ok {
		return id.ID{}, apperr.ErrReportTargetNotFound
	}
	return s, nil
}

func (e *env) withChat(t *testing.T) *fakeChat {
	t.Helper()
	f := &fakeChat{senders: map[id.ID]id.ID{}}
	e.svc.cfg.DirectMessages = f
	return f
}

func (f *fakeChat) message(sender id.ID) id.ID {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := id.New()
	f.senders[m] = sender
	return m
}

const secretEvidence = "Seni bulacağım, adresini biliyorum-GİZLİ"

func dmInput(m id.ID) CreateInput {
	return CreateInput{TargetType: TargetDMMessage, TargetID: m.String(), Reason: ReasonHarassment, Evidence: "  " + secretEvidence + "\n"}
}

type dmRow struct {
	tayfaNull, platformOnly        bool
	subject                        id.ID
	escalation, priority, evidence string
}

func (e *env) dmRow(t *testing.T, report id.ID) dmRow {
	t.Helper()
	var r dmRow
	var subj [16]byte
	must(t, e.pool.QueryRow(ctx, `SELECT tayfa_id IS NULL, platform_only, subject_user_id, escalation_reason, priority, evidence_text FROM reports WHERE id = $1`, report.PG()).
		Scan(&r.tayfaNull, &r.platformOnly, &subj, &r.escalation, &r.priority, &r.evidence))
	r.subject = id.ID(subj)
	return r
}

func TestDirectMessageReport(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	c := e.crew(t)
	// Reporter and sender share a tayfa whose staff runs a queue: the
	// report must never reach it.
	reporter, sender := c.member, c.member2
	msg := chat.message(sender)

	v, created, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(msg))
	must(t, err)
	if !created || v.TayfaID != (id.ID{}) || v.TargetType != TargetDMMessage || v.TargetID != msg || v.Status != StatusOpen {
		t.Fatalf("şikâyet = %+v %v", v, created)
	}
	r := e.dmRow(t, v.ID)
	if !r.tayfaNull || !r.platformOnly || r.subject != sender || r.escalation != EscalationDirectMessage ||
		r.priority != string(PriorityNormal) || r.evidence != secretEvidence {
		t.Fatalf("kayıt = %+v", r)
	}
	// Never in a tayfa's queue, for any staff member.
	for _, viewer := range []id.ID{c.admin, c.mod} {
		for _, st := range []Status{StatusOpen, StatusResolved} {
			list, _, err := e.svc.ListQueue(ctx, viewer, c.tayfa, st, 0, "")
			must(t, err)
			for _, m := range list {
				if m.ID == v.ID {
					t.Fatal("DM şikâyeti tayfa kuyruğunda")
				}
			}
		}
	}
	// The evidence is in no event, and nothing returns it.
	var n int
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE payload::text LIKE '%GİZLİ%'`).Scan(&n))
	if n != 0 {
		t.Fatal("kanıt metni olayda")
	}
	must(t, e.pool.QueryRow(ctx, `SELECT count(*) FROM domain_events WHERE type IN ('report.created', 'report.escalated') AND tayfa_id IS NULL AND actor_id IS NULL AND subject_id = $1`, v.ID.PG()).Scan(&n))
	if n != 2 {
		t.Fatalf("olaylar = %d", n)
	}
	mineV, err := e.svc.GetMine(ctx, reporter, v.ID)
	must(t, err)
	if mineV.TayfaID != (id.ID{}) || strings.Contains(mineV.Description, "GİZLİ") {
		t.Fatalf("kendi şikâyeti = %+v", mineV)
	}

	// A second report about the same message returns the first; chat is
	// not asked again.
	calls := chat.calls
	again, created, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(msg))
	must(t, err)
	if created || again.ID != v.ID || chat.calls != calls {
		t.Fatalf("ikinci şikâyet = %+v %v", again, created)
	}
	// The sender can report the reporter's message separately.
	back := chat.message(reporter)
	if _, created, err := e.svc.Create(ctx, sender, id.ID{}, dmInput(back)); err != nil || !created {
		t.Fatalf("karşı şikâyet: %v", err)
	}
}

func TestDirectMessageReportPriority(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter, sender := id.New(), id.New()
	e.minors.minors[reporter], e.minors.minors[sender] = false, true
	v, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	must(t, err)
	if r := e.dmRow(t, v.ID); r.priority != string(PriorityMinorInvolved) || r.escalation != EscalationDirectMessage {
		t.Fatalf("18 altı gönderen: %+v", r)
	}
	e.minors.minors[sender] = false
	v, _, err = e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	must(t, err)
	if r := e.dmRow(t, v.ID); r.priority != string(PriorityNormal) {
		t.Fatalf("yetişkinler: %+v", r)
	}
	// identity unreachable: the safe side.
	e.minors.down = true
	v, _, err = e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	must(t, err)
	if r := e.dmRow(t, v.ID); r.priority != string(PriorityMinorInvolved) {
		t.Fatalf("identity kapalı: %+v", r)
	}
}

func TestDirectMessageReportRefusals(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter, sender := id.New(), id.New()
	msg := chat.message(sender)

	_, _, err := e.svc.Create(ctx, reporter, id.New(), dmInput(msg))
	isValidation(t, "tayfalı", err, "tayfa_id")
	in := dmInput(msg)
	in.Reason = ReasonStaffConduct
	_, _, err = e.svc.Create(ctx, reporter, id.ID{}, in)
	isValidation(t, "yönetici işlemi", err, "reason")
	for name, ev := range map[string]string{
		"kanıtsız": "", "boşluk": " \n ", "uzun": strings.Repeat("ş", EvidenceMax+1), "kontrol": "a\x00b", "geçersiz": string([]byte{0xff}),
	} {
		in := dmInput(msg)
		in.Evidence = ev
		_, _, err := e.svc.Create(ctx, reporter, id.ID{}, in)
		isValidation(t, name, err, "evidence_text")
		if ev != "" && strings.Contains(err.Error(), ev) {
			t.Fatalf("%s: hata kanıt metnini içeriyor", name)
		}
	}
	in = dmInput(msg)
	in.Evidence = strings.Repeat("ş", EvidenceMax)
	if _, _, err := e.svc.Create(ctx, reporter, id.ID{}, in); err != nil {
		t.Fatalf("2000 karakter: %v", err)
	}
	// Evidence belongs to direct message reports only.
	c := e.crew(t)
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetUser, TargetID: c.member2.String(), Reason: ReasonSpam, Evidence: "x"})
	isValidation(t, "üye şikâyetinde kanıt", err, "evidence_text")

	// What chat does not confirm.
	_, _, err = e.svc.Create(ctx, reporter, id.ID{}, dmInput(id.New()))
	is(t, "bilinmeyen mesaj", err, apperr.ErrReportTargetNotFound)
	_, _, err = e.svc.Create(ctx, sender, id.ID{}, dmInput(chat.message(sender)))
	is(t, "kendi mesajı", err, apperr.ErrReportTargetNotFound)
	chat.err = apperr.ErrUnavailable
	_, _, err = e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	is(t, "chat kapalı", err, apperr.ErrUnavailable)
	e.svc.cfg.DirectMessages = nil
	_, _, err = e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	is(t, "chat yok", err, apperr.ErrUnavailable)
}

func TestDirectMessageReportDailyLimit(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter, sender := id.New(), id.New()
	for range DailyLimit {
		_, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
		must(t, err)
	}
	_, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
	is(t, "21. şikâyet", err, apperr.ErrReportLimit)
}

func TestDirectMessageReportLimitUnderConcurrency(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter, sender := id.New(), id.New()
	for range DailyLimit - 2 {
		_, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(sender)))
		must(t, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		m := chat.message(sender)
		wg.Go(func() {
			_, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(m))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, apperr.ErrReportLimit) {
			t.Fatal(err)
		}
	}
	if ok != 2 {
		t.Fatalf("%d şikâyet açıldı, 2 bekleniyordu", ok)
	}
}

// The database keeps the shape: a tayfa only for non-direct reports,
// evidence only for direct ones, and neither ever changes.
func TestDirectMessageReportConstraints(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter := id.New()
	v, _, err := e.svc.Create(ctx, reporter, id.ID{}, dmInput(chat.message(id.New())))
	must(t, err)
	c := e.crew(t)
	for name, sql := range map[string]string{
		"tayfasız üye şikâyeti":     `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, created_at) VALUES ($1, NULL, $2, 'user', $2, 'spam', now())`,
		"tayfalı DM":                `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason, evidence_text, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at) VALUES ($1, '` + c.tayfa.String() + `', $2, 'dm_message', $2, 'spam', 'x', true, now(), 'direct_message', 'open', now())`,
		"kanıtsız DM":               `INSERT INTO reports (id, reporter_id, target_type, target_id, reason, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at) VALUES ($1, $2, 'dm_message', $2, 'spam', true, now(), 'direct_message', 'open', now())`,
		"platforma özel olmayan DM": `INSERT INTO reports (id, reporter_id, target_type, target_id, reason, evidence_text, created_at) VALUES ($1, $2, 'dm_message', $2, 'spam', 'x', now())`,
	} {
		if _, err := e.pool.Exec(ctx, sql, id.New().PG(), id.New().PG()); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
	for name, sql := range map[string]string{
		"kanıt":    `UPDATE reports SET evidence_text = 'başka' WHERE id = $1`,
		"tayfa":    `UPDATE reports SET tayfa_id = '` + c.tayfa.String() + `' WHERE id = $1`,
		"yükselme": `UPDATE reports SET escalation_reason = 'unresolved' WHERE id = $1`,
	} {
		if _, err := e.pool.Exec(ctx, sql, v.ID.PG()); err == nil {
			t.Errorf("%s değişti", name)
		}
	}
}
