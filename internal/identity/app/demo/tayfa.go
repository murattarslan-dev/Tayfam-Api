package demo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// seedTayfa creates one demo tayfa (or replays its create), lets the demo
// members in, writes the content and adds the owner last, so the owner's
// devices are not notified of every demo record. A tayfa that no longer
// takes changes (archived, suspended, deletion pending) is reported and
// left as it is.
func (s *Seeder) seedTayfa(ctx context.Context, t tayfaSpec, users map[string]id.ID, owner id.ID) (TayfaResult, error) {
	res := TayfaResult{Name: t.Name, OwnerRole: t.OwnerRole.String()}
	founder := users[t.Founder]
	created, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.CreateTayfaResponse, error) {
		return s.cfg.Core.Tayfas.CreateTayfa(ctx, &corev1.CreateTayfaRequest{
			Name: t.Name, Description: t.Description, Interests: t.Interests,
			JoinPolicy:      corev1.JoinPolicy_JOIN_POLICY_APPROVAL,
			ClientRequestId: requestID(keyOf(t.Key, "tayfa")),
		})
	})
	if err != nil {
		return res, fmt.Errorf("tayfa oluşturma: %w", err)
	}
	tayfaID := created.GetTayfa().GetId()
	res.ID = tayfaID

	err = s.fillTayfa(ctx, t, tayfaID, users, owner)
	if errors.Is(err, errReadOnly) {
		res.ReadOnly = true
		s.cfg.Log.WarnContext(ctx, "demo tayfası salt okunur; kalan adımlar atlandı", "tayfa_id", tayfaID)
		return res, nil
	}
	return res, err
}

func (s *Seeder) fillTayfa(ctx context.Context, t tayfaSpec, tayfaID string, users map[string]id.ID, owner id.ID) error {
	founder := users[t.Founder]
	for _, m := range t.Members {
		if err := s.ensureMember(ctx, tayfaID, founder, users[m.Person], m.Role); err != nil {
			return err
		}
	}
	for _, e := range t.Events {
		if err := s.seedEvent(ctx, t, e, tayfaID, users); err != nil {
			return err
		}
	}
	for _, n := range t.Notices {
		if err := s.seedNotice(ctx, t, n, tayfaID, founder); err != nil {
			return err
		}
	}
	for _, q := range t.Questions {
		if err := s.seedQuestion(ctx, t, q, tayfaID, users); err != nil {
			return err
		}
	}
	for _, p := range t.Polls {
		if err := s.seedPoll(ctx, t, p, tayfaID, users); err != nil {
			return err
		}
	}
	for _, c := range t.Channels {
		if err := s.seedChannel(ctx, t, c, tayfaID, users); err != nil {
			return err
		}
	}
	return s.ensureMember(ctx, tayfaID, founder, owner, t.OwnerRole)
}

// activeMembers lists the tayfa's active members and their roles.
func (s *Seeder) activeMembers(ctx context.Context, tayfaID string, founder id.ID) (map[string]corev1.Role, error) {
	out := map[string]corev1.Role{}
	token := ""
	for {
		resp, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.ListMembersResponse, error) {
			return s.cfg.Core.Memberships.ListMembers(ctx, &corev1.ListMembersRequest{
				TayfaId: tayfaID, Status: corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE, PageSize: 100, PageToken: token,
			})
		})
		if err != nil {
			return nil, check("üye listesi", err)
		}
		for _, m := range resp.GetMembers() {
			out[m.GetUserId()] = m.GetRole()
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// ensureMember brings user into the tayfa with at least role: a join
// request by the tayfa's join code, approved by the founder, then a role
// change by the founder. A higher role than wanted is left alone.
func (s *Seeder) ensureMember(ctx context.Context, tayfaID string, founder, user id.ID, role corev1.Role) error {
	members, err := s.activeMembers(ctx, tayfaID, founder)
	if err != nil {
		return err
	}
	current, ok := members[user.String()]
	if !ok {
		got, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.GetTayfaResponse, error) {
			return s.cfg.Core.Tayfas.GetTayfa(ctx, &corev1.GetTayfaRequest{TayfaId: tayfaID})
		})
		if err != nil {
			return check("tayfa", err)
		}
		code := got.GetTayfa().GetJoinCode() // never logged
		_, err = call(ctx, s, user, func(ctx context.Context) (*corev1.JoinByCodeResponse, error) {
			return s.cfg.Core.Memberships.JoinByCode(ctx, &corev1.JoinByCodeRequest{Code: code})
		})
		if err != nil && !is(err, "ERROR_REASON_ALREADY_PENDING", "ERROR_REASON_ALREADY_MEMBER") {
			return check("katılım", err)
		}
		_, err = call(ctx, s, founder, func(ctx context.Context) (*corev1.ApproveMemberResponse, error) {
			return s.cfg.Core.Memberships.ApproveMember(ctx, &corev1.ApproveMemberRequest{TayfaId: tayfaID, UserId: user.String()})
		})
		if err != nil && !is(err, "ERROR_REASON_MEMBER_NOT_FOUND") {
			return check("onay", err)
		}
		current = corev1.Role_ROLE_MEMBER
	}
	if current >= role {
		return nil
	}
	_, err = call(ctx, s, founder, func(ctx context.Context) (*corev1.ChangeRoleResponse, error) {
		return s.cfg.Core.Memberships.ChangeRole(ctx, &corev1.ChangeRoleRequest{TayfaId: tayfaID, UserId: user.String(), NewRole: role})
	})
	return check("rol", err)
}

