package model

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	kitdto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"gorm.io/gorm"
)

var group2model2channels map[string]map[string][]int // enabled channel
var channelsIDM map[int]*Channel                     // all channels include disabled
// channel2advancedCustomConfig caches parsed Advanced Custom (type 58) configs so
// path-aware selection avoids re-parsing JSON per request. Refreshed on full sync.
var channel2advancedCustomConfig map[int]*kitdto.AdvancedCustomConfig

// Each candidate keeps its exact Ability key's priority, not Channel.Priority.
type cachedRouteCandidate struct {
	channelID int
	priority  int64
}

var group2model2routeCandidates map[string]map[string][]cachedRouteCandidate
var channelSyncLock sync.RWMutex

// Preserve the last complete snapshot for diagnostics, but never route through
// it after a failed refresh. Database fallback validates current eligibility.
var channelCacheRefreshError error

// Serialize loads as well as publication so an older refresh cannot replace a
// newer snapshot. Readers continue using the last complete snapshot while loading.
var channelCacheRefreshLock sync.Mutex

func InitChannelCache() {
	if err := RefreshChannelCache(); err != nil {
		common.SysError(fmt.Sprintf("refresh channel cache failed: %v", err))
	}
}

// RefreshChannelCache publishes only a complete, successful database snapshot.
// A nil route map means no successful initial load, distinct from a healthy empty map.
func RefreshChannelCache() error {
	channelCacheRefreshLock.Lock()
	defer channelCacheRefreshLock.Unlock()
	return refreshChannelCacheLocked()
}

// Caller holds channelCacheRefreshLock, including any preceding route commit.
func refreshChannelCacheLocked() (err error) {
	defer func() {
		channelSyncLock.Lock()
		channelCacheRefreshError = err
		channelSyncLock.Unlock()
	}()
	if !common.MemoryCacheEnabled {
		InvalidatePricingCache()
		rebuildTaskAliasView()
		return nil
	}
	if DB == nil {
		return errors.New("channel cache database is not initialized")
	}
	var channels []*Channel
	var abilities []Ability
	// Repeatable read keeps the two source tables from different commits from
	// being combined. SQLite supplies its normal read-transaction snapshot.
	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Find(&channels).Error; err != nil {
			return fmt.Errorf("load channels: %w", err)
		}
		if err := tx.Where("enabled = ?", true).Find(&abilities).Error; err != nil {
			return fmt.Errorf("load abilities: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}); err != nil {
		return err
	}
	newChannelId2channel := make(map[int]*Channel, len(channels))
	newChannel2advancedCustomConfig := make(map[int]*kitdto.AdvancedCustomConfig)
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		newChannelId2channel[channel.Id] = channel
		if channel.Type == constant.ChannelTypeAdvancedCustom {
			settings := kitdto.ChannelOtherSettings{}
			if channel.OtherSettings != "" {
				if err := common.UnmarshalJsonStr(channel.OtherSettings, &settings); err != nil {
					return fmt.Errorf("channel %d settings: %w", channel.Id, err)
				}
			}
			if settings.AdvancedCustom != nil {
				newChannel2advancedCustomConfig[channel.Id] = settings.AdvancedCustom
			}
		}
	}
	newCandidates := make(map[string]map[string][]cachedRouteCandidate)
	newGroup2model2channels := make(map[string]map[string][]int)
	for _, ability := range abilities {
		channel := newChannelId2channel[ability.ChannelId]
		if !ability.Enabled || channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if newCandidates[ability.Group] == nil {
			newCandidates[ability.Group] = make(map[string][]cachedRouteCandidate)
			newGroup2model2channels[ability.Group] = make(map[string][]int)
		}
		priority := int64(0)
		if ability.Priority != nil {
			priority = *ability.Priority
		}
		newCandidates[ability.Group][ability.Model] = append(newCandidates[ability.Group][ability.Model], cachedRouteCandidate{channelID: ability.ChannelId, priority: priority})
	}
	for group, models := range newCandidates {
		for model, candidates := range models {
			sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].priority > candidates[j].priority })
			for _, candidate := range candidates {
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], candidate.channelID)
			}
		}
	}
	channelSyncLock.Lock()
	for i, channel := range newChannelId2channel {
		if channel.ChannelInfo.IsMultiKey {
			channel.Keys = channel.GetKeys()
			if channel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
				if oldChannel := channelsIDM[i]; channelCacheRefreshError == nil && oldChannel != nil && oldChannel.ChannelInfo.IsMultiKey && oldChannel.ChannelInfo.MultiKeyMode == constant.MultiKeyModePolling {
					channel.ChannelInfo.MultiKeyPollingIndex = oldChannel.ChannelInfo.MultiKeyPollingIndex
				}
			}
		}
	}
	group2model2channels = newGroup2model2channels
	group2model2routeCandidates = newCandidates
	channelsIDM = newChannelId2channel
	channel2advancedCustomConfig = newChannel2advancedCustomConfig
	channelSyncLock.Unlock()
	// Pricing takes updatePricingLock before channelSyncLock: never invalidate
	// while holding channelSyncLock (AB-BA deadlock).
	InvalidatePricingCache()
	rebuildTaskAliasView()
	common.SysLog("channels synced from database")
	return nil
}

