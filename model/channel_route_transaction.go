package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RouteTransaction is the shared write boundary for channel membership and
// routing priority. SQLite obtains a database write reservation BEFORE any
// reads (including channel discovery); busy retries restart the whole unit.
// PostgreSQL callers lock channels in ascending ID order before reading state.
// RouteCommitResult distinguishes persistence from cache confirmation. RefreshError
// is NOT a transaction error: retry only RefreshChannelCache, never the mutation.
// Legacy error-only callers retain committed-success semantics; A2 can consume
// the explicit result without mistaking a committed insert/delete for rollback.
type RouteCommitResult struct {
	Committed    bool
	RefreshError error
}

func RouteTransaction(fn func(*gorm.DB) error) error {
	_, err := RouteTransactionWithResult(fn)
	return err
}

func RouteTransactionWithResult(fn func(*gorm.DB) error) (RouteCommitResult, error) {
	// Include commit in refresh serialization: an older load cannot publish
	// after this commit, and a failed refresh cannot erase a newer success.
	channelCacheRefreshLock.Lock()
	defer channelCacheRefreshLock.Unlock()
	if err := channelWriteTransaction(fn); err != nil {
		return RouteCommitResult{}, err
	}
	err := refreshChannelCacheLocked()
	if err != nil {
		common.SysError(fmt.Sprintf("routing committed; cache refresh failed (database fallback active): %v", err))
	}
	return RouteCommitResult{Committed: true, RefreshError: err}, nil
}

// Non-routing writes use the same database locking/retry protocol without
// Ability reconciliation, global cache refresh, alias scans or pricing invalidation.
func channelWriteTransaction(fn func(*gorm.DB) error) error {
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		err = DB.Transaction(func(tx *gorm.DB) error {
			if tx.Dialector.Name() == "sqlite" {
				if err := tx.Exec("UPDATE channels SET id = id WHERE 1 = 0").Error; err != nil {
					return err
				}
			}
			return fn(tx)
		})
		if err == nil || DB.Dialector.Name() != "sqlite" || !routeBusy(err) {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return err
}
func routeBusy(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "database is locked") || strings.Contains(s, "database table is locked") || strings.Contains(s, "sqlite_busy")
}

// LockRouteChannels is reusable by the priority API. The query is applied
// inside the transaction; rows are locked and reread in deterministic order.
func LockRouteChannels(tx *gorm.DB, query string, args ...interface{}) ([]Channel, error) {
	var channels []Channel
	q := tx.Where(query, args...).Order("id ASC")
	if tx.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := q.Find(&channels).Error
	return channels, err
}
func mutateRouteChannels(query string, args []interface{}, mutate func(*gorm.DB, *Channel) error) error {
	_, err := mutateRouteChannelsWithResult(query, args, mutate)
	return err
}

