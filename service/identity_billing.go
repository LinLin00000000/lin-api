package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

func BillingQuotaPerUnit(info *relaycommon.RelayInfo) float64 {
	if info.IdentityBilling != nil {
		return info.IdentityBilling.Quote.QuotaPerUnit
	}
	return common.QuotaPerUnit
}

// RefreshIdentityBillingForSelectedGroup uses only admitted B and B2 snapshot.
// It runs before I/O; settlement never calls this resolver.
func RefreshIdentityBillingForSelectedGroup(c *gin.Context, info *relaycommon.RelayInfo) error {
	b := info.IdentityBilling
	if b == nil {
		return nil
	}
	request := RequestIdentity(c)
	if request == nil {
		return fmt.Errorf("missing identity billing admission")
	}
	group := info.UsingGroup
	if g, ok := c.Get("auto_group"); ok {
		group = g.(string)
	}
	if err := request.Authorize(group, b.Quote.Ratios.PhysicalModel); err != nil {
		return err
	}
	components := map[string]identityservice.BaseComponent{}
	for name, component := range b.Quote.Components {
		components[name] = component.Base
	}
	q, err := request.Snapshot().Freeze(identityservice.QuoteInput{Identity: b.Quote.Ratios.Identity, Service: group, PhysicalModel: b.Quote.Ratios.PhysicalModel, BillingModel: b.Quote.BillingModel, QuotaPerUnit: b.Quote.QuotaPerUnit, Rounding: b.Quote.Rounding, Components: components})
	if err != nil {
		return err
	}
	multiplier := q.Components["multiplier"].Effective
	var quota int
	if info.TieredBillingSnapshot != nil {
		quota, err = billingexpr.QuotaRoundStrict(b.EstimatedQuotaBeforeGroup * multiplier)
	} else {
		quota, err = common.QuotaFromFloatStrict(b.EstimatedQuotaBeforeGroup * multiplier)
	}
	if err != nil {
		return err
	}
	b.Quote = q
	info.UsingGroup = group
	info.PriceData = b.Base
	info.PriceData.ReplaceOtherRatios(b.Base.OtherRatios())
	info.PriceData.GroupRatioInfo = hosttypes.GroupRatioInfo{GroupRatio: multiplier, GroupSpecialRatio: -1}
	info.PriceData.QuotaToPreConsume = quota
	// Zero product is unconditionally free, independent of the legacy preconsume toggle.
	info.PriceData.FreeModel = multiplier == 0
	if snap := info.TieredBillingSnapshot; snap != nil {
		snap.GroupRatio = multiplier
		snap.EstimatedQuotaAfterGroup = quota
	}
	return nil
}

// PrepareIdentityBillingForSelectedGroup reserves the actual selected quote
// before each attempt. High-water reserves are refunded by normal settlement.
func PrepareIdentityBillingForSelectedGroup(c *gin.Context, info *relaycommon.RelayInfo) *types.NewAPIError {
	if info.IdentityBilling == nil {
		return PrepareTieredBillingForSelectedGroup(c, info)
	}
	if err := RefreshIdentityBillingForSelectedGroup(c, info); err != nil {
		return types.NewError(err, types.ErrorCodeModelPriceError, types.ErrOptionWithSkipRetry())
	}
	if info.PriceData.FreeModel {
		return nil
	}
	if info.Billing == nil {
		return PreConsumeBilling(c, info.PriceData.QuotaToPreConsume, info)
	}
	if err := info.Billing.Reserve(info.PriceData.QuotaToPreConsume); err != nil {
		return types.NewError(err, types.ErrorCodeUpdateDataError, types.ErrOptionWithSkipRetry())
	}
	info.FinalPreConsumedQuota = info.Billing.GetPreConsumedQuota()
	return nil
}
