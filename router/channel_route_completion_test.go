package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRouteCompletionHTTP(t *testing.T) {
	// authz has no reset/export API. Isolate its global enforcer (and routing
	// snapshots) in a child of this same test binary rather than leaking a
	// closed fixture adapter into unrelated router tests.
	if os.Getenv("LINAPI_COMPLETION_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRouteCompletionHTTP$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "LINAPI_COMPLETION_CHILD=1")
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

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m"},{"id":"new"}]}`)
	}))
	defer upstream.Close()
	for _, family := range []string{"add", "batch_add", "delete", "batch_delete", "disabled_delete", "tag_disable", "tag_enable", "tag_edit", "batch_tag", "copy", "fix", "apply", "apply_all", "detect"} {
		for _, mode := range []string{"healthy", "degraded", "persist_fail"} {
			t.Run(family+"/"+mode, func(t *testing.T) {
				require.NoError(t, db.Exec("DELETE FROM abilities").Error)
				require.NoError(t, db.Exec("DELETE FROM channels").Error)
				tag := "tag"
				priority := int64(17)
				a := model.Channel{Name: "source", Key: "synthetic", Type: 1, Status: 1, Models: "m", Group: "default", Tag: &tag, Priority: &priority, BaseURL: &upstream.URL}
				a.SetOtherSettings(dto.ChannelOtherSettings{UpstreamModelUpdateCheckEnabled: true, UpstreamModelUpdateLastDetectedModels: []string{"new"}})
				if family == "tag_enable" || family == "disabled_delete" {
					a.Status = 2
				}
				require.NoError(t, a.Insert())
				require.NoError(t, db.Model(&model.Ability{}).Where("channel_id = ?", a.Id).Update("priority", 31).Error)
				if family == "fix" {
					require.NoError(t, db.Exec("DELETE FROM abilities").Error)
				}
				method, path, body := "POST", "/", any(map[string]any{"mode": "single", "channel": map[string]any{"name": "created", "key": "synthetic", "type": 1, "status": 1, "models": "m", "group": "default"}})
				switch family {
				case "batch_add":
					body = map[string]any{"mode": "batch", "channel": map[string]any{"name": "created", "key": "one\ntwo", "type": 1, "status": 1, "models": "m", "group": "default"}}
				case "delete":
					method = "DELETE"
					path = fmt.Sprintf("/%d", a.Id)
					body = nil
				case "batch_delete":
					path = "/batch"
					body = map[string]any{"ids": []int{a.Id}}
				case "disabled_delete":
					method = "DELETE"
					path = "/disabled"
					body = nil
				case "tag_disable":
					path = "/tag/disabled"
					body = map[string]any{"tag": tag}
				case "tag_enable":
					path = "/tag/enabled"
					body = map[string]any{"tag": tag}
				case "tag_edit":
					method = "PUT"
					path = "/tag"
					body = map[string]any{"tag": tag, "new_tag": "edited", "priority": 99}
				case "batch_tag":
					path = "/batch/tag"
					body = map[string]any{"ids": []int{a.Id}, "tag": "edited"}
				case "copy":
					path = fmt.Sprintf("/copy/%d", a.Id)
					body = nil
				case "fix":
					path = "/fix"
					body = nil
				case "apply":
					path = "/upstream_updates/apply"
					body = map[string]any{"id": a.Id, "add_models": []string{"new"}}
				case "apply_all":
					path = "/upstream_updates/apply_all"
					body = nil
				case "detect":
					path = "/upstream_updates/detect"
					body = map[string]any{"id": a.Id}
				}
				snapshot := func() string {
					var cs []model.Channel
					var as []model.Ability
					require.NoError(t, db.Order("id").Find(&cs).Error)
					require.NoError(t, db.Order("channel_id").Find(&as).Error)
					b, _ := json.Marshal([]any{cs, as})
					return string(b)
				}
				before := snapshot()
				fail := func(tx *gorm.DB) { tx.AddError(errors.New("synthetic persistence failure")) }
				if mode == "persist_fail" {
					require.NoError(t, db.Callback().Create().Before("gorm:create").Register("completion_fail", fail))
					require.NoError(t, db.Callback().Update().Before("gorm:update").Register("completion_fail", fail))
					require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("completion_fail", fail))
				}
				if mode == "degraded" {
					require.NoError(t, db.Callback().Query().Before("gorm:query").Register("completion_refresh", func(tx *gorm.DB) {
						if tx.Statement.Table == "channels" && len(tx.Statement.Selects) == 0 {
							if _, ok := tx.Statement.Clauses["WHERE"]; !ok {
								tx.AddError(errors.New("synthetic refresh failure"))
							}
						}
					}))
				}
				code, out := call(method, path, body, true)
				db.Callback().Create().Remove("completion_fail")
				db.Callback().Update().Remove("completion_fail")
				db.Callback().Delete().Remove("completion_fail")
				db.Callback().Query().Remove("completion_refresh")
				if mode == "persist_fail" {
					require.NotEqual(t, true, out["committed"], "%v", out)
					require.Equal(t, false, out["success"], "%v", out)
					require.Equal(t, before, snapshot())
					return
				}
				require.Equal(t, 200, code, "%v", out)
				require.Equal(t, true, out["success"], "%v", out)
				require.Equal(t, true, out["committed"], "%v", out)
				require.Equal(t, mode == "degraded", out["degraded"], "%v", out)
				if mode == "degraded" {
					require.NotEmpty(t, out["refresh_error"])
					require.Contains(t, out["message"], "Do not resend")
				}
				var cs []model.Channel
				require.NoError(t, db.Order("id").Find(&cs).Error)
				switch family {
				case "add":
					require.Len(t, cs, 2)
				case "batch_add":
					require.Len(t, cs, 3)
				case "delete", "batch_delete", "disabled_delete":
					require.Empty(t, cs)
					var n int64
					require.NoError(t, db.Model(&model.Ability{}).Count(&n).Error)
					require.Zero(t, n)
				case "copy":
					require.Len(t, cs, 2)
					var ab model.Ability
					require.NoError(t, db.Where("channel_id = ?", cs[1].Id).First(&ab).Error)
					require.Equal(t, int64(31), *ab.Priority)
				case "tag_disable":
					require.Equal(t, 2, cs[0].Status)
				case "tag_enable":
					require.Equal(t, 1, cs[0].Status)
				case "tag_edit", "batch_tag":
					require.Equal(t, "edited", *cs[0].Tag)
					var ab model.Ability
					require.NoError(t, db.First(&ab).Error)
					require.Equal(t, int64(31), *ab.Priority)
				case "fix":
					var ab model.Ability
					require.NoError(t, db.First(&ab).Error)
					require.Equal(t, int64(17), *ab.Priority)
				case "apply", "apply_all":
					require.Equal(t, []string{"m", "new"}, strings.Split(cs[0].Models, ","))
					var ab model.Ability
					require.NoError(t, db.Where("model = ?", "m").First(&ab).Error)
					require.Equal(t, int64(31), *ab.Priority)
				case "detect":
					settings, e := cs[0].ParseOtherSettings()
					require.NoError(t, e)
					require.Positive(t, settings.UpstreamModelUpdateLastCheckTime)
					require.Equal(t, []string{"new"}, settings.UpstreamModelUpdateLastDetectedModels)
				}
			})
		}
	}

	for _, mode := range []string{"partial", "late_scan_failure", "detect_fetch_failure"} {
		t.Run(mode, func(t *testing.T) {
			require.NoError(t, db.Exec("DELETE FROM abilities").Error)
			require.NoError(t, db.Exec("DELETE FROM channels").Error)
			channels := []model.Channel{}
			n := 2
			if mode == "late_scan_failure" {
				n = 101
			}
			for i := 0; i < n; i++ {
				c := model.Channel{Name: fmt.Sprintf("c%d", i), Key: "synthetic", Type: 1, Status: 1, Models: "m", Group: "default", BaseURL: &upstream.URL}
				c.SetOtherSettings(dto.ChannelOtherSettings{UpstreamModelUpdateCheckEnabled: true, UpstreamModelUpdateLastDetectedModels: []string{"new"}})
				channels = append(channels, c)
			}
			require.NoError(t, model.BatchInsertChannels(channels))
			if mode == "detect_fetch_failure" {
				bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
				defer bad.Close()
				require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("base_url", bad.URL).Error)
				code, out := call("POST", "/upstream_updates/detect", map[string]any{"id": channels[0].Id}, true)
				require.Equal(t, 200, code)
				require.Equal(t, false, out["success"])
				require.Equal(t, true, out["committed"])
				require.Equal(t, true, out["partial"])
				require.Contains(t, out["message"], "Do not resend")
				c, e := model.GetChannelById(channels[0].Id, true)
				require.NoError(t, e)
				s, e := c.ParseOtherSettings()
				require.NoError(t, e)
				require.Positive(t, s.UpstreamModelUpdateLastCheckTime)
				require.Equal(t, "m", c.Models)
				return
			}
			if mode == "partial" {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("partial_fail", func(tx *gorm.DB) {
					if c, ok := tx.Statement.Model.(*model.Channel); ok && c.Id == channels[1].Id {
						tx.AddError(errors.New("synthetic channel failure"))
					}
				}))
			}
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("partial_query", func(tx *gorm.DB) {
				if tx.Statement.Table != "channels" {
					return
				}
				if mode == "late_scan_failure" && len(tx.Statement.Selects) > 0 {
					if w, ok := tx.Statement.Clauses["WHERE"]; ok && strings.Contains(fmt.Sprint(w.Expression), "id >") {
						tx.AddError(errors.New("synthetic later page failure"))
					}
				}
				if len(tx.Statement.Selects) == 0 {
					if _, ok := tx.Statement.Clauses["WHERE"]; !ok {
						tx.AddError(errors.New("synthetic refresh failure"))
					}
				}
			}))
			code, out := call("POST", "/upstream_updates/apply_all", nil, true)
			db.Callback().Update().Remove("partial_fail")
			db.Callback().Query().Remove("partial_query")
			require.Equal(t, 200, code)
			require.Equal(t, false, out["success"])
			require.Equal(t, true, out["committed"])
			require.Equal(t, true, out["degraded"])
			require.Equal(t, true, out["partial"])
			require.Contains(t, out["message"], "Do not resend")
			data := out["data"].(map[string]any)
			if mode == "partial" {
				require.Equal(t, []any{float64(channels[1].Id)}, data["failed_channel_ids"])
				require.Len(t, data["outcomes"], 2)
				outcomes := data["outcomes"].([]any)
				require.Equal(t, true, outcomes[0].(map[string]any)["committed"])
				require.Equal(t, true, outcomes[0].(map[string]any)["degraded"])
				require.Equal(t, false, outcomes[1].(map[string]any)["committed"])
			} else {
				require.Equal(t, false, data["scan_complete"])
				require.Equal(t, float64(100), data["processed_channels"])
				require.Len(t, data["outcomes"], 100)
				require.Equal(t, float64(channels[99].Id), data["last_scanned_channel_id"])
			}
			first, e := model.GetChannelById(channels[0].Id, true)
			require.NoError(t, e)
			require.Equal(t, "m,new", first.Models)
			last, e := model.GetChannelById(channels[n-1].Id, true)
			require.NoError(t, e)
			require.Equal(t, "m", last.Models)
		})
	}
}
