package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"sort"
)

// This supplemental report cannot enable a mode: storage still unconditionally
// rejects identity_service. No production inventory is inferred from a draft.
func IdentityActivationBlockers(cfg identityservice.Config) []string {
	blockers := []string{"identity_service activation remains disabled by storage policy", "production authorization, backup/restore, migration equivalence, in-flight tasks, rollback compatibility, runtime validation and explicit activation gates pending"}
	for group, models := range cfg.ServiceModels {
		for name, entry := range models {
			if entry.Enabled {
				blockers = append(blockers, fmt.Sprintf("custom_async_monetary_quota_unverified: service=%q model=%q; arbitrary adaptor/plugin actualQuota currency or current-price conversion is not activation-certified; use frozen host token/usage-expression billing", group, name))
			}
		}
	}
	s, err := identityservice.NewSnapshot(cfg)
	if err != nil {
		return append(blockers, "invalid draft: "+err.Error())
	}
	fee := model_setting.GetGrokSettings()
	if fee == nil || !fee.ViolationDeductionEnabled || fee.ViolationDeductionAmount <= 0 {
		return blockers
	}
	for group, models := range cfg.ServiceModels {
		for name, entry := range models {
			if !entry.Enabled {
				continue
			}
			for identity := range cfg.IdentityDefaults {
				if err := s.Authorize(identity, group, name); err != nil {
					continue
				}
				ratios, err := s.ResolveRatios(identity, group, name)
				if err != nil {
					blockers = append(blockers, fmt.Sprintf("violation_fee_free_unverified: identity=%q service=%q model=%q; %v", identity, group, name, err))
					continue
				}
				free := ratios.ServiceFactor.Value == 0 || ratios.IdentityFactor.Value == 0
				if !free {
					// Price mode and add-on components must come from the real calculator
					// configuration. Expressions/optional tools cannot prove every request paid.
					reason := ""
					if billing_setting.GetBillingMode(name) == billing_setting.BillingModeTieredExpr {
						reason = "expression usage-dependent zero price is not statically verified"
					} else if price, fixed := ratio_setting.GetModelPrice(name, false); fixed {
						if price == 0 {
							free = true
							for _, price := range operation_setting.SnapshotToolPricesForModel(name) {
								if price > 0 {
									free = false
									reason = "zero fixed base with optional priced tools; zero-fee requests not statically verified"
								}
							}
						}
					} else {
						reason = "token/cache/audio usage-dependent zero components not statically verified"
					}
					if reason != "" {
						blockers = append(blockers, fmt.Sprintf("violation_fee_free_unverified: identity=%q service=%q model=%q; %s", identity, group, name, reason))
					}
				}
				if !free {
					continue
				}
				legacy := ratio_setting.GetGroupRatio(group)
				if pair, ok := ratio_setting.GetGroupGroupRatio(identity, group); ok {
					legacy = pair
				}
				if calcViolationFeeQuota(fee.ViolationDeductionAmount, legacy) > 0 {
					blockers = append(blockers, fmt.Sprintf("violation_fee_free_conflict: identity=%q service=%q model=%q; independent existing penalty remains chargeable; user policy decision required", identity, group, name))
				}
			}
		}
	}
	sort.Strings(blockers)
	return blockers
}
