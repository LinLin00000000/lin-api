package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetCurrentIdentityPricing(c *gin.Context) {
	GetCurrentIdentityPricingWithSnapshot(model.IdentityServiceSettings.Snapshot)(c)
}

// Constructor injection is only for isolated consumer tests; production always
// uses the activation-gated store. The ordinary API never accepts an identity.
func GetCurrentIdentityPricingWithSnapshot(provider service.IdentitySnapshotProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !currentQuoteQueryValid(c, false) {
			return
		}
		user, err := model.GetUserCache(c.GetInt("id"))
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "authenticated user unavailable"})
			return
		}
		var snapshot *identityservice.Snapshot
		if provider != nil {
			snapshot = provider()
		}
		if snapshot == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "configuration unavailable"})
			return
		}
		if snapshot.Config().Mode == identityservice.ModeLegacy {
			c.JSON(http.StatusOK, gin.H{"success": true, "mode": "legacy", "preview": false})
			return
		}
		serveCurrentIdentityPricing(c, snapshot, user.Id, user.Group, false)
	}
}

// Root-only, saved-draft preview. It does not Save, change the store mode,
// activate an admission path, or grant the previewed identity any permission.
func PreviewCurrentIdentityPricing(c *gin.Context) {
	if !currentQuoteQueryValid(c, true) {
		return
	}
	if err := model.IdentityServiceSettings.Refresh(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "configuration unavailable"})
		return
	}
	cfg := model.IdentityServiceSettings.Snapshot().Config()
	cfg.Mode = identityservice.ModeIdentityService
	snapshot, err := identityservice.NewSnapshot(cfg)
	if err != nil {
		identityServiceError(c, err)
		return
	}
	serveCurrentIdentityPricing(c, snapshot, c.GetInt("id"), c.Query("identity"), true)
}

func currentQuoteQueryValid(c *gin.Context, preview bool) bool {
	c.Header("Cache-Control", "no-store")
	for key, values := range c.Request.URL.Query() {
		if len(values) != 1 || (key != "model" && key != "service" && !(preview && key == "identity")) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid quote query"})
			return false
		}
	}
	if c.Query("model") == "" || c.Query("service") == "" || (preview && c.Query("identity") == "") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "exact model and service required; preview also requires identity"})
		return false
	}
	return true
}

func serveCurrentIdentityPricing(c *gin.Context, snapshot *identityservice.Snapshot, userID int, identity string, preview bool) {
	group, physicalModel := c.Query("service"), c.Query("model")
	common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
	common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, false)
	service.FreezeIdentityRequest(c, snapshot, userID, identity)
	// Reuse B2's exact product/identity, service-access and live-candidate
	// intersection. A zero factor or a pricing lookup is never an access grant.
	if !service.RequestModelVisible(c, []string{group}, physicalModel) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "current quote unavailable", "preview": preview})
		return
	}
	quote, err := helper.CurrentIdentityQuote(c, group, physicalModel)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"success": false, "message": "base pricing unavailable", "preview": preview})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "mode": "identity_service", "preview": preview, "activation_ready": false, "kind": "current_quote", "data": quote})
}
