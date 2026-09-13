package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRouteAPIA2HTTP(t *testing.T) {
	// authz has no reset/export API. Isolate its global enforcer (and routing
	// snapshots) in a child of this same test binary rather than leaking a
	// closed fixture adapter into unrelated router tests.
	if os.Getenv("LINAPI_A2_HTTP_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRouteAPIA2HTTP$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "LINAPI_A2_HTTP_CHILD=1")
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	oldDB, oldLog := model.DB, model.LOG_DB

	oldMaster, oldPath := common.IsMasterNode, common.SQLitePath
	oldMainType, oldLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.IsMasterNode = false
	common.SQLitePath = filepath.Join(t.TempDir(), "init.db")
	t.Setenv("SQL_DSN", "local")
	require.NoError(t, model.InitDB())
	initSQL, _ := model.DB.DB()
	require.NoError(t, initSQL.Close())
	common.IsMasterNode = oldMaster
	common.SQLitePath = oldPath
	t.Cleanup(func() { common.SetDatabaseTypes(oldMainType, oldLogType) })
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "route.db")), &gorm.Config{})
	require.NoError(t, err)
	oldRedis := common.RedisEnabled
	common.RedisEnabled = false
	model.LOG_DB = db
	t.Cleanup(func() { common.RedisEnabled = oldRedis; model.LOG_DB = oldLog })
	oldCache := common.MemoryCacheEnabled
	model.DB = db
	common.MemoryCacheEnabled = true
	t.Cleanup(func() { model.DB = oldDB; common.MemoryCacheEnabled = oldCache; sql, _ := db.DB(); sql.Close() })
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.User{}, &model.Log{}, &model.CasbinRule{}, &model.AuthzRole{}))
	require.NoError(t, authz.Init(db))
	require.NoError(t, authz.SetUserPermissions(2, authz.PermissionsMap{"channel": {"read": true, "write": false}}))
	a := model.Channel{Name: "first", Key: "synthetic", Type: 1, Status: 1, Models: "m, exact ", Group: "default,pro"}
	b := model.Channel{Name: "disabled", Key: "synthetic", Type: 1, Status: 2, Models: "m", Group: "pro"}
	require.NoError(t, a.Insert())
	require.NoError(t, b.Insert())
	gin.SetMode(gin.TestMode)
	e := gin.New()
	// Real permission middleware and production route table; authentication is
	// supplied at the boundary, with no sessions or external identity provider.
	e.Use(func(c *gin.Context) {
		c.Set("id", 1)
		if c.GetHeader("X-Test-Root") == "yes" {
			c.Set("role", common.RoleRootUser)
		} else if c.GetHeader("X-Test-Reader") == "yes" {
			c.Set("id", 2)
			c.Set("role", common.RoleAdminUser)
		} else {
			c.Set("role", common.RoleCommonUser)
		}
	})
	for _, r := range channelPermissionRoutes {
		e.Handle(r.method, "/api/channel"+r.path, middleware.RequirePermission(r.permission), r.handler)
	}
	s := httptest.NewServer(e)
	defer s.Close()
	call := func(method, path string, body any, root bool) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(method, s.URL+"/api/channel"+path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if root {
			req.Header.Set("X-Test-Root", "yes")
		}
		res, err := s.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		var out map[string]any
		require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
		return res.StatusCode, out
	}
	for _, method := range []string{"GET", "PUT"} {
		req, e := http.NewRequest(method, s.URL+"/api/channel/model_priority?group=pro&model=m", bytes.NewBufferString(`{"group":"pro","model":"m","channel_id":1,"priority":9}`))
		require.NoError(t, e)
		req.Header.Set("X-Test-Reader", "yes")
		req.Header.Set("Content-Type", "application/json")
		res, e := s.Client().Do(req)
		require.NoError(t, e)
		res.Body.Close()
		if method == "GET" {
			require.Equal(t, 200, res.StatusCode)
		} else {
			require.Equal(t, 403, res.StatusCode)
		}
	}
	code, out := call("GET", "/model_priority?group=pro&model=m", nil, false)
	require.NotEqual(t, 200, code)
	require.Equal(t, false, out["success"])
	code, out = call("GET", "/model_priority?group=pro&model=m", nil, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["success"])
	require.Len(t, out["data"], 2)
	_, out = call("GET", "/model_priority/options", nil, true)
	require.Len(t, out["data"], 4)
	payload := map[string]any{"group": "pro", "model": " exact ", "channel_id": a.Id, "priority": -19}
	code, out = call("PUT", "/model_priority", payload, false)
	require.NotEqual(t, 200, code)
	code, out = call("PUT", "/model_priority", payload, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["committed"])
	var ability model.Ability
	require.NoError(t, db.Where(map[string]any{"group": "pro", "model": " exact ", "channel_id": a.Id}).First(&ability).Error)
	require.Equal(t, int64(-19), *ability.Priority)
	require.NoError(t, db.Where(map[string]any{"group": "default", "model": " exact ", "channel_id": a.Id}).First(&model.Ability{}).Error)
	payload["model"] = "exact"
	code, _ = call("PUT", "/model_priority", payload, true)
	require.Equal(t, 400, code)
	payload["model"] = "m"
	payload["extra"] = 1
	code, _ = call("PUT", "/model_priority", payload, true)
	require.Equal(t, 400, code)
	delete(payload, "extra")
	_, out = call("GET", fmt.Sprintf("/%d", a.Id), nil, true)
	data := out["data"].(map[string]any)
	revision := data["revision"]
	require.NotEmpty(t, revision)
	edit := map[string]any{"id": a.Id, "name": "edited"}
	code, _ = call("PUT", "/", edit, true)
	require.Equal(t, 400, code)
	edit["revision"] = revision
	code, out = call("PUT", "/", edit, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["success"])
	require.NotEmpty(t, out["data"].(map[string]any)["revision"])
	code, _ = call("PUT", "/", edit, true)
	require.Equal(t, 409, code)
	current, err := model.GetChannelById(a.Id, true)
	require.NoError(t, err)
	require.Equal(t, "edited", current.Name)
	var untouched model.Ability
	require.NoError(t, db.Where(map[string]any{"group": "default", "model": " exact ", "channel_id": a.Id}).First(&untouched).Error)
	require.Equal(t, int64(0), *untouched.Priority)
	// No guessed keys, no missing fields, no side-effect membership creation.
	for _, invalid := range []any{
		map[string]any{"group": "pro", "model": "m", "channel_id": 99999, "priority": 0},
		map[string]any{"group": "pro", "model": "m", "channel_id": a.Id},
		map[string]any{"group": "pro", "model": "m", "channel_id": a.Id, "priority": "1"},
		map[string]any{"group": "pro", "model": "m", "channel_id": a.Id, "priority": 1.5},
	} {
		code, _ = call("PUT", "/model_priority", invalid, true)
		require.Equal(t, 400, code)
	}
	require.NoError(t, db.Create(&model.Ability{Group: "pro", Model: "stale", ChannelId: a.Id}).Error)
	code, _ = call("PUT", "/model_priority", map[string]any{"group": "pro", "model": "stale", "channel_id": a.Id, "priority": 12}, true)
	require.Equal(t, 400, code)
	code, out = call("PUT", "/model_priority", map[string]any{"group": "pro", "model": "m", "channel_id": b.Id, "priority": 88}, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["committed"])
	// Same revision: actual concurrent HTTP saves, exactly one winner.
	_, out = call("GET", fmt.Sprintf("/%d", a.Id), nil, true)
	rev := out["data"].(map[string]any)["revision"]
	codes := make(chan int, 2)
	for _, name := range []string{"winner-one", "winner-two"} {
		go func(name string) {
			status, _ := call("PUT", "/", map[string]any{"id": a.Id, "name": name, "revision": rev}, true)
			codes <- status
		}(name)
	}
	got := []int{<-codes, <-codes}
	require.ElementsMatch(t, []int{200, 409}, got)
	_, out = call("GET", fmt.Sprintf("/%d", a.Id), nil, true)
	rev = out["data"].(map[string]any)["revision"]
	require.NoError(t, model.AddChannelModels(a.Id, []string{"added-exact"}))
	code, _ = call("PUT", "/", map[string]any{"id": a.Id, "models": "m", "revision": rev}, true)
	require.Equal(t, 409, code)
	current, err = model.GetChannelById(a.Id, true)
	require.NoError(t, err)
	require.Contains(t, current.Models, "added-exact")
	// Two enabled channels exercise the real selector in both cache and DB mode.
	c2 := model.Channel{Name: "second", Key: "synthetic", Type: 1, Status: 1, Models: "m", Group: "default,pro"}
	require.NoError(t, c2.Insert())
	set := func(id int, p int) {
		t.Helper()
		code, out = call("PUT", "/model_priority", map[string]any{"group": "pro", "model": "m", "channel_id": id, "priority": p}, true)
		require.Equal(t, 200, code)
		require.Equal(t, true, out["success"])
	}
	set(a.Id, 30)
	set(c2.Id, 40)
	_, out = call("GET", "/model_priority?group=pro&model=m", nil, true)
	rows := out["data"].([]any)
	require.Equal(t, float64(b.Id), rows[0].(map[string]any)["channel_id"])
	for _, cache := range []bool{false, true} {
		common.MemoryCacheEnabled = cache
		selected, e := model.GetRandomSatisfiedChannel("pro", "m", 0, nil)
		require.NoError(t, e)
		require.Equal(t, c2.Id, selected.Id)
		selected, e = model.GetRandomSatisfiedChannel("pro", "m", 1, nil)
		require.NoError(t, e)
		require.Equal(t, a.Id, selected.Id)
	}
	common.MemoryCacheEnabled = true
	// Bad disabled configuration breaks refresh only. The committed priority is
	// still observable through HTTP and DB, and selector switches to DB fallback.
	bad := model.Channel{Type: constant.ChannelTypeAdvancedCustom, Status: 2, OtherSettings: "{"}
	require.NoError(t, db.Create(&bad).Error)
	set(a.Id, 60)
	require.Equal(t, true, out["degraded"])
	require.Equal(t, true, out["committed"])
	require.NotEmpty(t, out["refresh_error"])
	require.Contains(t, out["message"], "Do not resend")
	_, out = call("GET", "/model_priority?group=pro&model=m", nil, true)
	found := false
	for _, row := range out["data"].([]any) {
		v := row.(map[string]any)
		if v["channel_id"] == float64(a.Id) {
			require.Equal(t, float64(60), v["priority"])
			found = true
		}
	}
	require.True(t, found)
	selected, e2 := model.GetRandomSatisfiedChannel("pro", "m", 0, nil)
	require.NoError(t, e2)
	require.Equal(t, a.Id, selected.Id)
	code, out = call("POST", fmt.Sprintf("/%d/status", a.Id), map[string]any{"status": 2}, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["degraded"])
	require.Equal(t, true, out["committed"])
	selected, e2 = model.GetRandomSatisfiedChannel("pro", "m", 0, nil)
	require.NoError(t, e2)
	require.Equal(t, c2.Id, selected.Id)
	code, out = call("POST", "/status/batch", map[string]any{"ids": []int{a.Id, c2.Id}, "status": 1}, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["degraded"])
	_, out = call("GET", fmt.Sprintf("/%d", a.Id), nil, true)
	code, out = call("PUT", "/", map[string]any{"id": a.Id, "name": "degraded-save", "revision": out["data"].(map[string]any)["revision"]}, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["committed"])
	require.Equal(t, true, out["degraded"])
	require.NotEmpty(t, out["data"].(map[string]any)["revision"])
	current, err = model.GetChannelById(a.Id, true)
	require.NoError(t, err)
	require.Equal(t, "degraded-save", current.Name)
	code, out = call("POST", "/status/batch", map[string]any{"ids": []int{a.Id, 999999}, "status": 2}, true)
	require.Equal(t, false, out["committed"])
	current, err = model.GetChannelById(a.Id, true)
	require.NoError(t, err)
	require.Equal(t, 1, current.Status)
	require.NoError(t, db.Delete(&bad).Error)
	require.NoError(t, model.RefreshChannelCache())
	// All six server-side multi-key mutations share revision CAS. Exercise a
	// real multi-key write and ensure it does not disturb exact route priority.
	multi := model.Channel{Name: "multi", Type: 1, Status: 1, Key: "k0\nk1", Models: "m", Group: "pro", ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2}}
	require.NoError(t, multi.Insert())
	code, out = call("POST", "/multi_key/manage", map[string]any{"channel_id": multi.Id, "action": "disable_key", "key_index": 0}, true)
	require.Equal(t, 200, code)
	require.Equal(t, true, out["success"])
	current, err = model.GetChannelById(multi.Id, true)
	require.NoError(t, err)
	require.Equal(t, 2, current.ChannelInfo.MultiKeyStatusList[0])
}
