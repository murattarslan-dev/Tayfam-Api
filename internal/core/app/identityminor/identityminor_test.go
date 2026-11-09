package identityminor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

type fakeIdentity struct {
	resp *identityv1.IsMinorResponse
	err  error
	got  *identityv1.IsMinorRequest
}

func (f *fakeIdentity) IsMinor(ctx context.Context, in *identityv1.IsMinorRequest, _ ...grpc.CallOption) (*identityv1.IsMinorResponse, error) {
	f.got = in
	if _, ok := ctx.Deadline(); !ok {
		return nil, status.Error(codes.Internal, "süre sınırı yok")
	}
	return f.resp, f.err
}

func TestIsMinor(t *testing.T) {
	a, b, stranger := id.New(), id.New(), id.New()
	f := &fakeIdentity{resp: &identityv1.IsMinorResponse{Answers: []*identityv1.MinorAnswer{
		{UserId: a.String(), Minor: true}, {UserId: b.String()}, {UserId: stranger.String(), Minor: false}, {UserId: "x"},
	}}}
	got, err := New(f).IsMinor(context.Background(), []id.ID{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[a] || got[b] {
		t.Fatalf("cevaplar = %v", got)
	}
	if len(f.got.GetUserIds()) != 2 || f.got.GetUserIds()[0] != a.String() {
		t.Fatalf("istek = %v", f.got)
	}

	f = &fakeIdentity{err: status.Error(codes.Unavailable, "kapalı")}
	if _, err := New(f).IsMinor(context.Background(), []id.ID{a}); err == nil {
		t.Fatal("identity kapalıyken hata yok")
	}
	if _, err := (Unavailable{}).IsMinor(context.Background(), []id.ID{a}); err == nil {
		t.Fatal("yapılandırılmamış denetleyici cevap verdi")
	}
}
