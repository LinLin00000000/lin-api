package router

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// RED/GREEN regression source: before the task-read admission exception, the
// owner reads below return 403 for a retired service. No authorization resolver
// is mocked. Execution is intentionally delegated to the parent (disk budget).
func TestIdentityB2HistoricalTaskReads(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B2_TASK_READ_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB2HistoricalTaskReads$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_B2_TASK_READ_FIXTURE="+t.TempDir())
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "historical-reads.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}, &model.Midjourney{}, &model.Channel{}, &model.Ability{}, &model.Option{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","auto":"Auto"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default"]`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.IdentityDefaults = map[string]float64{"Friend": 0}
	cfg.ServiceDefaults = map[string]float64{"default": 1}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	auth := middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return snapshot })
	owner := model.User{Username: "historicalowner", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "historyowner"}
	other := model.User{Username: "historicalother", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "historyother"}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	addKey := func(key string, userID int, group string) model.Token {
		token := model.Token{UserId: userID, Key: key, Name: key, Group: group, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: "unrelated-model"}
		require.NoError(t, db.Create(&token).Error)
		return token
	}
	addKey("historyowner", owner.Id, "retired")
	addKey("historysecond", owner.Id, "retired") // ownership is user-, not creating-Key-scoped
	addKey("historyother", other.Id, "retired")
	addKey("historycurrent", owner.Id, "default")
	expired := addKey("historyexpired", owner.Id, "retired")
	require.NoError(t, db.Model(&expired).Update("expired_time", time.Now().Unix()-1).Error)
	disabled := addKey("historydisabled", owner.Id, "retired")
	require.NoError(t, db.Model(&disabled).Update("status", common.TokenStatusDisabled).Error)
	ip := addKey("historyip", owner.Id, "retired")
	require.NoError(t, db.Model(&ip).Update("allow_ips", "10.0.0.1").Error)
	exhausted := addKey("historyexhausted", owner.Id, "retired")
	require.NoError(t, db.Model(&exhausted).Updates(map[string]any{"unlimited_quota": false, "remain_quota": 0}).Error)
	task := model.Task{TaskID: "ownedtask", UserId: owner.Id, Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "removed-model"}}
	require.NoError(t, db.Create(&task).Error)
	mj := model.Midjourney{MjId: "ownedmj", UserId: owner.Id, Status: "SUCCESS", Prompt: "owner-only-prompt"}
	require.NoError(t, db.Create(&mj).Error)
	call := func(e *gin.Engine, method, path, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer sk-"+key)
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "192.0.2.10:2345"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, r)
		return w
	}
	actual := gin.New()
	actual.GET("/v1/tasks/:key", auth, controller.GetTask)
	actual.GET("/v1/tasks/:key/artifacts", auth, controller.GetTaskArtifacts)
	actual.GET("/v1/video/generations/:task_id", auth, middleware.Distribute(), controller.RelayTaskFetch)
	actual.GET("/mj/task/:id/fetch", auth, middleware.Distribute(), controller.RelayMidjourney)
	actual.POST("/mj/task/list-by-condition", auth, middleware.Distribute(), controller.RelayMidjourney)
	for _, key := range []string{"historyowner", "historysecond", "historycurrent"} {
		for _, path := range []string{"/v1/tasks/ownedtask", "/v1/tasks/ownedtask/artifacts", "/v1/video/generations/ownedtask", "/mj/task/ownedmj/fetch"} {
			w := call(actual, "GET", path, key, "")
			require.Equal(t, 200, w.Code, "%s: %s", path, w.Body.String())
		}
		w := call(actual, "POST", "/mj/task/list-by-condition", key, `{"ids":["ownedmj"]}`)
		require.Equal(t, 200, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "owner-only-prompt")
	}
	for _, path := range []string{"/v1/tasks/ownedtask", "/v1/tasks/ownedtask/artifacts"} {
		require.Equal(t, 404, call(actual, "GET", path, "historyother", "").Code)
	}
	for _, path := range []string{"/v1/video/generations/ownedtask", "/mj/task/ownedmj/fetch"} {
		w := call(actual, "GET", path, "historyother", "")
		require.NotContains(t, w.Body.String(), "owner-only-prompt")
		require.NotContains(t, w.Body.String(), "removed-model")
		require.Contains(t, w.Body.String(), "task_")
	}
	w := call(actual, "POST", "/mj/task/list-by-condition", "historyother", `{"ids":["ownedmj"]}`)
	require.Equal(t, "[]", strings.TrimSpace(w.Body.String()))
	for _, key := range []string{"historyexpired", "historydisabled", "historyip", "historyexhausted", "unknown"} {
		w := call(actual, "GET", "/v1/tasks/ownedtask", key, "")
		require.Contains(t, []int{401, 403}, w.Code, "%s: %s", key, w.Body.String())
	}
	// Every allowlist entry is checked at genuine TokenAuth; probes only replace
	// consumers that could fetch upstream bytes or require installed plugins.
	for _, tc := range []struct{ method, pattern, path string }{
		{"GET", "/v1/responses/:response_id", "/v1/responses/ownedtask"},
		{"GET", "/v1/videos/:task_id", "/v1/videos/ownedtask"},
		{"GET", "/v1/videos/:task_id/content", "/v1/videos/ownedtask/content"},
		{"HEAD", "/v1/videos/:task_id/content", "/v1/videos/ownedtask/content"},
		{"GET", "/v1/tasks/:key/artifacts/:artifact_key/content", "/v1/tasks/ownedtask/artifacts/video/content"},
		{"HEAD", "/v1/tasks/:key/artifacts/:artifact_key/content", "/v1/tasks/ownedtask/artifacts/video/content"},
		{"GET", "/mj/task/:id/image-seed", "/mj/task/ownedmj/image-seed"},
		{"GET", "/:mode/mj/task/:id/fetch", "/fast/mj/task/ownedmj/fetch"},
		{"GET", "/:mode/mj/task/:id/image-seed", "/fast/mj/task/ownedmj/image-seed"},
		{"POST", "/:mode/mj/task/list-by-condition", "/fast/mj/task/list-by-condition"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			e := gin.New()
			e.Handle(tc.method, tc.pattern, auth, func(c *gin.Context) {
				require.True(t, middleware.IsIdentityTaskRead(c))
				require.Equal(t, owner.Id, c.GetInt("id"))
				// Read admission must not weaken the normal resolver itself.
				require.Error(t, service.RequestIdentity(c).Authorize("retired", "removed-model"))
				c.Status(204)
			})
			require.Equal(t, 204, call(e, tc.method, tc.path, "historyowner", `{}`).Code)
		})
	}
	for i, tc := range []struct{ method, pattern, path string }{
		{"POST", "/v1/videos/:video_id/remix", "/v1/videos/ownedtask/remix"},
		{"POST", "/v1/tasks/:key", "/v1/tasks/plugin"},
		{"POST", "/v1/responses", "/v1/responses"},
		{"POST", "/mj/submit/action", "/mj/submit/action"},
		{"POST", "/mj/submit/modal", "/mj/submit/modal"},
		{"POST", "/mj/insight-face/swap", "/mj/insight-face/swap"},
		{"POST", "/v1/videos/:task_id", "/v1/videos/ownedtask"},
		{"GET", "/v1/videos/:task_id/remix", "/v1/videos/ownedtask/remix"},
		{"GET", "/untrusted/task/:id/fetch", "/untrusted/task/ownedtask/fetch"},
		{"GET", "/mj/image/:id", "/mj/image/ownedmj"},
		{"POST", "/native/dynamic", "/native/dynamic"},
	} {
		t.Run(fmt.Sprintf("not-read-%d", i), func(t *testing.T) {
			e := gin.New()
			e.Handle(tc.method, tc.pattern, func(c *gin.Context) {
				c.Set("identity_task_read", true)
				c.Set("shouldSelectChannel", false)
				c.Next()
			}, auth, func(c *gin.Context) { t.Error("billable or unknown route bypassed service admission"); c.Status(204) })
			w := call(e, tc.method, tc.path+"?action=query&identity_task_read=true", "historyowner", `{"action":"query","task_id":"ownedtask"}`)
			require.Equal(t, 403, w.Code, w.Body.String())
		})
	}
	// Genuine production native route pin + auth + owner/platform query renderer.
	// Only the actual pinned decoder decides read versus new work.
	source := routerPluginSource("historyplugin", "1.0.0", `[
		{method:"GET", path:"/history/native/:task_id", type:"query"},
		{method:"POST", path:"/history/dynamic", type:"dynamic"},
		{method:"POST", path:"/history/submit", type:"submit"}
	]`)
	source = strings.Replace(source, `return {kind: "submit", model: "model", requestBody: ctx.body.value};`, `
	 const body = ctx.body.value;
	 if (body.operation === "read") return {kind:"query", taskIds:[body.task_id]};
	 if (body.operation === "invalid") return {kind:"query", taskIds:"ownedtask"};
	 return {kind:"submit", model:"model", action:body.action || "create", requestBody:body};`, 1)
	plugin, err := jsplugin.CompilePlugin(source, jsplugin.Options{Key: "historyplugin", Version: "1.0.0"})
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{plugin}))
	generation := registry.Generation()
	require.NoError(t, db.Model(&task).Update("platform", "historyplugin").Error)
	native := gin.New()
	for _, binding := range generation.Routes() {
		production := productionPluginRouteHandlers(generation, binding)
		native.Handle(binding.Route.Method, binding.Route.Path, production[0], auth,
			middleware.PrepareTaskPluginRoute(), func(c *gin.Context) {
				t.Error("query preparation must render and abort, never submit")
				c.Status(500)
			})
	}
	w = call(native, "GET", "/history/native/ownedtask", "historyowner", "")
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "ownedtask")
	w = call(native, "GET", "/history/native/ownedtask", "historyother", "")
	require.NotEqual(t, 200, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "removed-model")
	w = call(native, "POST", "/history/dynamic", "historyowner", `{"operation":"read","task_id":"ownedtask"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "ownedtask")
	w = call(native, "POST", "/history/dynamic", "historyother", `{"operation":"read","task_id":"ownedtask"}`)
	require.Equal(t, 404, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), "removed-model")
	for _, body := range []string{`{"kind":"query","action":"query","task_id":"ownedtask"}`, `{"operation":"derive","action":"remix","task_id":"ownedtask"}`} {
		w = call(native, "POST", "/history/dynamic?identity_task_read=true", "historyowner", body)
		require.Equal(t, 403, w.Code, w.Body.String())
	}
	w = call(native, "POST", "/history/dynamic", "historyowner", `{"operation":"invalid"}`)
	require.Equal(t, 400, w.Code, w.Body.String())
	w = call(native, "POST", "/history/submit", "historycurrent", `{"operation":"read","task_id":"ownedtask"}`)
	require.Equal(t, 400, w.Code, w.Body.String())

	// Even a server context with the right path/type is not sufficient when
	// its decoder differs from the immutable generation's declared binding.
	for _, binding := range generation.Routes() {
		if binding.Route.Type != jsplugin.RouteTypeDynamic {
			continue
		}
		e := gin.New()
		production := productionPluginRouteHandlers(generation, binding)
		e.POST(binding.Route.Path, production[0], func(c *gin.Context) {
			value, _ := c.Get(jsplugin.ContextKeyPinnedRoute)
			pinned := value.(jsplugin.PinnedRoute)
			pinned.Route.Decode = "native"
			c.Set(jsplugin.ContextKeyPinnedRoute, pinned)
			c.Next()
		}, auth, middleware.PrepareTaskPluginRoute(), middleware.Distribute(), controller.RelayTask)
		for _, key := range []string{"historyowner", "historycurrent"} {
			w = call(e, "POST", binding.Route.Path, key, `{"operation":"read","task_id":"ownedtask"}`)
			require.Equal(t, 403, w.Code, w.Body.String())
		}
	}

	// Default storage cannot activate this exception; legacy behavior is intact.
	_, err = model.IdentityServiceSettings.Save(cfg, 0)
	require.ErrorIs(t, err, identityservice.ErrActivationPending)
	legacy := gin.New()
	legacy.GET("/v1/tasks/:key", middleware.TokenAuth(), controller.GetTask)
	require.Equal(t, 403, call(legacy, "GET", "/v1/tasks/ownedtask", "historyowner", "").Code)
	require.NoError(t, db.Model(&owner).Update("status", common.UserStatusDisabled).Error)
	require.Equal(t, 403, call(actual, "GET", "/v1/tasks/ownedtask", "historyowner", "").Code)
}
