// Package export returns a user's own core data for the KVKK data export
// (issue #144; ADR-0008, ADR-0021; fields in docs/data-export-format.md).
//
// identity calls ExportService.ExportMyData with the user's own access
// token; the user is always the verified caller, never a parameter, so
// nobody can ask for another user's data. Every section reads only rows
// that belong to the caller (as author, member, voter, …); other users
// appear by ID only. The whole export reads one snapshot (REPEATABLE READ,
// READ ONLY transaction) in UTC.
//
// Records are JSON built by the database (db/core/queries/export.sql).
// Nothing of them is ever logged: the package logs nothing at all, and
// errors carry the section name only.
package export

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Section names; the archive file is core/<name>.json.
const (
	SectionMemberships     = "memberships"
	SectionEventRSVPs      = "event_rsvps"
	SectionHikeCheckins    = "hike_checkins" // every roll call (#242); the Faz 1 name
	SectionConvoyVehicles  = "convoy_vehicles"
	SectionActivityRecords = "activity_records"
	SectionAnnouncements   = "announcements"
	SectionEvents          = "events"
	SectionQuestions       = "questions"
	SectionAnswers         = "answers"
	SectionAnswerVotes     = "answer_votes"
	SectionPolls           = "polls"
	SectionPollVotes       = "poll_votes"
	SectionFishingCatches  = "fishing_catches" // every log entry (#243); the Faz 1 name
	SectionEventPhotos     = "event_photos"
	SectionFriendships     = "friendships"
	SectionBlocks          = "blocks"
	SectionAvailability    = "availability"
	SectionReports         = "reports"
	SectionUploads         = "uploads"
	// Issue #208, ADR-0030 §9: the settings and how many people a score is
	// kept with; never the scores or who they are with.
	SectionSocialSettings    = "social_settings"
	SectionFriendSuggestions = "friend_suggestions"
	// Issue #210, ADR-0033 §13: one's feedback answers and the safety
	// contacts one chose; being someone else's contact is their data.
	SectionDiscover = "discover"
	// Issue #240: one's requests for a seat in someone else's vehicle.
	// One's own vehicles are SectionConvoyVehicles (the name is from Faz 1).
	SectionVehicleSeatRequests = "vehicle_seat_requests"
	// Issue #245: one's personal measurements; totals belong to the event.
	SectionMeasurements = "measurements"
	// Issue #248: the teams one is in.
	SectionTeamMemberships = "team_memberships"
	// Issue #249: one's places in draws.
	SectionDrawEntries = "draw_entries"
	// Issue #250: one's seats and scores at scoreboard tables.
	SectionScores = "scoreboard_scores"
	// Issue #251: one's matches in brackets and one's own scores.
	SectionBracketMatches = "bracket_matches"
	// Issue #252: the guest players one added and their nicknames.
	SectionGuestPlayers = "guest_players"
	// Issue #253: one's rankings entered by hand; the computed ones are
	// one's measurements.
	SectionRankingEntries = "ranking_entries"
	// Issue #255: the celebrations that named one a winner by hand.
	SectionCelebrationWins = "celebration_wins"
	// Issue #256: the questions one asked (anonymous ones included) and
	// voted for.
	SectionEventQuestions = "event_questions"
	// Issue #258: one's badges (ADR-0039 §5).
	SectionBadges = "badges"
	// Issue #266: one's statistics per tayfa and year (ADR-0026).
	SectionUserStats = "user_stats"
	// Issue #269: the daily content items one added (ADR-0027 §5).
	SectionDailyItems = "daily_items"
	// Issue #270: the library items one owns and one's loans and queue
	// places (ADR-0040 §1).
	SectionLibraryItems = "library_items"
	SectionLibraryLoans = "library_loans"
)

const (
	// DefaultPageSize is how many rows one query reads.
	DefaultPageSize = 500
	// DefaultChunkBytes bounds the JSON of one chunk; far below gRPC's
	// 4 MiB message limit.
	DefaultChunkBytes = 1 << 20
)

// Chunk is part of one section.
type Chunk struct {
	Section string
	Records [][]byte
}

// row is one record and its paging key.
type row struct {
	key  pgtype.UUID
	data []byte
}

type fetch func(ctx context.Context, q *store.Queries, user, after pgtype.UUID, n int32) ([]row, error)