// Caller holds channelSyncLock. Filtering must precede retry-tier selection.
func cachedRouteCandidates(group, key, requestedModel string, filters []dto.ChannelFilter) []cachedRouteCandidate {
	candidates := group2model2routeCandidates[group][key]
	filtered := make([]cachedRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		channel := channelsIDM[candidate.channelID]
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if ok, _ := ChannelSatisfiesFilters(channel, requestedModel, filters); ok {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func SyncChannelCache(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		common.SysLog("syncing channels from database")
		InitChannelCache()
	}
}

func GetRandomSatisfiedChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	// if memory cache is disabled, get channel directly from database
	if !common.MemoryCacheEnabled {
		return GetChannel(group, model, retry, filters)
	}

	channelSyncLock.RLock()
	if group2model2routeCandidates == nil {
		channelSyncLock.RUnlock()
		return nil, errors.New("channel cache has no successful snapshot")
	}
	if channelCacheRefreshError != nil {
		channelSyncLock.RUnlock()
		return GetChannel(group, model, retry, filters)
	}
	defer channelSyncLock.RUnlock()

	if retry < 0 {
		retry = 0
	}
	// Keep the actual matched key attached to candidates; normalized fallback
	// must not look priorities up under the original requested key.
	candidates := cachedRouteCandidates(group, model, model, filters)
	if len(candidates) == 0 {
		normalizedModel := ratio_setting.FormatMatchingModelName(model)
		if normalizedModel != model {
			candidates = cachedRouteCandidates(group, normalizedModel, model, filters)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if len(candidates) == 1 {
		return channelsIDM[candidates[0].channelID], nil
	}
	uniquePriorities := make(map[int64]bool)
	for _, candidate := range candidates {
		uniquePriorities[candidate.priority] = true
	}
	priorities := make([]int64, 0, len(uniquePriorities))
	for priority := range uniquePriorities {
		priorities = append(priorities, priority)
	}
	sort.Slice(priorities, func(i, j int) bool { return priorities[i] > priorities[j] })
	if retry >= len(priorities) {
		retry = len(priorities) - 1
	}
	targetPriority := priorities[retry]
	var sumWeight int
	var targetChannels []*Channel
	for _, candidate := range candidates {
		if candidate.priority == targetPriority {
			channel := channelsIDM[candidate.channelID]
			sumWeight += channel.GetWeight()
			targetChannels = append(targetChannels, channel)
		}
	}

	// smoothing factor and adjustment
	smoothingFactor := 1
	smoothingAdjustment := 0

	if sumWeight == 0 {
		// when all channels have weight 0, set sumWeight to the number of channels and set smoothing adjustment to 100
		// each channel's effective weight = 100
		sumWeight = len(targetChannels) * 100
		smoothingAdjustment = 100
	} else if sumWeight/len(targetChannels) < 10 {
		// when the average weight is less than 10, set smoothing factor to 100
		smoothingFactor = 100
	}

	// Calculate the total weight of all channels up to endIdx
	totalWeight := sumWeight * smoothingFactor

	// Generate a random value in the range [0, totalWeight)
	randomWeight := rand.Intn(totalWeight)

	// Find a channel based on its weight
	for _, channel := range targetChannels {
		randomWeight -= channel.GetWeight()*smoothingFactor + smoothingAdjustment
		if randomWeight < 0 {
			return channel, nil
		}
	}
	// return null if no channel is not found
	return nil, errors.New("channel not found")
}

func CacheGetChannel(id int) (*Channel, error) {
	if !common.MemoryCacheEnabled {
		return GetChannelById(id, true)
	}
	channelSyncLock.RLock()
	if channelCacheRefreshError != nil {
		channelSyncLock.RUnlock()
		return GetChannelById(id, true)
	}
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return c, nil
}

func CacheGetChannelInfo(id int) (*ChannelInfo, error) {
	info, _, err := channelPollingInfo(id)
	return info, err
}

// The persistence decision belongs to the same read as the cursor: degraded
// cache routing must not depend on a missing or stale old ChannelInfo entry.
func channelPollingInfo(id int) (*ChannelInfo, bool, error) {
	channelSyncLock.RLock()
	useDB := !common.MemoryCacheEnabled || channelCacheRefreshError != nil
	if useDB {
		channelSyncLock.RUnlock()
		channel, err := GetChannelById(id, true)
		if err != nil {
			return nil, true, err
		}
		return &channel.ChannelInfo, true, nil
	}
	defer channelSyncLock.RUnlock()

	c, ok := channelsIDM[id]
	if !ok {
		return nil, false, fmt.Errorf("渠道# %d，已不存在", id)
	}
	return &c.ChannelInfo, false, nil
}

// Enable must be called after the persistent channel/ability update commits.
// Re-reading is essential: a disabled snapshot may contain no candidates to restore.
func CacheUpdateChannelStatus(id int, status int) {
	if !common.MemoryCacheEnabled {
		return
	}
	if status == common.ChannelStatusEnabled {
		InitChannelCache()
		return
	}
	channelCacheRefreshLock.Lock()
	defer channelCacheRefreshLock.Unlock()
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	if channel := channelsIDM[id]; channel != nil {
		updated := *channel
		updated.Status = status
		channelsIDM[id] = &updated
	}
	removeCachedChannelRoutes(id)
}

// Caller holds channelSyncLock. Keep the membership and selection indexes in sync.
func removeCachedChannelRoutes(id int) {
	for group, models := range group2model2routeCandidates {
		for model, candidates := range models {
			kept := make([]cachedRouteCandidate, 0, len(candidates))
			ids := make([]int, 0, len(candidates))
			for _, candidate := range candidates {
				if candidate.channelID != id {
					kept = append(kept, candidate)
					ids = append(ids, candidate.channelID)
				}
			}
			group2model2routeCandidates[group][model] = kept
			group2model2channels[group][model] = ids
		}
	}
}

func CacheUpdateChannel(channel *Channel) {
	if !common.MemoryCacheEnabled {
		return
	}
	if channel == nil {
		return
	}
	channelCacheRefreshLock.Lock()
	channelSyncLock.Lock()
	oldChannel := channelsIDM[channel.Id]
	if channel.Status == common.ChannelStatusEnabled && oldChannel != nil && oldChannel.Status != common.ChannelStatusEnabled {
		channelSyncLock.Unlock()
		channelCacheRefreshLock.Unlock()
		InitChannelCache()
		return
	}
	defer channelCacheRefreshLock.Unlock()

	if channelsIDM == nil {
		channelsIDM = make(map[int]*Channel)
	}
	if oldChannel := channelsIDM[channel.Id]; oldChannel != nil {
		logger.LogDebug(nil, "CacheUpdateChannel before: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, oldChannel.ChannelInfo.MultiKeyPollingIndex)
	}
	channelsIDM[channel.Id] = channel
	if channel.Status != common.ChannelStatusEnabled {
		removeCachedChannelRoutes(channel.Id)
	}
	if channel2advancedCustomConfig == nil {
		channel2advancedCustomConfig = make(map[int]*kitdto.AdvancedCustomConfig)
	}
	delete(channel2advancedCustomConfig, channel.Id)
	if channel.Type == constant.ChannelTypeAdvancedCustom {
		if config := channel.GetOtherSettings().AdvancedCustom; config != nil {
			channel2advancedCustomConfig[channel.Id] = config
		}
	}
	logger.LogDebug(nil, "CacheUpdateChannel after: id=%d, name=%s, status=%d, polling_index=%d", channel.Id, channel.Name, channel.Status, channel.ChannelInfo.MultiKeyPollingIndex)
	// Lock ordering: do NOT hold channelSyncLock while calling
	// InvalidatePricingCache. GetPricing acquires updatePricingLock first and then
	// channelSyncLock.RLock (via loadPricingAdvancedCustomConfigs); acquiring
	// updatePricingLock while holding channelSyncLock would be an AB-BA deadlock.
	channelSyncLock.Unlock()
	InvalidatePricingCache()
}
