package model

import (
	"errors"
	"gorm.io/gorm"
	"sort"
	"strings"
)

var ErrUndeclaredChannelRoute = errors.New("unknown or undeclared channel route")

type ChannelModelPriority struct {
	ChannelID int     `json:"channel_id"`
	Name      string  `json:"name"`
	Tag       *string `json:"tag"`
	Status    int     `json:"status"`
	Priority  int64   `json:"priority"`
	Weight    int     `json:"weight"`
}
type ChannelModelPriorityOption struct {
	Group string `json:"group"`
	Model string `json:"model"`
}

func declaresRoute(c *Channel, group, model string) bool {
	has := func(raw, key string) bool {
		for _, v := range strings.Split(raw, ",") {
			if v == key {
				return true
			}
		}
		return false
	}
	return group != "" && model != "" && has(c.Group, group) && has(c.Models, model)
}

// Management discovery includes disabled declarations and never normalizes keys.
func GetChannelModelPriorities(group, model string) ([]ChannelModelPriority, error) {
	result := make([]ChannelModelPriority, 0)
	var abilities []Ability
	err := DB.Where(map[string]any{"group": group, "model": model}).Order("channel_id ASC").Find(&abilities).Error
	if err != nil {
		return nil, err
	}
	for _, a := range abilities {
		var c Channel
		if err = DB.First(&c, a.ChannelId).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !declaresRoute(&c, group, model) {
			continue
		}
		p := int64(0)
		if a.Priority != nil {
			p = *a.Priority
		}
		result = append(result, ChannelModelPriority{c.Id, c.Name, c.Tag, c.Status, p, int(c.GetWeight())})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		return result[i].ChannelID < result[j].ChannelID
	})
	return result, nil
}
func GetChannelModelPriorityOptions() ([]ChannelModelPriorityOption, error) {
	result := make([]ChannelModelPriorityOption, 0)
	var abilities []Ability
	if err := DB.Order("channel_id ASC").Find(&abilities).Error; err != nil {
		return nil, err
	}
	var channels []Channel
	if err := DB.Find(&channels).Error; err != nil {
		return nil, err
	}
	byID := map[int]*Channel{}
	for i := range channels {
		byID[channels[i].Id] = &channels[i]
	}
	seen := map[ChannelModelPriorityOption]bool{}
	for _, a := range abilities {
		c := byID[a.ChannelId]
		key := ChannelModelPriorityOption{a.Group, a.Model}
		if c != nil && declaresRoute(c, a.Group, a.Model) && !seen[key] {
			seen[key] = true
			result = append(result, key)
		}
	}
	return result, nil
}
func SetChannelModelPriority(group, model string, channelID int, priority int64) (RouteCommitResult, error) {
	return RouteTransactionWithResult(func(tx *gorm.DB) error {
		rows, err := LockRouteChannels(tx, "id = ?", channelID)
		if err != nil {
			return err
		}
		if len(rows) != 1 || !declaresRoute(&rows[0], group, model) {
			return ErrUndeclaredChannelRoute
		}
		q := tx.Model(&Ability{}).Where(map[string]any{"group": group, "model": model, "channel_id": channelID})
		var count int64
		if err = q.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrUndeclaredChannelRoute
		}
		return q.Update("priority", priority).Error
	})
}
