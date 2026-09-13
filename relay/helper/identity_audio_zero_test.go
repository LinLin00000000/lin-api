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
// This is not a protocol adaptor test; the router suite covers actual HTTP/WebSocket.
func TestIdentityAudioZeroLedger(t *testing.T) {
	fixture := os.Getenv("IDENTITY_AUDIO_ZERO_LEDGER")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityAudioZeroLedger$", "-test.count=1", "-test.timeout=120s", "-test.v")
		cmd.Env = append(os.Environ(), "IDENTITY_AUDIO_ZERO_LEDGER="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
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
	for _, name := range []string{"audio", "realtime"} {
		cfg.ServiceModels["default"][name] = identityservice.ServiceModel{Enabled: true}
		cfg.ServiceModels["pro"][name] = identityservice.ServiceModel{Enabled: true}
		cfg.ModelIdentityScopes[name] = identityservice.ModelIdentityScope{Mode: "public"}
	}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	plan := model.SubscriptionPlan{Title: "local", QuotaResetPeriod: "never"}
	require.NoError(t, db.Create(&plan).Error)
	sequence := 0
	for _, family := range []string{"audio", "realtime"} {
		for _, pref := range []string{"wallet_only", "subscription_only"} {
			for _, id := range []string{"ordinary", "Friend"} {
				for _, tc := range []struct {
					name       string
					audioRatio float64
					want       int
				}{{"zero", 0, 0}, {"positive", 2, 1080}} {
					group := "default"
					t.Run(family+"/"+pref+"/"+id+"/"+tc.name, func(t *testing.T) {
						sequence++
						name := fmt.Sprintf("family%d", sequence)
						common.QuotaPerUnit = 500000
						common.PreConsumedQuota = 10
						operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = true
						require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"audio":2,"realtime":2}`))
						require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"audio":2,"realtime":2}`))
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
						require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}))
						require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(fmt.Sprintf(`{"audio":%g,"realtime":%g}`, tc.audioRatio, tc.audioRatio)))
						require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"audio":4,"realtime":4}`))
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
						if id == "Friend" {
							require.Equal(t, 1000000, beforeK.RemainQuota)
							require.Nil(t, info.Billing)
						} else {
							require.Less(t, beforeK.RemainQuota, 1000000)
						}
						require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"audio":99,"realtime":99}`))
						require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"audio":99,"realtime":99}`))
						usage := &dto.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}
						usage.PromptTokensDetails.TextTokens = 0
						usage.PromptTokensDetails.AudioTokens = 100
						usage.CompletionTokenDetails.TextTokens = 0
						usage.CompletionTokenDetails.AudioTokens = 20
						// Derive expected base from independently fixed input ratios, asserted below.
						require.Equal(t, tc.audioRatio, info.PriceData.AudioRatio)
						require.Equal(t, float64(4), info.PriceData.AudioCompletionRatio)
						want := tc.want
						if id == "Friend" {
							want = 0
						}
						if family == "realtime" {
							segment := &dto.RealtimeUsage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}
							segment.InputTokenDetails.TextTokens = 0
							segment.InputTokenDetails.AudioTokens = 100
							segment.OutputTokenDetails.TextTokens = 0
							segment.OutputTokenDetails.AudioTokens = 20
							require.NoError(t, service.PreWssConsumeQuota(c, info, segment))
							require.NoError(t, service.PreWssConsumeQuota(c, info, segment))
							if want > 0 {
								require.Equal(t, 2*want, info.Billing.GetPreConsumedQuota())
							}
							// Final authoritative usage can be smaller than segment high-water.
							service.PostWssConsumeQuota(c, info, "upstream-alias", segment, "")
						} else {
							service.PostAudioConsumeQuota(c, info, usage, "")
						}
						require.NoError(t, db.First(&u, u.Id).Error)
						require.NoError(t, db.First(&k, k.Id).Error)
						require.NoError(t, db.First(&sub, sub.Id).Error)
						t.Logf("LEDGER family=%s pref=%s wallet=%d subscription=%d token=%d used=%d expected=%d", family, pref, 1000000-u.Quota, sub.AmountUsed, 1000000-k.RemainQuota, k.UsedQuota, want)
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
