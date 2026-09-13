package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestQualityReviewDisableAfterRefreshFailure(t *testing.T) {
	setupRouteCacheA1(t)
	c := Channel{Id: 701, Type: 1, Key: "synthetic", Models: "a", Group: "default", Status: common.ChannelStatusEnabled}
	require.NoError(t, c.Insert())
	// A persisted malformed, disabled unrelated advanced channel prevents refresh.
	require.NoError(t, DB.Create(&Channel{Id: 702, Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusManuallyDisabled, OtherSettings: "{"}).Error)
	changed := UpdateChannelStatus(c.Id, "synthetic", common.ChannelStatusAutoDisabled, "synthetic failure")
	require.True(t, changed)
	persisted, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Equal(t, common.ChannelStatusAutoDisabled, persisted.Status)
	var ability Ability
	require.NoError(t, DB.Where("channel_id = ?", c.Id).First(&ability).Error)
	require.False(t, ability.Enabled)
	selected, err := GetRandomSatisfiedChannel("default", "a", 0, nil)
	require.NoError(t, err)
	t.Logf("status API changed=%v DB status=%d ability.enabled=%v selected=%v", changed, persisted.Status, ability.Enabled, selected != nil)
	require.Nil(t, selected, "committed disabled channel must not remain eligible")
}

func TestQualityReviewPollingWritesRouteProjection(t *testing.T) {
	setupRouteCacheA1(t)
	common.MemoryCacheEnabled = false
	c := Channel{Id: 703, Type: 1, Key: "synthetic1\nsynthetic2", Models: "a,b,c", Group: "default,pro", Status: common.ChannelStatusEnabled, ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling, MultiKeySize: 2}}
	require.NoError(t, c.Insert())
	abilityUpdates, aliasScans := 0, 0
	require.NoError(t, DB.Callback().Update().After("gorm:update").Register("quality:count_ability", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			abilityUpdates++
		}
	}))
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("quality:count_alias", func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" && len(tx.Statement.Selects) == 4 {
			aliasScans++
		}
	}))
	defer DB.Callback().Update().Remove("quality:count_ability")
	defer DB.Callback().Query().Remove("quality:count_alias")
	key, idx, apiErr := c.GetNextEnabledKey()
	require.Nil(t, apiErr)
	require.Equal(t, "synthetic1", key)
	require.Zero(t, idx)
	t.Logf("one polling request: ability UPDATEs=%d global alias channel scans=%d", abilityUpdates, aliasScans)
	require.Zero(t, abilityUpdates, "polling cursor persistence must not rewrite every route")
}
