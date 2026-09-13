package model

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestQualityFixNewPollingChannelDuringFallback(t *testing.T) {
	setupRouteCacheA1(t)
	require.NoError(t, RefreshChannelCache())
	require.NoError(t, DB.Create(&Channel{Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusManuallyDisabled, OtherSettings: "{"}).Error)
	c := Channel{Key: "k0\nk1\nk2", Models: "a", Group: "default", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling, MultiKeySize: 3}}
	require.NoError(t, c.Insert())
	require.Error(t, channelCacheRefreshError)
	for i := 0; i < 2; i++ {
		ch, e := GetRandomSatisfiedChannel("default", "a", 0, nil)
		require.NoError(t, e)
		require.NotNil(t, ch)
		key, idx, apiErr := ch.GetNextEnabledKey()
		require.Nil(t, apiErr)
		require.Equal(t, i, idx)
		require.Equal(t, fmt.Sprintf("k%d", i), key)
	}
	require.NoError(t, DB.Model(&Channel{}).Where("type = ?", constant.ChannelTypeAdvancedCustom).Update("settings", "{}").Error)
	require.NoError(t, RefreshChannelCache())
	ch, e := CacheGetChannel(c.Id)
	require.NoError(t, e)
	key, idx, apiErr := ch.GetNextEnabledKey()
	require.Nil(t, apiErr)
	require.Equal(t, 2, idx)
	require.Equal(t, "k2", key)
}

func TestQualityFixCommittedRefreshFailure(t *testing.T) {
	for _, fault := range []string{"json", "channels", "abilities"} {
		for _, action := range []string{"disable", "delete", "restore"} {
			t.Run(fault+"/"+action, func(t *testing.T) {
				setupRouteCacheA1(t)
				c := Channel{Key: "synthetic", Type: 1, Models: "a", Group: "default", Status: common.ChannelStatusEnabled}
				require.NoError(t, c.Insert())
				if action == "restore" {
					require.True(t, UpdateChannelStatus(c.Id, "synthetic", common.ChannelStatusAutoDisabled, "test"))
				}
				original := channelsIDM[c.Id]
				// Fail only the refresh's full-table query, not the locked mutation read.
				if fault == "json" {
					require.NoError(t, DB.Create(&Channel{Type: constant.ChannelTypeAdvancedCustom, Status: common.ChannelStatusManuallyDisabled, OtherSettings: "{"}).Error)
				} else {
					require.NoError(t, DB.Callback().Query().Before("gorm:query").Register("quality:refresh_failure", func(tx *gorm.DB) {
						fullLoad := tx.Statement.Clauses["WHERE"].Expression == nil
						if where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where); ok && len(where.Exprs) == 1 {
							if expr, ok := where.Exprs[0].(clause.Expr); ok && expr.SQL == "enabled = ?" {
								fullLoad = true
							}
						}
						if tx.Statement.Table == fault && fullLoad {
							tx.AddError(errors.New("synthetic refresh failure"))
						}
					}))
					defer DB.Callback().Query().Remove("quality:refresh_failure")
				}
				var result RouteCommitResult
				var err error
				if action == "delete" {
					result, err = RouteTransactionWithResult(func(tx *gorm.DB) error {
						rows, e := LockRouteChannels(tx, "id = ?", c.Id)
						if e != nil {
							return e
						}
						if len(rows) != 1 {
							return errors.New("missing")
						}
						if e = tx.Where("channel_id = ?", c.Id).Delete(&Ability{}).Error; e != nil {
							return e
						}
						return tx.Delete(&rows[0]).Error
					})
				} else {
					status := common.ChannelStatusAutoDisabled
					if action == "restore" {
						status = common.ChannelStatusEnabled
					}
					var changed bool
					changed, result, err = UpdateChannelStatusWithResult(c.Id, "synthetic", status, "test")
					require.True(t, changed)
				}
				require.NoError(t, err, "committed mutation is not an ordinary retryable failure")
				require.True(t, result.Committed)
				require.Error(t, result.RefreshError)
				require.Same(t, original, channelsIDM[c.Id], "retain last complete snapshot")
				// Clear transient fault but do NOT refresh: selection must use current DB.
				if fault != "json" {
					require.NoError(t, DB.Callback().Query().Remove("quality:refresh_failure"))
				}
				ch, e := GetRandomSatisfiedChannel("default", "a", 0, nil)
				require.NoError(t, e)
				if action == "restore" {
					require.NotNil(t, ch)
					require.Equal(t, c.Id, ch.Id)
				} else {
					require.Nil(t, ch)
				}
				require.Equal(t, action == "restore", IsChannelEnabledForGroupModel("default", "a", c.Id))
				if action == "delete" {
					_, e = CacheGetChannel(c.Id)
					require.Error(t, e)
				}
				if fault == "json" {
					require.NoError(t, DB.Model(&Channel{}).Where("type = ?", constant.ChannelTypeAdvancedCustom).Update("settings", "{}").Error)
				}
				require.NoError(t, RefreshChannelCache())
				require.Nil(t, channelCacheRefreshError)
			})
		}
	}
}

