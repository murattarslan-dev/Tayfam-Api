package poll

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestAnonymousPollKeepsVotesSecret is the acceptance test of issue #60's
// anonymous poll rule: several members vote in an anonymous poll, then
//
//  1. no answer to anyone, admins included, holds another member's choice;
//  2. no table other than poll_options and poll_votes (audit_logs and
//     domain_events included) holds any of the poll's option IDs;
//  3. every voter sees their own vote.
//
// The log side is covered in internal/core/transport/grpc (the app layer
// has no logger).
func TestAnonymousPollKeepsVotesSecret(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	ctx := context.Background()
	// The creator does not vote, so created_by never points at a voter.
	p := e.open(t, c, c.mod2, CreateInput{Anonymous: true, MultipleChoice: true,
		Options: []string{"Cumartesi", "Pazar", "İki gün de", "Hiçbiri"}})
	opts := optionIDs(p)

	want := map[id.ID][]string{
		c.admin:   {opts[0]},
		c.admin2:  {opts[1], opts[2]},
		c.mod:     {opts[1]},
		c.member:  {opts[2]},
		c.member2: {opts[3]},
	}
	// Some change their vote or withdraw and vote again on the way.
	_, err := e.svc.Vote(ctx, c.member, c.tayfa, p.ID, []string{opts[0]})
	must(t, err)
	_, err = e.svc.Vote(ctx, c.member2, c.tayfa, p.ID, []string{opts[0], opts[1]})
	must(t, err)
	_, err = e.svc.Withdraw(ctx, c.member2, c.tayfa, p.ID)
	must(t, err)
	var answers []View
	for u, o := range want {
		v, err := e.svc.Vote(ctx, u, c.tayfa, p.ID, o)
		must(t, err)
		answers = append(answers, v)
	}

	// (1) and (3): every reader, whatever their role, gets the totals and
	// only their own vote.
	readers := []id.ID{c.admin, c.admin2, c.mod, c.mod2, c.member, c.member2, c.muted}
	for _, u := range readers {
		g, err := e.svc.Get(ctx, u, c.tayfa, p.ID)
		must(t, err)
		list, _, err := e.svc.List(ctx, u, c.tayfa, "", 0, "")
		must(t, err)
		if len(list) != 1 {
			t.Fatalf("liste = %d", len(list))
		}
		for _, v := range []View{g, list[0]} {
			got := make([]string, 0, len(v.OwnOptionIDs))
			for _, o := range v.OwnOptionIDs {
				got = append(got, o.String())
			}
			mine := slices.Clone(want[u])
			slices.Sort(mine)
			slices.Sort(got)
			if !slices.Equal(got, mine) {
				t.Errorf("%v kendi oyu = %v, %v bekleniyordu", u, got, mine)
			}
			if v.CreatedBy != c.mod2 || !slices.Equal(counts(v), []int64{1, 2, 2, 1}) || v.VoterCount != 5 {
				t.Errorf("görünüm: %+v", v)
			}
		}
		// Nobody lists the voters of an anonymous poll.
		for _, o := range opts {
			_, _, err := e.svc.ListVoters(ctx, u, c.tayfa, p.ID, o, 0, "")
			is(t, "gizli ankette oy verenler", err, apperr.ErrPollAnonymous)
		}
	}
	// Each vote answer carried the voter's own vote only.
	for _, v := range answers {
		if len(v.OwnOptionIDs) == 0 {
			t.Fatal("oy yanıtında kendi oyu yok")
		}
	}
	// The voters query itself returns nothing for an anonymous poll, even if
	// the app check were skipped.
	for _, o := range p.Options {
		rows, err := store.New(e.pool).ListPollVoters(ctx, store.ListPollVotersParams{
			TayfaID: c.tayfa.PG(), PollID: p.ID.PG(), OptionID: o.ID.PG(), PageSize: 100,
		})
		must(t, err)
		if len(rows) != 0 {
			t.Fatal("gizli anketin oy verenleri sorgudan döndü")
		}
	}

	// (2): no other table holds an option ID of the anonymous poll. The scan
	// itself finds the votes where they are kept.
	if all := e.tablesMentioning(t, opts); all["poll_votes"] != 6 || all["poll_options"] != 4 {
		t.Fatalf("tarama çalışmıyor: %v", all)
	}
	for table, n := range e.tablesMentioning(t, opts, "poll_options", "poll_votes") {
		t.Errorf("%s tablosunda gizli anketin seçeneği var (%d satır)", table, n)
	}
	// The poll.voted events name the voter (actor) and the poll only.
	rows, err := e.pool.Query(ctx, "SELECT payload FROM domain_events WHERE type = 'poll.voted' AND subject_id = $1", p.ID.PG())
	must(t, err)
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	must(t, err)
	if len(payloads) != 8 { // 5 votes, 2 changes on the way, 1 withdrawal
		t.Fatalf("oy olayı = %d", len(payloads))
	}
	for _, b := range payloads {
		var m map[string]any
		must(t, json.Unmarshal(b, &m))
		delete(m, "withdrawn")
		if len(m) != 2 || m["poll_id"] != p.ID.String() || m["tayfa_id"] != c.tayfa.String() {
			t.Errorf("gizli anketin oy olayı = %s", b)
		}
	}
	if n := e.count(t, "SELECT count(*) FROM audit_logs WHERE target_id = $1 AND action <> 'poll.created'", p.ID.PG()); n != 0 {
		t.Fatalf("oy denetime yazıldı: %d", n)
	}

	// The setting cannot be turned off now.
	_, err = e.pool.Exec(ctx, "UPDATE polls SET anonymous = false WHERE id = $1", p.ID.PG())
	if store.SQLState(err) != store.CodePollFrozen {
		t.Fatalf("oy sonrası gizlilik kapatıldı: %v", err)
	}
}

// tablesMentioning returns every table in the public schema, apart from
// skip, whose rows mention one of values, with the number of such rows.
func (e *env) tablesMentioning(t *testing.T, values []string, skip ...string) map[string]int {
	t.Helper()
	ctx := context.Background()
	rows, err := e.pool.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY table_name`)
	must(t, err)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	if !slices.Contains(tables, "audit_logs") || !slices.Contains(tables, "domain_events") {
		t.Fatalf("tablolar okunamadı: %v", tables)
	}
	out := map[string]int{}
	for _, table := range tables {
		if slices.Contains(skip, table) {
			continue
		}
		for _, v := range values {
			sql := "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize() + " x WHERE strpos(x::text, $1) > 0" //nolint:gosec // table names come from information_schema, quoted
			if n := e.count(t, sql, v); n > 0 {
				out[table] += n
			}
		}
	}
	return out
}
