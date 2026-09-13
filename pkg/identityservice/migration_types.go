package identityservice

// MigrationInput is an explicit, non-production projection. Matrix must contain
// every registered identity x service x model, including denied edges. Callers
// must separately audit token groups/auto/whitelists, recoverable disabled routes,
// and other request restrictions; this projection cannot certify those facts.
type MigrationInput struct {
	Identities          []string                           `json:"identities"`
	GroupRatio          map[string]float64                 `json:"group_ratio"`
	GroupGroupRatio     map[string]map[string]float64      `json:"group_group_ratio"`
	ServiceModels       map[string]map[string]ServiceModel `json:"service_models"`
	ModelIdentityScopes map[string]ModelIdentityScope      `json:"model_identity_scopes"`
	Matrix              []MigrationEdge                    `json:"matrix"`
}

type MigrationEdge struct {
	Identity    string `json:"identity"`
	Service     string `json:"service"`
	Model       string `json:"model"`
	Allowed     bool   `json:"allowed"`
	NonzeroBase bool   `json:"nonzero_base"`
}

type MigrationConflict struct {
	Code     string `json:"code"`
	Identity string `json:"identity,omitempty"`
	Service  string `json:"service,omitempty"`
	Model    string `json:"model,omitempty"`
	Detail   string `json:"detail"`
}

type AuthorizationChange struct {
	Identity   string `json:"identity"`
	Service    string `json:"service"`
	Model      string `json:"model"`
	OldAllowed bool   `json:"old_allowed"`
	NewAllowed bool   `json:"new_allowed"`
}

type MigrationReport struct {
	// Truncated marks omitted witnesses; NotVerified marks a budget-stopped proof.
	Truncated               bool                  `json:"truncated"`
	NotVerified             bool                  `json:"not_verified"`
	Config                  *Config               `json:"config,omitempty"`
	SourceDigest            string                `json:"source_digest"`
	Conflicts               []MigrationConflict   `json:"conflicts"`
	AuthorizationDiff       []AuthorizationChange `json:"authorization_diff"`
	UnconstrainedIdentities []string              `json:"unconstrained_identities"`
	CoefficientsEquivalent  bool                  `json:"coefficients_equivalent"`
	// Always false in this pure foundation; never an activation certificate.
	ActivationReady     bool     `json:"activation_ready"`
	PendingVerification []string `json:"pending_verification"`
}
