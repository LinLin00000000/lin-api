package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"sort"
)

// The entire batch commits or rolls back; use the same polling -> DB lock
// order as single-channel status updates, with deterministic multi-ID locking.
func BatchUpdateChannelStatusWithResult(ids []int, status int) (int, RouteCommitResult, error) {
	unique := map[int]bool{}
	ordered := []int{}
	for _, id := range ids {
		if !unique[id] {
			unique[id] = true
			ordered = append(ordered, id)
		}
	}
	sort.Ints(ordered)
	for _, id := range ordered {
		GetChannelPollingLock(id).Lock()
	}
	defer func() {
		for i := len(ordered) - 1; i >= 0; i-- {
			GetChannelPollingLock(ordered[i]).Unlock()
		}
	}()
	count := 0
	result, err := RouteTransactionWithResult(func(tx *gorm.DB) error {
		count = 0
		rows, err := LockRouteChannels(tx, "id IN ?", ordered)
		if err != nil {
			return err
		}
		if len(rows) != len(ordered) {
			return gorm.ErrRecordNotFound
		}
		for i := range rows {
			c := &rows[i]
			if c.Status != status || c.ChannelInfo.IsMultiKey {
				if c.ChannelInfo.IsMultiKey {
					handlerMultiKeyUpdate(c, "", status, "manual batch operation")
				} else {
					info := c.GetOtherInfo()
					info["status_reason"] = "manual batch operation"
					info["status_time"] = common.GetTimestamp()
					c.SetOtherInfo(info)
					c.Status = status
				}
				updates := map[string]any{"status": c.Status, "other_info": c.OtherInfo}
				if c.ChannelInfo.IsMultiKey {
					updates["channel_info"] = c.ChannelInfo
				}
				if err = tx.Model(c).Updates(updates).Error; err != nil {
					return err
				}
				count++
			}
			if err = c.UpdateAbilities(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, result, err
	}
	return count, result, nil
}
