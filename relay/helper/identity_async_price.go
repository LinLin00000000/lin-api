package helper

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// ModelPriceHelperPerCall freezes the existing Task/MJ base calculator, not
// synchronous token pricing. Retries only reselect S from the admitted snapshot.
func ModelPriceHelperPerCall(c *gin.Context, info *relaycommon.RelayInfo) (types.PriceData, error) {
	if service.RequestIdentity(c) == nil {
		return modelPriceHelperPerCall(c, info, HandleGroupRatio(c, info))
	}
	if info.IdentityBilling != nil {
		return RefreshAsyncIdentityPrice(c, info)
	}
	if billing_setting.GetBillingMode(info.OriginModelName) == billing_setting.BillingModeTieredExpr {
		return types.PriceData{}, fmt.Errorf("identity async per-call path does not support usage expression for model %q; use Task usage-expression route", info.OriginModelName)
	}
	base, err := modelPriceHelperPerCall(c, info, types.GroupRatioInfo{GroupRatio: 1, GroupSpecialRatio: -1})
	if err != nil {
		return types.PriceData{}, err
	}
	estimate := base.ModelRatio / 2 * common.QuotaPerUnit
	if base.UsePrice {
		estimate = base.ModelPrice * common.QuotaPerUnit
	}
	return FreezeAsyncIdentityPrice(c, info, base, estimate)
}

// FreezeAsyncIdentityPrice also accepts usage-expression estimates. The actual
// expression and its QPU are already detached in TieredBillingSnapshot.
func FreezeAsyncIdentityPrice(c *gin.Context, info *relaycommon.RelayInfo, base types.PriceData, estimate float64) (types.PriceData, error) {
	request := service.RequestIdentity(c)
	if request == nil {
		return base, nil
	}
	if info.IdentityBilling != nil {
		return RefreshAsyncIdentityPrice(c, info)
	}
	group := info.UsingGroup
	if g, ok := c.Get("auto_group"); ok {
		group = g.(string)
	}
	if err := request.Authorize(group, info.OriginModelName); err != nil {
		return types.PriceData{}, err
	}
	components := map[string]identityservice.BaseComponent{
		"multiplier": {Value: 1, Unit: "dimensionless", Source: "Task/MJ host multiplier"},
		"input":      {Value: base.ModelRatio, Unit: "quota/token", Source: "Task ModelRatio"},
	}
	if base.UsePrice {
		components["call"] = identityservice.BaseComponent{Value: base.ModelPrice, Unit: "USD/call", Source: "Task/MJ ModelPrice"}
	}
	rounding := "common.QuotaFromFloatStrict estimate; common.QuotaFromFloatChecked terminal; no minimum"
	if info.TieredBillingSnapshot != nil {
		rounding = "common.QuotaRoundChecked estimate; billingexpr terminal; no minimum"
		components["expression_estimate"] = identityservice.BaseComponent{Value: estimate, Unit: "quota/estimated request", Source: "Task usage expression snapshot"}
	}
	quote, err := request.Snapshot().Freeze(identityservice.QuoteInput{Identity: request.Identity(), Service: group, PhysicalModel: info.OriginModelName, BillingModel: info.GetBillingModelName(), QuotaPerUnit: common.QuotaPerUnit, Rounding: rounding, Components: components})
	if err != nil {
		return types.PriceData{}, err
	}
	info.IdentityBilling = &types.IdentityBilling{Quote: quote, Base: base, EstimatedQuotaBeforeGroup: estimate}
	info.BillingModelName = quote.BillingModel
	return RefreshAsyncIdentityPrice(c, info)
}

func RefreshAsyncIdentityPrice(c *gin.Context, info *relaycommon.RelayInfo) (types.PriceData, error) {
	if err := service.RefreshIdentityBillingForSelectedGroup(c, info); err != nil {
		return types.PriceData{}, err
	}
	info.PriceData.Quota = info.PriceData.QuotaToPreConsume
	// Only the complete async estimate is free. A zero component is not a waiver
	// for a separate charged component or tool in an expression.
	info.PriceData.FreeModel = info.PriceData.Quota == 0
	return info.PriceData, nil
}
