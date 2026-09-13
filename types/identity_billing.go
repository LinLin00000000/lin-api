package types

import (
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"sync"
)

// IdentityBilling is a synchronous request contract, not a persistent task price.
// Base holds detached original price components; only the selected group's S may
// change, against the same admission snapshot. Never refresh Base during retry.
type IdentityBilling struct {
	Quote                     identityservice.FrozenQuote
	Base                      PriceData
	EstimatedQuotaBeforeGroup float64
	ToolPrices                map[string]float64
	// Independent existing penalty policy; never used as a model-price multiplier.
	ViolationGroupRatios  map[string]float64
	GeminiInputAudioPrice float64
	ContainsAudioRatios   bool
	RealtimeMu            sync.Mutex
	RealtimeUsage         dto.RealtimeUsage
}
