package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fixture writes content for two users in one tayfa: me and other. Both
// vote in an anonymous poll; each writes an announcement; me has a friend,
// a block and a report.
func fixture(t *testing.T) (*Service, id.ID, id.ID, func(Config) *Service) {
	t.Helper()
	d := dbtest.Open(t, "core", coredb.Migrations())
	ctx := context.Background()
	tayfas, err := tayfa.New(tayfa.Config{Pool: d.App, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	me, other := id.New(), id.New()
	v, err := tayfas.Create(ctx, me, tayfa.CreateInput{Name: "Kamp Tayfası", JoinPolicy: tayfa.JoinOpen, ClientRequestID: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tf := v.ID
	poll, optA, optB := id.New(), id.New(), id.New()
	low, high := me, other
	if strings.Compare(high.String(), low.String()) < 0 {
		low, high = high, low
	}
	sql := fmt.Sprintf(`
BEGIN;
SET LOCAL app.actor_id = '%[3]s';
INSERT INTO memberships (tayfa_id, user_id, role, status, joined_via, joined_at) VALUES ('%[1]s', '%[3]s', 'member', 'active', 'code', now());
INSERT INTO announcements (id, tayfa_id, author_id, title, body, client_request_id) VALUES
  ('%[4]s', '%[1]s', '%[2]s', 'Benim duyurum', 'gövde', gen_random_uuid()),
  ('%[5]s', '%[1]s', '%[3]s', 'Onun duyurusu', 'gizli', gen_random_uuid());
INSERT INTO polls (id, tayfa_id, created_by, question, anonymous) VALUES ('%[6]s', '%[1]s', '%[3]s', 'Nereye?', true);
INSERT INTO poll_options (id, poll_id, text, position) VALUES ('%[7]s', '%[6]s', 'Göl', 0), ('%[8]s', '%[6]s', 'Dağ', 1);
COMMIT;
INSERT INTO poll_votes (poll_id, option_id, user_id) VALUES ('%[6]s', '%[7]s', '%[2]s'), ('%[6]s', '%[8]s', '%[3]s');
INSERT INTO friendships (user_low_id, user_high_id, status, requested_by, accepted_at) VALUES ('%[9]s', '%[10]s', 'accepted', '%[3]s', now());
INSERT INTO user_blocks (blocker_id, blocked_id) VALUES ('%[2]s', gen_random_uuid()), ('%[3]s', '%[2]s');
INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, reason) VALUES
  (gen_random_uuid(), '%[1]s', '%[2]s', 'announcement', '%[5]s', 'spam'),
  (gen_random_uuid(), '%[1]s', '%[3]s', 'announcement', '%[4]s', 'spam');
`, tf, me, other, id.New(), id.New(), poll, optA, optB, low, high)
	if _, err := d.Admin.Exec(ctx, sql); err != nil {
		t.Fatalf("veri: %v", err)
	}
	mk := func(c Config) *Service {
		c.Pool = d.App
		s, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	return mk(Config{}), me, other, mk
}

func collect(t *testing.T, s *Service, user id.ID) (map[string][]map[string]any, int) {
	t.Helper()
	out := map[string][]map[string]any{}
	chunks := 0
	err := s.Export(context.Background(), user, func(c Chunk) error {
		chunks++
		for _, r := range c.Records {
			var m map[string]any
			if err := json.Unmarshal(r, &m); err != nil {
				t.Fatalf("%s: geçersiz JSON: %v", c.Section, err)
			}
			out[c.Section] = append(out[c.Section], m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, chunks
}

// Only the caller's own records leave core; an anonymous vote only as the
// voter's own.
func TestExportOnlyCallersData(t *testing.T) {
	s, me, other, _ := fixture(t)
	got, _ := collect(t, s, me)

	if a := got[SectionAnnouncements]; len(a) != 1 || a[0]["title"] != "Benim duyurum" {
		t.Fatalf("duyurular: %v", a)
	}
	if v := got[SectionPollVotes]; len(v) != 1 || v[0]["option_text"] != "Göl" || v[0]["anonymous"] != true {
		t.Fatalf("oylar yalnızca kendi oyu olmalı: %v", v)
	}
	if p := got[SectionPolls]; len(p) != 0 {
		t.Fatalf("başkasının anketi girmemeli: %v", p)
	}
	if f := got[SectionFriendships]; len(f) != 1 || f[0]["user_id"] != other.String() || f[0]["requested_by_me"] != false {
		t.Fatalf("arkadaşlık: %v", f)
	}
	if b := got[SectionBlocks]; len(b) != 1 || b[0]["user_id"] == me.String() {
		t.Fatalf("engellemeler yalnızca kendi engelledikleri: %v", b)
	}
	if r := got[SectionReports]; len(r) != 1 {
		t.Fatalf("şikâyetler yalnızca kendi açtıkları: %v", r)
	}
	for _, r := range got[SectionReports] {
		for _, k := range []string{"handled_by", "priority", "reporter_id", "platform_status"} {
			if _, ok := r[k]; ok {
				t.Errorf("şikâyette %s olmamalı", k)
			}
		}
	}
	if m := got[SectionMemberships]; len(m) != 1 || m[0]["role"] != "admin" || m[0]["tayfa_name"] != "Kamp Tayfası" {
		t.Fatalf("üyelikler: %v", m)
	}
	// Times are UTC.
	if ts, _ := got[SectionAnnouncements][0]["created_at"].(string); !strings.HasSuffix(ts, "+00:00") {
		t.Fatalf("zaman UTC olmalı: %q", ts)
	}

	// The other user sees their own side.
	theirs, _ := collect(t, s, other)
	if a := theirs[SectionAnnouncements]; len(a) != 1 || a[0]["title"] != "Onun duyurusu" {
		t.Fatalf("diğer kullanıcının duyuruları: %v", a)
	}
	if v := theirs[SectionPollVotes]; len(v) != 1 || v[0]["option_text"] != "Dağ" {
		t.Fatalf("diğer kullanıcının oyu: %v", v)
	}
	if p := theirs[SectionPolls]; len(p) != 1 || len(p[0]["options"].([]any)) != 2 {
		t.Fatalf("anket seçenekleriyle: %v", p)
	}
	if f := theirs[SectionFriendships]; len(f) != 1 || f[0]["requested_by_me"] != true {
		t.Fatalf("arkadaşlık: %v", f)
	}

	// A user with nothing gets nothing.
	none, chunks := collect(t, s, id.New())
	if len(none) != 0 || chunks != 0 {
		t.Fatalf("boş kullanıcı: %v", none)
	}
}

// Paging and chunking keep every record exactly once.
func TestExportPagingAndChunks(t *testing.T) {
	_, me, _, mk := fixture(t)
	small := mk(Config{PageSize: 1, ChunkBytes: 1})
	got, chunks := collect(t, small, me)
	big, _ := collect(t, mk(Config{}), me)
	n := 0
	for sec, recs := range big {
		if len(got[sec]) != len(recs) {
			t.Errorf("%s: %d kayıt, %d bekleniyordu", sec, len(got[sec]), len(recs))
		}
		n += len(recs)
	}
	if chunks != n {
		t.Fatalf("1 baytlık parçada kayıt başına bir parça beklenir: %d/%d", chunks, n)
	}
}

func TestExportStopsOnSendError(t *testing.T) {
	s, me, _, _ := fixture(t)
	stop := errors.New("dur")
	if err := s.Export(context.Background(), me, func(Chunk) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("gönderim hatası dönmeli: %v", err)
	}
	if err := s.Export(context.Background(), id.ID{}, func(Chunk) error { return nil }); err == nil {
		t.Fatal("kullanıcısız dışa aktarma reddedilmeli")
	}
}
