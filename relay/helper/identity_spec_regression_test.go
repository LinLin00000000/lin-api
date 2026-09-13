package helper_test

import (
	"bytes"
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
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/relay/channel/ali"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Isolated real admission -> mapped Ali converter -> reserve/settlement -> both ledgers.
func TestIdentityB3aSpecRegressions(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3A_SPEC_FIX")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3aSpecRegressions$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "IDENTITY_B3A_SPEC_FIX="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "spec.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 77
	cfg.ServiceDefaults = map[string]float64{"default": 3}
	cfg.IdentityDefaults = map[string]float64{"ordinary": 0.5}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
	for _, name := range []string{"z-image-physical", "plain-image", "zero-fixed", "zero-cache", "tiny-cache", "zero-tools", "expr"} {
		cfg.ServiceModels["default"][name] = identityservice.ServiceModel{Enabled: true}
		cfg.ModelIdentityScopes[name] = identityservice.ModelIdentityScope{Mode: "public"}
	}
	snap, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "local", QuotaResetPeriod: "never"}
	require.NoError(t, db.Create(&plan).Error)
	seq := 0
	for _, kind := range []string{"z-image-physical", "plain-image", "zero-fixed", "zero-cache", "tiny-cache", "zero-tools"} {
		for _, pref := range []string{"wallet_only", "subscription_only"} {
			t.Run(kind+"/"+pref, func(t *testing.T) {
				common.QuotaPerUnit = 500000
				common.PreConsumedQuota = 10
				operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = true
				operation_setting.LoadToolPricesFromJSONString(`{"web_search":10}`)
				require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"z-image-physical":0.01,"plain-image":0.01,"zero-fixed":0}`))
				require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"zero-cache":2,"tiny-cache":2,"zero-tools":0}`))
				require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"zero-cache":0,"tiny-cache":0.000001}`))
				seq++
				name := fmt.Sprintf("spec%d", seq)
				u := model.User{Username: name, AffCode: name, Quota: 1000000, Status: common.UserStatusEnabled}
				require.NoError(t, db.Create(&u).Error)
				k := model.Token{UserId: u.Id, Key: name, RemainQuota: 1000000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
				require.NoError(t, db.Create(&k).Error)
				sub := model.UserSubscription{UserId: u.Id, PlanId: plan.Id, AmountTotal: 1000000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}
				require.NoError(t, db.Create(&sub).Error)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
				common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
				service.FreezeIdentityRequest(c, snap, u.Id, "ordinary")
				info := &relaycommon.RelayInfo{OriginModelName: kind, UserId: u.Id, UserGroup: "ordinary", UsingGroup: "default", TokenId: k.Id, TokenKey: k.Key, TokenGroup: "default", RequestId: name, ChannelMeta: &relaycommon.ChannelMeta{}, StartTime: time.Now(), UserSetting: dto.UserSetting{BillingPreference: pref}}
				meta := &types.TokenCountMeta{MaxTokens: 20}
				image := strings.Contains(kind, "image")
				wantReserve, want := 90, 0
				if kind == "zero-fixed" || kind == "zero-tools" {
					wantReserve = 0
				}
				if kind == "tiny-cache" {
					want = 1
				}
				if kind == "zero-tools" {
					want = 7500
				}
				if image {
					n := uint(1)
					info.Request = &dto.ImageRequest{Model: kind, N: &n, Prompt: "local", Extra: map[string]json.RawMessage{"parameters": json.RawMessage(`{"n":3,"prompt_extend":true}`)}}
					info.RelayMode = relayconstant.RelayModeImagesGenerations
					meta.BillingRatios = map[string]float64{"n": 1}
					want = 22500
					if kind == "z-image-physical" {
						want = 45000
					}
					wantReserve = want
				}
				price, err := helper.ModelPriceHelper(c, info, 10, meta)
				require.NoError(t, err)
				assert.Equal(t, wantReserve, price.QuotaToPreConsume, "admitted estimate")
				require.Nil(t, service.PreConsumeBilling(c, price.QuotaToPreConsume, info))
				readReserve := func() {
					var tk model.Token
					var us model.User
					var su model.UserSubscription
					require.NoError(t, db.First(&tk, k.Id).Error)
					require.NoError(t, db.First(&us, u.Id).Error)
					require.NoError(t, db.First(&su, sub.Id).Error)
					assert.Equal(t, wantReserve, 1000000-tk.RemainQuota, "token reserve")
					if pref == "wallet_only" {
						assert.Equal(t, wantReserve, 1000000-us.Quota)
						assert.Zero(t, su.AmountUsed)
					} else {
						assert.EqualValues(t, wantReserve, su.AmountUsed)
						assert.Equal(t, 1000000, us.Quota)
					}
				}
				readReserve()
				if pref == "subscription_only" && (kind == "zero-fixed" || kind == "zero-tools") {
					// The zero reserve still binds a real subscription and receipt;
					// retry is idempotent, negative/legacy-zero reserve remain invalid.
					res, err := model.PreConsumeUserSubscriptionAllowZero(name, u.Id, kind, 0, 0)
					require.NoError(t, err)
					require.Zero(t, res.PreConsumed)
					require.Equal(t, sub.Id, res.UserSubscriptionId)
					_, err = model.PreConsumeUserSubscription(name+"-legacy", u.Id, kind, 0, 0)
					require.Error(t, err)
					_, err = model.PreConsumeUserSubscriptionAllowZero(name+"-negative", u.Id, kind, 0, -1)
					require.Error(t, err)
					readReserve()
				}
				if image {
					var calls atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						attempt := calls.Add(1)
						var payload ali.AliImageRequest
						if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Parameters.N != 3 || !payload.Parameters.PromptExtendValue() {
							w.WriteHeader(400)
							return
						}
						var tk model.Token
						var us model.User
						var su model.UserSubscription
						if db.First(&tk, k.Id).Error != nil || db.First(&us, u.Id).Error != nil || db.First(&su, sub.Id).Error != nil || 1000000-tk.RemainQuota != wantReserve || (pref == "wallet_only" && 1000000-us.Quota != wantReserve) || (pref == "subscription_only" && su.AmountUsed != int64(wantReserve)) {
							w.WriteHeader(409)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						if attempt == 1 {
							w.WriteHeader(500)
							fmt.Fprint(w, `{"error":{"message":"retry local"}}`)
							return
						}
						fmt.Fprint(w, `{"output":{"results":[{"url":"https://invalid.example/local-only"}]},"usage":{"image_count":3}}`)
					}))
					defer upstream.Close()
					info.ChannelBaseUrl = upstream.URL
					info.ApiKey = "local-only"
					// Same physical request, two differently translated channel attempts.
					for _, mapped := range []string{"z-image-turbo", "qwen-image-plus"} {
						require.Nil(t, service.PrepareIdentityBillingForSelectedGroup(c, info))
						body := *info.Request.(*dto.ImageRequest)
						mapping, _ := json.Marshal(map[string]string{kind: mapped})
						c.Set("model_mapping", string(mapping))
						require.NoError(t, helper.ModelMappedHelper(c, info, &body))
						require.Equal(t, mapped, body.Model)
						adaptor := &ali.Adaptor{IsSyncImageModel: true}
						converted, err := adaptor.ConvertImageRequest(c, info, body)
						require.NoError(t, err)
						require.Equal(t, 3, converted.(*ali.AliImageRequest).Parameters.N)
						require.True(t, converted.(*ali.AliImageRequest).Parameters.PromptExtendValue())
						assert.Equal(t, float64(want), info.PriceData.ApplyOtherRatiosToFloat(info.PriceData.ModelPrice*500000*1.5), "converter must not change admitted charge")
						readReserve()
						data, err := json.Marshal(converted)
						require.NoError(t, err)
						response, err := adaptor.DoRequest(c, info, bytes.NewReader(data))
						require.NoError(t, err)
						resp := response.(*http.Response)
						if calls.Load() == 1 {
							require.Equal(t, 500, resp.StatusCode)
							resp.Body.Close()
						} else {
							require.Equal(t, 200, resp.StatusCode)
							_, apiErr := adaptor.DoResponse(c, resp, info)
							require.Nil(t, apiErr)
						}
					}
					require.EqualValues(t, 2, calls.Load())
					// A post-conversion mutation is not an authority to reprice the request.
					info.PriceData.AddOtherRatio("prompt_extend", 9)
				}
				usage := &dto.Usage{PromptTokens: 100, TotalTokens: 100}
				if kind == "zero-cache" || kind == "tiny-cache" {
					usage.PromptTokensDetails.CachedTokens = 100
				}
				if kind == "zero-tools" {
					c.Set("claude_web_search_requests", 1)
				}
				service.PostTextConsumeQuota(c, info, usage, nil)
				var tk model.Token
				var us model.User
				var su model.UserSubscription
				require.NoError(t, db.First(&tk, k.Id).Error)
				require.NoError(t, db.First(&us, u.Id).Error)
				require.NoError(t, db.First(&su, sub.Id).Error)
				assert.Equal(t, want, 1000000-tk.RemainQuota)
				assert.Equal(t, want, tk.UsedQuota)
				if pref == "wallet_only" {
					assert.Equal(t, want, 1000000-us.Quota)
					assert.Zero(t, su.AmountUsed)
				} else {
					assert.EqualValues(t, want, su.AmountUsed)
					assert.Equal(t, 1000000, us.Quota)
				}
			})
		}
	}
	t.Run("activation", func(t *testing.T) {
		fee := model_setting.GetGrokSettings()
		fee.ViolationDeductionEnabled = true
		fee.ViolationDeductionAmount = 0.01
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":7}`))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"zero-fixed":0,"zero-tools":0,"plain-image":0.01}`))
		require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"expr":"tiered_expr"}`, "billing_setting.billing_expr": `{"expr":"p * 0"}`}))
		// All configured surcharge components explicitly zero, not just B(call).
		operation_setting.LoadToolPricesFromJSONString(`{"web_search":0,"web_search_preview":0,"file_search":0,"google_search":0,"image_generation":0,"web_search:zero-tools*":10}`)
		report := service.IdentityActivationBlockers(cfg)
		has := func(code, name string) bool {
			for _, s := range report {
				if strings.Contains(s, code) && strings.Contains(s, `model="`+name+`"`) {
					return true
				}
			}
			return false
		}
		assert.True(t, has("violation_fee_free_conflict", "zero-fixed"), "%v", report)
		assert.False(t, has("violation_fee_free_conflict", "zero-tools"), "base zero with priced tools is not universally free")
		assert.True(t, has("violation_fee_free_unverified", "zero-tools"), "%v", report)
		assert.True(t, has("violation_fee_free_unverified", "expr"), "%v", report)
		assert.False(t, has("violation_fee_free_conflict", "plain-image"))
	})
	t.Run("image-parameters-and-legacy", func(t *testing.T) {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"z-image-physical":0.01}`))
		for _, raw := range []string{`{"n":-1}`, `{"n":999999}`, `{"n":1.5}`, `{"prompt_extend":"true"}`} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			service.FreezeIdentityRequest(c, snap, 1, "ordinary")
			info := &relaycommon.RelayInfo{OriginModelName: "z-image-physical", UsingGroup: "default", ChannelMeta: &relaycommon.ChannelMeta{}, Request: &dto.ImageRequest{Extra: map[string]json.RawMessage{"parameters": json.RawMessage(raw)}}}
			_, err := helper.ModelPriceHelper(c, info, 1, &types.TokenCountMeta{})
			require.Error(t, err)
			require.Nil(t, info.IdentityBilling)
			require.Nil(t, info.Billing)
		}
		// The existing legacy converter is still mapping-dependent, intentionally.
		for _, mapped := range []string{"z-image-turbo", "qwen-image-plus"} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			info := &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations, ChannelMeta: &relaycommon.ChannelMeta{UpstreamModelName: mapped}}
			_, err := (&ali.Adaptor{}).ConvertImageRequest(c, info, dto.ImageRequest{Model: mapped, Extra: map[string]json.RawMessage{"parameters": json.RawMessage(`{"n":3,"prompt_extend":true}`)}})
			require.NoError(t, err)
			want := float64(3)
			if strings.Contains(mapped, "z-image") {
				want = 6
			}
			require.Equal(t, want, info.PriceData.OtherRatioMultiplier())
		}
	})
}
