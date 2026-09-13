package router

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/QuantumNous/new-api/relay"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Real native submit + durable SQLite close/reopen + production polling and
// real wallet/Token ledgers. No settlement function is substituted.
func TestIdentityB3bDurableConsumer(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3B_FIXTURE")
	if fixture == "" {
		for _, funding := range []string{"wallet", "subscription"} {
			for _, kind := range []string{"success", "failure", "failure-missing-channel", "batch-failure-missing-channel", "failure-null-id", "explicit-zero", "actual-positive", "absent", "other-ratios", "submit-adjust", "submit-zero-adjust", "expression", "batch-expression", "live-expression", "expression-immediate-hook", "expression-immediate-facts", "expression-immediate-zero", "expression-immediate-zero-facts", "expression-tool-zero-base", "retry", "retry-auto", "percall", "percall-zero", "immediate-success", "immediate-failure", "immediate-zero", "expression-zero", "batch-success", "batch-zero", "batch-failure", "live-success", "live-failure", "live-zero", "live-cas-loser", "live-vertex", "free-S", "free-D", "free-B", "legacy-success", "legacy-failure", "expression-immediate-missing", "expression-immediate-invalid", "expression-immediate-hook-error", "legacy-expression-immediate-hook-error"} {
				t.Run(funding+"/"+kind, func(t *testing.T) {
					fixtureDir := t.TempDir()
					if evidenceRoot := os.Getenv("IDENTITY_B3B_READBACK_DIR"); evidenceRoot != "" {
						fixtureDir = filepath.Join(evidenceRoot, funding, kind)
						require.NoError(t, os.MkdirAll(fixtureDir, 0700))
					}
					cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3bDurableConsumer$", "-test.count=1", "-test.timeout=100s")
					cmd.Env = append(os.Environ(), "IDENTITY_B3B_FIXTURE="+fixtureDir, "IDENTITY_B3B_CASE="+kind, "IDENTITY_B3B_FUNDING="+funding)
					output, err := cmd.CombinedOutput()
					t.Log(string(output))
					require.NoError(t, err)
				})
			}
		}
		return
	}
	kind := os.Getenv("IDENTITY_B3B_CASE")
	subFunding := os.Getenv("IDENTITY_B3B_FUNDING") == "subscription"
	legacy := strings.HasPrefix(kind, "legacy")
	completionError := strings.HasPrefix(kind, "expression-immediate-") && (strings.HasSuffix(kind, "missing") || strings.HasSuffix(kind, "invalid") || strings.HasSuffix(kind, "hook-error"))
	wantReserve, wantFinal := 750000, 30
	if strings.Contains(kind, "missing-channel") || kind == "failure-null-id" {
		wantFinal = 0
	}
	if kind == "live-cas-loser" {
		wantFinal = 0
	}
	if kind == "batch-zero" || kind == "batch-failure" || kind == "live-zero" || kind == "live-failure" {
		wantFinal = 0
	}
	if legacy {
		wantReserve = 500000
		wantFinal = 15470
	}
	if kind == "failure" || kind == "legacy-failure" || kind == "explicit-zero" {
		wantFinal = 0
	}
	if kind == "percall" {
		wantReserve = 750
		wantFinal = 750
	}
	if kind == "percall-zero" {
		wantReserve = 750
		wantFinal = 0
	}
	if strings.Contains(kind, "immediate") {
		wantReserve = 30
		wantFinal = 30
		if kind != "immediate-success" {
			wantReserve = 0
			wantFinal = 0
		}
	}
	if kind == "expression-zero" {
		wantReserve = 1500
		wantFinal = 0
	}
	if kind == "retry-auto" {
		wantReserve = 1500000
		wantFinal = 60
	}
	if kind == "actual-positive" {
		wantFinal = 60
	}
	if kind == "other-ratios" {
		wantReserve = 1500000
		wantFinal = 60
	}
	if kind == "submit-adjust" || kind == "submit-zero-adjust" {
		wantReserve = 2250000
		wantFinal = 90
	}
	if kind == "expression" || kind == "batch-expression" || kind == "live-expression" || strings.HasPrefix(kind, "expression-immediate") {
		wantReserve = 1500
		wantFinal = 6000
	}
	if strings.HasPrefix(kind, "expression-immediate") {
		wantReserve = wantFinal
		if kind == "expression-immediate-zero" || kind == "expression-immediate-zero-facts" {
			wantReserve, wantFinal = 0, 0
		}
	}
	if kind == "expression-tool-zero-base" {
		wantReserve = 0
		wantFinal = 6000
	}
	if kind == "absent" {
		wantFinal = wantReserve
	}
	if strings.HasPrefix(kind, "free-") {
		wantReserve = 0
		wantFinal = 0
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "durable.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.RetryTimes = 0
	constant.TaskQueryLimit = 100
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Log{}, &model.Task{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"model":2}`))
	if strings.Contains(kind, "expression") {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"model":"tiered_expr"}`, "billing_setting.billing_expr": `{"model":"0 * u(\"base\") + 0.001 * u(\"seconds\")"}`}))
	}
	if strings.HasPrefix(kind, "percall") {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"model":0.001}`))
	}
	operation_setting.SelfUseModeEnabled = true
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = false
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 31
	cfg.ServiceDefaults = map[string]float64{"default": 3}
	cfg.IdentityDefaults = map[string]float64{"Friend": 0.5}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {"model": {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{"model": {Mode: "public"}}
	if kind == "free-S" {
		cfg.ServiceDefaults["default"] = 0
	}
	if kind == "free-D" {
		cfg.IdentityDefaults["Friend"] = 0
	}
	if kind == "free-B" {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"model":0}`))
	}
	if kind == "retry" || kind == "retry-auto" {
		common.RetryTimes = 2
	}
	if kind == "retry-auto" {
		require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","pro"]`))
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro","auto":"Auto"}`))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"pro":1}`))
		cfg.ServiceDefaults["pro"] = 6
		cfg.ServiceModels["pro"] = map[string]identityservice.ServiceModel{"model": {Enabled: true}}
	}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	var current atomic.Pointer[identityservice.Snapshot]
	current.Store(snapshot)
	auth := middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot {
		if legacy {
			return nil
		}
		return current.Load()
	})
	user := model.User{Username: "b3b", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "b3b", Quota: 10000000}
	if subFunding {
		user.Setting = `{"billing_preference":"subscription_only"}`
	} else {
		user.Setting = `{"billing_preference":"wallet_only"}`
	}
	require.NoError(t, db.Create(&user).Error)
	sub := model.UserSubscription{}
	if subFunding {
		plan := model.SubscriptionPlan{Title: "B3b fixture", Enabled: true}
		require.NoError(t, db.Create(&plan).Error)
		sub = model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 10000000, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}
		require.NoError(t, db.Create(&sub).Error)
	}
	token := model.Token{UserId: user.Id, Key: "b3blocal", Name: "b3b", Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 10000000}
	if kind == "retry-auto" {
		token.Group = "auto"
		token.CrossGroupRetry = true
	}
	require.NoError(t, db.Create(&token).Error)
	var attempts atomic.Int64
	var pollCalls atomic.Int64
	reserves := make(chan int, 8)
	submitDB := db
	tokenID := token.Id
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/submit":
			attempt := attempts.Add(1)
			if strings.Contains(kind, "expression") {
				var observed model.Token
				if err := submitDB.First(&observed, tokenID).Error; err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				reserves <- 10000000 - observed.RemainQuota
			}
			if kind == "retry" || kind == "retry-auto" {
				var observed model.Token
				if err := submitDB.First(&observed, tokenID).Error; err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				reserves <- 10000000 - observed.RemainQuota
				_ = ratio_setting.UpdateModelRatioByJSONString(`{"model":91}`)
				next := cfg
				next.Revision = 99
				next.ServiceDefaults = map[string]float64{"default": 99, "pro": 99}
				s, _ := identityservice.NewSnapshot(next)
				current.Store(s)
				if attempt <= 2 || (kind == "retry-auto" && attempt == 3) {
					http.Error(w, "local retry", 500)
					return
				}
			}
			if strings.Contains(kind, "immediate") {
				status := "SUCCESS"
				if kind == "immediate-failure" {
					status = "FAILURE"
				}
				immediate := map[string]any{"status": status, "totalTokens": 10}
				if kind == "expression-immediate-facts" || kind == "expression-immediate-zero" || kind == "expression-immediate-zero-facts" {
					immediate["usageFacts"] = map[string]any{"seconds": 8}
					if kind == "expression-immediate-zero-facts" {
						immediate["usageFacts"] = map[string]any{"seconds": 0}
					}
				}
				if kind == "immediate-zero" || kind == "expression-immediate-zero" {
					immediate["actualQuota"] = 0
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "remote-b3b", "immediate": immediate, "seconds": 8})
			} else {
				fmt.Fprint(w, `{"id":"remote-b3b"}`)
			}
		case "/poll":
			n := pollCalls.Add(1)
			if kind == "live-cas-loser" && n == 1 {
				service.RunTaskPollingOnce(context.Background(), nil)
			}
			body := map[string]any{"status": "SUCCESS", "totalTokens": 10, "seconds": 8}
			if kind == "failure" || kind == "legacy-failure" || kind == "batch-failure" || kind == "live-failure" || (kind == "live-cas-loser" && n > 1) {
				body["status"] = "FAILURE"
				body["reason"] = "bounded failure"
				delete(body, "totalTokens")
			}
			if kind == "explicit-zero" || kind == "expression-zero" || kind == "percall-zero" || kind == "batch-zero" || kind == "live-zero" {
				body["actualQuota"] = 0
			}
			if kind == "actual-positive" {
				body["actualQuota"] = 40
			}
			if kind == "absent" {
				delete(body, "totalTokens")
			}
			require.NoError(t, json.NewEncoder(w).Encode(body))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{upstream.URL[strings.LastIndex(upstream.URL, ":")+1:]}
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	pluginKey := "b3b-native"
	if strings.HasPrefix(kind, "live-") {
		pluginKey = "google"
	}
	if kind == "live-vertex" {
		pluginKey = "vertex-ai"
	}
	source := routerPluginSource(pluginKey, "1.0.0", `[{method:"POST",path:"/vendor/b3b/jobs",type:"submit",render:"native"}]`)
	source = strings.Replace(source, `export function buildSubmitRequest() { return {}; }`, `export function buildSubmitRequest(ctx) { return {url:ctx.baseUrl+"/submit",method:"POST",headers:{"Content-Type":"application/json"},body:ctx.requestBody}; }`, 1)
	source = strings.Replace(source, `export function parseSubmitResponse() { return {}; }`, `export function parseSubmitResponse(ctx,resp) { return {taskId:resp.body.id,taskData:resp.body,immediate:resp.body.immediate}; }`, 1)
	source = strings.Replace(source, `export function buildQueryRequest() { return {}; }`, `export function buildQueryRequest(ctx) { return {url:ctx.baseUrl+"/poll",method:"GET"}; }`, 1)
	source = strings.Replace(source, `export function parseTaskResult() { return {}; }`, `export function parseTaskResult(ctx,body) { return {status:body.status,reason:body.reason,totalTokens:body.totalTokens,actualQuota:body.actualQuota}; }`, 1)
	if kind == "other-ratios" || kind == "submit-adjust" || kind == "submit-zero-adjust" {
		source += `
export function extractUsage(){return {seconds:2};}`
	}
	if kind == "submit-zero-adjust" {
		source = strings.Replace(source, `return {seconds:2};`, `return {seconds:0};`, 1)
	}
	if kind == "submit-adjust" || kind == "submit-zero-adjust" {
		source += `
export function extractUsageOnSubmit(){return {seconds:3};}`
	}
	if strings.Contains(kind, "expression") && kind != "expression-immediate-facts" && kind != "expression-immediate-zero" && kind != "expression-immediate-zero-facts" {
		initial := 2
		if kind == "expression-tool-zero-base" {
			initial = 0
		}
		source += fmt.Sprintf(`
export function extractUsage(){return {base:10,seconds:%d};}
export function extractUsageOnComplete(ctx,result,body){return {base:99,seconds:body ? body.seconds : 2};}`, initial)
	}
	if kind == "expression-immediate-facts" || kind == "expression-immediate-zero" || kind == "expression-immediate-zero-facts" {
		source += `
export function extractUsage(){return {base:10,seconds:2};}`
	}
	if strings.HasPrefix(kind, "batch-") {
		source = strings.Replace(source, `fetchMode: "per_task"`, `fetchMode: "batch"`, 1)
		source += `
export function buildBatchQueryRequest(ctx){return {url:ctx.baseUrl+"/poll",method:"GET"};}
export function parseBatchResult(ctx,body){return ctx.tasks.map(function(t){return {taskId:t.taskId,status:body.status,reason:body.reason,actualQuota:body.actualQuota,data:body};});}
`
		if kind != "batch-expression" {
			source += `export function extractUsageOnComplete(ctx,result,body){return {totalTokens:10};}`
		}
	}
	if completionError || kind == "legacy-expression-immediate-hook-error" {
		common.RetryTimes = 1
		hook := `return {};`
		if strings.HasSuffix(kind, "invalid") {
			hook = `return {seconds:-1};`
		}
		if strings.HasSuffix(kind, "hook-error") {
			hook = `if (body && body.immediate) throw new Error("query response required"); return {seconds:8};`
		}
		source = strings.Replace(source, `return {base:99,seconds:body ? body.seconds : 2};`, hook, 1)
		if legacy {
			wantReserve, wantFinal = 1000, 1000
		}
	}
	plugin, err := jsplugin.CompilePlugin(source, jsplugin.Options{Key: pluginKey, Version: "1.0.0"})
	require.NoError(t, err)
	handlers := func(g *jsplugin.RoutingGeneration, b jsplugin.RouteBinding) []gin.HandlerFunc {
		chain := productionPluginRouteHandlers(g, b)
		chain[1] = auth
		return chain
	}
	outer, registry := newPluginRouterTest(t, []*jsplugin.LoadedPlugin{plugin}, handlers)
	jsplugin.DefaultRegistry = registry
	outer.NoRoute((&pluginRouteDispatcher{registry: registry}).dispatch)
	ch := model.Channel{Name: "b3b-native", Type: constant.ChannelTypeTaskPlugin, Key: "local-only", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: "model", Group: "default"}
	if kind == "retry-auto" {
		ch.Group = "default,pro"
	}
	ch.AutoBan = common.GetPointer(0)
	ch.SetSetting(relaydto.ChannelSettings{TaskPluginKey: pluginKey})
	require.NoError(t, ch.Insert())
	req := httptest.NewRequest("POST", "/vendor/b3b/jobs", strings.NewReader(`{"prompt":"local"}`))
	req.Header.Set("Authorization", "Bearer sk-b3blocal")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	outer.ServeHTTP(w, req)
	if completionError {
		require.Equal(t, 502, w.Code, w.Body.String())
		require.Equal(t, int64(1), attempts.Load(), "accepted upstream must never resubmit")
		var accepted model.Task
		require.NoError(t, db.First(&accepted).Error, "accepted SUCCESS must remain auditable")
		require.Equal(t, "remote-b3b", accepted.GetUpstreamTaskID())
		require.Equal(t, model.TaskStatus(model.TaskStatusSuccess), accepted.Status)
		require.Contains(t, accepted.FailReason, "settlement incomplete")
		// Preserve the existing public 5xx sanitization boundary. Trace through
		// the persisted owner-scoped task, not internal details in this response.
		require.Contains(t, w.Body.String(), "server_error")
		require.NotContains(t, w.Body.String(), accepted.TaskID)
		require.NotContains(t, w.Body.String(), accepted.GetUpstreamTaskID())
		require.NotContains(t, w.Body.String(), "query response required")
		require.Equal(t, user.Id, accepted.UserId)
		require.Equal(t, token.Id, accepted.PrivateData.TokenId)
		require.True(t, accepted.PrivateData.SettlementIncomplete)
		require.NotNil(t, accepted.PrivateData.Execution)
		require.Equal(t, "/vendor/b3b/jobs", accepted.PrivateData.Execution.RequestPath)
		require.Equal(t, "request-phase-two", accepted.PrivateData.Execution.RequestID)
		if subFunding {
			require.Equal(t, sub.Id, accepted.PrivateData.SubscriptionId)
			require.Equal(t, "subscription", accepted.PrivateData.BillingSource)
		} else {
			require.Equal(t, "wallet", accepted.PrivateData.BillingSource)
		}
		require.Equal(t, 1500, <-reserves, "actual submit observes frozen pre-consume")
		require.NotNil(t, accepted.PrivateData.BillingContext.IdentityQuote)
		require.Equal(t, uint64(31), accepted.PrivateData.BillingContext.IdentityQuote.Ratios.Revision)
		require.Zero(t, accepted.Quota, "no successful settlement, not a final free price")
		assertRefund := func() bool {
			var u model.User
			var tok model.Token
			var s model.UserSubscription
			if db.First(&u, user.Id).Error != nil || db.First(&tok, token.Id).Error != nil {
				return false
			}
			if subFunding && (db.First(&s, sub.Id).Error != nil || s.AmountUsed != 0) {
				return false
			}
			return u.Quota == 10000000 && u.UsedQuota == 0 && tok.RemainQuota == 10000000 && tok.UsedQuota == 0
		}
		require.Eventually(t, assertRefund, 3*time.Second, 10*time.Millisecond)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
		require.NoError(t, model.InitDB())
		db = model.DB
		model.LOG_DB = db
		read := gin.New()
		read.GET("/v1/video/generations/:task_id", auth, middleware.Distribute(), controller.RelayTaskFetch)
		service.GetTaskAdaptorFunc = func(p constant.TaskPlatform) service.TaskPollingAdaptor { return relay.GetTaskAdaptor(p) }
		for i := 0; i < 2; i++ {
			r := httptest.NewRequest("GET", "/v1/video/generations/"+accepted.TaskID, nil)
			r.Header.Set("Authorization", "Bearer sk-b3blocal")
			rw := httptest.NewRecorder()
			read.ServeHTTP(rw, r)
			require.Equal(t, 200, rw.Code, rw.Body.String())
			require.Contains(t, rw.Body.String(), "SUCCESS")
			require.Contains(t, rw.Body.String(), "settlement incomplete")
			service.RunTaskPollingOnce(context.Background(), nil)
		}
		otherUser := model.User{Username: "other-owner", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "other", Quota: 10000000}
		require.NoError(t, db.Create(&otherUser).Error)
		otherToken := model.Token{UserId: otherUser.Id, Key: "otherlocal", Name: "other", Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 10000000}
		require.NoError(t, db.Create(&otherToken).Error)
		denied := httptest.NewRequest("GET", "/v1/video/generations/"+accepted.TaskID, nil)
		denied.Header.Set("Authorization", "Bearer sk-otherlocal")
		deniedResponse := httptest.NewRecorder()
		read.ServeHTTP(deniedResponse, denied)
		require.NotEqual(t, 200, deniedResponse.Code)
		require.NotContains(t, deniedResponse.Body.String(), "settlement incomplete")
		require.NotContains(t, deniedResponse.Body.String(), "remote-b3b")
		require.NoError(t, db.First(&accepted).Error)
		require.True(t, accepted.PrivateData.SettlementIncomplete, "marker survives DB reopen")
		var taskCount int64
		require.NoError(t, db.Model(&model.Task{}).Count(&taskCount).Error)
		require.Equal(t, int64(1), taskCount)
		service.RecalculateTaskQuota(context.Background(), &accepted, 6000, "must not auto settle")
		require.True(t, assertRefund())
		require.Equal(t, int64(1), attempts.Load())
		require.Zero(t, pollCalls.Load())
		var logs []model.Log
		require.NoError(t, db.Where("type IN ?", []int{model.LogTypeConsume, model.LogTypeRefund}).Find(&logs).Error)
		require.Empty(t, logs, "existing billing-session refund precedes consumption; no invented settlement log")
		t.Logf("accepted=%s upstream=%s status=%s error=%s refunded=true", accepted.TaskID, accepted.GetUpstreamTaskID(), accepted.Status, accepted.FailReason)
		return
	}
	require.Equal(t, 200, w.Code, w.Body.String())
	if strings.Contains(kind, "expression") {
		estimate := 1500
		if legacy {
			estimate = 1000 // Proven pre-Q2 legacy control; no identity S*D.
		}
		if kind == "expression-tool-zero-base" {
			estimate = 0
		}
		require.Equal(t, estimate, <-reserves, "observed Token pre-consume at actual upstream submit")
	}
	var task model.Task
	require.NoError(t, db.First(&task).Error)
	require.Equal(t, wantReserve, task.Quota, "base 2/2*500000 multiplied by frozen S=3 D=.5")
	if kind == "retry" || kind == "retry-auto" {
		wantAttempts := int64(3)
		if kind == "retry-auto" {
			wantAttempts = 4
			require.Equal(t, 750000, <-reserves)
			require.Equal(t, "pro", task.Group)
		}
		require.Equal(t, wantAttempts, attempts.Load())
		require.Equal(t, 750000, <-reserves)
		require.Equal(t, 750000, <-reserves)
		require.Equal(t, wantReserve, <-reserves)
	}
	require.NotNil(t, task.PrivateData.BillingContext)
	if legacy {
		require.Nil(t, task.PrivateData.BillingContext.IdentityQuote)
	} else {
		require.NotNil(t, task.PrivateData.BillingContext.IdentityQuote)
		require.Equal(t, "Friend", task.PrivateData.BillingContext.IdentityQuote.Ratios.Identity)
	}
	if subFunding {
		require.Equal(t, sub.Id, task.PrivateData.SubscriptionId)
	}
	publicTaskID := task.TaskID
	// Discard request and task objects and close DB: terminal cannot use RelayInfo.
	req = nil
	outer = nil
	task = model.Task{}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, model.InitDB())
	db = model.DB
	model.LOG_DB = db
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"group": "revoked", "setting": `{"billing_preference":"wallet_only"}`}).Error)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"model":91}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":17}`))
	common.QuotaPerUnit = 900000
	if strings.Contains(kind, "expression") {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": `{"model":"999"}`}))
	}
	cfg.Revision = 32
	cfg.ServiceDefaults["default"] = 99
	snapshot, err = identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	current.Store(snapshot)
	if strings.Contains(kind, "missing-channel") {
		require.NoError(t, db.Delete(&model.Channel{}, ch.Id).Error)
	}
	if kind == "failure-null-id" {
		var malformed model.Task
		require.NoError(t, db.First(&malformed).Error)
		malformed.TaskID = ""
		malformed.PrivateData.UpstreamTaskID = ""
		require.NoError(t, db.Save(&malformed).Error)
	}
	service.GetTaskAdaptorFunc = func(p constant.TaskPlatform) service.TaskPollingAdaptor { return relay.GetTaskAdaptor(p) }
	if strings.HasPrefix(kind, "live-") {
		// Exercise the production Gemini/Vertex historical live-fetch consumer using
		// the same loopback JS adaptor and the persisted, already admitted task.
		channelType := constant.ChannelTypeGemini
		if kind == "live-vertex" {
			channelType = constant.ChannelTypeVertexAi
		}
		require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("type", channelType).Error)
		read := gin.New()
		read.GET("/v1/video/generations/:task_id", auth, middleware.Distribute(), controller.RelayTaskFetch)
		request := httptest.NewRequest("GET", "/v1/video/generations/"+publicTaskID, nil)
		request.Header.Set("Authorization", "Bearer sk-b3blocal")
		response := httptest.NewRecorder()
		read.ServeHTTP(response, request)
		require.Equal(t, 200, response.Code, response.Body.String())
		if kind == "live-cas-loser" {
			require.Contains(t, response.Body.String(), "FAILURE", "CAS loser must return persisted winner, not its rejected SUCCESS")
		}
		polls := pollCalls.Load()
		for i := 0; i < 2; i++ {
			request = httptest.NewRequest("GET", "/v1/video/generations/"+publicTaskID, nil)
			request.Header.Set("Authorization", "Bearer sk-b3blocal")
			response = httptest.NewRecorder()
			read.ServeHTTP(response, request)
			require.Equal(t, 200, response.Code, response.Body.String())
		}
		require.Equal(t, polls, pollCalls.Load(), "terminal reads must not hit upstream")
	} else {
		service.RunTaskPollingOnce(context.Background(), nil)
	}
	require.NoError(t, db.First(&task).Error)
	if strings.Contains(kind, "failure") || kind == "live-cas-loser" {
		require.Equal(t, model.TaskStatus(model.TaskStatusFailure), task.Status)
	} else {
		require.Equal(t, model.TaskStatus(model.TaskStatusSuccess), task.Status)
	}
	require.Equal(t, wantFinal, task.Quota)
	if strings.Contains(kind, "immediate") {
		require.Zero(t, pollCalls.Load(), "durable immediate terminal never polls")
	}
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	if subFunding {
		require.NoError(t, db.First(&sub, sub.Id).Error)
		require.Equal(t, int64(wantFinal), sub.AmountUsed)
		require.Equal(t, 10000000, user.Quota)
	} else {
		require.Equal(t, 10000000-wantFinal, user.Quota)
	}
	require.Equal(t, 10000000-wantFinal, token.RemainQuota)
	require.Equal(t, wantFinal, token.UsedQuota)
	require.Equal(t, wantFinal, user.UsedQuota)
	if kind == "legacy-expression-immediate-hook-error" {
		require.Equal(t, int64(1), attempts.Load())
		var logs []model.Log
		require.NoError(t, db.Where("type IN ?", []int{model.LogTypeConsume, model.LogTypeRefund}).Find(&logs).Error)
		netQuota := 0
		for _, entry := range logs {
			if entry.Type == model.LogTypeRefund {
				netQuota -= entry.Quota
			} else {
				netQuota += entry.Quota
			}
		}
		require.Equal(t, 1000, netQuota, "legacy baseline ledger must remain unchanged")
	}
	if !legacy {
		var logs []model.Log
		require.NoError(t, db.Where("user_id = ? AND type IN ?", user.Id, []int{model.LogTypeConsume, model.LogTypeRefund}).Find(&logs).Error)
		require.NotEmpty(t, logs)
		netQuota := 0
		for _, entry := range logs {
			if entry.Type == model.LogTypeRefund {
				netQuota -= entry.Quota
			} else {
				netQuota += entry.Quota
			}
			var other map[string]any
			require.NoError(t, json.Unmarshal([]byte(entry.Other), &other))
			quote, ok := other["identity_billing_quote"].(map[string]any)
			require.True(t, ok, entry.Other)
			ratios := quote["ratios"].(map[string]any)
			require.Equal(t, float64(31), ratios["revision"])
			require.Equal(t, "Friend", ratios["identity"])
		}
		require.Equal(t, wantFinal, netQuota, "consume minus refund log quota")
	}
	service.RunTaskPollingOnce(context.Background(), nil)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.Equal(t, wantFinal, token.UsedQuota)
	require.NoError(t, db.First(&user, user.Id).Error)
	if subFunding {
		require.NoError(t, db.First(&sub, sub.Id).Error)
		require.Equal(t, int64(wantFinal), sub.AmountUsed)
		require.Equal(t, 10000000, user.Quota)
	} else {
		require.Equal(t, 10000000-wantFinal, user.Quota)
	}
}
