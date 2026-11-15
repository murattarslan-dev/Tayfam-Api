package corerelay

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

func TestRequest(t *testing.T) {
	up, msg := id.New(), id.New()
	payload, _ := json.Marshal(map[string]string{"upload_id": up.String(), "message_id": msg.String()})
	e := outbox.Event{ID: id.New(), Type: TypeMediaReleased, Payload: payload, OccurredAt: time.Now()}
	req, err := Request(e)
	if err != nil {
		t.Fatal(err)
	}
	r := req.GetMediaReleased()
	if req.GetEventId() != e.ID.String() || r.GetUploadId() != up.String() || r.GetMessageId() != msg.String() {
		t.Fatalf("istek = %v", req)
	}
	for name, ev := range map[string]outbox.Event{
		"başka tür":  {ID: id.New(), Type: "channel.message_created", Payload: payload},
		"bozuk yük":  {ID: id.New(), Type: TypeMediaReleased, Payload: []byte(`{"upload_id":"x","message_id":"y"}`)},
		"json değil": {ID: id.New(), Type: TypeMediaReleased, Payload: []byte(`[`)},
	} {
		if _, err := Request(ev); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
	if len(Types) != 1 || Types[0] != TypeMediaReleased {
		t.Fatalf("core'a giden türler: %v", Types)
	}
}