func (s *Seeder) seedEvent(ctx context.Context, t tayfaSpec, e eventSpec, tayfaID string, users map[string]id.ID) error {
	founder := users[t.Founder]
	start := s.startTime(e.Start)
	req := &corev1.CreateEventRequest{
		TayfaId: tayfaID, Type: e.Type, Mode: corev1.EventMode_EVENT_MODE_IN_PERSON,
		Title: e.Title, Description: e.Description,
		StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Duration(e.Hours) * time.Hour)),
		Capacity:        e.Capacity,
		ClientRequestId: requestID(keyOf(t.Key, "event", e.Key)),
	}
	if e.Online {
		req.Mode, req.OnlineUrl = corev1.EventMode_EVENT_MODE_ONLINE, e.OnlineURL
	} else {
		req.MeetingPointName, req.City, req.District = e.PlaceName, e.City, e.District
		req.MeetingPoint = &corev1.LatLng{Lat: e.Lat, Lng: e.Lng}
	}
	switch {
	case e.Convoy != nil:
		req.Details = &corev1.CreateEventRequest_Convoy{Convoy: e.Convoy}
	case e.Camp != nil:
		req.Details = &corev1.CreateEventRequest_Camp{Camp: e.Camp}
	case e.Hike != nil:
		hike := &corev1.HikeDetails{Difficulty: e.Hike.Difficulty, DistanceKm: e.Hike.DistanceKm, ElevationM: e.Hike.ElevationM}
		if e.Hike.ReturnAfter > 0 {
			hike.ExpectedReturnTime = timestamppb.New(start.Add(time.Duration(e.Hike.ReturnAfter) * time.Hour))
		}
		req.Details = &corev1.CreateEventRequest_Hike{Hike: hike}
	case e.Fishing != nil:
		req.Details = &corev1.CreateEventRequest_Fishing{Fishing: e.Fishing}
	case e.Gaming != nil:
		req.Details = &corev1.CreateEventRequest_Gaming{Gaming: e.Gaming}
	case e.Sports != nil:
		req.Details = &corev1.CreateEventRequest_Sports{Sports: e.Sports}
	}
	created, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.CreateEventResponse, error) {
		return s.cfg.Core.Events.CreateEvent(ctx, req)
	})
	if err != nil {
		return check("etkinlik", err)
	}
	eventID := created.GetEvent().GetId()

	// An event that has ended or was cancelled since the first run takes no
	// more answers; its remaining steps are skipped.
	over := func(err error) bool { return is(err, "ERROR_REASON_EVENT_ENDED", "ERROR_REASON_EVENT_CANCELLED") }
	for _, r := range e.RSVPs {
		_, err := call(ctx, s, users[r.Person], func(ctx context.Context) (*corev1.SetRsvpResponse, error) {
			return s.cfg.Core.Events.SetRsvp(ctx, &corev1.SetRsvpRequest{TayfaId: tayfaID, EventId: eventID, Status: r.Status})
		})
		if over(err) {
			return nil
		}
		if err != nil {
			return check("katılım yanıtı", err)
		}
	}
	for _, v := range e.Vehicles {
		_, err := call(ctx, s, users[v.Person], func(ctx context.Context) (*corev1.AddConvoyVehicleResponse, error) {
			return s.cfg.Core.Events.AddConvoyVehicle(ctx, &corev1.AddConvoyVehicleRequest{
				TayfaId: tayfaID, EventId: eventID, Make: v.Make, Model: v.Model, ModelYear: v.Year, Color: v.Color,
			})
		})
		if over(err) {
			return nil
		}
		if err != nil {
			return check("konvoy aracı", err)
		}
	}
	if len(e.CampItems) == 0 {
		return nil
	}
	got, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.GetEventResponse, error) {
		return s.cfg.Core.Events.GetEvent(ctx, &corev1.GetEventRequest{TayfaId: tayfaID, EventId: eventID})
	})
	if err != nil {
		return check("etkinlik", err)
	}
	have := map[string]bool{}
	for _, it := range got.GetEvent().GetCamp().GetItems() {
		have[it.GetName()] = true
	}
	for _, it := range e.CampItems {
		if have[it.Name] {
			continue
		}
		req := &corev1.AddCampItemRequest{TayfaId: tayfaID, EventId: eventID, Name: it.Name, Quantity: it.Quantity}
		if it.Assigned != "" {
			req.AssignedUserId = users[it.Assigned].String()
		}
		_, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.AddCampItemResponse, error) {
			return s.cfg.Core.Events.AddCampItem(ctx, req)
		})
		if over(err) {
			return nil
		}
		if err != nil {
			return check("kamp malzemesi", err)
		}
	}
	return nil
}