// rows converts sqlc's per-query row types, which all hold K and Data.
func rows[T any](in []T, err error, conv func(T) row) ([]row, error) {
	if err != nil {
		return nil, err
	}
	out := make([]row, len(in))
	for i, r := range in {
		out[i] = conv(r)
	}
	return out, nil
}

// sections are read in this order.
var sections = []struct {
	name  string
	fetch fetch
}{
	{SectionMemberships, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportMemberships(ctx, store.ExportMembershipsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportMembershipsRow) row { return row{x.K, x.Data} })
	}},
	{SectionEventRSVPs, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportEventRSVPs(ctx, store.ExportEventRSVPsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportEventRSVPsRow) row { return row{x.K, x.Data} })
	}},
	{SectionHikeCheckins, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportHikeCheckins(ctx, store.ExportHikeCheckinsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportHikeCheckinsRow) row { return row{x.K, x.Data} })
	}},
	{SectionConvoyVehicles, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportConvoyVehicles(ctx, store.ExportConvoyVehiclesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportConvoyVehiclesRow) row { return row{x.K, x.Data} })
	}},
	{SectionActivityRecords, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportActivityRecords(ctx, store.ExportActivityRecordsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportActivityRecordsRow) row { return row{x.K, x.Data} })
	}},
	{SectionAnnouncements, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportAnnouncements(ctx, store.ExportAnnouncementsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportAnnouncementsRow) row { return row{x.K, x.Data} })
	}},
	{SectionEvents, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportEvents(ctx, store.ExportEventsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportEventsRow) row { return row{x.K, x.Data} })
	}},
	{SectionQuestions, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportQuestions(ctx, store.ExportQuestionsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportQuestionsRow) row { return row{x.K, x.Data} })
	}},
	{SectionAnswers, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportAnswers(ctx, store.ExportAnswersParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportAnswersRow) row { return row{x.K, x.Data} })
	}},
	{SectionAnswerVotes, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportAnswerVotes(ctx, store.ExportAnswerVotesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportAnswerVotesRow) row { return row{x.K, x.Data} })
	}},
	{SectionPolls, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportPolls(ctx, store.ExportPollsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportPollsRow) row { return row{x.K, x.Data} })
	}},
	{SectionPollVotes, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportPollVotes(ctx, store.ExportPollVotesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportPollVotesRow) row { return row{x.K, x.Data} })
	}},
	{SectionFishingCatches, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportLogEntries(ctx, store.ExportLogEntriesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportLogEntriesRow) row { return row{x.K, x.Data} })
	}},
	{SectionEventPhotos, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportEventPhotos(ctx, store.ExportEventPhotosParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportEventPhotosRow) row { return row{x.K, x.Data} })
	}},
	{SectionFriendships, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportFriendships(ctx, store.ExportFriendshipsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportFriendshipsRow) row { return row{x.K, x.Data} })
	}},
	{SectionBlocks, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportBlocks(ctx, store.ExportBlocksParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportBlocksRow) row { return row{x.K, x.Data} })
	}},
	{SectionAvailability, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportAvailability(ctx, store.ExportAvailabilityParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportAvailabilityRow) row { return row{x.K, x.Data} })
	}},
	{SectionReports, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportReports(ctx, store.ExportReportsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportReportsRow) row { return row{x.K, x.Data} })
	}},
	{SectionUploads, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportUploads(ctx, store.ExportUploadsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportUploadsRow) row { return row{x.K, x.Data} })
	}},
	{SectionSocialSettings, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportSocialSettings(ctx, store.ExportSocialSettingsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportSocialSettingsRow) row { return row{x.K, x.Data} })
	}},
	{SectionFriendSuggestions, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportFriendSuggestions(ctx, store.ExportFriendSuggestionsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportFriendSuggestionsRow) row { return row{x.K, x.Data} })
	}},
	{SectionDiscover, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportDiscover(ctx, store.ExportDiscoverParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportDiscoverRow) row { return row{x.K, x.Data} })
	}},
	{SectionVehicleSeatRequests, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportVehicleSeatRequests(ctx, store.ExportVehicleSeatRequestsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportVehicleSeatRequestsRow) row { return row{x.K, x.Data} })
	}},
	{SectionMeasurements, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportMeasurements(ctx, store.ExportMeasurementsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportMeasurementsRow) row { return row{x.K, x.Data} })
	}},
	{SectionTeamMemberships, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportTeamMemberships(ctx, store.ExportTeamMembershipsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportTeamMembershipsRow) row { return row{x.K, x.Data} })
	}},
	{SectionDrawEntries, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportDrawEntries(ctx, store.ExportDrawEntriesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportDrawEntriesRow) row { return row{x.K, x.Data} })
	}},
	{SectionScores, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportScores(ctx, store.ExportScoresParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportScoresRow) row { return row{x.K, x.Data} })
	}},
	{SectionBracketMatches, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportBracketMatches(ctx, store.ExportBracketMatchesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportBracketMatchesRow) row { return row{x.K, x.Data} })
	}},
	{SectionGuestPlayers, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportGuestPlayers(ctx, store.ExportGuestPlayersParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportGuestPlayersRow) row { return row{x.K, x.Data} })
	}},
	{SectionRankingEntries, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportRankingEntries(ctx, store.ExportRankingEntriesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportRankingEntriesRow) row { return row{x.K, x.Data} })
	}},
	{SectionCelebrationWins, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportCelebrationWins(ctx, store.ExportCelebrationWinsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportCelebrationWinsRow) row { return row{x.K, x.Data} })
	}},
	{SectionEventQuestions, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportEventQuestions(ctx, store.ExportEventQuestionsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportEventQuestionsRow) row { return row{x.K, x.Data} })
	}},
	{SectionBadges, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportUserBadges(ctx, store.ExportUserBadgesParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportUserBadgesRow) row { return row{x.K, x.Data} })
	}},
	{SectionUserStats, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportUserStats(ctx, store.ExportUserStatsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportUserStatsRow) row { return row{x.K, x.Data} })
	}},
	{SectionDailyItems, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportDailyItems(ctx, store.ExportDailyItemsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportDailyItemsRow) row { return row{x.K, x.Data} })
	}},
	{SectionLibraryItems, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportLibraryItems(ctx, store.ExportLibraryItemsParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportLibraryItemsRow) row { return row{x.K, x.Data} })
	}},
	{SectionLibraryLoans, func(ctx context.Context, q *store.Queries, u, a pgtype.UUID, n int32) ([]row, error) {
		r, err := q.ExportLibraryLoans(ctx, store.ExportLibraryLoansParams{UserID: u, After: a, N: n})
		return rows(r, err, func(x store.ExportLibraryLoansRow) row { return row{x.K, x.Data} })
	}},
}

