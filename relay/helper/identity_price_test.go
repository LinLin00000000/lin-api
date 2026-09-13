package helper

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Real price helper -> funding reserve -> real usage consumer -> SQLite readback.
// This is not a protocol adaptor test; the router suite covers actual HTTP/SSE.
func TestIdentityB3aBillingFamilies(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3A_FAMILIES")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3aBillingFamilies$", "-test.count=1", "-test.timeout=120s", "-test.v")
		cmd.Env = append(os.Environ(), "IDENTITY_B3A_FAMILIES="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	common.SQLitePath = filepath.Join(fixture, "families.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Log{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro","auto":"Auto"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","pro"]`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":7,"pro":9}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 55
	cfg.ServiceDefaults = map[string]float64{"default": 3, "pro": 5}
	cfg.IdentityDefaults = map[string]float64{"ordinary": 0.5, "Friend": 0}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {}, "pro": {}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
	for _, name := range []string{"token", "fixed", "expression", "audio", "realtime", "tools", "zero-tools", "cache", "gemini-2.5-flash", "refund"} {
		cfg.ServiceModels["default"][name] = identityservice.ServiceModel{Enabled: true}
		cfg.ServiceModels["pro"][name] = identityservice.ServiceModel{Enabled: true}
		cfg.ModelIdentityScopes[name] = identityservice.ModelIdentityScope{Mode: "public"}
	}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "local", QuotaResetPeriod: "never"}
	require.NoError(t, db.Create(&plan).Error)
	sequence := 0
	for _, family := range []string{"token", "fixed", "expression", "audio", "realtime", "tools", "zero-tools", "cache", "gemini-2.5-flash", "refund"} {
		for _, pref := range []string{"wallet_only", "subscription_only"} {
			for _, id := range []string{"ordinary", "Friend"} {
				for _, group := range []string{"default", "pro", "auto", "auto-free", "auto-to-free"} {
					t.Run(family+"/"+pref+"/"+id+"/"+group, func(t *testing.T) {
						sequence++
						name := fmt.Sprintf("family%d", sequence)
						common.QuotaPerUnit = 500000
						common.PreConsumedQuota = 10
						operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = true
						require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"token":2,"audio":2,"realtime":2,"zero-tools":0,"cache":2,"gemini-2.5-flash":2,"tools":2,"refund":2}`))
						require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"token":2,"audio":2,"realtime":2,"zero-tools":0,"cache":2,"gemini-2.5-flash":2,"tools":2,"refund":2}`))
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixed":0.01}`))
						require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"expression":"tiered_expr"}`, "billing_setting.billing_expr": `{"expression":"p * 4 + c * 8"}`}))
						require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"audio":3,"realtime":3}`))
						require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"audio":4,"realtime":4}`))
						require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"cache":0.5}`))
						require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"cache":1.25}`))
						require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"cache":3}`))
						operation_setting.LoadToolPricesFromJSONString(`{"web_search":10}`)
						u := model.User{Username: name, AffCode: name, Quota: 1000000, Status: common.UserStatusEnabled}
						require.NoError(t, db.Create(&u).Error)
						k := model.Token{UserId: u.Id, Key: name, RemainQuota: 1000000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
						require.NoError(t, db.Create(&k).Error)
						sub := model.UserSubscription{UserId: u.Id, PlanId: plan.Id, AmountTotal: 1000000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}
						require.NoError(t, db.Create(&sub).Error)
						c, _ := gin.CreateTestContext(httptest.NewRecorder())
						c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
						tokenGroup, initialGroup := group, group
						admitted := snapshot
						if group == "auto" || group == "auto-free" || group == "auto-to-free" {
							tokenGroup = "auto"
							initialGroup = "default"
							c.Set("auto_group", "default")
							if group != "auto" {
								local := snapshot.Config()
								if group == "auto-free" {
									local.ServiceDefaults["default"] = 0
								} else {
									local.ServiceDefaults["pro"] = 0
								}
								admitted, err = identityservice.NewSnapshot(local)
								require.NoError(t, err)
							}
						}
						common.SetContextKey(c, constant.ContextKeyTokenGroup, tokenGroup)
						service.FreezeIdentityRequest(c, admitted, u.Id, id)
						info := &relaycommon.RelayInfo{OriginModelName: family, UserId: u.Id, UserGroup: id, UsingGroup: initialGroup, TokenId: k.Id, TokenKey: k.Key, TokenGroup: tokenGroup, RequestId: name, ChannelMeta: &relaycommon.ChannelMeta{}, StartTime: time.Now(), UserSetting: dto.UserSetting{BillingPreference: pref}}
						meta := &types.TokenCountMeta{MaxTokens: 20, BillingRatios: map[string]float64{"n": 2}}
						price, err := ModelPriceHelper(c, info, 10, meta)
						require.NoError(t, err)
						require.EqualValues(t, 55, info.IdentityBilling.Quote.Ratios.Revision)
						apiErr := service.PreConsumeBilling(c, price.QuotaToPreConsume, info)
						if apiErr != nil {
							t.Fatal(apiErr.Error())
						}
						var beforeK model.Token
						require.NoError(t, db.First(&beforeK, k.Id).Error)
						if id == "Friend" || group == "auto-free" {
							require.Equal(t, 1000000, beforeK.RemainQuota)
							require.Nil(t, info.Billing)
						} else if family != "zero-tools" {
							require.Less(t, beforeK.RemainQuota, 1000000)
						}
						// Live changes cannot affect the admitted estimator, tool fees or settlement.
						require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"token":99,"audio":99,"tools":99,"refund":99}`))
						require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"token":99,"audio":99,"tools":99,"refund":99}`))
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"fixed":99}`))
						require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"cache":99}`))
						require.NoError(t, ratio_setting.UpdateCreateCacheRatioByJSONString(`{"cache":99}`))
						require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"cache":99}`))
						operation_setting.LoadToolPricesFromJSONString(`{"web_search":99}`)
						require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"audio":99}`))
						require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"audio":99}`))
						require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": `{"expression":"p * 999"}`}))
						common.QuotaPerUnit = 999999
						if tokenGroup == "auto" {
							c.Set("auto_group", "pro")
							require.Nil(t, service.PrepareIdentityBillingForSelectedGroup(c, info))
							require.EqualValues(t, 55, info.IdentityBilling.Quote.Ratios.Revision)
							require.Equal(t, "pro", info.IdentityBilling.Quote.Ratios.Service)
							if id != "Friend" && group != "auto-to-free" {
								require.NotNil(t, info.Billing)
								if family != "zero-tools" {
									require.Positive(t, info.Billing.GetPreConsumedQuota())
								}
							}
						}
						usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
						base := 280
						switch family {
						case "fixed":
							base = 10000
						case "expression":
							base = 280
						case "zero-tools":
							c.Set("claude_web_search_requests", 1)
							base = 5000
						case "cache":
							usage.UsageSemantic = "anthropic"
							usage.PromptTokensDetails.CachedTokens = 20
							usage.PromptTokensDetails.CachedCreationTokens = 12
							usage.ClaudeCacheCreation5mTokens = 4
							usage.ClaudeCacheCreation1hTokens = 8
							usage.PromptTokensDetails.ImageTokens = 10
							// 100 + cache 10 + writes 5+16 + image adjustment 20 + output 40.
							base = 382
						case "gemini-2.5-flash":
							usage.PromptTokensDetails.AudioTokens = 10
							base = 265
						case "tools":
							c.Set("claude_web_search_requests", 1)
							base = 5280
						case "audio", "realtime":
							usage.PromptTokensDetails.TextTokens = 90
							usage.PromptTokensDetails.AudioTokens = 10
							usage.CompletionTokenDetails.TextTokens = 15
							usage.CompletionTokenDetails.AudioTokens = 5
							// Derive expected base from independently fixed input ratios, asserted below.
							require.Equal(t, float64(3), info.PriceData.AudioRatio)
							require.Equal(t, float64(4), info.PriceData.AudioCompletionRatio)
							base = 420
						}
						want := (base*3 + 1) / 2
						if group == "pro" || tokenGroup == "auto" {
							want = (base*5 + 1) / 2
						}
						if id == "Friend" || group == "auto-to-free" || family == "refund" {
							want = 0
						}
						if family == "refund" {
							if info.Billing != nil {
								info.Billing.Refund(c)
								info.Billing.Refund(c)
							}
							require.Eventually(t, func() bool {
								var tk model.Token
								var su model.UserSubscription
								var us model.User
								return db.First(&tk, k.Id).Error == nil && db.First(&su, sub.Id).Error == nil && db.First(&us, u.Id).Error == nil && tk.RemainQuota == 1000000 && su.AmountUsed == 0 && us.Quota == 1000000
							}, time.Second, 10*time.Millisecond)
						} else if family == "realtime" {
							segment := &dto.RealtimeUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
							segment.InputTokenDetails.TextTokens = 90
							segment.InputTokenDetails.AudioTokens = 10
							segment.OutputTokenDetails.TextTokens = 15
							segment.OutputTokenDetails.AudioTokens = 5
							require.NoError(t, service.PreWssConsumeQuota(c, info, segment))
							require.NoError(t, service.PreWssConsumeQuota(c, info, segment))
							if want > 0 {
								require.Equal(t, 2*want, info.Billing.GetPreConsumedQuota())
							}
							// Final authoritative usage can be smaller than segment high-water.
							service.PostWssConsumeQuota(c, info, "upstream-alias", segment, "")
						} else if family == "audio" {
							service.PostAudioConsumeQuota(c, info, usage, "")
						} else {
							service.PostTextConsumeQuota(c, info, usage, nil)
						}
						if family != "refund" {
							require.NoError(t, service.SettleBilling(c, info, want))
							if info.Billing != nil {
								info.Billing.Refund(c)
							}
						}
						require.NoError(t, db.First(&u, u.Id).Error)
						require.NoError(t, db.First(&k, k.Id).Error)
						require.NoError(t, db.First(&sub, sub.Id).Error)
						require.Equal(t, want, 1000000-k.RemainQuota)
						require.Equal(t, want, k.UsedQuota)
						if pref == "wallet_only" {
							require.Equal(t, want, 1000000-u.Quota)
							require.Zero(t, sub.AmountUsed)
						} else {
							require.Equal(t, 1000000, u.Quota)
							require.EqualValues(t, want, sub.AmountUsed)
						}
					})
				}
			}
		}
	}
}
