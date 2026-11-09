package grpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/poll"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// fakePolls records what transport passed on and returns err.
type fakePolls struct {
	err     error
	user    id.ID
	tayfa   id.ID
	p       id.ID
	create  poll.CreateInput
	options []string
	event   string
	option  string
	view    poll.View
}

func (f *fakePolls) Create(_ context.Context, u, t id.ID, in poll.CreateInput) (poll.View, error) {
	f.user, f.tayfa, f.create = u, t, in
	return f.view, f.err
}
func (f *fakePolls) Get(_ context.Context, u, t, p id.ID) (poll.View, error) {
	f.user, f.tayfa, f.p = u, t, p
	return f.view, f.err
}
func (f *fakePolls) List(_ context.Context, u, t id.ID, event string, _ int32, _ string) ([]poll.View, string, error) {
	f.user, f.tayfa, f.event = u, t, event
	return []poll.View{f.view, f.view}, "sonraki", f.err
}
func (f *fakePolls) Vote(_ context.Context, u, t, p id.ID, options []string) (poll.View, error) {
	f.user, f.tayfa, f.p, f.options = u, t, p, options
	return f.view, f.err
}
func (f *fakePolls) Withdraw(_ context.Context, u, t, p id.ID) (poll.View, error) {
	f.user, f.tayfa, f.p = u, t, p
	return f.view, f.err
}
func (f *fakePolls) ListVoters(_ context.Context, u, t, p id.ID, option string, _ int32, _ string) ([]poll.Voter, string, error) {
	f.user, f.tayfa, f.p, f.option = u, t, p, option
	return []poll.Voter{{UserID: u, VotedAt: time.Now()}}, "", f.err
}
func (f *fakePolls) Close(_ context.Context, u, t, p id.ID) (poll.View, error) {
	f.user, f.tayfa, f.p = u, t, p
	return f.view, f.err
}
func (f *fakePolls) Delete(_ context.Context, u, t, p id.ID) error {
	f.user, f.tayfa, f.p = u, t, p
	return f.err
}

// callAllPolls calls every PollService method.
func callAllPolls(ctx context.Context, c corev1.PollServiceServer, tayfaID, pollID string) map[string]error {
	out := map[string]error{}
	_, out["CreatePoll"] = c.CreatePoll(ctx, &corev1.CreatePollRequest{TayfaId: tayfaID, Question: "Soru", Options: []string{"A", "B"}})
	_, out["GetPoll"] = c.GetPoll(ctx, &corev1.GetPollRequest{TayfaId: tayfaID, PollId: pollID})
	_, out["ListPolls"] = c.ListPolls(ctx, &corev1.ListPollsRequest{TayfaId: tayfaID})
	_, out["VotePoll"] = c.VotePoll(ctx, &corev1.VotePollRequest{TayfaId: tayfaID, PollId: pollID, OptionIds: []string{id.New().String()}})
	_, out["WithdrawPollVote"] = c.WithdrawPollVote(ctx, &corev1.WithdrawPollVoteRequest{TayfaId: tayfaID, PollId: pollID})
	_, out["ListPollVoters"] = c.ListPollVoters(ctx, &corev1.ListPollVotersRequest{TayfaId: tayfaID, PollId: pollID, OptionId: id.New().String()})
	_, out["ClosePoll"] = c.ClosePoll(ctx, &corev1.ClosePollRequest{TayfaId: tayfaID, PollId: pollID})
	_, out["DeletePoll"] = c.DeletePoll(ctx, &corev1.DeletePollRequest{TayfaId: tayfaID, PollId: pollID})
	return out
}

func TestPollServerNeedsCaller(t *testing.T) {
	f := &fakePolls{}
	srv := NewPollServer(f, discard)
	for method, err := range callAllPolls(context.Background(), srv, id.New().String(), id.New().String()) {
		if code, info := infoOf(t, err); code != codes.Unauthenticated || info != nil {
			t.Errorf("%s: %v", method, err)
		}
	}
	if f.user != (id.ID{}) {
		t.Fatal("token'sız istek app katmanına ulaştı")
	}
}

