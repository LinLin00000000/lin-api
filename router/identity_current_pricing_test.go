package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIdentityC2CurrentPricingHTTP(t *testing.T) {
	fixture := os.Getenv("IDENTITY_C2_HTTP_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityC2CurrentPricingHTTP$", "-test.count=1", "-test.v", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_C2_HTTP_FIXTURE="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	require.Empty(t, os.Getenv("LOG_SQL_DSN"))
	common.SQLitePath = filepath.Join(fixture, "quote.db")
	common.IsMasterNode = false
	require.NoError(t, model.InitDB())
	db := model.DB
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Log{}, &model.Model{}, &model.Vendor{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":7,"pro":9}`))
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"token":2,"private":2,"closed":2,"orphan":2}`))
	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"token":3}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixed":0.01}`))
	require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"token":0.5}`))
	require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"token":1.25}`))
	require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"token":4}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"token":5}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"token":6}`))
	operation_setting.LoadToolPricesFromJSONString(`{"web_search":10}`)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"expr":"tiered_expr"}`, "billing_setting.billing_expr": `{"expr":"p * 4 + c * 8"}`}))
	users := map[string]model.User{}
	for _, actor := range []string{"ordinary", "Friend", "root", "admin", "disabled"} {
		role := common.RoleCommonUser
		if actor == "root" {
			role = common.RoleRootUser
		}
		if actor == "admin" {
			role = common.RoleAdminUser
		}
		status := common.UserStatusEnabled
		if actor == "disabled" {
			status = common.UserStatusDisabled
		}
		token := "synthetic-c2-" + actor
		u := model.User{Username: "c2-" + actor, Group: actor, Role: role, Status: status, AccessToken: &token, AuthVersion: 1, AffCode: "c2-" + actor}
		require.NoError(t, db.Create(&u).Error)
		users[actor] = u
	}
	ch := model.Channel{Type: constant.ChannelTypeOpenAI, Name: "quote-only-never-contacted", Status: common.ChannelStatusEnabled, Key: "synthetic", Group: "default,pro", Models: "token,fixed,private,closed,unknown,expr"}
	require.NoError(t, ch.Insert())
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 3
	cfg.ServiceDefaults = map[string]float64{"default": 2, "pro": 4, "unavailable": 1}
	cfg.IdentityDefaults = map[string]float64{"ordinary": 0.5, "Friend": 0}
	for _, g := range []string{"default", "pro", "unavailable"} {
		cfg.ServiceModels[g] = map[string]identityservice.ServiceModel{}
		for _, m := range []string{"token", "fixed", "private", "closed", "orphan", "unknown", "expr"} {
			cfg.ServiceModels[g][m] = identityservice.ServiceModel{Enabled: m != "closed"}
			cfg.ModelIdentityScopes[m] = identityservice.Scope{Mode: "public"}
		}
	}
	cfg.ModelIdentityScopes["private"] = identityservice.Scope{Mode: "restricted", Identities: []string{"Friend"}}
	var current atomic.Pointer[identityservice.Snapshot]
	install := func() { s, e := identityservice.NewSnapshot(cfg); require.NoError(t, e); current.Store(s) }
	install()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/api/pricing/current", middleware.UserAuth(), controller.GetCurrentIdentityPricingWithSnapshot(current.Load))
	e.GET("/api/pricing", middleware.UserAuth(), controller.GetPricing)
	registerIdentityServiceRoutes(e.Group("/api/option", middleware.RootAuth()))
	server := httptest.NewServer(e)
	defer server.Close()
	call := func(path, actor string) (int, map[string]json.RawMessage) {
		t.Helper()
		req, er := http.NewRequest("GET", server.URL+path, nil)
		require.NoError(t, er)
		if actor != "" {
			req.Header.Set("Authorization", "Bearer synthetic-c2-"+actor)
		}
		resp, er := server.Client().Do(req)
		require.NoError(t, er)
		defer resp.Body.Close()
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return resp.StatusCode, body
	}
	path := func(m, g string) string {
		return "/api/pricing/current?model=" + url.QueryEscape(m) + "&service=" + url.QueryEscape(g)
	}
	quote := func(actor, m, g string) helper.CurrentQuote {
		t.Helper()
		code, body := call(path(m, g), actor)
		require.Equal(t, 200, code, body)
		var out helper.CurrentQuote
		require.NoError(t, json.Unmarshal(body["data"], &out))
		return out
	}
	t.Run("auth_identity_and_exact_catalog_boundaries", func(t *testing.T) {
		code, _ := call(path("token", "default"), "")
		require.Equal(t, 401, code)
		code, _ = call(path("token", "default"), "disabled")
		require.Equal(t, 401, code)
		code, _ = call(path("token", "default")+"&identity=Friend", "ordinary")
		require.Equal(t, 400, code)
		code, _ = call(path("token", "default")+"&model=private", "ordinary")
		require.Equal(t, 400, code)
		for _, x := range []struct{ m, g string }{{"private", "default"}, {"closed", "default"}, {"orphan", "default"}, {"token ", "default"}, {"TOKEN", "default"}, {"token", "unavailable"}, {"token", "ordinary"}} {
			code, body := call(path(x.m, x.g), "ordinary")
			require.Equal(t, 403, code, body)
			require.NotContains(t, body, "data")
		}
		require.Zero(t, quote("Friend", "private", "pro").Quote.Components["input"].Effective)
		code, _ = call(path("unknown", "default"), "ordinary")
		require.Equal(t, 422, code)
	})
	t.Run("actual_helper_components_and_current_base_changes", func(t *testing.T) {
		q := quote("ordinary", "token", "default")
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
		common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
		service.FreezeIdentityRequest(c, current.Load(), users["ordinary"].Id, "ordinary")
		info := &relaycommon.RelayInfo{OriginModelName: "token", UserId: users["ordinary"].Id, UserGroup: "ordinary", UsingGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{}}
		_, er := helper.ModelPriceHelper(c, info, 10, &types.TokenCountMeta{MaxTokens: 20})
		require.NoError(t, er)
		require.Equal(t, info.IdentityBilling.Quote, q.Quote)
		for name, value := range map[string]float64{"input": 2, "output": 6, "cache_read": 1, "cache_write": 2.5, "cache_write_5m": 2.5, "cache_write_1h": 4, "image": 8, "audio_input": 10, "audio_output": 60, "tool/web_search": 10} {
			require.Equal(t, value, q.Quote.Components[name].Base.Value, name)
			require.Equal(t, value, q.Quote.Components[name].Effective, name)
			require.NotEmpty(t, q.Quote.Components[name].Base.Source)
			require.NotEmpty(t, q.Quote.Components[name].Base.Unit)
		}
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"token":4,"private":2}`))
		updated := quote("ordinary", "token", "default")
		require.Equal(t, 4.0, updated.Quote.Components["input"].Effective)
		require.Equal(t, q.Quote.Ratios.Revision, updated.Quote.Ratios.Revision)
		require.Equal(t, 2.0, info.IdentityBilling.Quote.Components["input"].Base.Value)
		require.Equal(t, 0.01, quote("ordinary", "fixed", "default").Quote.Components["call"].Effective)
		expr := quote("ordinary", "expr", "default")
		require.False(t, expr.FinalPriceKnown)
		require.Equal(t, "p * 4 + c * 8", expr.Expression)
		require.Len(t, expr.Quote.Components, 1)
		require.NotContains(t, expr.Quote.Components, "input")
	})
	t.Run("defaults_overrides_delete_zero_one_missing", func(t *testing.T) {
		for _, v := range []float64{0, 1, 3} {
			cfg.Revision++
			cfg.ServiceModels["pro"]["token"] = identityservice.ServiceModel{Enabled: true, Ratio: &v}
			cfg.IdentityModelRatios["ordinary"] = map[string]float64{"token": 1}
			install()
			q := quote("ordinary", "token", "pro").Quote
			require.Equal(t, v, q.Ratios.ServiceFactor.Value)
			require.Contains(t, q.Ratios.ServiceFactor.Source, "service_models")
			require.Contains(t, q.Ratios.IdentityFactor.Source, "identity_model_ratios")
			require.Equal(t, 4*v, q.Components["input"].Effective)
		}
		cfg.ServiceModels["pro"]["token"] = identityservice.ServiceModel{Enabled: true}
		delete(cfg.IdentityModelRatios["ordinary"], "token")
		cfg.Revision++
		install()
		q := quote("ordinary", "token", "pro").Quote
		require.Equal(t, 8.0, q.Components["input"].Effective)
		require.Contains(t, q.Ratios.ServiceFactor.Source, "service_defaults")
		require.Contains(t, q.Ratios.IdentityFactor.Source, "identity_defaults")
		delete(cfg.IdentityDefaults, "ordinary")
		delete(cfg.IdentityModelRatios, "ordinary")
		install()
		code, _ := call(path("token", "pro"), "ordinary")
		require.Equal(t, 403, code)
		cfg.IdentityDefaults["ordinary"] = 0.5
		install()
	})
	t.Run("root_saved_preview_never_activates_or_expands_access", func(t *testing.T) {
		legacy := cfg
		legacy.Mode = identityservice.ModeLegacy
		legacy.Revision = 0
		_, er := model.IdentityServiceSettings.Save(legacy, 0)
		require.NoError(t, er)
		preview := "/api/option/identity_service/quote?model=token&service=pro&identity=Friend"
		for _, actor := range []string{"ordinary", "Friend", "admin"} {
			code, _ := call(preview, actor)
			require.Equal(t, 403, code)
		}
		code, _ := call(preview, "")
		require.Equal(t, 401, code)
		code, body := call(preview, "root")
		require.Equal(t, 200, code, body)
		require.Equal(t, "true", string(body["preview"]))
		var q helper.CurrentQuote
		require.NoError(t, json.Unmarshal(body["data"], &q))
		require.Zero(t, q.Quote.Components["input"].Effective)
		code, _ = call(strings.Replace(preview, "model=token", "model=private", 1)+"&extra=1", "root")
		require.Equal(t, 400, code)
		code, _ = call("/api/option/identity_service/quote?model=private&service=pro&identity=ordinary", "root")
		require.Equal(t, 403, code)
		require.Equal(t, "legacy", model.IdentityServiceSettings.Snapshot().Config().Mode)
		require.ErrorIs(t, identityservice.ValidateForStorage(cfg), identityservice.ErrActivationPending)
		code, body = call("/api/option/identity_service", "root")
		require.Equal(t, 200, code)
		require.Contains(t, string(body["data"]), `"mode":"legacy"`)
	})
	t.Run("legacy_api_remains_pair_compatible", func(t *testing.T) {
		legacy := cfg
		legacy.Mode = identityservice.ModeLegacy
		s, er := identityservice.NewSnapshot(legacy)
		require.NoError(t, er)
		current.Store(s)
		code, body := call(path("token", "pro"), "ordinary")
		require.Equal(t, 200, code)
		require.Equal(t, `"legacy"`, string(body["mode"]))
		require.NotContains(t, body, "data")
		code, body = call("/api/pricing", "ordinary")
		require.Equal(t, 200, code)
		require.Contains(t, string(body["group_ratio"]), `"pro":9`)
	})
	fmt.Println("C2: real loopback HTTP + SQLite; no upstream I/O, no storage activation")
}
