package catalogrelay

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

type fakeClient struct {
	got []*identityv1.ApplyCatalogChangeRequest
	err error
}

func (f *fakeClient) ApplyCatalogChange(_ context.Context, in *identityv1.ApplyCatalogChangeRequest, _ ...grpc.CallOption) (*identityv1.ApplyCatalogChangeResponse, error) {
	f.got = append(f.got, in)
	return &identityv1.ApplyCatalogChangeResponse{Applied: true}, f.err
}

func event(payload string) outbox.Event {
	return outbox.Event{ID: id.New(), Type: catalog.EventChanged, Payload: []byte(payload)}
}

func TestRequest(t *testing.T) {
	e := event(`{"catalog_version": 7, "interests": [{"key": "nature", "status": "active"}, {"key": "water", "status": "retired"}]}`)
	req, err := Request(e)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetEventId() != e.ID.String() || req.GetCatalogVersion() != 7 || len(req.GetInterests()) != 2 {
		t.Fatalf("istek = %v", req)
	}
	if i := req.GetInterests()[0]; i.GetKey() != "nature" || i.GetStatus() != identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_ACTIVE {
		t.Fatalf("ilk ilgi alanı = %v", i)
	}
	if i := req.GetInterests()[1]; i.GetKey() != "water" || i.GetStatus() != identityv1.CatalogInterestStatus_CATALOG_INTEREST_STATUS_RETIRED {
		t.Fatalf("ikinci ilgi alanı = %v", i)
	}

	for name, e := range map[string]outbox.Event{
		"başka tür":         {ID: id.New(), Type: "platform.audit", Payload: []byte(`{"catalog_version": 7}`)},
		"bozuk yük":         event(`{"catalog_version": "yedi"}`),
		"sürüm yok":         event(`{"interests": [{"key": "nature", "status": "active"}]}`),
		"bilinmeyen durum":  event(`{"catalog_version": 7, "interests": [{"key": "nature", "status": "deleted"}]}`),
		"durum yok":         event(`{"catalog_version": 7, "interests": [{"key": "nature"}]}`),
		"nesne olmayan yük": event(`[]`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Request(e); err == nil {
				t.Fatal("hata bekleniyordu")
			}
		})
	}
}

// TestConsumer: one consumer of its own, only catalog.changed; a failed call
// or a broken payload is an error, so the relay retries and does not move
// past the event.
func TestConsumer(t *testing.T) {
	f := &fakeClient{}
	c := Consumer(f)
	if c.Name != ConsumerName || len(c.Types) != 1 || c.Types[0] != catalog.EventChanged {
		t.Fatalf("tüketici = %s %v", c.Name, c.Types)
	}
	ok := event(`{"catalog_version": 3, "interests": [{"key": "games", "status": "active"}]}`)
	if err := c.Deliver(context.Background(), ok); err != nil || len(f.got) != 1 || f.got[0].GetCatalogVersion() != 3 {
		t.Fatalf("teslim = %v %v", err, f.got)
	}
	if err := c.Deliver(context.Background(), event(`{}`)); err == nil || len(f.got) != 1 {
		t.Fatalf("bozuk yük teslim edildi: %v", err)
	}
	f.err = errors.New("kapalı")
	if err := c.Deliver(context.Background(), ok); err == nil {
		t.Fatal("başarısız çağrı başarılı sayıldı")
	}
}
