package userevents

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/export"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The KVKK inventory of core (issue #275, the last audit of v3): every
// column that holds a person, what account deletion does to it and where the
// person finds the rows in the data export (ADR-0021,
// docs/data-export-format.md). TestPersonColumnInventory fails when a new
// column that holds a person is not written here, so a new table cannot
// skip the decision.

// fate is what account deletion does to a column that holds the person.
type fate int

const (
	// removed: the rows that refer to the person are deleted.
	removed fate = iota + 1
	// cleared: the column becomes NULL and the row stays (it is the
	// tayfa's, the event's or someone else's).
	cleared
	// kept: the reference stays; note says why.
	kept
)

type personColumn struct {
	fate fate
	// export is the core export section with the person's rows, "" when
	// they are not exported; note says why.
	export string
	note   string
}

// Notes for the columns that are not exported.
const (
	noteModeration = "başkasının içeriğinde organizatör ya da moderatör işlemi; denetim kaydı gibi dışa aktarılmaz"
	noteOthers     = "karşı tarafın verisi; kişi kendi tarafını kendi bölümünde görür"
	noteInternal   = "iç kayıt; içerik ya da kişisel tercih değil"
)

var personColumns = map[string]personColumn{
	"announcements.author_id":                {cleared, export.SectionAnnouncements, ""},
	"announcements.deleted_by":               {cleared, "", noteModeration},
	"answer_votes.user_id":                   {removed, export.SectionAnswerVotes, ""},
	"answers.author_id":                      {cleared, export.SectionAnswers, ""},
	"answers.deleted_by":                     {cleared, "", noteModeration},
	"audit_logs.actor_id":                    {cleared, "", "denetim kaydı (Girmeyenler)"},
	"blocked_domains.created_by":             {kept, "", "ekleyen platform yöneticisi; yazma yolu yok (#262), hesap silmede boşaltılması #262'de eklenmeli"},
	"discover_joins.user_id":                 {removed, export.SectionDiscover, ""},
	"domain_events.actor_id":                 {cleared, "", noteInternal + " (olay kuyruğu)"},
	"domain_events.subject_id":               {kept, "", "olay kuyruğu; silmenin kendi olayları kişiyi konu olarak taşır (chat silmeyi bununla uygular)"},
	"event_celebration_winners.user_id":      {removed, export.SectionCelebrationWins, ""},
	"event_celebrations.announced_by":        {cleared, "", noteModeration},
	"event_checklist_items.assigned_user_id": {cleared, export.SectionChecklistItems, ""},
	"event_checklist_items.created_by":       {cleared, export.SectionChecklistItems, ""},
	"event_draw_entries.user_id":             {cleared, export.SectionDrawEntries, ""},
	"event_feedback.user_id":                 {removed, export.SectionDiscover, ""},
	"event_guest_players.created_by":         {cleared, export.SectionGuestPlayers, ""},
	"event_join_qr_tokens.created_by":        {removed, "", "2 dakikalık QR kodu, yalnızca özeti"},
	"event_log_entries.deleted_by":           {cleared, "", noteModeration},
	"event_log_entries.user_id":              {removed, export.SectionFishingCatches, ""},
	"event_measurements.entered_by":          {cleared, "", noteModeration + " (başkası adına girilen değer)"},
	"event_measurements.user_id":             {removed, export.SectionMeasurements, ""},
	"event_photos.deleted_by":                {cleared, "", noteModeration},
	"event_photos.uploader_id":               {removed, export.SectionEventPhotos, ""},
	"event_question_votes.user_id":           {removed, export.SectionEventQuestions, ""},
	"event_questions.answered_by":            {cleared, "", noteModeration},
	"event_questions.author_id":              {removed, export.SectionEventQuestions, ""},
	"event_questions.deleted_by":             {cleared, "", noteModeration},
	"event_ranking_entries.entered_by":       {cleared, "", noteModeration + " (başkası adına girilen derece)"},
	"event_ranking_entries.user_id":          {removed, export.SectionRankingEntries, ""},
	"event_referees.user_id":                 {removed, export.SectionEventReferees, ""},
	"event_roll_call_checkins.user_id":       {removed, export.SectionHikeCheckins, ""},
	"event_rsvps.attendance_marked_by":       {cleared, "", noteModeration},
	"event_rsvps.user_id":                    {removed, export.SectionEventRSVPs, ""},
	"event_safety_contacts.contact_user_id":  {removed, "", noteOthers + " (sizi güvenlik kişisi seçen)"},
	"event_safety_contacts.user_id":          {removed, export.SectionDiscover, ""},
	"event_score_players.user_id":            {cleared, export.SectionScores, ""},
	"event_series.created_by":                {cleared, "", "serinin tekrar kuralı; serinin etkinlikleri events bölümünde"},
	"event_team_members.user_id":             {removed, export.SectionTeamMemberships, ""},
	"event_vehicle_seat_requests.user_id":    {removed, export.SectionVehicleSeatRequests, ""},
	"event_vehicles.user_id":                 {removed, export.SectionConvoyVehicles, ""},
	"event_vote_closures.closed_by":          {cleared, "", noteModeration},
	"event_vote_options.created_by":          {cleared, "", "oylamanın seçeneği etkinliğin planıdır; organizatör işlemi"},
	"event_votes.candidate_user_id":          {removed, "", noteOthers + " (size verilen oy)"},
	"event_votes.user_id":                    {removed, export.SectionEventVotes, ""},
	"events.cancelled_by":                    {cleared, "", noteModeration},
	"events.created_by":                      {cleared, export.SectionEvents, ""},
	"events.deleted_by":                      {cleared, "", noteModeration},
	"friend_request_outcomes.recipient_id":   {removed, "", noteInternal + " (arkadaşlık puanının girdisi, ADR-0030)"},
	"friend_request_outcomes.requester_id":   {removed, "", noteInternal + " (arkadaşlık puanının girdisi, ADR-0030)"},
	"friend_scores.user_high_id":             {removed, export.SectionSocialSettings, ""},
	"friend_scores.user_low_id":              {removed, export.SectionSocialSettings, ""},
	"friend_suggestions.suggested_user_id":   {removed, "", noteOthers},
	"friend_suggestions.user_id":             {removed, export.SectionFriendSuggestions, ""},
	"friendships.requested_by":               {removed, export.SectionFriendships, ""},
	"friendships.user_high_id":               {removed, export.SectionFriendships, ""},
	"friendships.user_low_id":                {removed, export.SectionFriendships, ""},
	"garage_tayfa_visibility.user_id":        {removed, export.SectionGarageVisibility, ""},
	"garage_vehicles.deleted_by":             {cleared, "", noteModeration},
	"garage_vehicles.owner_id":               {removed, export.SectionGarageVehicles, ""},
	"invitation_outcomes.user_id":            {removed, "", noteInternal + " (arkadaşlık puanının girdisi, ADR-0030)"},
	"invites.created_by":                     {cleared, "", "davet bağlantısı tayfanındır; yalnızca özeti saklanır"},
	"library_items.owner_id":                 {cleared, export.SectionLibraryItems, ""},
	"library_items.withdrawn_by":             {cleared, "", noteModeration},
	"library_loans.borrower_id":              {cleared, export.SectionLibraryLoans, ""},
	"library_queue.user_id":                  {removed, export.SectionLibraryLoans, ""},
	"materials.author_id":                    {cleared, export.SectionMaterials, ""},
	"materials.deleted_by":                   {cleared, "", noteModeration},
	"memberships.approved_by":                {cleared, "", noteModeration},
	"memberships.invited_by_user_id":         {cleared, "", noteModeration},
	"memberships.muted_by":                   {cleared, "", noteModeration},
	"memberships.user_id":                    {removed, export.SectionMemberships, ""},
	"now_notification_counters.subject_id":   {removed, "", noteInternal + " (günlük şimdi bildirimi sayacı)"},
	"poll_votes.user_id":                     {removed, export.SectionPollVotes, ""},
	"polls.created_by":                       {cleared, export.SectionPolls, ""},
	"polls.deleted_by":                       {cleared, "", noteModeration},
	"questions.author_id":                    {cleared, export.SectionQuestions, ""},
	"questions.deleted_by":                   {cleared, "", noteModeration},
	"reports.handled_by":                     {cleared, "", noteModeration},
	"reports.platform_handled_by":            {cleared, "", noteModeration},
	"reports.reporter_id":                    {cleared, export.SectionReports, ""},
	"reports.subject_user_id":                {cleared, "", "hakkınızdaki şikâyet şikâyetçinin verisidir (ADR-0021)"},
	"social_settings.user_id":                {removed, export.SectionSocialSettings, ""},
	"tayfa_daily_items.created_by":           {cleared, export.SectionDailyItems, ""},
	"tayfa_daily_items.deleted_by":           {cleared, "", noteModeration},
	"tayfa_discover.requested_by":            {cleared, "", "tayfanın Keşfet başvurusu; tayfanındır"},
	"tayfa_discover.reviewed_by":             {cleared, "", noteModeration},
	"tayfa_vocabulary.created_by":            {cleared, export.SectionVocabulary, ""},
	"tayfas.created_by":                      {cleared, export.SectionMemberships, ""},
	"tayfas.deleted_by":                      {cleared, "", noteModeration},
	"tayfas.deletion_requested_by":           {cleared, "", noteModeration},
	"tayfas.suspended_by":                    {cleared, "", noteModeration},
	"uploads.user_id":                        {kept, export.SectionUploads, "dosyalar silme kuyruğuna girer (status deleting), media-cleanup satırı dosyayla siler; tayfa kapağı ve şikâyet kanıtı sahipsiz kalır"},
	"user_activity_records.user_id":          {removed, export.SectionActivityRecords, ""},
	"user_availability.user_id":              {removed, export.SectionAvailability, ""},
	"user_badges.user_id":                    {removed, export.SectionBadges, ""},
	"user_blocks.blocked_id":                 {removed, "", noteOthers + " (sizi engelleyen)"},
	"user_blocks.blocker_id":                 {removed, export.SectionBlocks, ""},
	"user_platform_bans.user_id":             {removed, "", "platform kararı (identity'de tutulur)"},
	"user_profiles.user_id":                  {kept, "", "silinmiş profil kaydı (tombstone): ad ve avatar boşalır; profil identity/profile.json'dadır"},
	"user_stats.user_id":                     {removed, export.SectionUserStats, ""},
}

