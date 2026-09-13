package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/pkg/identityservice"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrIdentityServiceConflict = errors.New("identity/service revision conflict; refresh before saving")
var ErrIdentityServiceDedicatedAPI = errors.New("identity_service_model_setting requires the dedicated validated CAS API")

// IdentityServiceStore owns a detached immutable snapshot. Its mutex orders this
// process's commits and refreshes; the conditional SQL write (not this mutex)
// arbitrates writers in other processes. No runtime consumer is wired in B1.
// A nil db follows model.DB for the existing application lifecycle.
type IdentityServiceStore struct {
	db       *gorm.DB
	mu       sync.Mutex
	snapshot atomic.Pointer[identityservice.Snapshot]
}

func NewIdentityServiceStore(db *gorm.DB) *IdentityServiceStore {
	s := &IdentityServiceStore{db: db}
	snap, err := identityservice.NewSnapshot(identityservice.DefaultConfig())
	if err != nil {
		panic(err)
	}
	s.snapshot.Store(snap)
	return s
}

var IdentityServiceSettings = NewIdentityServiceStore(nil)

func (s *IdentityServiceStore) database() *gorm.DB {
	if s.db != nil {
		return s.db
	}
	return DB
}
func (s *IdentityServiceStore) Snapshot() *identityservice.Snapshot { return s.snapshot.Load() }

func decodeStoredIdentityService(raw string) (*identityservice.Snapshot, error) {
	c, err := identityservice.Decode([]byte(raw))
	if err != nil {
		return nil, err
	}
	if err = identityservice.ValidateForStorage(c); err != nil {
		return nil, err
	}
	if c.Revision == 0 {
		return nil, errors.New("persisted identity/service revision must be positive")
	}
	return identityservice.NewSnapshot(c)
}

// Refresh never creates an option, and never replaces the last good snapshot on
// a database/validation error. The API propagates the error, not stale success.
func (s *IdentityServiceStore) Refresh() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.database()
	if db == nil {
		return errors.New("identity/service database unavailable")
	}
	var row Option
	err := db.Where("key = ?", identityservice.OptionKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		snap, _ := identityservice.NewSnapshot(identityservice.DefaultConfig())
		s.snapshot.Store(snap)
		return nil
	}
	if err != nil {
		return err
	}
	snap, err := decodeStoredIdentityService(row.Value)
	if err != nil {
		return err
	}
	s.snapshot.Store(snap)
	return nil
}

// ValidateIdentityServiceDraft verifies optional provenance against the exact
// derived candidate. A client-provided digest alone is never proof of migration.
// The report certifies only the explicit synthetic/provided coefficient matrix;
// all runtime, rounding and in-flight data activation checks remain pending.
func ValidateIdentityServiceDraft(c identityservice.Config, source *identityservice.MigrationInput) error {
	if err := identityservice.ValidateForStorage(c); err != nil {
		return err
	}
	if source == nil {
		if c.MigrationSourceDigest != "" {
			return errors.New("migration_source_digest requires its explicit migration source")
		}
		return nil
	}
	report := identityservice.Migrate(*source)
	if report.Config == nil || !report.CoefficientsEquivalent || len(report.Conflicts) != 0 || len(report.AuthorizationDiff) != 0 {
		return errors.New("migration has coefficient or authorization conflicts")
	}
	derived := *report.Config
	derived.Revision = c.Revision
	a, err := json.Marshal(c)
	if err != nil {
		return err
	}
	b, err := json.Marshal(derived)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("migration source does not match the exact candidate configuration")
	}
	return nil
}

// Save requires the revision read by the caller in both payload and CAS. It
// increments only on a committed write. Optional source must bind provenance.
func (s *IdentityServiceStore) Save(c identityservice.Config, expected uint64, source ...identityservice.MigrationInput) (*identityservice.Snapshot, error) {
	if c.Revision != expected {
		return nil, ErrIdentityServiceConflict
	}
	if expected == math.MaxUint64 {
		return nil, errors.New("identity/service revision exhausted")
	}
	if len(source) > 1 {
		return nil, errors.New("only one migration source is allowed")
	}
	var migration *identityservice.MigrationInput
	if len(source) == 1 {
		migration = &source[0]
	}
	if err := ValidateIdentityServiceDraft(c, migration); err != nil {
		return nil, err
	}
	// Clone before publishing or persisting: neither caller maps nor returned DTO
	// maps can mutate this generation. Concurrent mutation during a call is, as
	// with encoding/json, outside the caller contract.
	detached, err := identityservice.NewSnapshot(c)
	if err != nil {
		return nil, err
	}
	c = detached.Config()
	c.Revision = expected + 1
	snap, err := identityservice.NewSnapshot(c)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.database()
	if db == nil {
		return nil, errors.New("identity/service database unavailable")
	}
	for attempt := 0; attempt < 8; attempt++ {
		err = db.Transaction(func(tx *gorm.DB) error {
			// Acquire SQLite's writer reservation before reading; busy retries restart
			// the whole transaction. Other databases use an atomic predicate below.
			if tx.Dialector.Name() == "sqlite" {
				if e := tx.Exec("UPDATE options SET value = value WHERE 1 = 0").Error; e != nil {
					return e
				}
			}
			var row Option
			e := tx.Where("key = ?", identityservice.OptionKey).First(&row).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				if expected != 0 {
					return ErrIdentityServiceConflict
				}
				result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: identityservice.OptionKey, Value: string(raw)})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrIdentityServiceConflict
				}
				return nil
			}
			if e != nil {
				return e
			}
			current, e := decodeStoredIdentityService(row.Value)
			if e != nil {
				return fmt.Errorf("stored identity/service configuration invalid: %w", e)
			}
			if current.Config().Revision != expected {
				return ErrIdentityServiceConflict
			}
			result := tx.Model(&Option{}).Where("key = ? AND value = ?", identityservice.OptionKey, row.Value).Update("value", string(raw))
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrIdentityServiceConflict
			}
			return nil
		})
		if err == nil || db.Dialector.Name() != "sqlite" || !routeBusy(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	if err != nil {
		return nil, err
	}
	s.snapshot.Store(snap)
	return snap, nil
}
