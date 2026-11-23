package grpc

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/badge"
)

// earnedBadgesPB converts earned badges (issue #258) for the profile and
// the tayfa.
func earnedBadgesPB(in []badge.Earned) []*corev1.EarnedBadge {
	out := make([]*corev1.EarnedBadge, 0, len(in))
	for _, b := range in {
		out = append(out, &corev1.EarnedBadge{
			BadgeKey: b.Key, Year: b.Year, TayfaId: optionalID(b.Tayfa), EventId: optionalID(b.Event),
			EarnedAt: timestamppb.New(b.EarnedAt),
		})
	}
	return out
}
