package identityservice

import (
	"errors"
	"fmt"
	"math"
	"math/big"
)

func (s *Snapshot) known(identity, service, model string) error {
	if s == nil || s.config.Version != Version {
		return errors.New("uninitialized identity/service snapshot")
	}
	if !validKey(model) {
		return fmt.Errorf("invalid exact physical model key %q", model)
	}
	if _, ok := s.config.IdentityDefaults[identity]; !ok {
		return fmt.Errorf("unknown identity %q", identity)
	}
	if _, ok := s.config.ServiceDefaults[service]; !ok {
		return fmt.Errorf("unknown service %q", service)
	}
	return nil
}

// Authorize is only the pure product gate. It is NOT a replacement for account,
// Key, group access, token whitelist, candidate or pin/origin-task validation.
// Pricing is deliberately not consulted, including zero identity discounts.
func (s *Snapshot) Authorize(identity, service, model string) error {
	if e := s.known(identity, service, model); e != nil {
		return e
	}
	row, ok := s.config.ServiceModels[service][model]
	if !ok || !row.Enabled {
		return fmt.Errorf("%w: service %q model %q not enabled", ErrDenied, service, model)
	}
	scope, ok := s.config.ModelIdentityScopes[model]
	if !ok {
		return fmt.Errorf("%w: missing exact scope for %q", ErrDenied, model)
	}
	if scope.Mode == "public" {
		return nil
	}
	for _, id := range scope.Identities {
		if id == identity {
			return nil
		}
	}
	return fmt.Errorf("%w: identity %q outside model %q scope", ErrDenied, identity, model)
}

// ResolveRatios is price-only. A successful quote grants no permission and may
// describe a closed model. Unknown defaults are errors, never implicit one.
func (s *Snapshot) ResolveRatios(identity, service, model string) (Ratios, error) {
	if e := s.known(identity, service, model); e != nil {
		return Ratios{}, e
	}
	r := Ratios{Revision: s.config.Revision, Identity: identity, Service: service, PhysicalModel: model, ServiceFactor: Factor{Value: s.config.ServiceDefaults[service], Source: fmt.Sprintf("service_defaults[%q]", service)}, IdentityFactor: Factor{Value: s.config.IdentityDefaults[identity], Source: fmt.Sprintf("identity_defaults[%q]", identity)}}
	if row, ok := s.config.ServiceModels[service][model]; ok && row.Ratio != nil {
		r.ServiceFactor = Factor{Value: *row.Ratio, Source: fmt.Sprintf("service_models[%q][%q].ratio", service, model)}
	}
	if v, ok := s.config.IdentityModelRatios[identity][model]; ok {
		r.IdentityFactor = Factor{Value: v, Source: fmt.Sprintf("identity_model_ratios[%q][%q]", identity, model)}
	}
	return r, nil
}

// Freeze takes a detached unit-price quote from this one config revision. It
// records caller-supplied units and rounding policy; it does NOT execute quota
// rounding, precharge, settlement, persistence, retries or runtime authorization.
func (s *Snapshot) Freeze(in QuoteInput) (FrozenQuote, error) {
	r, e := s.ResolveRatios(in.Identity, in.Service, in.PhysicalModel)
	if e != nil {
		return FrozenQuote{}, e
	}
	if !validKey(in.BillingModel) {
		return FrozenQuote{}, errors.New("billing model: invalid exact key")
	}
	if !finiteNonnegative(in.QuotaPerUnit) || in.QuotaPerUnit == 0 {
		return FrozenQuote{}, errors.New("quota_per_unit must be finite and positive")
	}
	if !validKey(in.Rounding) {
		return FrozenQuote{}, errors.New("rounding policy must be explicit")
	}
	if len(in.Components) == 0 {
		return FrozenQuote{}, errors.New("base components must be explicit")
	}
	q := FrozenQuote{Ratios: r, BillingModel: in.BillingModel, QuotaPerUnit: in.QuotaPerUnit, Rounding: in.Rounding, Components: make(map[string]PricedComponent, len(in.Components))}
	for _, name := range keys(in.Components) {
		base := in.Components[name]
		if !validKey(name) || !validKey(base.Unit) || !validKey(base.Source) || !finiteNonnegative(base.Value) {
			return FrozenQuote{}, fmt.Errorf("component %q: finite nonnegative base, unit and source required", name)
		}
		// Exact binary products avoid intermediate overflow/underflow. Each original
		// component receives S and D exactly once, with one float conversion at end.
		product := new(big.Rat).SetFloat64(base.Value)
		product.Mul(product, new(big.Rat).SetFloat64(r.ServiceFactor.Value))
		product.Mul(product, new(big.Rat).SetFloat64(r.IdentityFactor.Value))
		effective, _ := product.Float64()
		if math.IsInf(effective, 0) || (product.Sign() != 0 && effective == 0) {
			return FrozenQuote{}, fmt.Errorf("component %q: effective price overflow/underflow", name)
		}
		q.Components[name] = PricedComponent{Base: base, Effective: effective}
	}
	return q, nil
}
