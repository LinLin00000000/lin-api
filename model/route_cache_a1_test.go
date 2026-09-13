package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// All A1 fixtures use an isolated in-memory database; never a configured DSN.
func setupRouteCacheA1(t *testing.T) {
	t.Helper()
	oldDB, oldMemory := DB, common.MemoryCacheEnabled
	channelSyncLock.RLock()
	oldCandidates, oldIDs, oldChannels, oldConfigs := group2model2routeCandidates, group2model2channels, channelsIDM, channel2advancedCustomConfig
	channelSyncLock.RUnlock()
	oldRefreshError := channelCacheRefreshError
	oldAliasView := taskAliasViewPtr.Load()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB, common.MemoryCacheEnabled = db, true
	channelCacheRefreshError = nil
	t.Cleanup(func() {
		DB, common.MemoryCacheEnabled = oldDB, oldMemory
		channelCacheRefreshError = oldRefreshError
		channelSyncLock.Lock()
		group2model2routeCandidates, group2model2channels, channelsIDM, channel2advancedCustomConfig = oldCandidates, oldIDs, oldChannels, oldConfigs
		channelSyncLock.Unlock()
		taskAliasViewPtr.Store(oldAliasView)
		InvalidatePricingCache()
		require.NoError(t, sqlDB.Close())
	})
}

func routeCacheA1Channel(t *testing.T, id int, priority int64) {
	t.Helper()
	require.NoError(t, DB.Create(&Channel{Id: id, Type: 1, Key: "synthetic", Status: common.ChannelStatusEnabled, Group: "default,pro", Models: "a,b", Priority: &priority}).Error)
}

func routeCacheA1Ability(t *testing.T, group, model string, id int, priority int64, enabled bool) {
	t.Helper()
	require.NoError(t, DB.Create(&Ability{Group: group, Model: model, ChannelId: id, Priority: &priority, Enabled: enabled}).Error)
}

func routeCacheA1Want(t *testing.T, group, model string, retry, id int) {
	t.Helper()
	ch, err := GetRandomSatisfiedChannel(group, model, retry, nil)
	require.NoError(t, err)
	if id == 0 {
		require.Nil(t, ch)
		return
	}
	require.NotNil(t, ch)
	require.Equal(t, id, ch.Id)
}

func TestRouteCacheA1AbilityPriorities(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Channel(t, 2, -99)
	for _, row := range []struct {
		group, model string
		high         int
	}{
		{"default", "a", 1}, {"default", "b", 2}, {"pro", "a", 2}, {"pro", "b", 1},
	} {
		for id := 1; id <= 2; id++ {
			priority := int64(-8)
			if id == row.high {
				priority = 7
			}
			routeCacheA1Ability(t, row.group, row.model, id, priority, true)
		}
	}
	InitChannelCache()
	for _, row := range []struct {
		group, model string
		high         int
	}{
		{"default", "a", 1}, {"default", "b", 2}, {"pro", "a", 2}, {"pro", "b", 1},
	} {
		for _, memory := range []bool{true, false} {
			common.MemoryCacheEnabled = memory
			routeCacheA1Want(t, row.group, row.model, 0, row.high)
			routeCacheA1Want(t, row.group, row.model, 1, 3-row.high)
		}
	}
}

func TestRouteCacheA1EnabledIntersection(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Channel(t, 2, 98)
	routeCacheA1Channel(t, 3, 97)
	routeCacheA1Channel(t, 4, 1)
	routeCacheA1Ability(t, "default", "a", 1, 99, false)
	routeCacheA1Ability(t, "default", "a", 2, 98, true)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 2).Update("status", common.ChannelStatusManuallyDisabled).Error)
	routeCacheA1Ability(t, "default", "a", 4, 1, true)
	routeCacheA1Ability(t, "default", "a", 999, 999, true)
	InitChannelCache()
	routeCacheA1Want(t, "default", "a", 0, 4)
	routeCacheA1Want(t, "default", "a", 99, 4)
	routeCacheA1Want(t, "default", "b", 0, 0)
}

func TestRouteCacheA1ExactNormalizedAndNullPriority(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Channel(t, 2, -99)
	routeCacheA1Channel(t, 3, 999)
	exact, normalized := "gpt-4o-gizmo-specific", "gpt-4o-gizmo-*"
	routeCacheA1Ability(t, "default", normalized, 1, -8, true)
	routeCacheA1Ability(t, "default", normalized, 2, 0, true)
	// Persist SQL NULL, bypassing the schema's creation default.
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", 2).Update("priority", nil).Error)
	routeCacheA1Ability(t, "default", exact, 3, -99, true)
	require.NoError(t, RefreshChannelCache())
	for _, memory := range []bool{false, true} {
		common.MemoryCacheEnabled = memory
		routeCacheA1Want(t, "default", exact, -1, 3)
		routeCacheA1Want(t, "default", exact, 99, 3)
		routeCacheA1Want(t, "default", "gpt-4o-gizmo-other", -7, 2)
		routeCacheA1Want(t, "default", "gpt-4o-gizmo-other", 1, 1)
		routeCacheA1Want(t, "default", "gpt-4o-gizmo-other", 99, 1)
	}
	routeCacheA1Ability(t, "default", " a ", 1, 50, true)
	require.NoError(t, RefreshChannelCache())
	routeCacheA1Want(t, "default", "a", 0, 0)
	routeCacheA1Want(t, "default", " a ", 0, 1)
}

