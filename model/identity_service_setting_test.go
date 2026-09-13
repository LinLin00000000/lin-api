package model

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func identityB1DB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "identity.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { sql.Close() })
	return db
}
func TestIdentityB1StoreDefaultCASImmutable(t *testing.T) {
	db := identityB1DB(t)
	s := NewIdentityServiceStore(db)
	require.NoError(t, s.Refresh())
	require.Equal(t, "legacy", s.Snapshot().Config().Mode)
	var count int64
	require.NoError(t, db.Model(&Option{}).Count(&count).Error)
	require.Zero(t, count)
	c := identityservice.DefaultConfig()
	c.ServiceDefaults = map[string]float64{"default": 0}
	got, err := s.Save(c, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, got.Config().Revision)
	c.ServiceDefaults["default"] = 99
	require.Zero(t, s.Snapshot().Config().ServiceDefaults["default"])
	read := s.Snapshot().Config()
	read.ServiceDefaults["default"] = 22
	require.Zero(t, s.Snapshot().Config().ServiceDefaults["default"])
	_, err = s.Save(c, 0)
	require.ErrorIs(t, err, ErrIdentityServiceConflict)
	fresh := NewIdentityServiceStore(db)
	require.NoError(t, fresh.Refresh())
	require.Zero(t, fresh.Snapshot().Config().ServiceDefaults["default"])
	active := fresh.Snapshot().Config()
	active.Mode = "identity_service"
	_, err = fresh.Save(active, 1)
	require.ErrorContains(t, err, "pending runtime/activation verification")
	require.NoError(t, fresh.Refresh())
	require.Equal(t, "legacy", fresh.Snapshot().Config().Mode)
}
func TestIdentityB1StoreIndependentConcurrentCAS(t *testing.T) {
	db := identityB1DB(t)
	// Independent stores deliberately do not share the process publication mutex.
	for _, initial := range []bool{true, false} {
		if !initial {
			require.NoError(t, db.Exec("DELETE FROM options").Error)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		out := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := NewIdentityServiceStore(db)
				c := identityservice.DefaultConfig()
				<-start
				_, err := s.Save(c, 0)
				out <- err
			}()
		}
		close(start)
		wg.Wait()
		close(out)
		wins := 0
		for err := range out {
			if err == nil {
				wins++
			} else {
				require.ErrorIs(t, err, ErrIdentityServiceConflict)
			}
		}
		require.Equal(t, 1, wins)
	}
	s := NewIdentityServiceStore(db)
	require.NoError(t, s.Refresh())
	var wg sync.WaitGroup
	start := make(chan struct{})
	out := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := s.Snapshot().Config()
			other := NewIdentityServiceStore(db)
			<-start
			_, err := other.Save(c, 1)
			out <- err
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	wins := 0
	for err := range out {
		if err == nil {
			wins++
		} else {
			require.ErrorIs(t, err, ErrIdentityServiceConflict)
		}
	}
	require.Equal(t, 1, wins)
}
func TestIdentityB1StoreRollbackReadFailureAndBypass(t *testing.T) {
	db := identityB1DB(t)
	s := NewIdentityServiceStore(db)
	c := identityservice.DefaultConfig()
	_, err := s.Save(c, 0)
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TRIGGER reject_identity BEFORE UPDATE ON options BEGIN SELECT RAISE(ABORT, 'injected rollback'); END;").Error)
	c = s.Snapshot().Config()
	c.ServiceDefaults = map[string]float64{"pro": 2}
	_, err = s.Save(c, 1)
	require.ErrorContains(t, err, "injected rollback")
	require.EqualValues(t, 1, s.Snapshot().Config().Revision)
	var row Option
	require.NoError(t, db.First(&row, "key = ?", identityservice.OptionKey).Error)
	parsed, err := identityservice.Decode([]byte(row.Value))
	require.NoError(t, err)
	require.EqualValues(t, 1, parsed.Revision)
	old := DB
	DB = db
	t.Cleanup(func() { DB = old })
	require.Error(t, UpdateOption(identityservice.OptionKey, "{}"))
	require.Error(t, UpdateOptionsBulk(map[string]string{"harmless": "x", identityservice.OptionKey: "{}"}))
	var count int64
	require.NoError(t, db.Model(&Option{}).Where("key = ?", "harmless").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Exec("DROP TRIGGER reject_identity").Error)
	require.NoError(t, db.Model(&Option{}).Where("key = ?", identityservice.OptionKey).Update("value", "{}").Error)
	require.Error(t, s.Refresh())
	require.EqualValues(t, 1, s.Snapshot().Config().Revision)
	require.NoError(t, db.Migrator().DropTable(&Option{}))
	require.Error(t, s.Refresh())
	require.EqualValues(t, 1, s.Snapshot().Config().Revision)
	require.False(t, errors.Is(err, ErrIdentityServiceConflict))
}