func (s *Seeder) seedNotice(ctx context.Context, t tayfaSpec, n noticeSpec, tayfaID string, founder id.ID) error {
	created, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.CreateAnnouncementResponse, error) {
		return s.cfg.Core.Announcements.CreateAnnouncement(ctx, &corev1.CreateAnnouncementRequest{
			TayfaId: tayfaID, Title: n.Title, Body: n.Body,
			ClientRequestId: requestID(keyOf(t.Key, "notice", n.Key)),
		})
	})
	if err != nil {
		return check("duyuru", err)
	}
	if !n.Pinned || created.GetAnnouncement().GetPinned() {
		return nil
	}
	_, err = call(ctx, s, founder, func(ctx context.Context) (*corev1.SetAnnouncementPinnedResponse, error) {
		return s.cfg.Core.Announcements.SetAnnouncementPinned(ctx, &corev1.SetAnnouncementPinnedRequest{
			TayfaId: tayfaID, AnnouncementId: created.GetAnnouncement().GetId(), Pinned: true,
		})
	})
	return check("duyuru sabitleme", err)
}

func (s *Seeder) seedQuestion(ctx context.Context, t tayfaSpec, q questionSpec, tayfaID string, users map[string]id.ID) error {
	created, err := call(ctx, s, users[q.Author], func(ctx context.Context) (*corev1.CreateQuestionResponse, error) {
		return s.cfg.Core.QA.CreateQuestion(ctx, &corev1.CreateQuestionRequest{
			TayfaId: tayfaID, Title: q.Title, Body: q.Body, Tags: q.Tags,
			ClientRequestId: requestID(keyOf(t.Key, "question", q.Key)),
		})
	})
	if err != nil {
		return check("soru", err)
	}
	questionID := created.GetQuestion().GetId()
	for _, a := range q.Answers {
		ans, err := call(ctx, s, users[a.Author], func(ctx context.Context) (*corev1.CreateAnswerResponse, error) {
			return s.cfg.Core.QA.CreateAnswer(ctx, &corev1.CreateAnswerRequest{
				TayfaId: tayfaID, QuestionId: questionID, Body: a.Body,
				ClientRequestId: requestID(keyOf(t.Key, "question", q.Key, "answer", a.Key)),
			})
		})
		if err != nil {
			return check("cevap", err)
		}
		for _, v := range a.Voters {
			_, err := call(ctx, s, users[v], func(ctx context.Context) (*corev1.SetAnswerVoteResponse, error) {
				return s.cfg.Core.QA.SetAnswerVote(ctx, &corev1.SetAnswerVoteRequest{TayfaId: tayfaID, AnswerId: ans.GetAnswer().GetId(), Voted: true})
			})
			if err != nil {
				return check("faydalı oyu", err)
			}
		}
	}
	return nil
}

