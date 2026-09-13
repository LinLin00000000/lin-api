package router

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// This exercises real native JS decoding/rendering, RelayTask persistence and
// RelayMidjourney submission, not post-distributor probes. Zero legacy base
// prices deliberately exclude B3 billing, settlement and free-price semantics.
// Polling/completion, retries and origin-task actions are not certified here.
func TestIdentityB2AsyncSubmissionConsumers(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B2_ASYNC_FIXTURE")
	if fixture == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIdentityB2AsyncSubmissionConsumers$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_B2_ASYNC_FIXTURE="+t.TempDir())
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "async.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.RetryTimes = 0
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Log{}, &model.Task{}, &model.Midjourney{}, &model.UserSubscription{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"model":0,"mj_imagine":0}`))
	operation_setting.SelfUseModeEnabled = true
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 10
	cfg.ServiceDefaults = map[string]float64{"default": 1}
	cfg.IdentityDefaults = map[string]float64{"Friend": 1, "ordinary": 1, "VIP": 1}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{
		"default": {"model": {Enabled: true}, "mj_imagine": {Enabled: true}},
	}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{
		"model":      {Mode: "restricted", Identities: []string{"Friend"}},
		"mj_imagine": {Mode: "restricted", Identities: []string{"Friend"}},
	}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	auth := middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return snapshot })
	users := map[string]model.User{}
	for i, identity := range []string{"Friend", "ordinary", "VIP"} {
		u := model.User{Username: "async-" + identity, Group: identity, Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("async%d", i), Quota: 1000000}
		require.NoError(t, db.Create(&u).Error)
		users[identity] = u
		token := model.Token{UserId: u.Id, Key: "async" + identity, Name: identity, Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
		require.NoError(t, db.Create(&token).Error)
	}
	for _, name := range []string{"limited", "disabled"} {
		token := model.Token{UserId: users["Friend"].Id, Key: "async" + name, Name: name, Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
		if name == "limited" {
			token.ModelLimitsEnabled = true
			token.ModelLimits = "different-model"
		} else {
			token.Status = common.TokenStatusDisabled
		}
		require.NoError(t, db.Create(&token).Error)
	}

	type upstreamRequest struct{ Path, Method, Body string }
	requests := make(chan upstreamRequest, 32)
	var totalCalls, pluginCalls, mjCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalCalls.Add(1)
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 4097))
		if readErr != nil || len(body) > 4096 {
			http.Error(w, "fixture body exceeded bound", http.StatusBadRequest)
			return
		}
		select {
		case requests <- upstreamRequest{r.URL.Path, r.Method, string(body)}:
		default:
			http.Error(w, "fixture request bound exceeded", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/native-submit":
			n := pluginCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"id":"plugin-upstream-%d"}`, n)
		case "/mj/submit/imagine":
			n := mjCalls.Add(1)
			_, _ = fmt.Fprintf(w, `{"code":1,"description":"queued locally","result":"mj-upstream-%d"}`, n)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	// Only the ephemeral loopback upstream is configured; no worker is started.
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{strings.TrimPrefix(upstream.URL[strings.LastIndex(upstream.URL, ":"):], ":")}
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())

	// Reuse the existing source/builder helper, replacing only inert JS hooks.
	source := routerPluginSource("identity-b2-native", "1.0.0", `[{method:"POST",path:"/vendor/b2/jobs",type:"submit",render:"native"},{method:"POST",path:"/vendor/b2/dynamic",type:"dynamic",render:"native"}]`)
	source = strings.Replace(source, `return {kind: "submit", model: "model", requestBody: ctx.body.value};`, `
	 const body = ctx.body.value;
	 if (body.operation === "read") return {kind:"query",taskIds:[body.task_id]};
	 if (body.operation === "derive") return {kind:"submit",model:"model",action:"remix",originTaskIds:[body.task_id],requestBody:body};
	 return {kind:"submit",model:"model",requestBody:body};`, 1)
	source = strings.Replace(source, `export function buildSubmitRequest() { return {}; }`, `export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/native-submit",method:"POST",headers:{"Content-Type":"application/json"},body:ctx.requestBody,action:"submit"}; }`, 1)
	source = strings.Replace(source, `export function parseSubmitResponse() { return {}; }`, `export function parseSubmitResponse(ctx, resp) { if (!resp.body.id) throw new Error("missing upstream id"); return {taskId:resp.body.id,taskData:resp.body}; }`, 1)
	source = strings.Replace(source, `native: function(ctx, task) { return task; }`, `native: function(ctx, task) { return {nativeRendered:true,task:task}; }`, 1)
	plugin, err := jsplugin.CompilePlugin(source, jsplugin.Options{Key: "identity-b2-native", Version: "1.0.0"})
	require.NoError(t, err)
	handlers := func(generation *jsplugin.RoutingGeneration, binding jsplugin.RouteBinding) []gin.HandlerFunc {
		chain := productionPluginRouteHandlers(generation, binding)
		// Substitute the provider-aware REAL TokenAuth, not authentication results.
		chain[1] = auth
		return chain
	}
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	jsplugin.DefaultRegistry = registry
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
	mj := gin.New()
	mj.POST("/mj/submit/imagine", auth, middleware.Distribute(), controller.RelayMidjourney)
	pluginChannel := model.Channel{Name: "async-native", Type: constant.ChannelTypeTaskPlugin, Key: "local-only", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: "model", Group: "default"}
	pluginChannel.SetSetting(relaydto.ChannelSettings{TaskPluginKey: "identity-b2-native"})
	require.NoError(t, pluginChannel.Insert())
	mjChannel := model.Channel{Name: "async-mj", Type: constant.ChannelTypeMidjourney, Key: "local-only", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: "mj_imagine", Group: "default"}
	require.NoError(t, mjChannel.Insert())
	counts := func() (int64, int64) {
		t.Helper()
		var tasks, midjourneys int64
		require.NoError(t, db.Model(&model.Task{}).Count(&tasks).Error)
		require.NoError(t, db.Model(&model.Midjourney{}).Count(&midjourneys).Error)
		return tasks, midjourneys
	}
	var wantTasks, wantMJ int64
	for _, cache := range []bool{false, true} {
		common.MemoryCacheEnabled = cache
		if cache {
			model.InitChannelCache()
		}
		for _, protocol := range []struct {
			name, path, body, upstreamPath string
			handler                        http.Handler
		}{
			{"native", "/vendor/b2/jobs", `{"prompt":"bounded-local","identity":"Friend"}`, "/native-submit", outer},
			{"mj", "/mj/submit/imagine", `{"prompt":"bounded-local","identity":"Friend"}`, "/mj/submit/imagine", mj},
		} {
			t.Run(fmt.Sprintf("cache=%t/%s", cache, protocol.name), func(t *testing.T) {
				for _, key := range []string{"ordinary", "VIP", "limited", "disabled", "missing", "Friend"} {
					before := totalCalls.Load()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					req := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(protocol.body)).WithContext(ctx)
					req.Header.Set("Authorization", "Bearer sk-async"+key)
					req.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					protocol.handler.ServeHTTP(w, req)
					cancel()
					if key != "Friend" {
						if key == "disabled" || key == "missing" {
							require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
						} else {
							require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
						}
						require.Equal(t, before, totalCalls.Load(), "rejected request reached upstream")
					} else {
						require.Equal(t, http.StatusOK, w.Code, w.Body.String())
						require.Equal(t, before+1, totalCalls.Load())
						select {
						case observed := <-requests:
							require.Equal(t, protocol.upstreamPath, observed.Path)
							require.Equal(t, http.MethodPost, observed.Method)
							require.Contains(t, observed.Body, "bounded-local")
						default:
							t.Fatal("successful consumer did not issue upstream request")
						}
						if protocol.name == "native" {
							wantTasks++
							var response struct {
								NativeRendered bool `json:"nativeRendered"`
								Task           struct {
									TaskID string `json:"task_id"`
								} `json:"task"`
							}
							require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
							require.True(t, response.NativeRendered, "host fallback is not native rendering")
							require.NotEmpty(t, response.Task.TaskID)
							var task model.Task
							require.NoError(t, db.Where("task_id = ?", response.Task.TaskID).First(&task).Error)
							require.Equal(t, users["Friend"].Id, task.UserId)
							require.Equal(t, pluginChannel.Id, task.ChannelId)
							require.Equal(t, "default", task.Group)
							require.Equal(t, "model", task.Properties.OriginModelName)
							require.Equal(t, "identity-b2-native", string(task.Platform))
							require.Equal(t, fmt.Sprintf("plugin-upstream-%d", wantTasks), task.PrivateData.UpstreamTaskID)
							require.NotNil(t, task.PrivateData.Execution)
							require.NotNil(t, task.PrivateData.Execution.TaskPlugin)
							require.Equal(t, "identity-b2-native", task.PrivateData.Execution.TaskPlugin.Key)
						} else {
							wantMJ++
							var response struct {
								Code   int    `json:"code"`
								Result string `json:"result"`
							}
							require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
							require.Equal(t, 1, response.Code)
							require.Equal(t, fmt.Sprintf("mj-upstream-%d", wantMJ), response.Result)
							var task model.Midjourney
							require.NoError(t, db.Where("mj_id = ?", response.Result).First(&task).Error)
							require.Equal(t, users["Friend"].Id, task.UserId)
							require.Equal(t, mjChannel.Id, task.ChannelId)
							require.Equal(t, constant.MjActionImagine, task.Action)
							require.Equal(t, "bounded-local", task.Prompt)
							require.Equal(t, 1, task.Code)
						}
					}
					tasks, midjourneys := counts()
					require.Equal(t, wantTasks, tasks, "exact durable task count after "+key)
					require.Equal(t, wantMJ, midjourneys, "exact durable MJ count after "+key)
				}
			})
		}
	}
	require.EqualValues(t, 2, wantTasks)
	require.EqualValues(t, 2, wantMJ)
	require.Equal(t, wantTasks, pluginCalls.Load())
	require.Equal(t, wantMJ, mjCalls.Load())
	require.Equal(t, wantTasks+wantMJ, totalCalls.Load())
	require.Empty(t, requests)

	call := func(handler http.Handler, path, key, body string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer sk-async"+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	// Positive control uses the exact dynamic chain and real upstream/persistence.
	w := call(outer, "/vendor/b2/dynamic", "Friend", `{"prompt":"dynamic-positive"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	wantTasks++
	require.Equal(t, wantTasks, pluginCalls.Load())
	observed := <-requests
	require.Contains(t, observed.Body, "dynamic-positive")
	var origin model.Task
	require.NoError(t, db.Order("id DESC").First(&origin).Error)
	for _, key := range []string{"ordinary", "limited"} {
		before := totalCalls.Load()
		for _, body := range []string{`{"prompt":"not-authorized"}`, fmt.Sprintf(`{"operation":"derive","task_id":%q}`, origin.TaskID)} {
			w = call(outer, "/vendor/b2/dynamic", key, body)
			require.Equal(t, 403, w.Code, w.Body.String())
		}
		require.Equal(t, before, totalCalls.Load())
		tasks, midjourneys := counts()
		require.Equal(t, wantTasks, tasks)
		require.Equal(t, wantMJ, midjourneys)
	}
	// Withdrawal happens after the task exists. Decoder read remains owner-
	// scoped; a client action/read flag cannot convert submit/derive into read.
	delete(cfg.ServiceDefaults, "default")
	delete(cfg.ServiceModels, "default")
	cfg.Revision++
	snapshot, err = identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	before := totalCalls.Load()
	for _, key := range []string{"Friend", "ordinary"} {
		w = call(outer, "/vendor/b2/dynamic", key, fmt.Sprintf(`{"operation":"read","task_id":%q}`, origin.TaskID))
		if key == "Friend" {
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), origin.TaskID)
		} else {
			require.NotContains(t, w.Body.String(), origin.TaskID)
		}
	}
	for _, body := range []string{
		`{"kind":"query","action":"query","identity_task_read":true,"shouldSelectChannel":false}`,
		fmt.Sprintf(`{"operation":"derive","task_id":%q}`, origin.TaskID),
	} {
		w = call(outer, "/vendor/b2/dynamic?action=query", "Friend", body)
		require.Equal(t, 403, w.Code, w.Body.String())
	}
	for _, path := range []string{"/mj/submit/action", "/mj/submit/modal", "/mj/insight-face/swap", "/mj/submit/upload-discord-images"} {
		mj.POST(path, auth, middleware.Distribute(), controller.RelayMidjourney)
		w = call(mj, path, "Friend", `{"taskId":"mj-upstream-1","action":"query","identity_task_read":true}`)
		require.Equal(t, 403, w.Code, w.Body.String())
	}
	require.Equal(t, before, totalCalls.Load(), "retired-service queries/rejected new work must not issue upstream calls")
	tasks, midjourneys := counts()
	require.Equal(t, wantTasks, tasks)
	require.Equal(t, wantMJ, midjourneys)
	require.Empty(t, requests)
}