func TestPollServerValidatesIDs(t *testing.T) {
	f := &fakePolls{}
	srv := NewPollServer(f, discard)
	ctx := callerCtx(id.New())
	field := func(err error) string {
		t.Helper()
		code, info := infoOf(t, err)
		if code != codes.InvalidArgument || info.GetReason() != corev1.ErrorReason_ERROR_REASON_VALIDATION.String() {
			t.Fatalf("doğrulama hatası değil: %v", err)
		}
		return info.GetMetadata()["field"]
	}
	good := id.New().String()
	for _, bad := range []string{"", "abc", "' OR 1=1 --"} {
		for method, err := range callAllPolls(ctx, srv, bad, good) {
			if got := field(err); got != "tayfa_id" {
				t.Errorf("%s tayfa %q: %s", method, bad, got)
			}
		}
		for method, err := range callAllPolls(ctx, srv, good, bad) {
			if method == "CreatePoll" || method == "ListPolls" {
				continue
			}
			if got := field(err); got != "poll_id" {
				t.Errorf("%s anket %q: %s", method, bad, got)
			}
		}
	}
	if f.p != (id.ID{}) || f.tayfa.String() != good {
		t.Fatal("geçersiz kimlik app katmanına ulaştı")
	}
	_, err := srv.CreatePoll(ctx, &corev1.CreatePollRequest{TayfaId: good, CloseTime: &timestamppb.Timestamp{Seconds: -1 << 62}})
	if got := field(err); got != "close_time" {
		t.Fatalf("bozuk kapanma zamanı: %s", got)
	}
}

