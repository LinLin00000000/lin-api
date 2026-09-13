package model

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestIdentityB3aSubscriptionRefundAtomic(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "refund.db")), &gorm.Config{})
	require.NoError(t, err)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&UserSubscription{}, &SubscriptionPreConsumeRecord{}))
	sub := UserSubscription{UserId: 1, AmountTotal: 1000, AmountUsed: 90}
	require.NoError(t, db.Create(&sub).Error)
	record := SubscriptionPreConsumeRecord{RequestId: "atomic-refund", UserId: 1, UserSubscriptionId: sub.Id, PreConsumed: 90, Status: "consumed"}
	require.NoError(t, db.Create(&record).Error)
	fail := true
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("b3a-refund-failure", func(tx *gorm.DB) {
		if fail && tx.Statement.Table == "subscription_pre_consume_records" {
			tx.AddError(errors.New("injected receipt failure"))
		}
	}))
	require.Error(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	require.EqualValues(t, 90, sub.AmountUsed)
	require.NoError(t, db.First(&record, record.Id).Error)
	require.Equal(t, "consumed", record.Status)
	fail = false
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, RefundSubscriptionPreConsume(record.RequestId))
	require.NoError(t, db.First(&sub, sub.Id).Error)
	require.Zero(t, sub.AmountUsed)
	require.NoError(t, db.First(&record, record.Id).Error)
	require.Equal(t, "refunded", record.Status)
}
