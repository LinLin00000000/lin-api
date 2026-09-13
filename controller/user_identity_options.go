package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"sort"
)

// GetUserIdentityOptions exposes names only under the user-management AdminAuth.
// Identity names are not service groups; legacy mode retains existing choices.
func GetUserIdentityOptions(c *gin.Context) {
	if err := model.IdentityServiceSettings.Refresh(); err != nil {
		common.ApiError(c, err)
		return
	}
	cfg := model.IdentityServiceSettings.Snapshot().Config()
	// Configured draft identities can be assigned before billing activation.
	// In legacy mode preserve existing group choices without writing GroupRatio.
	selected := make(map[string]bool)
	for name := range cfg.IdentityDefaults {
		selected[name] = true
	}
	if cfg.Mode == identityservice.ModeLegacy {
		for name := range ratio_setting.GetGroupRatioCopy() {
			selected[name] = true
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	common.ApiSuccess(c, gin.H{"mode": cfg.Mode, "identities": names})
}
