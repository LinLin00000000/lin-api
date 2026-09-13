package helper

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"strings"
)

// ModelPriceHelper freezes actual calculator inputs, not just the B1 revision.
// Legacy executes the original helper and pair replacement unchanged.
func ModelPriceHelper(c *gin.Context, info *relaycommon.RelayInfo, promptTokens int, meta *types.TokenCountMeta) (hosttypes.PriceData, error) {
	request := service.RequestIdentity(c)
	if request == nil {
		return modelPriceHelper(c, info, promptTokens, meta, HandleGroupRatio(c, info))
	}
	if info.IdentityBilling != nil {
		return info.PriceData, nil
	}
	if group, ok := c.Get("auto_group"); ok {
		info.UsingGroup = group.(string)
	}
	if err := request.Authorize(info.UsingGroup, info.OriginModelName); err != nil {
		return hosttypes.PriceData{}, err
	}
	// Resolve against the authorization identity, never the mutable user row.
	ratios, err := request.Snapshot().ResolveRatios(request.Identity(), info.UsingGroup, info.OriginModelName)
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	// Compute original base with multiplier one using the existing pricing family.
	// Model prices do not use legacy group/pair ratios. Independent penalties retain them.
	base, err := modelPriceHelper(c, info, promptTokens, meta, hosttypes.GroupRatioInfo{GroupRatio: 1, GroupSpecialRatio: -1})
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	qpu := common.QuotaPerUnit
	// Image extras are part of the original physical request, not channel pricing.
	// Decode before reserving; a mapped upstream model must never select this rate.
	if image, ok := info.Request.(*dto.ImageRequest); ok {
		var parameters struct {
			N            int  `json:"n"`
			PromptExtend bool `json:"prompt_extend"`
		}
		if raw, exists := image.Extra["parameters"]; exists {
			if err := json.Unmarshal(raw, &parameters); err != nil {
				return hosttypes.PriceData{}, fmt.Errorf("invalid image parameters: %w", err)
			}
			if parameters.N < 0 || parameters.N > dto.MaxImageN {
				return hosttypes.PriceData{}, fmt.Errorf("parameters.n must be between 1 and %d", dto.MaxImageN)
			}
		}
		if parameters.N != 0 {
			base.AddOtherRatio("n", float64(parameters.N))
		}
		if strings.Contains(info.GetBillingModelName(), "z-image") && parameters.PromptExtend {
			base.AddOtherRatio("prompt_extend", 2)
		}
	}
	components := map[string]identityservice.BaseComponent{}
	add := func(name string, value float64, unit string) {
		components[name] = identityservice.BaseComponent{Value: value, Unit: unit, Source: "existing synchronous calculator/" + name}
	}
	if base.UsePrice {
		add("call", base.ModelPrice, "USD/call")
	} else {
		add("input", base.ModelRatio, "quota/token")
		add("output", base.ModelRatio*base.CompletionRatio, "quota/token")
		add("cache_read", base.ModelRatio*base.CacheRatio, "quota/token")
		add("cache_write", base.ModelRatio*base.CacheCreationRatio, "quota/token")
		add("cache_write_5m", base.ModelRatio*base.CacheCreation5mRatio, "quota/token")
		add("cache_write_1h", base.ModelRatio*base.CacheCreation1hRatio, "quota/token")
		add("image", base.ModelRatio*base.ImageRatio, "quota/token")
		add("audio_input", base.ModelRatio*base.AudioRatio, "quota/token")
		add("audio_output", base.ModelRatio*base.AudioRatio*base.AudioCompletionRatio, "quota/token")
	}
	toolPrices := operation_setting.SnapshotToolPricesForModel(info.GetBillingModelName())
	for name, v := range toolPrices {
		add("tool/"+name, v, "USD/1000 calls")
	}
	audio := operation_setting.GetGeminiInputAudioPricePerMillionTokens(info.GetBillingModelName())
	add("gemini_audio", audio, "USD/1000000 tokens")
	// A unit component validates the combined S*D even when every base is zero.
	add("multiplier", 1, "dimensionless")
	rounding := "common.QuotaFromFloatStrict estimate; QuotaFromDecimalChecked half-away settlement; paid token minimum 1"
	estimate := float64(common.Max(promptTokens, common.PreConsumedQuota)+meta.MaxTokens) * base.ModelRatio
	if base.UsePrice {
		estimate = base.ApplyOtherRatiosToFloat(base.ModelPrice * qpu)
	}
	if snap := info.TieredBillingSnapshot; snap != nil {
		estimate = snap.EstimatedQuotaBeforeGroup
		rounding = "billingexpr.QuotaRoundStrict estimate; ComputeTieredQuotaWithRequest settlement"
		add("expression_estimate", estimate, "quota/request estimate; expression stored in TieredBillingSnapshot")
	}
	quote, err := request.Snapshot().Freeze(identityservice.QuoteInput{Identity: ratios.Identity, Service: ratios.Service, PhysicalModel: ratios.PhysicalModel, BillingModel: info.GetBillingModelName(), QuotaPerUnit: qpu, Rounding: rounding, Components: components})
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	base.ReplaceOtherRatios(base.OtherRatios())
	info.IdentityBilling = &hosttypes.IdentityBilling{Quote: quote, Base: base, EstimatedQuotaBeforeGroup: estimate, ToolPrices: toolPrices, GeminiInputAudioPrice: audio, ContainsAudioRatios: ratio_setting.ContainsAudioRatio(info.GetBillingModelName()) || ratio_setting.ContainsAudioCompletionRatio(info.GetBillingModelName())}
	// Preserve the existing independent penalty multiplier separately from S*D.
	info.IdentityBilling.ViolationGroupRatios = map[string]float64{}
	for group := range request.Snapshot().Config().ServiceDefaults {
		ratio := ratio_setting.GetGroupRatio(group)
		if pair, ok := ratio_setting.GetGroupGroupRatio(request.Identity(), group); ok {
			ratio = pair
		}
		info.IdentityBilling.ViolationGroupRatios[group] = ratio
	}
	// Preserve the original pricing alias before any adaptor/channel translation.
	info.BillingModelName = quote.BillingModel
	if err := service.RefreshIdentityBillingForSelectedGroup(c, info); err != nil {
		return hosttypes.PriceData{}, err
	}
	return info.PriceData, nil
}
