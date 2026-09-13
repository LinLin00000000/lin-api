package model

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func routePriority(t *testing.T, id int, g, m string) int64 {
	t.Helper()
	var a Ability
	require.NoError(t, DB.Where(map[string]interface{}{"channel_id": id, "group": g, "model": m}).First(&a).Error)
	if a.Priority == nil {
		return 0
	}
	return *a.Priority
}
func setRouteTestPriority(tx *gorm.DB, id int, g, m string, p int64) error {
	if _, err := LockRouteChannels(tx, "id = ?", id); err != nil {
		return err
	}
	return tx.Model(&Ability{}).Where(map[string]interface{}{"channel_id": id, "group": g, "model": m}).Update("priority", p).Error
}
func TestRouteCoreTagCopyFixAndCAS(t *testing.T) {
	routeCoreDB(t)
	p := int64(3)
	tag := "old"
	c := Channel{Key: "synthetic", Models: "a,b", Group: "default,pro", Priority: &p, Tag: &tag}
	require.NoError(t, c.Insert())
	require.NoError(t, RouteTransaction(func(tx *gorm.DB) error { return setRouteTestPriority(tx, c.Id, "pro", "b", -9) }))
	stale := c
	revision := ChannelRevision(&stale)
	c.Priority = common.GetPointer[int64](99)
	require.NoError(t, c.Save())
	require.Equal(t, int64(-9), routePriority(t, c.Id, "pro", "b"))
	stale.Models = "clobber"
	require.ErrorIs(t, stale.UpdateWithRevision(revision), ErrChannelRevisionConflict)
	newTag := "new"
	models := "a,b,c"
	groups := "default,pro,extra"
	w := uint(0)
	require.NoError(t, EditChannelByTag(tag, &newTag, nil, &models, &groups, common.GetPointer[int64](41), &w, nil, nil))
	require.Equal(t, int64(-9), routePriority(t, c.Id, "pro", "b"))
	require.Equal(t, int64(41), routePriority(t, c.Id, "extra", "c"))
	tag = "batch"
	require.NoError(t, BatchSetChannelTag([]int{c.Id}, &tag))
	fresh, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	clone := *fresh
	clone.Id = 0
	clone.Models = "b,d"
	require.NoError(t, clone.CopyWithAbilities(c.Id))
	require.Equal(t, int64(-9), routePriority(t, clone.Id, "pro", "b"))
	require.Equal(t, int64(41), routePriority(t, clone.Id, "pro", "d"))
	require.NoError(t, DB.Create(&Ability{Group: "orphan", Model: "a", ChannelId: 99999}).Error)
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", c.Id, "a").Delete(&Ability{}).Error)
	for i := 0; i < 2; i++ {
		_, _, err = FixAbility()
		require.NoError(t, err)
	}
	require.Equal(t, int64(-9), routePriority(t, c.Id, "pro", "b"))
	require.Equal(t, int64(41), routePriority(t, c.Id, "pro", "a"))
	var n int64
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ?", 99999).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, DisableChannelByTag(tag))
	require.NoError(t, EnableChannelByTag(tag))
	require.True(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusManuallyDisabled, "test"))
	require.True(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusEnabled, "test"))
	require.Equal(t, int64(-9), routePriority(t, c.Id, "pro", "b"))
}