func TestQualityFixRefreshCommitOrdering(t *testing.T) {
	setupRouteCacheA1(t)
	c := Channel{Key: "synthetic", Models: "a", Group: "default"}
	require.NoError(t, c.Insert())
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("quality:pause", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			once.Do(func() { close(entered); <-release; tx.AddError(errors.New("older refresh failure")) })
		}
	}))
	defer DB.Callback().Query().Remove("quality:pause")
	oldDone := make(chan error, 1)
	go func() { oldDone <- RefreshChannelCache() }()
	<-entered
	newDone := make(chan error, 1)
	go func() {
		_, result, e := UpdateChannelStatusWithResult(c.Id, "synthetic", common.ChannelStatusAutoDisabled, "test")
		if e == nil && !result.Committed {
			e = errors.New("not committed")
		}
		newDone <- e
	}()
	close(release)
	require.Error(t, <-oldDone)
	require.NoError(t, <-newDone)
	require.Nil(t, channelCacheRefreshError, "older failure must not mask later successful commit+refresh")
	routeCacheA1Want(t, "default", "a", 0, 0)
}

func TestQualityFixPollingNoProjectionAndMerge(t *testing.T) {
	routeCoreDB(t)
	c := Channel{Key: "k0\nk1\nk2", Models: "a,b,c", Group: "default,pro", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling, MultiKeySize: 3}}
	require.NoError(t, c.Insert())
	var abilityWrites, globalScans, channelWrites atomic.Int64
	require.NoError(t, DB.Callback().Update().After("gorm:update").Register("quality:writes", func(tx *gorm.DB) {
		if tx.Statement.Table == "abilities" {
			abilityWrites.Add(1)
		}
		if tx.Statement.Table == "channels" {
			channelWrites.Add(1)
		}
	}))
	require.NoError(t, DB.Callback().Query().After("gorm:query").Register("quality:scans", func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" && len(tx.Statement.Selects) == 4 {
			globalScans.Add(1)
		}
	}))
	defer DB.Callback().Update().Remove("quality:writes")
	defer DB.Callback().Query().Remove("quality:scans")
	updatePricingLock.Lock()
	lastGetPricingTime = time.Unix(123, 0)
	updatePricingLock.Unlock()
	defer InvalidatePricingCache()
	oldAlias := taskAliasViewPtr.Load()
	for i := 0; i < 3; i++ {
		key, idx, e := c.GetNextEnabledKey()
		require.Nil(t, e)
		require.Equal(t, i, idx)
		require.Equal(t, fmt.Sprintf("k%d", i), key)
	}
	require.Zero(t, abilityWrites.Load())
	require.Zero(t, globalScans.Load())
	require.EqualValues(t, 3, channelWrites.Load())
	require.Equal(t, time.Unix(123, 0), lastGetPricingTime)
	require.Same(t, oldAlias, taskAliasViewPtr.Load())
	// Real concurrent locked delta vs stale cursor object, neither may erase the other's columns.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			if e := MutateChannelRouting(c.Id, []string{"channel_info"}, func(current *Channel) error {
				current.ChannelInfo.MultiKeyDisabledReason = map[int]string{2: "latest"}
				current.ChannelInfo.MultiKeyStatusList = map[int]int{2: common.ChannelStatusAutoDisabled}
				current.ChannelInfo.MultiKeySize = 9
				return nil
			}); e != nil {
				errs <- e
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		cursor := Channel{Id: c.Id, ChannelInfo: ChannelInfo{MultiKeyPollingIndex: 1}}
		for i := 0; i < 15; i++ {
			if e := cursor.SaveChannelInfo(); e != nil {
				errs <- e
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	latest, e := GetChannelById(c.Id, true)
	require.NoError(t, e)
	require.Equal(t, 1, latest.ChannelInfo.MultiKeyPollingIndex)
	require.Equal(t, 9, latest.ChannelInfo.MultiKeySize)
	require.Equal(t, "latest", latest.ChannelInfo.MultiKeyDisabledReason[2])
	require.Equal(t, common.ChannelStatusAutoDisabled, latest.ChannelInfo.MultiKeyStatusList[2])
	t.Logf("3 polling requests: channel_info UPDATEs=3 Ability UPDATEs=0 alias scans=0 pricing unchanged; concurrent locked cursor/status merge preserved")
}

func TestQualityFixPollingRefreshRace(t *testing.T) {
	setupRouteCacheA1(t)
	c := Channel{Key: "k0\nk1", Models: "a", Group: "default", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeyMode: constant.MultiKeyModePolling, MultiKeySize: 2}}
	require.NoError(t, c.Insert())
	stale, e := CacheGetChannel(c.Id)
	require.NoError(t, e)
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				_, _, apiErr := stale.GetNextEnabledKey()
				if apiErr != nil {
					errs <- errors.New("poll failed")
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if e := RefreshChannelCache(); e != nil {
				errs <- e
				return
			}
			_, e := CacheGetChannelInfo(c.Id)
			if e != nil {
				errs <- e
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.Zero(t, stale.ChannelInfo.MultiKeyPollingIndex, "published old snapshot is immutable")
	info, e := CacheGetChannelInfo(c.Id)
	require.NoError(t, e)
	require.Zero(t, info.MultiKeyPollingIndex, "80 polls preserve cursor across refreshes")
}
