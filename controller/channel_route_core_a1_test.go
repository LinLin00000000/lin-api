package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteCoreUpstreamDeltaAndRollback(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	old := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = old })
	c := model.Channel{Key: "synthetic", Models: "a, exact ", Group: "default,pro"}
	c.SetOtherSettings(dto.ChannelOtherSettings{UpstreamModelUpdateLastDetectedModels: []string{"b"}, UpstreamModelUpdateLastRemovedModels: []string{"a"}})
	require.NoError(t, c.Insert())
	stale := c
	require.NoError(t, model.AddChannelModels(c.Id, []string{"concurrent"}))
	require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", c.Id).Update("priority", -21).Error)
	added, removed, _, _, changed, err := applyChannelUpstreamModelUpdates(&stale, []string{"b"}, nil, []string{"a"})
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, []string{"b"}, added)
	require.Equal(t, []string{"a"}, removed)
	require.Equal(t, " exact ,concurrent,b", stale.Models)
	var a model.Ability
	require.NoError(t, db.Where("channel_id = ? AND model = ?", c.Id, "concurrent").First(&a).Error)
	require.Equal(t, int64(-21), *a.Priority)
	// Exercise the actual auto-sync writer; network is a local synthetic server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"auto"},{"id":"rollback"}]}`))
	}))
	defer server.Close()
	current, err := model.GetChannelById(c.Id, true)
	require.NoError(t, err)
	settings := current.GetOtherSettings()
	settings.UpstreamModelUpdateAutoSyncEnabled = true
	current.SetOtherSettings(settings)
	current.Type = 1
	current.BaseURL = &server.URL
	require.NoError(t, current.Save())
	stale = *current
	require.NoError(t, model.AddChannelModels(c.Id, []string{"later"}))
	changed, addedCount, err := checkAndPersistChannelUpstreamModelUpdates(&stale, &settings, true, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, 2, addedCount)
	require.Contains(t, stale.Models, " exact ,concurrent,b,later")
	// Remove a discovered model locally so the next real auto-sync tries to add
	// it, then inject projection failure and verify both tables roll back.
	current, err = model.GetChannelById(c.Id, true)
	require.NoError(t, err)
	current.Models = " exact ,concurrent,b,later,auto"
	require.NoError(t, current.Save())
	before, err := model.GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TRIGGER reject_projection BEFORE INSERT ON abilities BEGIN SELECT RAISE(ABORT, 'injected'); END").Error)
	settings = before.GetOtherSettings()
	_, _, err = checkAndPersistChannelUpstreamModelUpdates(before, &settings, true, true)
	require.Error(t, err)
	after, err := model.GetChannelById(c.Id, true)
	require.NoError(t, err)
	require.Equal(t, model.ChannelRevision(current), model.ChannelRevision(after))
}
