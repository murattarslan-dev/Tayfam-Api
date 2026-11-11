package export

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	chatdb "github.com/murattarslan-dev/tayfam-api/db/chat"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/chat/app/channelcrypt"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The export holds the caller's own channel messages, decrypted, and
// their preferences; another user's messages and the text of deleted
// messages stay out.
func TestExport(t *testing.T) {
	d := dbtest.Open(t, "chat", chatdb.Migrations())
	cipher := channelcrypt.Ephemeral()
	me, other := id.New(), id.New()
	tayfa, channel := id.New(), id.New()
	_, err := d.Admin.Exec(ctx, `INSERT INTO channels (id, tayfa_id, name, name_normalized) VALUES ($1, $2, 'genel', 'genel')`, channel.PG(), tayfa.PG())
	must(t, err)
	write := func(author id.ID, body string, deleted bool) id.ID {
		m := id.New()
		s := cipher.Encrypt(m, channel, body)
		_, err := d.Admin.Exec(ctx, `INSERT INTO channel_messages (id, channel_id, author_id, body_encrypted, key_version, client_request_id, deleted_at, deleted_by)
			VALUES ($1, $2, $3, $4, $5, gen_random_uuid(), CASE WHEN $6 THEN now() END, CASE WHEN $6 THEN $3::uuid END)`,
			m.PG(), channel.PG(), author.PG(), s.Data, s.Version, deleted)
		must(t, err)
		return m
	}
	mine := write(me, "Selam tayfa", false)
	write(me, "silinen metin", true)
	write(other, "başkasının mesajı", false)
	_, err = d.Admin.Exec(ctx, `INSERT INTO notification_preferences (user_id, scope_type, scope_id, level) VALUES ($1, 'tayfa', $2, 'none'), ($3, 'tayfa', $2, 'all')`,
		me.PG(), tayfa.PG(), other.PG())
	must(t, err)

	for _, cfg := range []Config{{}, {PageSize: 1, ChunkBytes: 1}} {
		cfg.Pool, cfg.Cipher = d.App, cipher
		s, err := New(cfg)
		must(t, err)
		got := map[string][]map[string]any{}
		must(t, s.Export(ctx, me, func(c Chunk) error {
			for _, r := range c.Records {
				var m map[string]any
				must(t, json.Unmarshal(r, &m))
				got[c.Section] = append(got[c.Section], m)
			}
			return nil
		}))
		msgs := got[SectionChannelMessages]
		if len(msgs) != 2 || msgs[0]["id"] != mine.String() || msgs[0]["body"] != "Selam tayfa" || msgs[0]["tayfa_id"] != tayfa.String() {
			t.Fatalf("mesajlar: %v", msgs)
		}
		if msgs[1]["body"] != nil || msgs[1]["deleted_at"] == nil {
			t.Fatalf("silinen mesajın metni girmemeli: %v", msgs[1])
		}
		if p := got[SectionNotificationPreferences]; len(p) != 1 || p[0]["level"] != "none" {
			t.Fatalf("tercihler: %v", p)
		}
	}

	off, err := New(Config{Pool: d.App})
	must(t, err)
	if err := off.Export(ctx, me, func(Chunk) error { return nil }); !errors.Is(err, apperr.ErrChannelsUnavailable) {
		t.Fatalf("anahtar yokken: %v", err)
	}
	// A body that does not open is an error naming the message only.
	s, err := New(Config{Pool: d.App, Cipher: channelcrypt.Ephemeral()})
	must(t, err)
	if err := s.Export(ctx, me, func(Chunk) error { return nil }); err == nil || strings.Contains(err.Error(), "Selam") {
		t.Fatalf("başka anahtarla: %v", err)
	}
}
