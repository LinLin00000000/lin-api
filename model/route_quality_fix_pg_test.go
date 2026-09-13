package model

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestQualityFixPostgresIsolated(t *testing.T) {
	portString := os.Getenv("ROUTE_A1_POSTGRES_PORT")
	if portString == "" {
		t.Skip("isolated postgres port not supplied")
	}
	port, e := strconv.Atoi(portString)
	require.NoError(t, e)
	require.Greater(t, port, 30000)
	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=postgres dbname=linapi_a1_test sslmode=disable", port)
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, e)
	peer, e := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, e)
	oldDB, oldMemory := DB, common.MemoryCacheEnabled
	oldCandidates, oldIDs, oldChannels, oldConfigs, oldError, oldAlias := group2model2routeCandidates, group2model2channels, channelsIDM, channel2advancedCustomConfig, channelCacheRefreshError, taskAliasViewPtr.Load()
	t.Cleanup(func() {
		group2model2routeCandidates, group2model2channels, channelsIDM, channel2advancedCustomConfig, channelCacheRefreshError = oldCandidates, oldIDs, oldChannels, oldConfigs, oldError
		taskAliasViewPtr.Store(oldAlias)
		InvalidatePricingCache()
	})
	oldMain, oldLog := common.MainDatabaseType(), common.LogDatabaseType()
	DB = db
	common.MemoryCacheEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() {
		a, _ := db.DB()
		_ = a.Close()
		b, _ := peer.DB()
		_ = b.Close()
		DB = oldDB
		common.MemoryCacheEnabled = oldMemory
		common.SetDatabaseTypes(oldMain, oldLog)
		initCol()
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	c := Channel{Key: "k0\nk1", Models: "quality_pg", Group: "quality", ChannelInfo: ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}}
	require.NoError(t, c.Insert())
	// Independent connection holds FOR UPDATE without the process refresh lock.
	locked, release := make(chan struct{}), make(chan struct{})
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- peer.Transaction(func(tx *gorm.DB) error {
			rows, e := LockRouteChannels(tx, "id = ?", c.Id)
			if e != nil {
				return e
			}
			close(locked)
			<-release
			rows[0].ChannelInfo.MultiKeyDisabledReason = map[int]string{1: "peer-latest"}
			rows[0].ChannelInfo.MultiKeyStatusList = map[int]int{1: common.ChannelStatusAutoDisabled}
			return tx.Model(&rows[0]).Update("channel_info", rows[0].ChannelInfo).Error
		})
	}()
	<-locked
	cursorDone := make(chan error, 1)
	go func() {
		cursor := Channel{Id: c.Id, ChannelInfo: ChannelInfo{MultiKeyPollingIndex: 1}}
		cursorDone <- cursor.SaveChannelInfo()
	}()
	select {
	case e := <-cursorDone:
		t.Fatalf("cursor bypassed independent DB lock: %v", e)
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-peerDone)
	require.NoError(t, <-cursorDone)
	fresh, e := GetChannelById(c.Id, true)
	require.NoError(t, e)
	require.Equal(t, "peer-latest", fresh.ChannelInfo.MultiKeyDisabledReason[1])
	require.Equal(t, 1, fresh.ChannelInfo.MultiKeyPollingIndex)
	// PostgreSQL read-only repeatable-read refresh and committed/failure distinction.
	common.MemoryCacheEnabled = true
	require.NoError(t, RefreshChannelCache())
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("quality:pg-refresh", func(tx *gorm.DB) {
		if tx.Statement.Table == "channels" && tx.Statement.Clauses["WHERE"].Expression == nil {
			tx.AddError(errors.New("synthetic pg refresh error"))
		}
	}))
	changed, result, e := UpdateChannelStatusWithResult(c.Id, "k0", common.ChannelStatusAutoDisabled, "test")
	require.NoError(t, e)
	require.True(t, changed)
	require.True(t, result.Committed)
	require.Error(t, result.RefreshError)
	require.NoError(t, db.Callback().Query().Remove("quality:pg-refresh"))
	selected, e := GetRandomSatisfiedChannel("quality", "quality_pg", 0, nil)
	require.NoError(t, e)
	require.Nil(t, selected)
	require.NoError(t, RefreshChannelCache())
	t.Log("independent PostgreSQL FOR UPDATE cursor merge and committed-refresh-failure fallback passed")
}
