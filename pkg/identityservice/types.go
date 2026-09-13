// Package identityservice defines the pure, inactive identity/service configuration.
// It does not install runtime hooks or authorize activation.
package identityservice

const (
	OptionKey           = "identity_service_model_setting"
	Version             = 1
	ModeLegacy          = "legacy"
	ModeIdentityService = "identity_service"
)

type Config struct {
	Version               int                                `json:"version"`
	Mode                  string                             `json:"mode"`
	Revision              uint64                             `json:"revision"`
	ServiceDefaults       map[string]float64                 `json:"service_defaults"`
	IdentityDefaults      map[string]float64                 `json:"identity_defaults"`
	ServiceModels         map[string]map[string]ServiceModel `json:"service_models"`
	IdentityModelRatios   map[string]map[string]float64      `json:"identity_model_ratios"`
	ModelIdentityScopes   map[string]ModelIdentityScope      `json:"model_identity_scopes"`
	MigrationSourceDigest string                             `json:"migration_source_digest"`
}

type ServiceModel struct {
	Enabled bool     `json:"enabled"`
	Ratio   *float64 `json:"ratio,omitempty"`
}

type ModelIdentityScope struct {
	Mode       string   `json:"mode"`
	Identities []string `json:"identities"`
}

// Scope is a short alias for callers constructing configuration values.
type Scope = ModelIdentityScope

// Snapshot owns all its maps and slices. Config returns a detached copy.
type Snapshot struct{ config Config }

// Factor identifies the exact default or replacement used, never a pair ratio.
type Factor struct {
	Value  float64 `json:"value"`
	Source string  `json:"source"`
}

type Ratios struct {
	Revision       uint64 `json:"revision"`
	Identity       string `json:"identity"`
	Service        string `json:"service"`
	PhysicalModel  string `json:"physical_model"`
	ServiceFactor  Factor `json:"service_factor"`
	IdentityFactor Factor `json:"identity_factor"`
}

// QuoteInput must describe the billing contract before an upstream request.
// Components are original base unit prices, not prices with any S/D applied.
type QuoteInput struct {
	Identity      string
	Service       string
	PhysicalModel string
	BillingModel  string
	Components    map[string]BaseComponent
	QuotaPerUnit  float64
	Rounding      string
}

type BaseComponent struct {
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Source string  `json:"source"`
}

type PricedComponent struct {
	Base      BaseComponent `json:"base"`
	Effective float64       `json:"effective"`
}

type FrozenQuote struct {
	Ratios       Ratios                     `json:"ratios"`
	BillingModel string                     `json:"billing_model"`
	Components   map[string]PricedComponent `json:"components"`
	QuotaPerUnit float64                    `json:"quota_per_unit"`
	Rounding     string                     `json:"rounding"`
}