func TestRouteCacheA1FiltersBeforeRetryAndFallback(t *testing.T) {
	setupRouteCacheA1(t)
	for id := 1; id <= 3; id++ {
		routeCacheA1Channel(t, id, int64(id))
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Update("type", constant.ChannelTypeTaskPlugin).Error)
	exact, normalized := "gpt-4o-gizmo-specific", "gpt-4o-gizmo-*"
	routeCacheA1Ability(t, "default", exact, 1, 99, true)
	routeCacheA1Ability(t, "default", normalized, 2, 8, true)
	routeCacheA1Ability(t, "default", normalized, 3, -8, true)
	filters := []dto.ChannelFilter{{Kind: dto.FilterTaskPluginIdentity, TaskPluginKey: "synthetic-plugin", TaskPluginChannelTypes: []int{1}}}
	require.NoError(t, RefreshChannelCache())
	for retry, want := range []int{2, 3, 3} {
		ch, err := GetRandomSatisfiedChannel("default", exact, retry, filters)
		require.NoError(t, err)
		require.NotNil(t, ch)
		require.Equal(t, want, ch.Id)
	}
	// Also filter a high-priority candidate within the same exact key.
	routeCacheA1Ability(t, "default", normalized, 1, 999, true)
	require.NoError(t, RefreshChannelCache())
	ch, err := GetRandomSatisfiedChannel("default", normalized, 0, filters)
	require.NoError(t, err)
	require.NotNil(t, ch)
	require.Equal(t, 2, ch.Id)
}

func TestRouteCacheA1RefreshFailurePreservesSnapshot(t *testing.T) {
	for _, table := range []string{"channels", "abilities"} {
		t.Run(table, func(t *testing.T) {
			setupRouteCacheA1(t)
			routeCacheA1Channel(t, 1, 1)
			routeCacheA1Ability(t, "default", "a", 1, 1, true)
			require.NoError(t, RefreshChannelCache())
			original, err := CacheGetChannel(1)
			require.NoError(t, err)
			require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Update("name", "not-published").Error)
			require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("a1_read_error", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("synthetic read failure"))
				}
			}))
			defer DB.Callback().Query().Remove("a1_read_error")
			require.ErrorContains(t, RefreshChannelCache(), "synthetic read failure")
			selected, err := GetRandomSatisfiedChannel("default", "a", 0, nil)
			if table == "abilities" {
				require.Error(t, err)
			}
			require.Nil(t, selected)
			channelSyncLock.RLock()
			require.Same(t, original, channelsIDM[1], "failed refresh retains complete snapshot but cannot route through it")
			channelSyncLock.RUnlock()
			require.False(t, IsChannelEnabledForGroupModel("default", "a", 1))
			require.NoError(t, DB.Callback().Query().Remove("a1_read_error"))
			require.NoError(t, RefreshChannelCache())
			after, err := CacheGetChannel(1)
			require.NoError(t, err)
			require.Equal(t, "not-published", after.Name)
		})
	}
}

func TestRouteCacheA1InitialFailureRejectsSelection(t *testing.T) {
	setupRouteCacheA1(t)
	channelSyncLock.Lock()
	group2model2routeCandidates = nil
	group2model2channels = nil
	channelsIDM = nil
	channelSyncLock.Unlock()
	require.NoError(t, DB.Migrator().DropTable(&Ability{}))
	require.Error(t, RefreshChannelCache())
	ch, err := GetRandomSatisfiedChannel("default", "a", 0, nil)
	require.ErrorContains(t, err, "no successful snapshot")
	require.Nil(t, ch)
	require.NoError(t, DB.AutoMigrate(&Ability{}))
	require.NoError(t, RefreshChannelCache())
	routeCacheA1Want(t, "default", "a", 0, 0)
}

func TestRouteCacheA1DisableRestoreAndPolling(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Ability(t, "default", "a", 1, -8, true)
	require.NoError(t, RefreshChannelCache())
	channelSyncLock.Lock()
	channelsIDM[1].ChannelInfo.IsMultiKey = true
	channelsIDM[1].ChannelInfo.MultiKeyMode = constant.MultiKeyModePolling
	channelsIDM[1].ChannelInfo.MultiKeyPollingIndex = 3
	info := channelsIDM[1].ChannelInfo
	channelSyncLock.Unlock()
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Update("channel_info", info).Error)
	require.NoError(t, RefreshChannelCache())
	ch, err := CacheGetChannel(1)
	require.NoError(t, err)
	require.Equal(t, 3, ch.ChannelInfo.MultiKeyPollingIndex)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Update("status", common.ChannelStatusManuallyDisabled).Error)
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", 1).Update("enabled", false).Error)
	CacheUpdateChannelStatus(1, common.ChannelStatusManuallyDisabled)
	routeCacheA1Want(t, "default", "a", 0, 0)
	require.False(t, IsChannelEnabledForGroupModel("default", "a", 1))
	require.NoError(t, RefreshChannelCache())
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 1).Update("status", common.ChannelStatusEnabled).Error)
	// A channel alone must not resurrect a disabled Ability.
	CacheUpdateChannelStatus(1, common.ChannelStatusEnabled)
	routeCacheA1Want(t, "default", "a", 0, 0)
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", 1).Update("enabled", true).Error)
	CacheUpdateChannelStatus(1, common.ChannelStatusEnabled)
	routeCacheA1Want(t, "default", "a", 0, 1)
	require.True(t, IsChannelEnabledForGroupModel("default", "a", 1))
}

