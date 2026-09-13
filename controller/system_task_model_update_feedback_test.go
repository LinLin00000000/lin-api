package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Exercise the actual leased handler, finish consumer and HTTP history against
// a disposable database. Only upstream HTTP and database failures are injected.
func TestRouteModelUpdateTaskHistory(t *testing.T) {
	// Routing snapshots have no reset API. Isolate this fixture so its closed
	// SQLite handle and cached channels cannot affect other controller tests.
	if os.Getenv("LINAPI_TASK_FEEDBACK_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRouteModelUpdateTaskHistory$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "LINAPI_TASK_FEEDBACK_CHILD=1")
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	oldDB, oldLog := model.DB, model.LOG_DB
	oldCache, oldRedis := common.MemoryCacheEnabled, common.RedisEnabled
	oldMain, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	oldInterval := common.RequestInterval
	t.Cleanup(func() {
		model.DB, model.LOG_DB = oldDB, oldLog
		common.MemoryCacheEnabled, common.RedisEnabled = oldCache, oldRedis
		common.SetDatabaseTypes(oldMain, oldLogType)
		common.RequestInterval = oldInterval
	})
	initModelListColumnNames(t)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.RequestInterval = 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m"},{"id":"new"}]}`)
	}))
	defer upstream.Close()
	for _, mode := range []string{"success", "scan_failure", "partial", "degraded"} {
		t.Run(mode, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "task.db")), &gorm.Config{})
			require.NoError(t, err)
			sql, err := db.DB()
			require.NoError(t, err)
			defer sql.Close()
			model.DB, model.LOG_DB = db, db
			common.MemoryCacheEnabled = false
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			channel := model.Channel{Name: "fixture", Key: "synthetic-not-a-secret", Type: 1, Status: 1, Models: "m", Group: "default", BaseURL: &upstream.URL}
			channel.SetOtherSettings(dto.ChannelOtherSettings{UpstreamModelUpdateCheckEnabled: true, UpstreamModelUpdateAutoSyncEnabled: true})
			require.NoError(t, channel.Insert())
			if mode == "partial" {
				broken := model.Channel{Name: "broken", Key: "synthetic", Type: 1, Status: 1, Models: "m", Group: "default", OtherSettings: "{broken"}
				require.NoError(t, broken.Insert())
			}
			common.MemoryCacheEnabled = true
			require.NoError(t, model.RefreshChannelCache())
			// Suppress notification delivery at its existing throttling boundary.
			channelUpstreamModelUpdateNotifyState.Lock()
			prevTime, prevChanged, prevFailed := channelUpstreamModelUpdateNotifyState.lastNotifiedAt, channelUpstreamModelUpdateNotifyState.lastChangedChannels, channelUpstreamModelUpdateNotifyState.lastFailedChannels
			channelUpstreamModelUpdateNotifyState.lastNotifiedAt = common.GetTimestamp()
			channelUpstreamModelUpdateNotifyState.lastChangedChannels = 1
			channelUpstreamModelUpdateNotifyState.lastFailedChannels = 0
			if mode == "partial" {
				channelUpstreamModelUpdateNotifyState.lastFailedChannels = 1
			}
			channelUpstreamModelUpdateNotifyState.Unlock()
			defer func() {
				channelUpstreamModelUpdateNotifyState.Lock()
				defer channelUpstreamModelUpdateNotifyState.Unlock()
				channelUpstreamModelUpdateNotifyState.lastNotifiedAt, channelUpstreamModelUpdateNotifyState.lastChangedChannels, channelUpstreamModelUpdateNotifyState.lastFailedChannels = prevTime, prevChanged, prevFailed
			}()
			refreshAttempts := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("feedback_failure", func(tx *gorm.DB) {
				if tx.Statement.Table != "channels" {
					return
				}
				if mode == "scan_failure" && len(tx.Statement.Selects) > 0 {
					tx.AddError(errors.New("synthetic scan failure"))
				}
				if len(tx.Statement.Selects) == 0 {
					if _, ok := tx.Statement.Clauses["WHERE"]; !ok {
						refreshAttempts++
						if mode == "degraded" {
							tx.AddError(errors.New("synthetic refresh failure"))
						}
					}
				}
			}))
			task, err := model.CreateSystemTask(model.SystemTaskTypeModelUpdate, modelUpdateTaskPayload{}, nil)
			require.NoError(t, err)
			leased, claimed, err := model.ClaimSystemTask(task.ID, task.Type, "fixture-runner", common.GetTimestamp()+300)
			require.NoError(t, err)
			require.True(t, claimed)
			var lock model.SystemTaskLock
			require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&lock).Error)
			require.Greater(t, lock.LockedUntil, common.GetTimestamp())
			modelUpdateHandler{}.Run(context.Background(), leased, "fixture-runner")
			require.NoError(t, db.Callback().Query().Remove("feedback_failure"))
			stored, err := model.GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			expected := model.SystemTaskStatusSucceeded
			if mode == "scan_failure" || mode == "partial" {
				expected = model.SystemTaskStatusFailed
			}
			require.Equal(t, expected, stored.Status)
			if expected == model.SystemTaskStatusFailed {
				require.NotEmpty(t, stored.Error)
			} else {
				require.Empty(t, stored.Error)
			}
			require.Nil(t, stored.ActiveKey)
			var remaining int64
			require.NoError(t, db.Model(&model.SystemTaskLock{}).Count(&remaining).Error)
			require.Zero(t, remaining)
			var summary upstreamModelUpdateSummary
			require.NoError(t, json.Unmarshal([]byte(stored.Result), &summary))
			require.Equal(t, mode != "scan_failure", summary.ScanComplete)
			if mode == "scan_failure" {
				require.Empty(t, summary.Outcomes)
				require.Zero(t, refreshAttempts)
			} else {
				require.Equal(t, 1, summary.AutoAddedModels)
				require.True(t, summary.Outcomes[0].Committed)
				require.Equal(t, mode == "degraded", summary.Outcomes[0].Degraded)
				require.Equal(t, 1, refreshAttempts, "handler must not refresh a second time to guess transaction outcome")
				fresh, err := model.GetChannelById(channel.Id, true)
				require.NoError(t, err)
				require.Equal(t, "m,new", fresh.Models)
				if mode == "degraded" {
					require.NotEmpty(t, summary.Outcomes[0].RefreshError)
					require.Empty(t, summary.Outcomes[0].Error)
					require.Zero(t, summary.FailedChannels)
				}
				if mode == "partial" {
					require.Equal(t, 1, summary.FailedChannels)
					require.Len(t, summary.Outcomes, 2)
					require.False(t, summary.Outcomes[1].Committed)
					require.NotEmpty(t, summary.Outcomes[1].Error)
				}
			}
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.GET("/api/system/task", ListSystemTasks)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest("GET", "/api/system/task?limit=20", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var response struct {
				Success bool `json:"success"`
				Data    []struct {
					TaskID string                 `json:"task_id"`
					Status model.SystemTaskStatus `json:"status"`
					Error  string                 `json:"error"`
					Result json.RawMessage        `json:"result"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.True(t, response.Success)
			require.Len(t, response.Data, 1)
			require.Equal(t, task.TaskID, response.Data[0].TaskID)
			require.Equal(t, expected, response.Data[0].Status)
			require.Equal(t, stored.Error, response.Data[0].Error)
			require.JSONEq(t, stored.Result, string(response.Data[0].Result))
			require.NotContains(t, rec.Body.String(), channel.Key)
		})
	}
}