func (s *Seeder) seedPoll(ctx context.Context, t tayfaSpec, p pollSpec, tayfaID string, users map[string]id.ID) error {
	created, err := call(ctx, s, users[p.Author], func(ctx context.Context) (*corev1.CreatePollResponse, error) {
		return s.cfg.Core.Polls.CreatePoll(ctx, &corev1.CreatePollRequest{
			TayfaId: tayfaID, Question: p.Question, Options: p.Options,
			Anonymous: p.Anonymous, MultipleChoice: p.Multiple,
			ClientRequestId: requestID(keyOf(t.Key, "poll", p.Key)),
		})
	})
	if err != nil {
		return check("anket", err)
	}
	poll := created.GetPoll()
	options := poll.GetOptions()
	for _, v := range p.Votes {
		ids := make([]string, 0, len(v.Options))
		for _, i := range v.Options {
			if i < 0 || i >= len(options) {
				return fmt.Errorf("anket %s: seçenek %d yok", p.Key, i)
			}
			ids = append(ids, options[i].GetId())
		}
		_, err := call(ctx, s, users[v.Person], func(ctx context.Context) (*corev1.VotePollResponse, error) {
			return s.cfg.Core.Polls.VotePoll(ctx, &corev1.VotePollRequest{TayfaId: tayfaID, PollId: poll.GetId(), OptionIds: ids})
		})
		if is(err, "ERROR_REASON_POLL_CLOSED") {
			return nil
		}
		if err != nil {
			return check("anket oyu", err)
		}
	}
	return nil
}

func (s *Seeder) seedChannel(ctx context.Context, t tayfaSpec, c channelSpec, tayfaID string, users map[string]id.ID) error {
	founder := users[t.Founder]
	channelID, err := s.findChannel(ctx, tayfaID, founder, c.Name)
	if err != nil {
		return err
	}
	if channelID == "" {
		vis := chatv1.ChannelVisibility_CHANNEL_VISIBILITY_MEMBERS
		if c.Staff {
			vis = chatv1.ChannelVisibility_CHANNEL_VISIBILITY_STAFF
		}
		created, err := call(ctx, s, founder, func(ctx context.Context) (*chatv1.CreateChannelResponse, error) {
			return s.cfg.Chat.CreateChannel(ctx, &chatv1.CreateChannelRequest{TayfaId: tayfaID, Name: c.Name, Visibility: vis})
		})
		switch {
		case err == nil:
			channelID = created.GetChannel().GetId()
		case is(err, "ERROR_REASON_CHANNEL_NAME_TAKEN"):
			if channelID, err = s.findChannel(ctx, tayfaID, founder, c.Name); err != nil {
				return err
			}
			if channelID == "" {
				return errors.New("kanal: ad alınmış ama kanal görünmüyor")
			}
		default:
			return check("kanal", err)
		}
	}
	for _, m := range c.Messages {
		_, err := call(ctx, s, users[m.Author], func(ctx context.Context) (*chatv1.SendMessageResponse, error) {
			return s.cfg.Chat.SendMessage(ctx, &chatv1.SendMessageRequest{
				TayfaId: tayfaID, ChannelId: channelID, Body: m.Body,
				ClientRequestId: requestID(keyOf(t.Key, "channel", c.Name, m.Key)),
			})
		})
		if err != nil {
			return check("kanal mesajı", err)
		}
	}
	return nil
}

func (s *Seeder) findChannel(ctx context.Context, tayfaID string, founder id.ID, name string) (string, error) {
	resp, err := call(ctx, s, founder, func(ctx context.Context) (*chatv1.ListChannelsResponse, error) {
		return s.cfg.Chat.ListChannels(ctx, &chatv1.ListChannelsRequest{TayfaId: tayfaID})
	})
	if err != nil {
		return "", check("kanal listesi", err)
	}
	for _, ch := range resp.GetChannels() {
		if ch.GetName() == name {
			return ch.GetId(), nil
		}
	}
	return "", nil
}