func TestRouteCacheA1WeightsAndConcurrentRefresh(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Channel(t, 2, -99)
	routeCacheA1Ability(t, "default", "a", 1, 5, true)
	routeCacheA1Ability(t, "default", "a", 2, 5, true)
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 2).Update("weight", 100).Error)
	require.NoError(t, RefreshChannelCache())
	// Existing cache weighting gives a zero-weight peer no chance when total > 0;
	// the DB's historical Ability.Weight+10 smoothing intentionally differs.
	for i := 0; i < 50; i++ {
		routeCacheA1Want(t, "default", "a", 0, 2)
	}
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", 2).Update("weight", 0).Error)
	require.NoError(t, RefreshChannelCache())
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ch, err := GetRandomSatisfiedChannel("default", "a", -1, nil)
				if err != nil {
					failures <- err
					return
				}
				if ch == nil || (ch.Id != 1 && ch.Id != 2) {
					failures <- errors.New("partial route snapshot")
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		require.NoError(t, RefreshChannelCache())
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
}

func TestRouteCacheA1NilCandidateAndMetadataUpdate(t *testing.T) {
	setupRouteCacheA1(t)
	routeCacheA1Channel(t, 1, 99)
	routeCacheA1Channel(t, 2, -99)
	routeCacheA1Ability(t, "default", "a", 1, -8, true)
	routeCacheA1Ability(t, "default", "a", 2, 8, true)
	require.NoError(t, RefreshChannelCache())
	var updated Channel
	require.NoError(t, DB.First(&updated, 2).Error)
	priority := int64(-999)
	updated.Priority = &priority
	CacheUpdateChannel(&updated)
	routeCacheA1Want(t, "default", "a", 0, 2)
	CacheUpdateChannel(nil)
	channelSyncLock.Lock()
	channelsIDM[2] = nil
	channelSyncLock.Unlock()
	routeCacheA1Want(t, "default", "a", -1, 1)
}

func TestRouteCacheA1MalformedSettingsReadOnly(t *testing.T) {
	setupRouteCacheA1(t)
	raw := "{broken"
	c := Channel{Type: constant.ChannelTypeTaskPlugin, Key: "synthetic", Models: "a", Group: "default", Setting: &raw}
	require.NoError(t, c.Insert())
	before := ChannelRevision(&c)
	for _, memory := range []bool{false, true} {
		common.MemoryCacheEnabled = memory
		picked, err := GetRandomSatisfiedChannel("default", "a", 0, []dto.ChannelFilter{{Kind: dto.FilterTaskPluginIdentity, TaskPluginKey: "x"}})
		require.NoError(t, err)
		require.Nil(t, picked)
	}
	current, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Equal(t, before, ChannelRevision(current))
	require.NoError(t, DB.Model(&Channel{}).Where("id = ?", c.Id).Updates(map[string]interface{}{"type": constant.ChannelTypeAdvancedCustom, "settings": raw}).Error)
	require.Error(t, RefreshChannelCache())
	// Failed parsing cannot publish a half-rebuilt snapshot or repair persistence.
	require.Equal(t, constant.ChannelTypeTaskPlugin, channelsIDM[c.Id].Type)
}

func TestRouteCacheA1CommittedStatusAndRollback(t *testing.T) {
	setupRouteCacheA1(t)
	tag := "t"
	c := Channel{Key: "synthetic", Models: "a", Group: "default", Tag: &tag}
	require.NoError(t, c.Insert())
	routeCacheA1Want(t, "default", "a", 0, c.Id)
	require.True(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusManuallyDisabled, "test"))
	routeCacheA1Want(t, "default", "a", 0, 0)
	require.True(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusEnabled, "test"))
	routeCacheA1Want(t, "default", "a", 0, c.Id)
	require.NoError(t, DisableChannelByTag(tag))
	routeCacheA1Want(t, "default", "a", 0, 0)
	require.NoError(t, EnableChannelByTag(tag))
	routeCacheA1Want(t, "default", "a", 0, c.Id)
	require.NoError(t, DB.Exec("CREATE TRIGGER reject_status BEFORE UPDATE ON abilities BEGIN SELECT RAISE(ABORT, 'injected'); END").Error)
	require.False(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusManuallyDisabled, "test"))
	routeCacheA1Want(t, "default", "a", 0, c.Id)
	current, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Equal(t, common.ChannelStatusEnabled, current.Status)
}
