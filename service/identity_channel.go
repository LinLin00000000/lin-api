package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

// AuthorizeSelectedRequestChannel checks the actual group of an already chosen
// connection. Auto pins without a group select only from the frozen ordered list.
func AuthorizeSelectedRequestChannel(c *gin.Context, physicalModel string, channelID int) error {
	r := RequestIdentity(c)
	if r == nil {
		return nil
	}
	group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
	if group == "auto" {
		group = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
		if group == "" {
			for _, g := range r.AutoGroups() {
				if AuthorizeRequestChannel(c, g, physicalModel, channelID) == nil {
					group = g
					break
				}
			}
		}
		if err := AuthorizeRequestChannel(c, group, physicalModel, channelID); err != nil {
			return err
		}
		common.SetContextKey(c, constant.ContextKeyAutoGroup, group)
		return nil
	}
	return AuthorizeRequestChannel(c, group, physicalModel, channelID)
}
