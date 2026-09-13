package middleware

import (
	"net/http"
	"reflect"

	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// IsIdentityTaskRead separates existing owner-scoped reads from new work. It is
// deliberately not !shouldSelectChannel: remix and origin-task submissions also
// skip initial selection. Callers must still run the existing owner lookup and
// protocol preparation. This grants no task ownership or channel credentials.
func IsIdentityTaskRead(c *gin.Context) bool {
	return service.RequestIdentity(c) != nil && identityTaskReadRoute(c)
}

func identityTaskReadRoute(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	// Match the router's complete registered pattern AND method, never a URL
	// prefix/suffix, query action, body field, or user-populatable context flag.
	path, method := c.FullPath(), c.Request.Method
	switch method {
	case http.MethodGet:
		switch path {
		case "/v1/tasks/:key", "/v1/tasks/:key/artifacts",
			"/v1/tasks/:key/artifacts/:artifact_key/content",
			"/v1/responses/:response_id", "/v1/videos/:task_id",
			"/v1/videos/:task_id/content", "/v1/video/generations/:task_id",
			"/mj/task/:id/fetch", "/mj/task/:id/image-seed",
			"/:mode/mj/task/:id/fetch", "/:mode/mj/task/:id/image-seed":
			return true
		}
	case http.MethodHead:
		return path == "/v1/videos/:task_id/content" || path == "/v1/tasks/:key/artifacts/:artifact_key/content"
	case http.MethodPost:
		if path == "/mj/task/list-by-condition" || path == "/:mode/mj/task/list-by-condition" {
			return true
		}
	}
	// A fixed native query is safe to classify before preparation. Dynamic
	// routes are NOT reads here: their trusted decoder must run first.
	pinned, ok := identityPinnedRoute(c)
	return ok && pinned.Route.Type == jsplugin.RouteTypeQuery && pinned.Route.TaskIDParam != ""
}

// identityDynamicPreparation permits authentication to defer only product
// admission. Preparation cannot submit: after decoding, every submit must pass
// Authorize before origin preparation, distribution, persistence or upstream IO.
func identityDynamicPreparation(c *gin.Context) bool {
	pinned, ok := identityPinnedRoute(c)
	return ok && pinned.Route.Type == jsplugin.RouteTypeDynamic && pinned.Route.Decode != ""
}

func identityPinnedRoute(c *gin.Context) (jsplugin.PinnedRoute, bool) {
	if c == nil || c.Request == nil {
		return jsplugin.PinnedRoute{}, false
	}
	value, ok := c.Get(jsplugin.ContextKeyPinnedRoute)
	pinned, typed := value.(jsplugin.PinnedRoute)
	if !ok || !typed || pinned.Generation == nil || pinned.Plugin == nil ||
		pinned.Route.Path != c.FullPath() || pinned.Route.Method != c.Request.Method {
		return jsplugin.PinnedRoute{}, false
	}
	for _, binding := range pinned.Generation.Routes() {
		if binding.Plugin == pinned.Plugin && reflect.DeepEqual(binding.Route, pinned.Route) {
			return pinned, true
		}
	}
	return jsplugin.PinnedRoute{}, false
}
