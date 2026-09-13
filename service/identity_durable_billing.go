package service

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// PreConsumeDurableBilling binds even a zero quote to its actual funding owner.
// This async-only seam leaves the accepted synchronous free fast path unchanged.
func PreConsumeDurableBilling(c *gin.Context, quota int, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info.IdentityBilling != nil && quota == 0 && info.QuotaClamp == nil {
		session, err := NewBillingSession(c, info, 0)
		if err != nil {
			return err
		}
		info.Billing = session
		return nil
	}
	return PreConsumeBilling(c, quota, info)
}

func TaskBillingContextFromRelayInfo(info *relaycommon.RelayInfo) *model.TaskBillingContext {
	bc := &model.TaskBillingContext{ModelPrice: info.PriceData.ModelPrice, GroupRatio: info.PriceData.GroupRatioInfo.GroupRatio, ModelRatio: info.PriceData.ModelRatio, OtherRatios: info.PriceData.OtherRatios(), OriginModelName: info.OriginModelName, PerCallBilling: common.StringsContains(constant.TaskPricePatches, info.OriginModelName) || info.PriceData.UsePrice, TieredSnapshot: info.TieredBillingSnapshot}
	if info.IdentityBilling != nil {
		q := info.IdentityBilling.Quote
		bc.IdentityQuote = &q
	}
	return bc
}

// SettleTaskBillingAfterTerminalCAS is used only by a caller that won the
// nonterminal-to-terminal status CAS. It does not add cross-table exactly-once.
func SettleTaskBillingAfterTerminalCAS(ctx context.Context, adaptor TaskPollingAdaptor, task *model.Task, result *relaycommon.TaskInfo) {
	settled := settleTaskBillingOnComplete(ctx, adaptor, task, result)
	if task.Status == model.TaskStatusFailure && !settled && task.Quota != 0 {
		RefundTaskQuota(ctx, task, task.FailReason)
	}
}

// IdentityTaskTerminalQuota is the same frozen calculator for immediate and
// polled completions. It never touches funding, current config or current user.
// false means no usage; retain the persisted estimate, not a current price.
func IdentityTaskTerminalQuota(bc *model.TaskBillingContext, result *relaycommon.TaskInfo) (int, bool, error) {
	if bc == nil || bc.IdentityQuote == nil {
		return 0, false, fmt.Errorf("missing durable identity quote")
	}
	if result.Status == model.TaskStatusFailure {
		return 0, true, nil
	}
	if result.Status != model.TaskStatusSuccess {
		return 0, false, nil
	}
	q := bc.IdentityQuote
	multiplier, exists := q.Components["multiplier"]
	if !exists {
		return 0, false, fmt.Errorf("invalid durable multiplier")
	}
	if result.ActualQuota != nil && *result.ActualQuota < 0 {
		return 0, false, fmt.Errorf("negative adaptor actual quota")
	}
	if multiplier.Effective == 0 || (result.ActualQuota != nil && *result.ActualQuota == 0) {
		return 0, true, nil
	}
	if snap := bc.TieredSnapshot; snap != nil {
		facts := make(map[string]any, len(snap.UsageFacts)+len(result.UsageFacts))
		for k, v := range snap.UsageFacts {
			facts[k] = v
		}
		for k, v := range result.UsageFacts {
			facts[k] = v
		}
		actual, err := billingexpr.ComputeTieredQuotaWithRequest(snap, billingexpr.TokenParams{}, billingexpr.RequestInput{Usage: facts})
		if err != nil {
			return 0, false, err
		}
		snap.UsageFacts = facts
		snap.EstimatedTier = actual.MatchedTier
		return actual.ActualQuotaAfterGroup, true, nil
	}
	if bc.PerCallBilling {
		return 0, false, nil
	}
	if result.ActualQuota != nil {
		actual, clamp := common.QuotaFromFloatChecked(float64(*result.ActualQuota) * multiplier.Effective)
		if clamp != nil {
			return 0, false, clamp
		}
		return actual, true, nil
	}
	tokens := result.TotalTokens
	if tokens == 0 {
		tokens = result.CompletionTokens
	}
	if tokens <= 0 {
		return 0, false, nil
	}
	input, exists := q.Components["input"]
	if !exists {
		return 0, false, fmt.Errorf("invalid durable token component")
	}
	other := 1.0
	if prices := taskBillingContextPriceData(bc); prices != nil {
		other = prices.OtherRatioMultiplier()
	}
	actual, clamp := common.QuotaFromFloatChecked(float64(tokens) * input.Effective * other)
	if clamp != nil {
		return 0, false, clamp
	}
	return actual, true, nil
}