func TestPollServerConverts(t *testing.T) {
	user, tid, pid, ev, o1, o2 := id.New(), id.New(), id.New(), id.New(), id.New(), id.New()
	now := time.Now().UTC()
	f := &fakePolls{view: poll.View{
		ID: pid, TayfaID: tid, EventID: ev, Question: "Soru?", Anonymous: true, MultipleChoice: true,
		ClosesAt: now.Add(time.Hour), CreatedAt: now, VoterCount: 3,
		Options:      []poll.Option{{ID: o1, Text: "A", Position: 0, VoteCount: 2}, {ID: o2, Text: "B", Position: 1, VoteCount: 1}},
		OwnOptionIDs: []id.ID{o2},
	}}
	srv := NewPollServer(f, discard)
	ctx := callerCtx(user)

	closes := now.Add(2 * time.Hour)
	resp, err := srv.CreatePoll(ctx, &corev1.CreatePollRequest{
		TayfaId: tid.String(), Question: "Soru?", Options: []string{"A", "B"}, Anonymous: true, MultipleChoice: true,
		CloseTime: timestamppb.New(closes), EventId: ev.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.user != user || f.tayfa != tid || !f.create.ClosesAt.Equal(closes) || f.create.EventID != ev.String() ||
		!f.create.Anonymous || !f.create.MultipleChoice || !slices.Equal(f.create.Options, []string{"A", "B"}) {
		t.Fatalf("app'e giden = %+v", f.create)
	}
	p := resp.GetPoll()
	if p.GetId() != pid.String() || p.GetEventId() != ev.String() || p.GetCreatedByUserId() != "" || !p.GetAnonymous() ||
		p.GetClosedTime() != nil || !p.GetCloseTime().AsTime().Equal(now.Add(time.Hour)) || p.GetVoterCount() != 3 ||
		len(p.GetOptions()) != 2 || p.GetOptions()[0].GetVoteCount() != 2 || p.GetOptions()[1].GetPosition() != 1 ||
		!slices.Equal(p.GetOwnOptionIds(), []string{o2.String()}) {
		t.Fatalf("dönüştürme = %v", p)
	}
	if _, err := srv.CreatePoll(ctx, &corev1.CreatePollRequest{TayfaId: tid.String()}); err != nil || !f.create.ClosesAt.IsZero() {
		t.Fatalf("kapanmasız: %v", err)
	}
	if _, err := srv.VotePoll(ctx, &corev1.VotePollRequest{TayfaId: tid.String(), PollId: pid.String(), OptionIds: []string{o1.String(), o2.String()}}); err != nil ||
		!slices.Equal(f.options, []string{o1.String(), o2.String()}) || f.p != pid {
		t.Fatalf("oy: %v", err)
	}
	if _, err := srv.ListPolls(ctx, &corev1.ListPollsRequest{TayfaId: tid.String(), EventId: ev.String()}); err != nil || f.event != ev.String() {
		t.Fatalf("liste: %v", err)
	}
	if _, err := srv.ListPollVoters(ctx, &corev1.ListPollVotersRequest{TayfaId: tid.String(), PollId: pid.String(), OptionId: o1.String()}); err != nil || f.option != o1.String() {
		t.Fatalf("oy verenler: %v", err)
	}

	f.err = apperr.ErrPollAnonymous
	_, err = srv.ListPollVoters(ctx, &corev1.ListPollVotersRequest{TayfaId: tid.String(), PollId: pid.String(), OptionId: o1.String()})
	if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_POLL_ANONYMOUS.String() {
		t.Fatalf("gizli: %v", err)
	}
	f.err = apperr.ErrModuleDisabled
	for method, err := range callAllPolls(ctx, srv, tid.String(), pid.String()) {
		code, info := infoOf(t, err)
		if code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_MODULE_DISABLED.String() {
			t.Errorf("%s: %v", method, err)
		}
	}
}

// TestAnonymousPollBehindGuard serves the real services behind auth.Guard,
// as cmd/core does, with the server's log captured. Several members vote in
// an anonymous poll; no response to anyone, admins included, carries another
// member's choice or user ID, each voter gets their own vote back, and the
// log holds none of the poll's option IDs, error paths included.
func TestAnonymousPollBehindGuard(t *testing.T) {
	key, err := auth.ParseSigningKey("test", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(auth.SignerConfig{Key: key, Issuer: "tayfam-test", Audience: "tayfam-api", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(auth.VerifierConfig{Keys: auth.StaticKeys{key.ID: key.Public().Key}, Issuer: "tayfam-test", Audience: "tayfam-api"})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pool := dbtest.New(t, coredb.Migrations())
	tayfas, err := tayfa.New(tayfa.Config{Pool: pool, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	polls, err := poll.New(poll.Config{Pool: pool})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(auth.NewGuard(v).ServerOptions()...)
	corev1.RegisterTayfaServiceServer(s, NewTayfaServer(tayfas, log))
	corev1.RegisterPollServiceServer(s, NewPollServer(polls, log))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	as := func(user id.ID) context.Context {
		tok, _, err := signer.Issue(user.String(), id.New().String())
		if err != nil {
			t.Fatal(err)
		}
		return auth.WithToken(context.Background(), tok)
	}
	tc, pc := corev1.NewTayfaServiceClient(conn), corev1.NewPollServiceClient(conn)

	// An open tayfa: the admin and four members who join with the code.
	admin, creator := id.New(), id.New()
	voters := []id.ID{admin, id.New(), id.New(), id.New()}
	created, err := tc.CreateTayfa(as(admin), &corev1.CreateTayfaRequest{Name: "Gizli Oy Tayfası", JoinPolicy: corev1.JoinPolicy_JOIN_POLICY_OPEN, ClientRequestId: id.New().String()})
	if err != nil {
		t.Fatal(err)
	}
	tid := created.GetTayfa().GetId()
	tayfaID, _ := id.Parse(tid)
	for _, u := range append([]id.ID{creator}, voters[1:]...) {
		// Joining with the code, as MembershipService.JoinByCode does.
		if err := txn.WithActor(context.Background(), pool, u, func(_ pgx.Tx, q *store.Queries) error {
			return q.CreateMembership(context.Background(), store.CreateMembershipParams{
				TayfaID: tayfaID.PG(), UserID: u.PG(), Role: "member", Status: "active", JoinedVia: "code",
				JoinedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			})
		}); err != nil {
			t.Fatal(err)
		}
	}

	p, err := pc.CreatePoll(as(creator), &corev1.CreatePollRequest{
		TayfaId: tid, Question: "Kamp hangi hafta sonu?", Options: []string{"Bu hafta", "Gelecek hafta", "Ay sonu"}, Anonymous: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.GetPoll().GetId()
	var opts []string
	for _, o := range p.GetPoll().GetOptions() {
		opts = append(opts, o.GetId())
	}
	choice := map[id.ID]string{voters[0]: opts[0], voters[1]: opts[1], voters[2]: opts[1], voters[3]: opts[2]}

	var answers []proto.Message
	for _, u := range voters {
		r, err := pc.VotePoll(as(u), &corev1.VotePollRequest{TayfaId: tid, PollId: pid, OptionIds: []string{choice[u]}})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.GetPoll().GetOwnOptionIds(); !slices.Equal(got, []string{choice[u]}) {
			t.Fatalf("oy yanıtındaki kendi oyu = %v", got)
		}
		answers = append(answers, r)
	}
	// Error paths: an option of no poll, two options, a voters list.
	for _, u := range voters {
		_, err := pc.VotePoll(as(u), &corev1.VotePollRequest{TayfaId: tid, PollId: pid, OptionIds: []string{opts[0], opts[1]}})
		if code, _ := infoOf(t, err); code != codes.InvalidArgument {
			t.Fatalf("iki seçenek: %v", err)
		}
		for _, o := range opts {
			_, err := pc.ListPollVoters(as(u), &corev1.ListPollVotersRequest{TayfaId: tid, PollId: pid, OptionId: o})
			if code, info := infoOf(t, err); code != codes.FailedPrecondition || info.GetReason() != corev1.ErrorReason_ERROR_REASON_POLL_ANONYMOUS.String() {
				t.Fatalf("gizli ankette oy verenler: %v", err)
			}
		}
	}

	// Every reader, the admin included, sees the totals and only their own
	// vote; no answer holds a voter's user ID.
	for _, u := range append([]id.ID{creator}, voters...) {
		g, err := pc.GetPoll(as(u), &corev1.GetPollRequest{TayfaId: tid, PollId: pid})
		if err != nil {
			t.Fatal(err)
		}
		l, err := pc.ListPolls(as(u), &corev1.ListPollsRequest{TayfaId: tid})
		if err != nil {
			t.Fatal(err)
		}
		mine := []string{}
		if c, ok := choice[u]; ok {
			mine = []string{c}
		}
		for _, got := range []*corev1.Poll{g.GetPoll(), l.GetPolls()[0]} {
			if !slices.Equal(got.GetOwnOptionIds(), mine) {
				t.Errorf("%v kendi oyu = %v, %v bekleniyordu", u, got.GetOwnOptionIds(), mine)
			}
			var totals []int64
			for _, o := range got.GetOptions() {
				totals = append(totals, o.GetVoteCount())
			}
			if !slices.Equal(totals, []int64{1, 2, 1}) || got.GetVoterCount() != 4 {
				t.Errorf("sayım = %v", got)
			}
		}
		answers = append(answers, g, l)
	}
	for _, a := range answers {
		b, err := protojson.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range voters {
			if strings.Contains(string(b), u.String()) {
				t.Fatalf("yanıtta oy verenin kimliği var: %s", b)
			}
		}
	}

	// Close the poll, then try again: the closed path logs nothing either.
	if _, err := pc.ClosePoll(as(admin), &corev1.ClosePollRequest{TayfaId: tid, PollId: pid}); err != nil {
		t.Fatal(err)
	}
	_, err = pc.VotePoll(as(voters[1]), &corev1.VotePollRequest{TayfaId: tid, PollId: pid, OptionIds: []string{opts[0]}})
	if _, info := infoOf(t, err); info.GetReason() != corev1.ErrorReason_ERROR_REASON_POLL_CLOSED.String() {
		t.Fatalf("kapalı anket: %v", err)
	}

	for _, o := range opts {
		if strings.Contains(logs.String(), o) {
			t.Fatalf("log'da gizli anketin seçeneği var: %s", logs.String())
		}
	}
}
