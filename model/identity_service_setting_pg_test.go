package model

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestIdentityB1PostgresIsolated(t *testing.T) {
	raw := os.Getenv("IDENTITY_B1_POSTGRES_PORT")
	if raw == "" {
		t.Skip("isolated postgres port not supplied")
	}
	port, err := strconv.Atoi(raw)
	require.NoError(t, err)
	require.Greater(t, port, 30000)
	dsn := fmt.Sprintf("host=127.0.0.1 port=%d user=postgres dbname=linapi_b1_test sslmode=disable", port)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sql, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { sql.Close() })
	require.NoError(t, db.AutoMigrate(&Option{}))
	for _, revision := range []uint64{0, 1} {
		var wg sync.WaitGroup
		start := make(chan struct{})
		out := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := NewIdentityServiceStore(db)
				c := identityservice.DefaultConfig()
				c.Revision = revision
				<-start
				_, err := s.Save(c, revision)
				out <- err
			}()
		}
		close(start)
		wg.Wait()
		close(out)
		wins := 0
		for e := range out {
			if e == nil {
				wins++
			} else {
				require.ErrorIs(t, e, ErrIdentityServiceConflict)
			}
		}
		require.Equal(t, 1, wins)
	}
	s := NewIdentityServiceStore(db)
	require.NoError(t, s.Refresh())
	require.EqualValues(t, 2, s.Snapshot().Config().Revision)
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_b1() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'b1 injected rollback'; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_b1 BEFORE UPDATE ON options FOR EACH ROW EXECUTE FUNCTION reject_b1()`).Error)
	c := s.Snapshot().Config()
	_, err = s.Save(c, 2)
	require.ErrorContains(t, err, "b1 injected rollback")
	require.NoError(t, s.Refresh())
	require.EqualValues(t, 2, s.Snapshot().Config().Revision)
	c.Mode = "identity_service"
	_, err = s.Save(c, 2)
	require.ErrorContains(t, err, "pending runtime/activation verification")
}