func TestRouteCoreWriteFailuresRollback(t *testing.T) {
	for _, operation := range []string{"update", "save", "tag", "batch_tag", "status", "tag_status", "copy", "fix", "delete", "batch_delete", "delete_disabled", "channel_update"} {
		t.Run(operation, func(t *testing.T) {
			routeCoreDB(t)
			tag := "tag"
			c := Channel{Key: "synthetic", Models: "a", Group: "default", Tag: &tag}
			require.NoError(t, c.Insert())
			if operation == "delete_disabled" {
				require.True(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusManuallyDisabled, "test"))
			}
			before, err := GetChannelById(c.Id, true)
			require.NoError(t, err)
			revision := ChannelRevision(before)
			table, event := "abilities", "UPDATE"
			if operation == "copy" {
				event = "INSERT"
			}
			if operation == "delete" || operation == "batch_delete" || operation == "delete_disabled" {
				event = "DELETE"
			}
			if operation == "channel_update" {
				table = "channels"
			}
			require.NoError(t, DB.Exec(fmt.Sprintf("CREATE TRIGGER reject_write BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected'); END", event, table)).Error)
			switch operation {
			case "update", "channel_update":
				c.Name = "changed"
				err = c.Update()
			case "save":
				c.Name = "changed"
				err = c.Save()
			case "tag":
				x := "changed"
				err = EditChannelByTag(tag, &x, nil, nil, nil, nil, nil, nil, nil)
			case "batch_tag":
				x := "changed"
				err = BatchSetChannelTag([]int{c.Id}, &x)
			case "status":
				require.False(t, UpdateChannelStatus(c.Id, "", common.ChannelStatusManuallyDisabled, "test"))
				err = errors.New("bool failure verified")
			case "tag_status":
				err = DisableChannelByTag(tag)
			case "copy":
				clone := c
				clone.Id = 0
				err = clone.CopyWithAbilities(c.Id)
			case "fix":
				_, _, err = FixAbility()
			case "delete":
				err = c.Delete()
			case "batch_delete":
				_, err = BatchDeleteChannels([]int{c.Id})
			case "delete_disabled":
				_, err = DeleteDisabledChannel()
			}
			require.Error(t, err)
			after, err := GetChannelById(c.Id, true)
			require.NoError(t, err)
			require.Equal(t, revision, ChannelRevision(after))
			var n int64
			require.NoError(t, DB.Model(&Channel{}).Count(&n).Error)
			require.Equal(t, int64(1), n)
			require.NoError(t, DB.Model(&Ability{}).Count(&n).Error)
			require.Equal(t, int64(1), n)
		})
	}
}

func exerciseRouteConcurrency(t *testing.T) {
	t.Helper()
	c := Channel{Key: "synthetic", Models: "base", Group: "default,pro"}
	require.NoError(t, c.Insert())
	start := make(chan struct{})
	errs := make(chan error, 14)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; errs <- AddChannelModels(c.Id, []string{fmt.Sprintf("m%d", i)}) }(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errs <- RouteTransaction(func(tx *gorm.DB) error { return setRouteTestPriority(tx, c.Id, "pro", "base", -31) })
	}()
	wg.Add(1)
	go func() { defer wg.Done(); <-start; _, _, err := FixAbility(); errs <- err }()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	fresh, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Len(t, strings.Split(fresh.Models, ","), 11)
	require.Equal(t, int64(-31), routePriority(t, c.Id, "pro", "base"))
	// A second transaction cannot enter its mutation until the first releases
	// the database lock. This works across independent DB connections, no mutex.
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- RouteTransaction(func(tx *gorm.DB) error {
			_, err := LockRouteChannels(tx, "id = ?", c.Id)
			if err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	entered := make(chan struct{})
	done2 := make(chan error, 1)
	go func() {
		done2 <- MutateChannelRouting(c.Id, []string{"name"}, func(c *Channel) error { close(entered); c.Name = "locked"; return nil })
	}()
	select {
	case <-entered:
		t.Error("second writer entered before release")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done2)
}
func TestRouteCoreConcurrentSQLite(t *testing.T) { routeCoreDB(t); exerciseRouteConcurrency(t) }

// Only a dedicated container's random loopback port is accepted; production
// DSNs and standard ports are never read by this test.
func TestRouteCorePostgresIsolated(t *testing.T) {
	portString := os.Getenv("ROUTE_A1_POSTGRES_PORT")
	if portString == "" {
		t.Skip("isolated postgres port not supplied")
	}
	port, err := strconv.Atoi(portString)
	require.NoError(t, err)
	require.Greater(t, port, 30000)
	db, err := gorm.Open(postgres.Open(fmt.Sprintf("host=127.0.0.1 port=%d user=postgres dbname=linapi_a1_test sslmode=disable", port)), &gorm.Config{})
	require.NoError(t, err)
	old := DB
	oldMemory := common.MemoryCacheEnabled
	DB = db
	common.MemoryCacheEnabled = false
	mainType, logType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypePostgreSQL, common.DatabaseTypePostgreSQL)
	initCol()
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		DB = old
		common.MemoryCacheEnabled = oldMemory
		common.SetDatabaseTypes(mainType, logType)
		initCol()
	})
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	exerciseRouteConcurrency(t)
	c := Channel{Key: "synthetic", Models: "rollback", Group: "default"}
	require.NoError(t, c.Insert())
	revision := ChannelRevision(&c)
	err = RouteTransaction(func(tx *gorm.DB) error {
		_, err := LockRouteChannels(tx, "id = ?", c.Id)
		if err != nil {
			return err
		}
		if err = tx.Model(&c).Update("models", "changed").Error; err != nil {
			return err
		}
		return errors.New("injected after write")
	})
	require.Error(t, err)
	fresh, err := GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Equal(t, revision, ChannelRevision(fresh))
}