func mutateRouteChannelsWithResult(query string, args []interface{}, mutate func(*gorm.DB, *Channel) error) (RouteCommitResult, error) {
	return RouteTransactionWithResult(func(tx *gorm.DB) error {
		channels, err := LockRouteChannels(tx, query, args...)
		if err != nil {
			return err
		}
		if query == "id = ?" && len(channels) != 1 {
			return gorm.ErrRecordNotFound
		}
		for i := range channels {
			if err = mutate(tx, &channels[i]); err != nil {
				return err
			}
			if err = channels[i].UpdateAbilities(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

// MutateChannelRouting runs a delta against the latest locked channel, then
// persists only declared columns and reconciles abilities in the same commit.
func MutateChannelRouting(id int, columns []string, fn func(*Channel) error) error {
	_, err := MutateChannelRoutingWithResult(id, columns, fn)
	return err
}

func MutateChannelRoutingWithResult(id int, columns []string, fn func(*Channel) error) (RouteCommitResult, error) {
	return mutateRouteChannelsWithResult("id = ?", []interface{}{id}, func(tx *gorm.DB, c *Channel) error {
		if err := fn(c); err != nil {
			return err
		}
		return tx.Model(c).Select(columns).Updates(c).Error
	})
}

var ErrChannelRevisionConflict = errors.New("channel changed; refresh before saving")

// ChannelRevision intentionally excludes cache-only Keys. A2 may expose this
// opaque revision with GET and require it on full-payload saves. The check and
// save MUST execute under the same RouteTransaction/LockRouteChannels boundary.
func ChannelRevision(c *Channel) string {
	copy := *c
	copy.Keys = nil
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (channel *Channel) UpdateWithRevision(revision string) error {
	if revision == "" {
		return ErrChannelRevisionConflict
	}
	return channel.updateRoute(revision, false)
}

// AddChannelModels applies an exact additive delta against the latest locked
// membership, never against an upstream probe's stale complete payload.
func AddChannelModels(id int, models []string) error {
	return mutateRouteChannels("id = ?", []interface{}{id}, func(tx *gorm.DB, c *Channel) error {
		current := []string{}
		if c.Models != "" {
			current = strings.Split(c.Models, ",")
		}
		seen := map[string]bool{}
		for _, m := range current {
			seen[m] = true
		}
		for _, m := range models {
			if m == "" {
				return errors.New("empty model")
			}
			if !seen[m] {
				current = append(current, m)
				seen[m] = true
			}
		}
		c.Models = strings.Join(current, ",")
		return tx.Model(c).Update("models", c.Models).Error
	})
}

func setRouteTagStatus(tag string, status int) error {
	_, err := setRouteTagStatusWithResult(tag, status)
	return err
}

func setRouteTagStatusWithResult(tag string, status int) (RouteCommitResult, error) {
	return mutateRouteChannelsWithResult("tag = ?", []interface{}{tag}, func(tx *gorm.DB, c *Channel) error {
		c.Status = status
		return tx.Model(c).Update("status", status).Error
	})
}
func deleteRouteChannels(query string, args ...interface{}) (int64, error) {
	count, _, err := deleteRouteChannelsWithResult(query, args...)
	return count, err
}

func deleteRouteChannelsWithResult(query string, args ...interface{}) (int64, RouteCommitResult, error) {
	var count int64
	result, err := RouteTransactionWithResult(func(tx *gorm.DB) error {
		count = 0
		channels, err := LockRouteChannels(tx, query, args...)
		if err != nil {
			return err
		}
		for _, c := range channels {
			if err = tx.Where("channel_id = ?", c.Id).Delete(&Ability{}).Error; err != nil {
				return err
			}
			r := tx.Delete(&c)
			if r.Error != nil {
				return r.Error
			}
			count += r.RowsAffected
		}
		return nil
	})
	if err != nil {
		return 0, result, err
	}
	return count, result, nil
}

func (channel *Channel) updateRoute(revision string, full bool) error {
	_, err := channel.updateRouteWithResult(revision, full)
	return err
}

func (channel *Channel) UpdateWithRevisionResult(revision string) (RouteCommitResult, error) {
	if revision == "" {
		return RouteCommitResult{}, ErrChannelRevisionConflict
	}
	return channel.updateRouteWithResult(revision, false)
}

func (channel *Channel) updateRouteWithResult(revision string, full bool) (RouteCommitResult, error) {
	input := *channel
	var saved Channel
	result, err := RouteTransactionWithResult(func(tx *gorm.DB) error {
		rows, err := LockRouteChannels(tx, "id = ?", input.Id)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return gorm.ErrRecordNotFound
		}
		if revision != "" && revision != ChannelRevision(&rows[0]) {
			return ErrChannelRevisionConflict
		}
		working := input
		working.normalizeRouteKeys(&rows[0])
		if working.ChannelInfo.IsMultiKey && working.Key == "" {
			working.Key = rows[0].Key
		}
		q := tx.Model(&Channel{}).Where("id = ?", input.Id)
		if full {
			q = q.Select("*")
		}
		if err = q.Updates(&working).Error; err != nil {
			return err
		}
		if err = tx.First(&saved, input.Id).Error; err != nil {
			return err
		}
		return saved.UpdateAbilities(tx)
	})
	if err == nil {
		*channel = saved
	}
	return result, err
}

// CopyWithAbilities preserves shared exact (group,model) priorities while the
// source is locked. Fields supplied by the copy controller remain authoritative.
func (channel *Channel) CopyWithAbilities(sourceID int) error {
	_, err := channel.CopyWithAbilitiesResult(sourceID)
	return err
}

func (channel *Channel) CopyWithAbilitiesResult(sourceID int) (RouteCommitResult, error) {
	original := *channel
	var saved Channel
	result, err := RouteTransactionWithResult(func(tx *gorm.DB) error {
		rows, err := LockRouteChannels(tx, "id = ?", sourceID)
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return gorm.ErrRecordNotFound
		}
		saved = original
		saved.Id = 0
		if err = tx.Create(&saved).Error; err != nil {
			return err
		}
		if err = saved.AddAbilities(tx); err != nil {
			return err
		}
		var abilities []Ability
		if err = tx.Where("channel_id = ?", sourceID).Find(&abilities).Error; err != nil {
			return err
		}
		for _, a := range abilities {
			if err = tx.Model(&Ability{}).Where(map[string]interface{}{"channel_id": saved.Id, "group": a.Group, "model": a.Model}).Update("priority", a.Priority).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		*channel = saved
	}
	return result, err
}
