package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/gin-gonic/gin"
)

// Registered only under the existing RootAuth option-management boundary.
func registerIdentityServiceRoutes(optionRoute *gin.RouterGroup) {
	optionRoute.GET("/identity_service", controller.GetIdentityServiceSetting)
	optionRoute.GET("/identity_service/quote", controller.PreviewCurrentIdentityPricing)
	optionRoute.PUT("/identity_service", controller.PutIdentityServiceSetting)
	optionRoute.POST("/identity_service/validate", controller.ValidateIdentityServiceSetting)
}
