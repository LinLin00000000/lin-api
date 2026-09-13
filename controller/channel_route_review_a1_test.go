package controller

import (
	"context"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewA1UpstreamEntryParseFailure(t *testing.T) {
	for _, entry := range []string{"apply", "detect", "scan", "apply_all"} {
		t.Run(entry, func(t *testing.T) {
			db := setupModelListControllerTestDB(t)
			old := common.MemoryCacheEnabled
			common.MemoryCacheEnabled = false
			t.Cleanup(func() { common.MemoryCacheEnabled = old })
			c := model.Channel{Key: "synthetic-preserve", Name: "preserve", Models: "a", Group: "default", Status: common.ChannelStatusEnabled, OtherSettings: "{broken"}
			require.NoError(t, c.Insert())
			if entry == "scan" {
				// Avoid notification side effects; this test only exercises the real scan.
				channelUpstreamModelUpdateNotifyState.Lock()
				previousTime := channelUpstreamModelUpdateNotifyState.lastNotifiedAt
				previousChanged := channelUpstreamModelUpdateNotifyState.lastChangedChannels
				previousFailed := channelUpstreamModelUpdateNotifyState.lastFailedChannels
				channelUpstreamModelUpdateNotifyState.lastNotifiedAt = common.GetTimestamp()
				channelUpstreamModelUpdateNotifyState.lastChangedChannels = 0
				channelUpstreamModelUpdateNotifyState.lastFailedChannels = 1
				channelUpstreamModelUpdateNotifyState.Unlock()
				defer func() {
					channelUpstreamModelUpdateNotifyState.Lock()
					defer channelUpstreamModelUpdateNotifyState.Unlock()
					channelUpstreamModelUpdateNotifyState.lastNotifiedAt = previousTime
					channelUpstreamModelUpdateNotifyState.lastChangedChannels = previousChanged
					channelUpstreamModelUpdateNotifyState.lastFailedChannels = previousFailed
				}()
				added := false
				summary := runChannelUpstreamModelUpdateTaskOnce(context.Background(), true, true, func(processed, total int) {
					if !added && processed == 1 {
						added = true
						require.NoError(t, model.AddChannelModels(c.Id, []string{"b"}))
					}
				})
				require.Equal(t, 1, summary.FailedChannels)
			} else {
				require.NoError(t, model.AddChannelModels(c.Id, []string{"b"}))
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				ctx.Request = httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"id":%d,"add_models":["x"]}`, c.Id)))
				ctx.Request.Header.Set("Content-Type", "application/json")
				if entry == "apply_all" {
					require.NoError(t, db.AutoMigrate(&model.Log{}))
					ApplyAllChannelUpstreamModelUpdates(ctx)
					require.Contains(t, rec.Body.String(), fmt.Sprintf(`"failed_channel_ids":[%d]`, c.Id))
				} else {
					if entry == "apply" {
						ApplyChannelUpstreamModelUpdates(ctx)
					} else {
						DetectChannelUpstreamModelUpdates(ctx)
					}
					require.Contains(t, rec.Body.String(), `"success":false`)
					require.Contains(t, rec.Body.String(), "settings")
				}
			}
			fresh, err := model.GetChannelById(c.Id, true)
			require.NoError(t, err)
			require.Equal(t, "a,b", fresh.Models)
			require.Equal(t, "{broken", fresh.OtherSettings)
			require.Equal(t, c.Key, fresh.Key)
			var n int64
			require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ? AND model = ?", c.Id, "b").Count(&n).Error)
			require.EqualValues(t, 1, n)
		})
	}
}
