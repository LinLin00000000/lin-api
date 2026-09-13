package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestIdentityB2AutoRetrySnapshot(t *testing.T) {
	db := setupChannelSelectAutoGroupsTest(t)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro","vip":"OldVIP","auto":"Auto"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["vip","pro","default"]`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.ServiceDefaults = map[string]float64{"default": 1, "pro": 2, "vip": 1}
	cfg.IdentityDefaults = map[string]float64{"Friend": 0}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {"m": {Enabled: true}}, "pro": {"m": {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{"m": {Mode: "restricted", Identities: []string{"Friend"}}}
	s, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	for i, g := range []string{"vip", "pro", "default"} {
		createChannelSelectAutoGroupsChannel(t, db, 5100+i, g, "m")
	}
	model.InitChannelCache()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "auto")
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, true)
	r := FreezeIdentityRequest(c, s, 44, "Friend")
	p := &RetryParam{Ctx: c, TokenGroup: "auto", ModelName: "m"}
	ch, g, err := CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	require.NotNil(t, ch)
	require.Equal(t, "pro", g)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`[]`))
	common.SetContextKey(c, constant.ContextKeyUserGroup, "ordinary")
	p.IncreaseRetry()
	ch, g, err = CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	require.NotNil(t, ch)
	require.Equal(t, "default", g)
	require.Equal(t, "Friend", r.Identity())
	p.IncreaseRetry()
	ch, _, err = CacheGetRandomSatisfiedChannel(p)
	require.NoError(t, err)
	require.Nil(t, ch)
}

func TestIdentityB2FrozenRequest(t *testing.T) {
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 7
	cfg.ServiceDefaults = map[string]float64{"default": 1, "pro": 2, "Friend": 1}
	cfg.IdentityDefaults = map[string]float64{"Friend": 0, "ordinary": 1, "VIP": 1}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {"friend-model": {Enabled: true}}, "pro": {"friend-model": {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{"friend-model": {Mode: "restricted", Identities: []string{"Friend"}}}
	s, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(c, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{"friend-model": true})
	common.SetContextKey(c, constant.ContextKeyTokenGroup, "default")
	r := FreezeIdentityRequest(c, s, 42, "Friend")
	require.NotNil(t, r)
	require.False(t, r.ServiceAllowed("Friend"), "identity must not auto-enroll as a service")
	require.NoError(t, r.Authorize("default", "friend-model"))
	require.Error(t, r.Authorize("default", "unknown"))
	cfg.ModelIdentityScopes["friend-model"] = identityservice.ModelIdentityScope{Mode: "public"}
	common.SetContextKey(c, constant.ContextKeyUserGroup, "ordinary")
	common.SetContextKey(c, constant.ContextKeyTokenModelLimit, map[string]bool{})
	require.Equal(t, "Friend", r.Identity())
	require.Equal(t, 42, r.UserID())
	require.Equal(t, uint64(7), r.Snapshot().Config().Revision)
	require.NoError(t, r.Authorize("default", "friend-model"))
	for _, id := range []string{"ordinary", "VIP"} {
		other, _ := gin.CreateTestContext(httptest.NewRecorder())
		other.Request = httptest.NewRequest("POST", "/", nil)
		common.SetContextKey(other, constant.ContextKeyTokenGroup, "default")
		require.Error(t, FreezeIdentityRequest(other, s, 43, id).Authorize("default", "friend-model"))
	}
}
