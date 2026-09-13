package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func routeCoreDB(t *testing.T) {
	t.Helper()
	old := DB
	enabled := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "route.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
	DB = db
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close(); DB = old; common.MemoryCacheEnabled = enabled })
}
func TestRouteCorePreserveReconcile(t *testing.T) {
	routeCoreDB(t)
	p := int64(3)
	c := Channel{Key: "synthetic", Models: "a,b", Group: "default,pro", Priority: &p, Status: common.ChannelStatusEnabled}
	require.NoError(t, c.Insert())
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ? AND model = ?", c.Id, "a").Update("priority", -7).Error)
	p = 99
	require.NoError(t, c.Update())
	var a Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ? AND "+commonGroupCol+" = ?", c.Id, "a", "pro").First(&a).Error)
	require.Equal(t, int64(-7), *a.Priority)
	c.Models = "a,c"
	require.NoError(t, c.Update())
	c.Models = "a,b,c"
	require.NoError(t, c.Update())
	a = Ability{}
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", c.Id, "b").First(&a).Error)
	require.Equal(t, int64(99), *a.Priority)
	_, _, err := FixAbility()
	require.NoError(t, err)
	var kept Ability
	require.NoError(t, DB.Where("channel_id = ? AND model = ?", c.Id, "a").First(&kept).Error)
	require.Equal(t, int64(-7), *kept.Priority)
}
func TestRouteCoreInsertRollback(t *testing.T) {
	routeCoreDB(t)
	require.NoError(t, DB.Exec("CREATE TRIGGER reject_ability BEFORE INSERT ON abilities BEGIN SELECT RAISE(ABORT, 'injected'); END").Error)
	c := Channel{Key: "synthetic", Models: "a", Group: "default"}
	require.Error(t, c.Insert())
	var count int64
	require.NoError(t, DB.Model(&Channel{}).Count(&count).Error)
	require.Zero(t, count)
}
