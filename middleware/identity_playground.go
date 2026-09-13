package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

// PlaygroundAuth uses precisely UserAuth's credential/status checks. Only this
// relay entry captures authorization; dashboard management is unchanged.
func PlaygroundAuth() gin.HandlerFunc {
	return PlaygroundAuthWithIdentitySnapshot(model.IdentityServiceSettings.Snapshot)
}

// PlaygroundAuthWithIdentitySnapshot is constructor injection, not an HTTP or
// environment activation switch. Identity always comes from authenticated user.
func PlaygroundAuthWithIdentitySnapshot(provider service.IdentitySnapshotProvider) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHelperWithAdmission(c, common.RoleCommonUser, func(c *gin.Context, user *model.UserBase) bool {
			if provider == nil {
				return true
			}
			snapshot := provider()
			if snapshot == nil || snapshot.Config().Mode != "identity_service" {
				return true
			}
			var body dto.PlayGroundRequest
			if err := common.UnmarshalBodyReusable(c, &body); err != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, "invalid playground request")
				return false
			}
			group := body.Group
			if group == "" {
				group = user.Group
			} // preserve omitted-service meaning; no implicit grant
			common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
			common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, false)
			request := service.FreezeIdentityRequest(c, snapshot, user.Id, user.Group)
			if (group == "auto" && len(request.AutoGroups()) == 0) || (group != "auto" && !request.ServiceAllowed(group)) {
				abortWithOpenAiMessage(c, http.StatusForbidden, "identity/service group access denied")
				return false
			}
			common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
			return true
		})
	}
}
