package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

func int2(v int16) pgtype.Int2 { return pgtype.Int2{Int16: v, Valid: true} }

func at(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// sealed is a stand-in ciphertext of the minimum stored length.
func sealed(b byte) []byte { return bytes.Repeat([]byte{b}, 29) }

func newChannel(t *testing.T, q *Queries, tayfa id.ID, name, visibility string) id.ID {
	t.Helper()
	ch := id.New()
	if _, err := q.CreateChannel(context.Background(), CreateChannelParams{
		ID: ch.PG(), TayfaID: tayfa.PG(), Name: name, NameNormalized: name, Visibility: visibility, CreatedBy: id.New().PG(),
	}); err != nil {
		t.Fatal(err)
	}
	return ch
}

func newMessage(t *testing.T, q *Queries, ch, author id.ID) id.ID {
	t.Helper()
	m := id.New()
	row, err := q.CreateMessage(context.Background(), CreateMessageParams{
		ID: m.PG(), ChannelID: ch.PG(), AuthorID: author.PG(), BodyEncrypted: sealed(1), KeyVersion: int2(1), ClientRequestID: id.New().PG(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != m.PG() {
		t.Fatal("mesaj kimliği farklı")
	}
	return m
}

func violates(err error, constraint string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.ConstraintName == constraint
}

func TestChannelConstraints(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	tayfa := id.New()
	ch := newChannel(t, q, tayfa, "genel", "members")

	// Same normalized name in the same tayfa: refused while not archived.
	_, err := q.CreateChannel(ctx, CreateChannelParams{ID: id.New().PG(), TayfaID: tayfa.PG(), Name: "Genel", NameNormalized: "genel", Visibility: "members"})
	if !violates(err, "channels_name_unique") {
		t.Fatalf("aynı isim: %v", err)
	}
	// Another tayfa may use it.
	newChannel(t, q, id.New(), "genel", "members")
	// After archiving, the name is free again.
	if _, err := q.SetChannelArchived(ctx, SetChannelArchivedParams{TayfaID: tayfa.PG(), ID: ch.PG(), ArchivedAt: at(time.Now())}); err != nil {
		t.Fatal(err)
	}
	newChannel(t, q, tayfa, "genel", "members")

	for name, p := range map[string]CreateChannelParams{
		"channels_name_len":            {Name: "", NameNormalized: "x", Visibility: "members"},
		"channels_visibility":          {Name: "x", NameNormalized: "x", Visibility: "public"},
		"channels_slow_mode":           {Name: "x", NameNormalized: "x", Visibility: "members", SlowModeSeconds: 3601},
		"channels_name_normalized_len": {Name: "x", NameNormalized: strings.Repeat("a", 41), Visibility: "members"},
	} {
		p.ID, p.TayfaID = id.New().PG(), tayfa.PG()
		if _, err := q.CreateChannel(ctx, p); !violates(err, name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMessageConstraints(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	ch := newChannel(t, q, id.New(), "genel", "members")
	author := id.New()
	for name, p := range map[string]CreateMessageParams{
		"channel_messages_key":         {BodyEncrypted: sealed(1)},
		"channel_messages_key_version": {BodyEncrypted: sealed(1), KeyVersion: int2(0)},
		"channel_messages_body_len":    {BodyEncrypted: []byte{1, 2, 3}, KeyVersion: int2(1)},
		"channel_messages_live_body":   {},
	} {
		p.ID, p.ChannelID, p.AuthorID, p.ClientRequestID = id.New().PG(), ch.PG(), author.PG(), id.New().PG()
		if _, err := q.CreateMessage(ctx, p); !violates(err, name) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A message cannot belong to a channel that does not exist.
	_, err := q.CreateMessage(ctx, CreateMessageParams{
		ID: id.New().PG(), ChannelID: id.New().PG(), AuthorID: author.PG(), BodyEncrypted: sealed(1), KeyVersion: int2(1), ClientRequestID: id.New().PG(),
	})
	if !violates(err, "channel_messages_channel_id_fkey") {
		t.Errorf("olmayan kanal: %v", err)
	}
}

func TestCreateMessageIsIdempotent(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	ch := newChannel(t, q, id.New(), "genel", "members")
	author, req := id.New(), id.New()
	p := CreateMessageParams{ID: id.New().PG(), ChannelID: ch.PG(), AuthorID: author.PG(), BodyEncrypted: sealed(1), KeyVersion: int2(1), ClientRequestID: req.PG()}
	first, err := q.CreateMessage(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	p.ID = id.New().PG()
	if _, err := q.CreateMessage(ctx, p); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("tekrar gönderim yeni satır açtı: %v", err)
	}
	got, err := q.GetMessageByRequest(ctx, GetMessageByRequestParams{AuthorID: author.PG(), ClientRequestID: req.PG()})
	if err != nil || got.ID != first.ID {
		t.Fatalf("GetMessageByRequest = %v, %v", got.ID, err)
	}
	// Another author may use the same client_request_id.
	createMessageWith := CreateMessageParams{ID: id.New().PG(), ChannelID: ch.PG(), AuthorID: id.New().PG(), BodyEncrypted: sealed(1), KeyVersion: int2(1), ClientRequestID: req.PG()}
	if _, err := q.CreateMessage(ctx, createMessageWith); err != nil {
		t.Fatal(err)
	}
}

func TestMessagePagingAndUnread(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	tayfa := id.New()
	ch := newChannel(t, q, tayfa, "genel", "members")
	staff := newChannel(t, q, tayfa, "yönetim", "staff")
	reader, other := id.New(), id.New()
	var ids []id.ID
	for range 5 {
		ids = append(ids, newMessage(t, q, ch, other))
	}
	own := newMessage(t, q, ch, reader)

	page, err := q.ListMessagesBefore(ctx, ListMessagesBeforeParams{ChannelID: ch.PG(), N: 2})
	if err != nil || len(page) != 2 || page[0].ID != own.PG() || page[1].ID != ids[4].PG() {
		t.Fatalf("ilk sayfa: %v", err)
	}
	page, err = q.ListMessagesBefore(ctx, ListMessagesBeforeParams{ChannelID: ch.PG(), BeforeID: ids[4].PG(), N: 10})
	if err != nil || len(page) != 4 || page[0].ID != ids[3].PG() {
		t.Fatalf("ikinci sayfa: %d, %v", len(page), err)
	}
	after, err := q.ListMessagesAfter(ctx, ListMessagesAfterParams{ChannelID: ch.PG(), AfterID: ids[2].PG(), N: 10})
	if err != nil || len(after) != 3 || after[0].ID != ids[3].PG() || after[2].ID != own.PG() {
		t.Fatalf("sonrası: %d, %v", len(after), err)
	}

	unread := func(includeStaff bool) map[id.ID]int64 {
		t.Helper()
		rows, err := q.ListChannels(ctx, ListChannelsParams{UserID: reader.PG(), MaxUnread: 3, TayfaID: tayfa.PG(), IncludeStaff: includeStaff})
		if err != nil {
			t.Fatal(err)
		}
		out := map[id.ID]int64{}
		for _, r := range rows {
			out[id.ID(r.ID.Bytes)] = r.UnreadCount
		}
		return out
	}
	// Five from others, capped at 3; the reader's own message does not count.
	if got := unread(false); len(got) != 1 || got[ch] != 3 {
		t.Fatalf("okunmamış = %v", got)
	}
	if got := unread(true); len(got) != 2 || got[staff] != 0 {
		t.Fatalf("staff dahil = %v", got)
	}
	// Read up to ids[2]; a later mark of an older message does not go back.
	mark := func(m id.ID) {
		t.Helper()
		if err := q.MarkChannelRead(ctx, MarkChannelReadParams{ChannelID: ch.PG(), UserID: reader.PG(), MessageID: m.PG()}); err != nil {
			t.Fatal(err)
		}
	}
	mark(ids[2])
	mark(ids[0])
	if got := unread(false); got[ch] != 2 {
		t.Fatalf("okunduktan sonra = %v", got)
	}
	// A deleted message is not unread.
	if _, err := q.HideMessage(ctx, HideMessageParams{DeletedAt: at(time.Now()), DeletedBy: other.PG(), ChannelID: ch.PG(), ID: ids[3].PG()}); err != nil {
		t.Fatal(err)
	}
	if got := unread(false); got[ch] != 1 {
		t.Fatalf("silinen sayıldı = %v", got)
	}
}

func TestDeleteRestoreAndPurge(t *testing.T) {
	_, q := setup(t)
	ctx := context.Background()
	ch := newChannel(t, q, id.New(), "genel", "members")
	author, mod := id.New(), id.New()
	erased := newMessage(t, q, ch, author)
	hidden := newMessage(t, q, ch, author)
	now := time.Now().UTC()

	if n, err := q.EraseMessage(ctx, EraseMessageParams{DeletedAt: at(now), DeletedBy: author.PG(), ChannelID: ch.PG(), ID: erased.PG()}); err != nil || n != 1 {
		t.Fatalf("EraseMessage = %d, %v", n, err)
	}
	m, err := q.GetMessage(ctx, GetMessageParams{ChannelID: ch.PG(), ID: erased.PG()})
	if err != nil || m.BodyEncrypted != nil || m.KeyVersion.Valid || !m.DeletedAt.Valid {
		t.Fatalf("yazar silmesi metni bıraktı: %v", err)
	}
	// An erased message cannot be restored.
	if n, _ := q.RestoreMessage(ctx, RestoreMessageParams{ChannelID: ch.PG(), ID: erased.PG()}); n != 0 {
		t.Fatal("silinen metin geri alındı")
	}

	if n, err := q.HideMessage(ctx, HideMessageParams{DeletedAt: at(now.Add(-31 * 24 * time.Hour)), DeletedBy: mod.PG(), ChannelID: ch.PG(), ID: hidden.PG()}); err != nil || n != 1 {
		t.Fatalf("HideMessage = %d, %v", n, err)
	}
	// Hiding twice changes nothing.
	if n, _ := q.HideMessage(ctx, HideMessageParams{DeletedAt: at(now), DeletedBy: mod.PG(), ChannelID: ch.PG(), ID: hidden.PG()}); n != 0 {
		t.Fatal("iki kez gizlendi")
	}
	// Scoped by channel: the same ID in another channel is not found.
	if n, _ := q.RestoreMessage(ctx, RestoreMessageParams{ChannelID: id.New().PG(), ID: hidden.PG()}); n != 0 {
		t.Fatal("başka kanaldan geri alındı")
	}

	n, err := q.PurgeDeletedMessages(ctx, PurgeDeletedMessagesParams{Before: at(now.Add(-30 * 24 * time.Hour)), N: 100})
	if err != nil || n != 1 {
		t.Fatalf("PurgeDeletedMessages = %d, %v", n, err)
	}
	if _, err := q.GetMessage(ctx, GetMessageParams{ChannelID: ch.PG(), ID: hidden.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("30 günlük mesaj kaldı: %v", err)
	}
	if _, err := q.GetMessage(ctx, GetMessageParams{ChannelID: ch.PG(), ID: erased.PG()}); err != nil {
		t.Fatalf("yeni silinen mesaj gitti: %v", err)
	}
}

func TestUserAndTayfaCleanup(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	tayfa := id.New()
	ch := newChannel(t, q, tayfa, "genel", "members")
	user := id.New()
	m := newMessage(t, q, ch, user)
	if err := q.MarkChannelRead(ctx, MarkChannelReadParams{ChannelID: ch.PG(), UserID: user.PG(), MessageID: m.PG()}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(context.Context, pgtype.UUID) error{
		q.ClearChannelMessageAuthor, q.ClearChannelMessageDeletedBy, q.ClearChannelCreator, q.DeleteUserChannelReads,
	} {
		if err := f(ctx, user.PG()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := q.GetMessage(ctx, GetMessageParams{ChannelID: ch.PG(), ID: m.PG()})
	if err != nil || got.AuthorID.Valid || got.BodyEncrypted == nil {
		t.Fatalf("hesap silme: yazar kaldı ya da içerik gitti: %v", err)
	}
	if _, err := q.GetChannelRead(ctx, GetChannelReadParams{ChannelID: ch.PG(), UserID: user.PG()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("okunma kaydı kaldı: %v", err)
	}

	if err := q.DeleteTayfaChannels(ctx, tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM channels) + (SELECT count(*) FROM channel_messages) + (SELECT count(*) FROM channel_reads)").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("tayfa silinince %d satır kaldı", left)
	}
}
