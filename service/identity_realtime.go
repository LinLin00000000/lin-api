package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
)

// Segments are deltas, not separate funding sessions. Reserve the cumulative
// usage quote (not estimate + usage) and let final settlement release high water.
// This retains BillingSession's existing crash/retry boundaries.
func reserveIdentityRealtime(c *gin.Context, info *relaycommon.RelayInfo, segment *dto.RealtimeUsage) error {
	b := info.IdentityBilling
	b.RealtimeMu.Lock()
	defer b.RealtimeMu.Unlock()
	if segment == nil {
		return fmt.Errorf("missing realtime usage")
	}
	next := b.RealtimeUsage
	next.InputTokens += segment.InputTokens
	next.OutputTokens += segment.OutputTokens
	next.TotalTokens += segment.TotalTokens
	next.InputTokenDetails.TextTokens += segment.InputTokenDetails.TextTokens
	next.InputTokenDetails.AudioTokens += segment.InputTokenDetails.AudioTokens
	next.OutputTokenDetails.TextTokens += segment.OutputTokenDetails.TextTokens
	next.OutputTokenDetails.AudioTokens += segment.OutputTokenDetails.AudioTokens
	quota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: next.InputTokenDetails.TextTokens, AudioTokens: next.InputTokenDetails.AudioTokens},
		OutputDetails: TokenDetails{TextTokens: next.OutputTokenDetails.TextTokens, AudioTokens: next.OutputTokenDetails.AudioTokens},
		ModelName:     b.Quote.BillingModel, UsePrice: b.Base.UsePrice, ModelPrice: b.Base.ModelPrice,
		ModelRatio: b.Base.ModelRatio, GroupRatio: b.Quote.Components["multiplier"].Effective, Frozen: b,
	})
	if clamp != nil {
		return clamp
	}
	if ok, q, _ := TryTieredSettle(info, billingexpr.TokenParams{P: float64(next.InputTokens), C: float64(next.OutputTokens), Len: float64(next.InputTokens)}); ok {
		quota = q
	}
	if quota > 0 {
		if info.Billing == nil {
			return fmt.Errorf("identity realtime requires admitted funding session")
		}
		if err := info.Billing.Reserve(quota); err != nil {
			return err
		}
		info.FinalPreConsumedQuota = info.Billing.GetPreConsumedQuota()
	}
	b.RealtimeUsage = next
	return nil
}
