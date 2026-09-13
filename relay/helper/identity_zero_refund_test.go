package helper_test

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise the real billing consumer, including its asynchronous failure refund.
// A subprocess isolates global DB/pricing state from other package tests.
func TestIdentityB3aZeroReceiptFailureRefund(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3A_ZERO_REFUND")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3aZeroReceiptFailureRefund$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "IDENTITY_B3A_ZERO_REFUND="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "refund.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.ServiceDefaults = map[string]float64{"default": 3}
	cfg.IdentityDefaults = map[string]float64{"ordinary": 0.5}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {"zero-fixed": {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{"zero-fixed": {Mode: "public"}}
	snap, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	common.QuotaPerUnit = 500000
	operation_setting.GetQuotaSetting().EnableFreeModelPreConsume = true
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"zero-fixed":0}`))
	plan := model.SubscriptionPlan{Title: "local", QuotaResetPeriod: "never"}
	require.NoError(t, db.Create(&plan).Error)
	for _, topup := range []int{0, 37} {
		t.Run(fmt.Sprintf("topup-%d", topup), func(t *testing.T) {
			name := fmt.Sprintf("zero-refund-%d", topup)
			u := model.User{Username: name, AffCode: name, Quota: 1000, Status: common.UserStatusEnabled}
			require.NoError(t, db.Create(&u).Error)
			k := model.Token{UserId: u.Id, Key: name, RemainQuota: 1000, Status: common.TokenStatusEnabled, ExpiredTime: -1}
			require.NoError(t, db.Create(&k).Error)
			sub := model.UserSubscription{UserId: u.Id, PlanId: plan.Id, AmountTotal: 1000, Status: "active", StartTime: time.Now().Unix() - 1, EndTime: time.Now().Add(time.Hour).Unix()}
			require.NoError(t, db.Create(&sub).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
			service.FreezeIdentityRequest(c, snap, u.Id, "ordinary")
			newInfo := func() *relaycommon.RelayInfo {
				return &relaycommon.RelayInfo{OriginModelName: "zero-fixed", UserId: u.Id, UserGroup: "ordinary", UsingGroup: "default", TokenId: k.Id, TokenKey: k.Key, TokenGroup: "default", RequestId: name, ChannelMeta: &relaycommon.ChannelMeta{}, StartTime: time.Now(), UserSetting: dto.UserSetting{BillingPreference: "subscription_only"}}
			}
			info := newInfo()
			price, err := helper.ModelPriceHelper(c, info, 0, &types.TokenCountMeta{})
			require.NoError(t, err)
			require.Zero(t, price.QuotaToPreConsume)
			require.Nil(t, service.PreConsumeBilling(c, 0, info))
			session, ok := info.Billing.(*service.BillingSession)
			require.True(t, ok)
			read := func() (model.SubscriptionPreConsumeRecord, model.User, model.Token, model.UserSubscription) {
				var receipt model.SubscriptionPreConsumeRecord
				var user model.User
				var token model.Token
				var subscription model.UserSubscription
				require.NoError(t, db.Where("request_id = ?", name).First(&receipt).Error)
				require.NoError(t, db.First(&user, u.Id).Error)
				require.NoError(t, db.First(&token, k.Id).Error)
				require.NoError(t, db.First(&subscription, sub.Id).Error)
				return receipt, user, token, subscription
			}
			receipt, user, token, subscription := read()
			require.Equal(t, "consumed", receipt.Status)
			require.Zero(t, receipt.PreConsumed)
			require.Equal(t, sub.Id, receipt.UserSubscriptionId)
			require.Equal(t, 1000, user.Quota)
			require.Equal(t, 1000, token.RemainQuota)
			require.Zero(t, token.UsedQuota)
			require.Zero(t, subscription.AmountUsed)
			require.NoError(t, session.Reserve(topup))
			_, user, token, subscription = read()
			require.Equal(t, 1000, user.Quota)
			require.Equal(t, 1000-topup, token.RemainQuota)
			require.Equal(t, topup, token.UsedQuota)
			require.EqualValues(t, topup, subscription.AmountUsed)
			// Model the controller's final upstream-failure branch (no successful settle).
			session.Refund(c)
			require.Eventually(t, func() bool {
				receipt, user, token, subscription = read()
				return receipt.Status == "refunded" && user.Quota == 1000 && token.RemainQuota == 1000 && token.UsedQuota == 0 && subscription.AmountUsed == 0
			}, 3*time.Second, 10*time.Millisecond, "async refund must restore all ledgers AND terminate the zero receipt")
			require.False(t, session.NeedsRefund())
			session.Refund(c)
			for i := 0; i < 10; i++ {
				r, u, k, s := read()
				require.True(t, r.Status == "refunded" && r.PreConsumed == 0 && u.Quota == 1000 && k.RemainQuota == 1000 && k.UsedQuota == 0 && s.AmountUsed == 0, "duplicate consumer refund must not mutate balances or terminal receipt")
				time.Sleep(10 * time.Millisecond)
			}
			retry := newInfo()
			_, err = helper.ModelPriceHelper(c, retry, 0, &types.TokenCountMeta{})
			require.NoError(t, err)
			apiErr := service.PreConsumeBilling(c, 0, retry)
			require.NotNil(t, apiErr, "same request ID must not bind a refunded receipt")
			require.Contains(t, apiErr.Error(), "already refunded")
			r, u2, k2, s2 := read()
			require.Equal(t, "refunded", r.Status)
			require.Equal(t, 1000, u2.Quota)
			require.Equal(t, 1000, k2.RemainQuota)
			require.Zero(t, k2.UsedQuota)
			require.Zero(t, s2.AmountUsed)
		})
	}
}