// personColumnPattern finds the columns that hold a person by their name.
const personColumnPattern = `_by$|(^|_)(user|author|owner|borrower|uploader|actor|reporter|requester|recipient|blocker|blocked)_id$|^user_(low|high)_id$`

func (e *env) adminCount(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	must(t, e.admin.QueryRow(ctx, sql, args...).Scan(&n))
	return n
}

// uuidColumns lists table.column for every uuid column of core's tables.
func (e *env) uuidColumns(t *testing.T, pattern string) []string {
	t.Helper()
	rows, err := e.admin.Query(ctx, `SELECT c.table_name || '.' || c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables t USING (table_schema, table_name)
		WHERE c.table_schema = current_schema() AND t.table_type = 'BASE TABLE'
		  AND c.data_type = 'uuid' AND c.column_name ~ $1
		ORDER BY 1`, pattern)
	must(t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	must(t, err)
	return out
}

// Every column that holds a person is in personColumns with a decision,
// and personColumns names no column that is gone.
func TestPersonColumnInventory(t *testing.T) {
	e := setup(t)
	var missing []string
	for _, c := range e.uuidColumns(t, personColumnPattern) {
		if _, ok := personColumns[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		t.Errorf("kişi tutan yeni kolonlar KVKK envanterinde yok (hesap silme ve dışa aktarma kararını personColumns'a, davranışı account_deletion.sql'e ve export.sql'e yaz): %v", missing)
	}
	all := e.uuidColumns(t, ".")
	sections := export.Sections()
	for name, c := range personColumns {
		if !slices.Contains(all, name) {
			t.Errorf("%s: şemada uuid kolon değil", name)
		}
		if c.fate < removed || c.fate > kept {
			t.Errorf("%s: silme davranışı yok", name)
		}
		if c.export != "" && !slices.Contains(sections, c.export) {
			t.Errorf("%s: dışa aktarma bölümü %q yok", name, c.export)
		}
		if (c.export == "" || c.fate == kept) && c.note == "" {
			t.Errorf("%s: dışa aktarılmayan ya da silmede kalan kolonun gerekçesi yazılmalı", name)
		}
	}
}

// v3Seed writes one row of the person (%[1]s) in every v3 table, with rows
// of another member (%[2]s) beside them: tayfa %[3]s, another member's
// event %[4]s, the person's event %[5]s, team %[6]s, score table %[7]s,
// guest %[8]s, the person's seat %[9]s, the person's question %[10]s and
// another's %[11]s, the person's library item %[12]s and another's %[13]s.
const v3Seed = `
BEGIN;
SET LOCAL session_replication_role = replica;
INSERT INTO events (id, tayfa_id, type, mode, title, starts_at, ends_at, online_url, created_by) VALUES
  ('%[4]s', '%[3]s', 'general', 'online', 'Onun etkinliği', now() - interval '2 days', now() - interval '1 day', 'https://example.test/a', '%[2]s'),
  ('%[5]s', '%[3]s', 'general', 'online', 'Benim etkinliğim', now() - interval '2 days', now() - interval '1 day', 'https://example.test/b', '%[1]s');
INSERT INTO event_field_values (event_id, field_key, value) VALUES ('%[5]s', 'notes', '"Göl kenarı"');
INSERT INTO event_referees (id, event_id, user_id, component_key) VALUES
  (gen_random_uuid(), '%[4]s', '%[1]s', 'scoreboard'), (gen_random_uuid(), '%[4]s', '%[2]s', NULL);
INSERT INTO event_checklist_items (id, event_id, component_key, name, assigned_user_id, created_by) VALUES
  (gen_random_uuid(), '%[4]s', 'gear', 'Çadır', '%[1]s', '%[2]s'),
  (gen_random_uuid(), '%[4]s', 'gear', 'Ocak', NULL, '%[1]s'),
  (gen_random_uuid(), '%[4]s', 'gear', 'Masa', '%[2]s', '%[2]s');
INSERT INTO event_roll_call_checkins (event_id, component_key, user_id, started_at, started_received_at) VALUES
  ('%[4]s', 'roll_call', '%[1]s', now(), now()), ('%[4]s', 'roll_call', '%[2]s', now(), now());
INSERT INTO event_log_entries (id, event_id, component_key, user_id, client_request_id, lat, lng, location_visibility, deleted_at, deleted_by) VALUES
  (gen_random_uuid(), '%[4]s', 'catches', '%[1]s', gen_random_uuid(), 40.1, 29.1, 'self', NULL, NULL),
  (gen_random_uuid(), '%[4]s', 'catches', '%[2]s', gen_random_uuid(), NULL, NULL, NULL, now(), '%[1]s');
INSERT INTO tayfa_vocabulary (id, tayfa_id, namespace, value, value_normalized, created_by) VALUES
  (gen_random_uuid(), '%[3]s', 'fish_species', 'Levrek', 'levrek', '%[1]s'),
  (gen_random_uuid(), '%[3]s', 'fish_species', 'Çipura', 'çipura', '%[2]s');
INSERT INTO event_measurements (id, event_id, component_key, user_id, value, entered_by) VALUES
  (gen_random_uuid(), '%[4]s', 'distance', '%[1]s', 12.5, '%[2]s'),
  (gen_random_uuid(), '%[4]s', 'distance', '%[2]s', 8, '%[1]s'),
  (gen_random_uuid(), '%[4]s', 'total', NULL, 3, '%[1]s');
INSERT INTO event_teams (id, event_id, component_key, position) VALUES ('%[6]s', '%[4]s', 'teams', 1);
INSERT INTO event_team_members (event_id, component_key, team_id, user_id) VALUES
  ('%[4]s', 'teams', '%[6]s', '%[1]s'), ('%[4]s', 'teams', '%[6]s', '%[2]s');
INSERT INTO event_draws (event_id, component_key, subject, drawn_at) VALUES ('%[4]s', 'draw', 'participants', now());
INSERT INTO event_draw_entries (id, event_id, component_key, position, user_id) VALUES
  (gen_random_uuid(), '%[4]s', 'draw', 1, '%[1]s'), (gen_random_uuid(), '%[4]s', 'draw', 2, '%[2]s');
INSERT INTO event_score_tables (id, event_id, component_key, label, side, rounds, winner, result) VALUES
  ('%[7]s', '%[4]s', 'scoreboard', 'Masa 1', 'player', 'round', 'high', 'points');
INSERT INTO event_guest_players (id, event_id, created_by, nickname) VALUES ('%[8]s', '%[4]s', '%[1]s', 'Kuzen');
INSERT INTO event_score_players (id, table_id, event_id, component_key, position, user_id, guest_id) VALUES
  ('%[9]s', '%[7]s', '%[4]s', 'scoreboard', 1, '%[1]s', NULL),
  (gen_random_uuid(), '%[7]s', '%[4]s', 'scoreboard', 2, NULL, '%[8]s');
INSERT INTO event_scores (id, table_id, event_id, component_key, player_id, round, points) VALUES (gen_random_uuid(), '%[7]s', '%[4]s', 'scoreboard', '%[9]s', 1, 10);
INSERT INTO event_ranking_entries (id, event_id, component_key, user_id, value, entered_by) VALUES
  (gen_random_uuid(), '%[4]s', 'ranking', '%[1]s', 61.5, '%[2]s'),
  (gen_random_uuid(), '%[4]s', 'ranking', '%[2]s', 70, '%[1]s');
INSERT INTO event_celebrations (event_id, component_key, announced_at, updated_at, announced_by) VALUES ('%[4]s', 'celebration', now(), now(), '%[1]s');
INSERT INTO event_celebration_winners (id, event_id, component_key, user_id, position) VALUES
  (gen_random_uuid(), '%[4]s', 'celebration', '%[1]s', 1), (gen_random_uuid(), '%[4]s', 'celebration', '%[2]s', 2);
INSERT INTO event_questions (id, event_id, component_key, author_id, anonymous, body, client_request_id, answered_at, answered_by) VALUES
  ('%[10]s', '%[4]s', 'questions', '%[1]s', true, 'Benim sorum?', gen_random_uuid(), NULL, NULL),
  ('%[11]s', '%[4]s', 'questions', '%[2]s', false, 'Onun sorusu?', gen_random_uuid(), now(), '%[1]s');
INSERT INTO event_question_votes (question_id, event_id, component_key, user_id) VALUES
  ('%[11]s', '%[4]s', 'questions', '%[1]s'), ('%[10]s', '%[4]s', 'questions', '%[2]s');
INSERT INTO event_vote_options (id, event_id, component_key, label, label_normalized, position, created_by) VALUES
  (gen_random_uuid(), '%[4]s', 'vote', 'Suç ve Ceza', 'suç ve ceza', 1, '%[1]s');
INSERT INTO event_votes (id, event_id, component_key, user_id, option_id, created_at, updated_at)
  SELECT gen_random_uuid(), '%[4]s', 'vote', '%[1]s', o.id, now(), now() FROM event_vote_options o WHERE o.event_id = '%[4]s';
INSERT INTO event_votes (id, event_id, component_key, user_id, candidate_user_id, created_at, updated_at) VALUES
  (gen_random_uuid(), '%[4]s', 'mvp', '%[2]s', '%[1]s', now(), now());
INSERT INTO event_vote_closures (event_id, component_key, closed_by, closed_at) VALUES ('%[4]s', 'vote', '%[1]s', now());
INSERT INTO user_badges (id, user_id, badge_key, period_key, earned_at) VALUES
  (gen_random_uuid(), '%[1]s', 'first_event', 'all', now()), (gen_random_uuid(), '%[2]s', 'first_event', 'all', now());
INSERT INTO user_stats (id, user_id, tayfa_id, year, metric_key, source, value, event_count, computed_at) VALUES
  (gen_random_uuid(), '%[1]s', '%[3]s', 0, 'attended', 'common', 1, 1, now()),
  (gen_random_uuid(), '%[2]s', '%[3]s', 0, 'attended', 'common', 1, 1, now());
INSERT INTO event_join_qr_tokens (id, event_id, created_by, token_hash, created_at, expires_at) VALUES
  (gen_random_uuid(), '%[4]s', '%[1]s', sha256('qr'::bytea), now(), now() + interval '1 minute');
INSERT INTO now_notification_counters (scope, subject_id, day, sent) VALUES
  ('user', '%[1]s', current_date, 1), ('tayfa', '%[3]s', current_date, 1);
INSERT INTO tayfa_daily_items (id, tayfa_id, body, position, client_request_id, created_at, updated_at, created_by) VALUES
  (gen_random_uuid(), '%[3]s', 'Günün sözü', 0, gen_random_uuid(), now(), now(), '%[1]s');
INSERT INTO library_items (id, tayfa_id, kind, name, client_request_id, created_at, updated_at, owner_id) VALUES
  ('%[12]s', '%[3]s', 'tool', 'Matkap', gen_random_uuid(), now(), now(), '%[1]s'),
  ('%[13]s', '%[3]s', 'tool', 'Testere', gen_random_uuid(), now(), now(), '%[2]s');
INSERT INTO library_loans (id, item_id, tayfa_id, borrower_id, state, requested_at, approved_at, handed_over_at, ended_at) VALUES
  (gen_random_uuid(), '%[13]s', '%[3]s', '%[1]s', 'returned', now() - interval '2 days', now() - interval '2 days', now() - interval '2 days', now());
INSERT INTO library_queue (id, item_id, tayfa_id, user_id, position, created_at) VALUES (gen_random_uuid(), '%[13]s', '%[3]s', '%[1]s', 1, now());
INSERT INTO materials (id, tayfa_id, kind, title, url, domain, subject_key, client_request_id, created_at, updated_at, author_id) VALUES
  (gen_random_uuid(), '%[3]s', 'link', 'Ders notu', 'https://example.test/not', 'example.test', 'math', gen_random_uuid(), now(), now(), '%[1]s');
INSERT INTO garage_vehicles (id, owner_id, make, client_request_id, created_at, updated_at) VALUES
  (gen_random_uuid(), '%[1]s', 'Ford', gen_random_uuid(), now(), now()), (gen_random_uuid(), '%[2]s', 'Fiat', gen_random_uuid(), now(), now());
INSERT INTO garage_tayfa_visibility (user_id, tayfa_id, visible, updated_at) VALUES ('%[1]s', '%[3]s', true, now());
COMMIT;
`

// The table test of issue #275: every v3 personal record is in the
// person's export, and after account deletion nothing in core refers to
// the person, while the rows that are the tayfa's, the event's or someone
// else's stay without the person.
func TestV3PersonalDataExportAndDeletion(t *testing.T) {
	e := setup(t)
	u, other := id.New(), id.New()
	tf := e.tayfa(t, other)
	e.member(t, tf, other, "admin", "active", now.Add(-2*time.Hour))
	e.member(t, tf, u, "member", "active", now.Add(-time.Hour))
	ev, evU, team, table, guest, seat := id.New(), id.New(), id.New(), id.New(), id.New(), id.New()
	myQ, otherQ, myItem, otherItem := id.New(), id.New(), id.New(), id.New()
	if _, err := e.admin.Exec(ctx, fmt.Sprintf(v3Seed, u, other, tf, ev, evU, team, table, guest, seat, myQ, otherQ, myItem, otherItem)); err != nil {
		t.Fatalf("veri: %v", err)
	}

	exp, err := export.New(export.Config{Pool: e.pool})
	must(t, err)
	got := map[string][]map[string]any{}
	must(t, exp.Export(ctx, u, func(c export.Chunk) error {
		for _, r := range c.Records {
			var m map[string]any
			if err := json.Unmarshal(r, &m); err != nil {
				return fmt.Errorf("%s: %w", c.Section, err)
			}
			got[c.Section] = append(got[c.Section], m)
		}
		return nil
	}))
	for sec, want := range map[string]int{
		export.SectionMemberships:      1,
		export.SectionEvents:           1,
		export.SectionEventReferees:    1,
		export.SectionChecklistItems:   2, // added one, took on another
		export.SectionHikeCheckins:     1,
		export.SectionFishingCatches:   1,
		export.SectionVocabulary:       1,
		export.SectionMeasurements:     1,
		export.SectionTeamMemberships:  1,
		export.SectionDrawEntries:      1,
		export.SectionScores:           1,
		export.SectionGuestPlayers:     1,
		export.SectionRankingEntries:   1,
		export.SectionCelebrationWins:  1,
		export.SectionEventQuestions:   2, // the own question and the vote
		export.SectionBadges:           1,
		export.SectionUserStats:        1,
		export.SectionDailyItems:       1,
		export.SectionLibraryItems:     1,
		export.SectionLibraryLoans:     2, // the loan and the queue place
		export.SectionMaterials:        1,
		export.SectionGarageVehicles:   1,
		export.SectionGarageVisibility: 1,
		export.SectionEventVotes:       1, // the own vote; the vote for the person is the voter's
	} {
		if len(got[sec]) != want {
			t.Errorf("dışa aktarma %s: %d kayıt, %d bekleniyordu", sec, len(got[sec]), want)
		}
	}
	if ev := got[export.SectionEvents]; len(ev) == 1 {
		if f, _ := ev[0]["fields"].(map[string]any); f["notes"] != "Göl kenarı" || ev[0]["open_ended"] != false {
			t.Errorf("etkinliğin alan değerleri: %v", ev[0])
		}
	}
	for _, it := range got[export.SectionChecklistItems] {
		if it["assigned_to_me"] == it["added_by_me"] {
			t.Errorf("liste maddesi: %v", it)
		}
		for _, k := range []string{"assigned_user_id", "created_by"} {
			if _, ok := it[k]; ok {
				t.Errorf("liste maddesinde %s olmamalı", k)
			}
		}
	}
	if v := got[export.SectionVocabulary]; len(v) == 1 && v[0]["value"] != "Levrek" {
		t.Errorf("sözlük: %v", v)
	}
	if v := got[export.SectionEventVotes]; len(v) == 1 && (v[0]["target"] != "option" || v[0]["option_label"] != "Suç ve Ceza") {
		t.Errorf("oy: %v", v)
	}
	for sec, recs := range got {
		for _, r := range recs {
			if b, _ := json.Marshal(r); strings.Contains(string(b), other.String()) {
				t.Errorf("%s: başkasının kimliği dışa aktarıldı: %s", sec, b)
			}
		}
	}

	must(t, e.svc.Apply(ctx, Event{ID: id.New(), User: u, Deleted: true}))
	if refs := e.userRefs(t, u); len(refs) != 0 {
		t.Fatalf("core'da kullanıcıya ait kayıt kaldı: %v", refs)
	}
	for name, sql := range map[string]string{
		"sözlük değeri kalır":            `SELECT count(*) FROM tayfa_vocabulary WHERE value = 'Levrek' AND created_by IS NULL`,
		"misafir anonimleşir":            `SELECT count(*) FROM event_guest_players WHERE id = $2 AND nickname IS NULL AND created_by IS NULL`,
		"liste maddeleri kalır":          `SELECT (count(*) = 3)::int FROM event_checklist_items WHERE event_id = $1`,
		"kutlama kalır":                  `SELECT count(*) FROM event_celebrations WHERE event_id = $1 AND announced_by IS NULL`,
		"toplam ölçüm kalır":             `SELECT count(*) FROM event_measurements WHERE event_id = $1 AND user_id IS NULL AND entered_by IS NULL`,
		"başkasının ölçümü kalır":        `SELECT count(*) FROM event_measurements WHERE event_id = $1 AND user_id = $3 AND entered_by IS NULL`,
		"başkasının derecesi kalır":      `SELECT count(*) FROM event_ranking_entries WHERE event_id = $1 AND user_id = $3 AND entered_by IS NULL`,
		"yazboz yeri ve skoru kalır":     `SELECT count(*) FROM event_score_players p JOIN event_scores s ON s.player_id = p.id WHERE p.id = $4 AND p.user_id IS NULL`,
		"kura yeri boşalır":              `SELECT count(*) FROM event_draw_entries WHERE event_id = $1 AND position = 1 AND user_id IS NULL`,
		"başkasının sorusu kalır":        `SELECT count(*) FROM event_questions WHERE id = $5 AND answered_by IS NULL AND answered_at IS NOT NULL`,
		"başkasının yoklaması kalır":     `SELECT count(*) FROM event_roll_call_checkins WHERE event_id = $1 AND user_id = $3`,
		"başkasının günlük kaydı kalır":  `SELECT count(*) FROM event_log_entries WHERE event_id = $1 AND user_id = $3 AND deleted_by IS NULL`,
		"başkasının hakemliği kalır":     `SELECT count(*) FROM event_referees WHERE event_id = $1 AND user_id = $3`,
		"günün içeriği kalır":            `SELECT count(*) FROM tayfa_daily_items WHERE body = 'Günün sözü' AND created_by IS NULL`,
		"kütüphane kalemi geri çekilir":  `SELECT count(*) FROM library_items WHERE id = $6 AND owner_id IS NULL AND status = 'withdrawn'`,
		"ödünç kaydı kalır":              `SELECT count(*) FROM library_loans WHERE item_id = $7 AND borrower_id IS NULL`,
		"materyal kalır":                 `SELECT count(*) FROM materials WHERE title = 'Ders notu' AND author_id IS NULL`,
		"başkasının garajı kalır":        `SELECT count(*) FROM garage_vehicles WHERE owner_id = $3`,
		"tayfanın sayacı kalır":          `SELECT count(*) FROM now_notification_counters WHERE scope = 'tayfa'`,
		"oylama seçeneği kalır":          `SELECT count(*) FROM event_vote_options WHERE event_id = $1 AND created_by IS NULL`,
		"oylama kapanışı kalır":          `SELECT count(*) FROM event_vote_closures WHERE event_id = $1 AND closed_by IS NULL`,
		"kişiye verilen oy gider":        `SELECT (count(*) = 0)::int FROM event_votes WHERE event_id = $1`,
		"başkasının rozeti kalır":        `SELECT count(*) FROM user_badges WHERE user_id = $3`,
		"başkasının istatistiği kalır":   `SELECT count(*) FROM user_stats WHERE user_id = $3`,
		"başkasının etkinliği kalır":     `SELECT count(*) FROM events WHERE id = $1`,
		"kendi etkinliği sahipsiz kalır": `SELECT count(*) FROM events WHERE id = $8 AND created_by IS NULL`,
	} {
		// Every parameter is typed here, whichever the check uses.
		if n := e.adminCount(t, `SELECT n FROM (`+sql+`) x(n), (SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8::uuid) p`,
			ev.PG(), guest.PG(), other.PG(), seat.PG(), otherQ.PG(), myItem.PG(), otherItem.PG(), evU.PG()); n != 1 {
			t.Errorf("%s: %d", name, n)
		}
	}
}
