package model

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Ability struct {
	Group     string  `json:"group" gorm:"type:varchar(64);primaryKey;autoIncrement:false"`
	Model     string  `json:"model" gorm:"type:varchar(255);primaryKey;autoIncrement:false"`
	ChannelId int     `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool    `json:"enabled"`
	Priority  *int64  `json:"priority" gorm:"bigint;default:0;index"`
	Weight    uint    `json:"weight" gorm:"default:0;index"`
	Tag       *string `json:"tag" gorm:"index"`
}

type AbilityWithChannel struct {
	Ability
	ChannelType int `json:"channel_type"`
}

func GetAllEnableAbilityWithChannels() ([]AbilityWithChannel, error) {
	var abilities []AbilityWithChannel
	err := DB.Table("abilities").
		Select("abilities.*, channels.type as channel_type").
		Joins("left join channels on abilities.channel_id = channels.id").
		Where("abilities.enabled = ?", true).
		Scan(&abilities).Error
	return abilities, err
}

func GetGroupEnabledModels(group string) []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where(commonGroupCol+" = ? and enabled = ?", group, true).Distinct("model").Pluck("model", &models)
	return models
}

func GetEnabledModels() []string {
	var models []string
	// Find distinct models
	DB.Table("abilities").Where("enabled = ?", true).Distinct("model").Pluck("model", &models)
	return models
}

func GetAllEnableAbilities() []Ability {
	var abilities []Ability
	DB.Find(&abilities, "enabled = ?", true)
	return abilities
}

func GetChannel(
	group string,
	model string,
	retry int,
	filters []dto.ChannelFilter,
) (*Channel, error) {
	var abilities []Ability
	if retry < 0 {
		retry = 0
	}
	err := DB.Where(commonGroupCol+" = ? and model = ? and enabled = ?", group, model, true).Where("channel_id IN (?)", DB.Model(&Channel{}).Select("id").Where("status = ?", common.ChannelStatusEnabled)).Order("priority DESC, weight DESC").Find(&abilities).Error
	if err != nil {
		return nil, err
	}
	abilities, err = filterAbilitiesByConstraints(abilities, model, filters)
	if err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		normalized := ratio_setting.FormatMatchingModelName(model)
		if normalized != model {
			err = DB.Where(commonGroupCol+" = ? AND model = ? AND enabled = ?", group, normalized, true).Where("channel_id IN (?)", DB.Model(&Channel{}).Select("id").Where("status = ?", common.ChannelStatusEnabled)).Find(&abilities).Error
			if err != nil {
				return nil, err
			}
			abilities, err = filterAbilitiesByConstraints(abilities, model, filters)
			if err != nil {
				return nil, err
			}
		}
	}
	if len(abilities) > 0 {
		priorities := make([]int64, 0)
		seen := make(map[int64]bool)
		for _, ability := range abilities {
			priority := int64(0)
			if ability.Priority != nil {
				priority = *ability.Priority
			}
			if !seen[priority] {
				seen[priority] = true
				priorities = append(priorities, priority)
			}
		}
		sort.Slice(priorities, func(i, j int) bool { return priorities[i] > priorities[j] })
		if retry >= len(priorities) {
			retry = len(priorities) - 1
		}
		targetPriority := priorities[retry]
		abilities = lo.Filter(abilities, func(ability Ability, _ int) bool {
			return ability.Priority == nil && targetPriority == 0 || ability.Priority != nil && *ability.Priority == targetPriority
		})
	}
	channel := Channel{}
	if len(abilities) > 0 {
		// Randomly choose one
		weightSum := uint(0)
		for _, ability_ := range abilities {
			weightSum += ability_.Weight + 10
		}
		// Randomly choose one
		weight := common.GetRandomInt(int(weightSum))
		for _, ability_ := range abilities {
			weight -= int(ability_.Weight) + 10
			//log.Printf("weight: %d, ability weight: %d", weight, *ability_.Weight)
			if weight <= 0 {
				channel.Id = ability_.ChannelId
				break
			}
		}
	} else {
		return nil, nil
	}
	err = DB.First(&channel, "id = ? AND status = ?", channel.Id, common.ChannelStatusEnabled).Error
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

// filterAbilitiesByConstraints applies the same predicate as the cache and propagates read failures.
func filterAbilitiesByConstraints(abilities []Ability, modelName string, filters []dto.ChannelFilter) ([]Ability, error) {
	if len(abilities) == 0 {
		return nil, nil
	}

	channelIds := make([]int, 0, len(abilities))
	seen := make(map[int]struct{}, len(abilities))
	for _, ability := range abilities {
		if _, ok := seen[ability.ChannelId]; ok {
			continue
		}
		seen[ability.ChannelId] = struct{}{}
		channelIds = append(channelIds, ability.ChannelId)
	}

	var channels []*Channel
	if err := DB.Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
		return nil, err
	}

	channelsByID := make(map[int]*Channel, len(channels))
	for _, channel := range channels {
		channelsByID[channel.Id] = channel
	}

	filtered := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		channel := channelsByID[ability.ChannelId]
		if channel == nil || channel.Status != common.ChannelStatusEnabled {
			continue
		}
		if ok, _ := ChannelSatisfiesFilters(channel, modelName, filters); ok {
			filtered = append(filtered, ability)
		}
	}
	return filtered, nil
}

func (channel *Channel) AddAbilities(tx *gorm.DB) error {
	if tx == nil {
		return channel.UpdateAbilities(nil)
	}
	if channel.Models == "" || channel.Group == "" {
		return nil
	}
	models_ := strings.Split(channel.Models, ",")
	groups_ := strings.Split(channel.Group, ",")
	abilitySet := make(map[string]struct{})
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			key := group + "\x00" + model
			if _, exists := abilitySet[key]; exists {
				continue
			}
			abilitySet[key] = struct{}{}
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == common.ChannelStatusEnabled,
				Priority:  channel.Priority,
				Weight:    uint(channel.GetWeight()),
				Tag:       channel.Tag,
			}
			abilities = append(abilities, ability)
		}
	}
	if len(abilities) == 0 {
		return nil
	}
	// choose DB or provided tx
	useDB := DB
	if tx != nil {
		useDB = tx
	}
	for _, chunk := range lo.Chunk(abilities, 50) {
		err := useDB.Clauses(clause.OnConflict{DoNothing: true}).Create(&chunk).Error
		if err != nil {
			return err
		}
	}
	return nil
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities(tx *gorm.DB) error {
	if tx == nil {
		return RouteTransaction(func(tx *gorm.DB) error {
			rows, err := LockRouteChannels(tx, "id = ?", channel.Id)
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return gorm.ErrRecordNotFound
			}
			return rows[0].UpdateAbilities(tx)
		})
	}
	var existing []Ability
	if err := tx.Where("channel_id = ?", channel.Id).Find(&existing).Error; err != nil {
		return err
	}
	type key struct{ group, model string }
	desired := map[key]bool{}
	if channel.Models != "" && channel.Group != "" {
		for _, m := range strings.Split(channel.Models, ",") {
			for _, g := range strings.Split(channel.Group, ",") {
				desired[key{g, m}] = true
			}
		}
	}
	for _, a := range existing {
		q := tx.Model(&Ability{}).Where(map[string]interface{}{"channel_id": channel.Id, "group": a.Group, "model": a.Model})
		if !desired[key{a.Group, a.Model}] {
			if err := q.Delete(&Ability{}).Error; err != nil {
				return err
			}
			continue
		}
		if err := q.Updates(map[string]interface{}{"enabled": channel.Status == common.ChannelStatusEnabled, "weight": uint(channel.GetWeight()), "tag": channel.Tag}).Error; err != nil {
			return err
		}
		delete(desired, key{a.Group, a.Model})
	}
	for k := range desired {
		a := Ability{Group: k.group, Model: k.model, ChannelId: channel.Id, Enabled: channel.Status == common.ChannelStatusEnabled, Priority: channel.Priority, Weight: uint(channel.GetWeight()), Tag: channel.Tag}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
	}
	return nil
}

// RouteMembershipDiagnostic identifies an exact stored member without changing
// its identity. Position is a zero-based comma-separated member index.
type RouteMembershipDiagnostic struct {
	ChannelID int    `json:"channel_id"`
	Field     string `json:"field"`
	Key       string `json:"key"`
	Position  int    `json:"position"`
}

// ValidateRouteMembership treats a wholly empty field as an empty set, as the
// existing routing contract does. Embedded empty members require manual review.
func (channel *Channel) ValidateRouteMembership() []RouteMembershipDiagnostic {
	var diagnostics []RouteMembershipDiagnostic
	for _, field := range []struct{ name, value string }{{"models", channel.Models}, {"group", channel.Group}} {
		if field.value == "" {
			continue
		}
		for i, key := range strings.Split(field.value, ",") {
			if key == "" {
				diagnostics = append(diagnostics, RouteMembershipDiagnostic{channel.Id, field.name, key, i})
			}
		}
	}
	return diagnostics
}

type RouteMembershipValidationError struct {
	Diagnostics []RouteMembershipDiagnostic
}

func (e *RouteMembershipValidationError) Error() string {
	messages := make([]string, 0, len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		messages = append(messages, fmt.Sprintf("channel_id=%d field=%s key=%q position=%d: empty route member", d.ChannelID, d.Field, d.Key, d.Position))
	}
	return strings.Join(messages, "; ")
}

func FixAbility() (int, int, error) {
	count, failures, _, err := FixAbilityWithResult()
	return count, failures, err
}

func FixAbilityWithResult() (int, int, RouteCommitResult, error) {
	count, failures := 0, 0
	result, err := RouteTransactionWithResult(func(tx *gorm.DB) error {
		count, failures = 0, 0
		channels, err := LockRouteChannels(tx, "1 = 1")
		if err != nil {
			return err
		}
		var diagnostics []RouteMembershipDiagnostic
		for _, c := range channels {
			if issues := c.ValidateRouteMembership(); len(issues) > 0 {
				failures++
				diagnostics = append(diagnostics, issues...)
			}
		}
		// Abort before reconciliation: do not guess how malformed stored membership
		// should be repaired, or partially repair other channels on validation failure.
		if len(diagnostics) > 0 {
			return &RouteMembershipValidationError{Diagnostics: diagnostics}
		}
		for _, c := range channels {
			if err = c.UpdateAbilities(tx); err != nil {
				return err
			}
			count++
		}
		return tx.Where("channel_id NOT IN (?)", tx.Model(&Channel{}).Select("id")).Delete(&Ability{}).Error
	})
	if err != nil {
		return 0, failures, result, err
	}
	return count, 0, result, nil
}
