package controller

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
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"sync/atomic"
)

// A real MJ HTTP submit, persisted row, DB close/reopen, and the scheduler's
// actual poll consumer. Funding/token refunds are not mocked or called directly.
func TestIdentityB3bMidjourneyDurableConsumer(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3B_MJ_FIXTURE")
	if fixture == "" {
		for _, funding := range []string{"wallet", "subscription"} {
			for _, kind := range []string{"success", "failure", "failure-no-reason", "failure-missing-channel", "failure-null-id", "nonbill-inpaint", "nonbill-customzoom", "submit-rejected", "submit-http-error", "submit-malformed", "free-S", "free-D", "free-B", "unsupported-expression", "legacy-success", "legacy-failure"} {
				if funding == "subscription" && strings.HasPrefix(kind, "legacy") {
					continue
				} // Legacy MJ never supported subscriptions.
				t.Run(funding+"/"+kind, func(t *testing.T) {
					cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3bMidjourneyDurableConsumer$", "-test.count=1", "-test.timeout=100s")
					cmd.Env = append(os.Environ(), "IDENTITY_B3B_MJ_FIXTURE="+t.TempDir(), "IDENTITY_B3B_CASE="+kind, "IDENTITY_B3B_FUNDING="+funding)
					out, err := cmd.CombinedOutput()
					t.Log(string(out))
					require.NoError(t, err)
				})
			}
		}
		return
	}
	kind := os.Getenv("IDENTITY_B3B_CASE")
	subscription := os.Getenv("IDENTITY_B3B_FUNDING") == "subscription"
	legacy := strings.HasPrefix(kind, "legacy")
	nonbill := strings.HasPrefix(kind, "nonbill-")
	submitError := strings.HasPrefix(kind, "submit-")
	modelName, path, body := "mj_imagine", "/mj/submit/imagine", `{"prompt":"bounded local"}`
	if nonbill {
		modelName = "mj_inpaint"
		custom := "Inpaint"
		if kind == "nonbill-customzoom" {
			modelName = service.CovertMjpActionToModelName(constant.MjActionCustomZoom)
			custom = "CustomZoom"
		}
		path = "/mj/submit/action"
		body = fmt.Sprintf(`{"taskId":"mj-origin","customId":"MJ::%s::fixture"}`, custom)
	}
	want := 750
	if legacy {
		want = 500
	}
	if strings.HasPrefix(kind, "free-") {
		want = 0
	}
	if nonbill || submitError {
		want = 0
	}
	final := want
	if strings.Contains(kind, "failure") {
		final = 0
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "mj.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Midjourney{}, &model.Log{}, &model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(fmt.Sprintf(`{"%s":0.001}`, modelName)))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 51
	cfg.IdentityDefaults = map[string]float64{"Friend": 0.5}
	cfg.ServiceDefaults = map[string]float64{"default": 3}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {modelName: {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{modelName: {Mode: "public"}}
	if kind == "free-S" {
		cfg.ServiceDefaults["default"] = 0
	}
	if kind == "free-D" {
		cfg.IdentityDefaults["Friend"] = 0
	}
	if kind == "free-B" {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"mj_imagine":0}`))
	}
	if kind == "unsupported-expression" {
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"mj_imagine":"tiered_expr"}`, "billing_setting.billing_expr": `{"mj_imagine":"1"}`}))
	}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	auth := middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot {
		if legacy {
			return nil
		}
		return snapshot
	})
	user := model.User{Username: "mjdurable", AffCode: "mjdurable", Group: "Friend", Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Quota: 1000000, Setting: `{"billing_preference":"wallet_only"}`}
	if subscription {
		user.Setting = `{"billing_preference":"subscription_only"}`
	}
	require.NoError(t, db.Create(&user).Error)
	sub := model.UserSubscription{}
	if subscription {
		plan := model.SubscriptionPlan{Title: "MJ local", Enabled: true}
		require.NoError(t, db.Create(&plan).Error)
		sub = model.UserSubscription{UserId: user.Id, PlanId: plan.Id, AmountTotal: 1000000, Status: "active", StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix()}
		require.NoError(t, db.Create(&sub).Error)
	}
	token := model.Token{UserId: user.Id, Key: "mjdurable", Name: "mjdurable", Group: "default", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 1000000}
	require.NoError(t, db.Create(&token).Error)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case path:
			if kind == "submit-http-error" {
				http.Error(w, "bounded upstream error", 500)
				return
			}
			if kind == "submit-malformed" {
				fmt.Fprint(w, `not-json`)
				return
			}
			if kind == "submit-rejected" {
				fmt.Fprint(w, `{"code":23,"description":"bounded queue full","result":"mj-local-durable"}`)
				return
			}
			fmt.Fprint(w, `{"code":1,"result":"mj-local-durable"}`)
		case "/mj/task/list-by-condition":
			status, reason := "SUCCESS", ""
			progress := "100%"
			if strings.Contains(kind, "failure") {
				status = "FAILURE"
				reason = "fixture failure"
			}
			if kind == "failure-no-reason" {
				status = "FAILURE"
				reason = ""
				progress = ""
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "mj-local-durable", "status": status, "progress": progress, "failReason": reason, "submitTime": time.Now().UnixMilli(), "finishTime": time.Now().UnixMilli()}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	ch := model.Channel{Name: "mj local", Type: constant.ChannelTypeMidjourney, Key: "local-only", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Models: modelName, Group: "default"}
	require.NoError(t, ch.Insert())
	if nonbill {
		require.NoError(t, db.Create(&model.Midjourney{UserId: user.Id, MjId: "mj-origin", Status: "SUCCESS", Progress: "100%", ChannelId: ch.Id}).Error)
	}
	engine := gin.New()
	engine.Use(middleware.RequestId())
	engine.POST(path, auth, middleware.Distribute(), RelayMidjourney)
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-mjdurable")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "mj-b3b")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if kind == "unsupported-expression" {
		require.Contains(t, w.Body.String(), "does not support usage expression")
		require.Zero(t, calls.Load())
		var count int64
		require.NoError(t, db.Model(&model.Midjourney{}).Count(&count).Error)
		require.Zero(t, count)
		return
	}
	if submitError {
		require.Eventually(t, func() bool {
			var u model.User
			var tok model.Token
			var s model.UserSubscription
			if db.First(&u, user.Id).Error != nil || db.First(&tok, token.Id).Error != nil {
				return false
			}
			if subscription && (db.First(&s, sub.Id).Error != nil || s.AmountUsed != 0) {
				return false
			}
			return u.Quota == 1000000 && tok.RemainQuota == 1000000 && tok.UsedQuota == 0
		}, 3*time.Second, 10*time.Millisecond, "failed submit must refund original funding and Token")
		var rows []model.Midjourney
		require.NoError(t, db.Find(&rows).Error)
		for _, row := range rows {
			require.Zero(t, row.Quota)
		}
		require.Positive(t, calls.Load())
		return
	}
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "mj-local-durable")
	var task model.Midjourney
	require.NoError(t, db.Where("mj_id = ?", "mj-local-durable").First(&task).Error)
	require.Equal(t, want, task.Quota)
	if !legacy {
		require.NotNil(t, task.PrivateData.BillingContext)
		require.NotNil(t, task.PrivateData.BillingContext.IdentityQuote)
		require.Equal(t, "Friend", task.PrivateData.BillingContext.IdentityQuote.Ratios.Identity)
		if nonbill {
			require.Equal(t, "not_billed", task.PrivateData.BillingSource)
		}
		if subscription && !nonbill {
			require.Equal(t, sub.Id, task.PrivateData.SubscriptionId)
		}
	}
	task = model.Midjourney{}
	req = nil
	engine = nil
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	require.NoError(t, model.InitDB())
	db = model.DB
	model.LOG_DB = db
	require.NoError(t, db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]any{"group": "revoked", "setting": `{"billing_preference":"wallet_only"}`}).Error)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"mj_imagine":99}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":99}`))
	snapshot = nil
	common.QuotaPerUnit = 900000
	if kind == "failure-missing-channel" {
		require.NoError(t, db.Delete(&model.Channel{}, ch.Id).Error)
	}
	if kind == "failure-null-id" {
		require.NoError(t, db.Model(&model.Midjourney{}).Where("mj_id = ?", "mj-local-durable").Update("mj_id", "").Error)
	}
	runMidjourneyTaskUpdateOnce(context.Background(), nil)
	queryID := "mj-local-durable"
	if kind == "failure-null-id" {
		queryID = ""
	}
	require.NoError(t, db.Where("mj_id = ?", queryID).First(&task).Error)
	require.Equal(t, final, task.Quota)
	require.Equal(t, "100%", task.Progress)
	require.NoError(t, db.First(&user, user.Id).Error)
	require.NoError(t, db.First(&token, token.Id).Error)
	if subscription {
		require.NoError(t, db.First(&sub, sub.Id).Error)
		require.Equal(t, int64(final), sub.AmountUsed)
		require.Equal(t, 1000000, user.Quota)
	} else {
		require.Equal(t, 1000000-final, user.Quota)
	}
	require.Equal(t, 1000000-final, token.RemainQuota)
	require.Equal(t, final, token.UsedQuota)
	runMidjourneyTaskUpdateOnce(context.Background(), nil)
	require.NoError(t, db.First(&token, token.Id).Error)
	require.Equal(t, final, token.UsedQuota)
	require.NoError(t, db.First(&user, user.Id).Error)
	if subscription {
		require.NoError(t, db.First(&sub, sub.Id).Error)
		require.Equal(t, int64(final), sub.AmountUsed)
		require.Equal(t, 1000000, user.Quota)
	} else {
		require.Equal(t, 1000000-final, user.Quota)
	}
}