// Sections returns the section names in export order.
func Sections() []string {
	out := make([]string, len(sections))
	for i, s := range sections {
		out[i] = s.name
	}
	return out
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// PageSize and ChunkBytes; zero means the defaults (tests use small
	// values).
	PageSize   int32
	ChunkBytes int
}

// Service reads exports.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("export: eksik yapılandırma")
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = DefaultChunkBytes
	}
	return &Service{cfg: cfg}, nil
}

// Export sends user's records section by section to send. A send error
// stops the export and is returned as it is.
func (s *Service) Export(ctx context.Context, user id.ID, send func(Chunk) error) error {
	if user == (id.ID{}) {
		return errors.New("export: kullanıcı yok")
	}
	tx, err := s.cfg.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("dışa aktarma: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Times in the records are RFC 3339 in UTC.
	if _, err := tx.Exec(ctx, "SET LOCAL TimeZone = 'UTC'"); err != nil {
		return fmt.Errorf("dışa aktarma: %w", err)
	}
	q := store.New(tx)
	for _, sec := range sections {
		if err := s.section(ctx, q, user, sec.name, sec.fetch, send); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) section(ctx context.Context, q *store.Queries, user id.ID, name string, f fetch, send func(Chunk) error) error {
	after := pgtype.UUID{Valid: true} // the nil UUID: before every key
	chunk := Chunk{Section: name}
	size := 0
	for {
		page, err := f(ctx, q, user.PG(), after, s.cfg.PageSize)
		if err != nil {
			return fmt.Errorf("dışa aktarma %s: %w", name, err)
		}
		for _, r := range page {
			if len(chunk.Records) > 0 && size+len(r.data) > s.cfg.ChunkBytes {
				if err := send(chunk); err != nil {
					return err
				}
				chunk, size = Chunk{Section: name}, 0
			}
			chunk.Records = append(chunk.Records, r.data)
			size += len(r.data)
		}
		if int32(len(page)) < s.cfg.PageSize { //nolint:gosec // at most PageSize rows
			break
		}
		after = page[len(page)-1].key
	}
	if len(chunk.Records) == 0 {
		return nil
	}
	return send(chunk)
}
