package helper

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/gin-gonic/gin"
)

// CurrentIdentityQuote is a read-only projection, not an admission or a bill.
// No reserve/settle helper is invoked. Each query reads today's base settings;
// the identity revision identifies only S/D and product permissions.
type CurrentQuote struct {
	Quote           identityservice.FrozenQuote `json:"quote"`
	Calculator      string                      `json:"calculator"`
	Expression      string                      `json:"expression,omitempty"`
	FinalPriceKnown bool                        `json:"final_price_known"`
	Conditions      []string                    `json:"conditions"`
}

func CurrentIdentityQuote(c *gin.Context, group, physicalModel string) (CurrentQuote, error) {
	out := CurrentQuote{Calculator: "synchronous", Conditions: []string{"unit_prices_not_bill", "request_usage_and_other_ratios_apply", "not_async_monetary_certification", "revision_covers_identity_config_only"}}
	request := service.RequestIdentity(c)
	if request == nil {
		return out, fmt.Errorf("identity/service quote unavailable in legacy mode")
	}
	if err := request.Authorize(group, physicalModel); err != nil {
		return out, err
	}
	if billing_setting.GetBillingMode(physicalModel) == billing_setting.BillingModeTieredExpr {
		expr, ok := billing_setting.GetBillingExpr(physicalModel)
		if !ok || strings.TrimSpace(expr) == "" {
			return out, fmt.Errorf("billing expression unavailable")
		}
		// Dynamic request/usage inputs are unknown. Never run an invented usage vector
		// or display the zero-valued token placeholders from the expression estimator.
		q, err := request.Snapshot().Freeze(identityservice.QuoteInput{Identity: request.Identity(), Service: group, PhysicalModel: physicalModel, BillingModel: physicalModel, QuotaPerUnit: common.QuotaPerUnit, Rounding: "billingexpr actual request/usage settlement", Components: map[string]identityservice.BaseComponent{"multiplier": {Value: 1, Unit: "dimensionless", Source: "existing expression group multiplier"}}})
		out.Quote, out.Expression, out.Calculator = q, expr, "expression"
		out.Conditions = append(out.Conditions, "expression_requires_actual_request_and_usage")
		return out, err
	}
	if !HasModelBillingConfig(physicalModel) {
		return out, fmt.Errorf("base pricing unavailable")
	}
	info := &relaycommon.RelayInfo{OriginModelName: physicalModel, UserId: request.UserID(), UserGroup: request.Identity(), UsingGroup: group, ChannelMeta: &relaycommon.ChannelMeta{}}
	// The actual admission consumer constructs all base components and calls the
	// same resolver. No second formula, price service, unset-price opt-in or alias.
	if _, err := ModelPriceHelper(c, info, 0, &types.TokenCountMeta{}); err != nil {
		return out, err
	}
	out.Quote = info.IdentityBilling.Quote
	return out, nil
}
