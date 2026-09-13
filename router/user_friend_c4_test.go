package router

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestC4FriendAccountHTTP(t *testing.T) {
	fixture := os.Getenv("C4_HTTP_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestC4FriendAccountHTTP$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "C4_HTTP_FIXTURE="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("LOG_SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "friend.db")
	common.IsMasterNode = false
	require.NoError(t, model.InitDB())
	model.LOG_DB = model.DB
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	common.PasswordLoginEnabled = true
	common.PasswordLoginEncryptionEnabled = false
	common.SessionSecret = "c4-isolated-test-secret-not-production"
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.TwoFA{}, &model.Token{}, &model.Log{}, &model.Option{}))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeLegacy
	cfg.Revision = 1
	cfg.IdentityDefaults = map[string]float64{"default": 1, "Friend": 0}
	cfg.ServiceDefaults = map[string]float64{"default": 1}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, model.DB.Create(&model.Option{Key: identityservice.OptionKey, Value: string(raw)}).Error)
	model.IdentityServiceSettings = model.NewIdentityServiceStore(model.DB)
	pat := "c4-admin-fixture-pat"
	admin := model.User{Username: "c4-admin", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default", AccessToken: &pat, AuthVersion: 1}
	require.NoError(t, model.DB.Create(&admin).Error)
	gin.SetMode(gin.TestMode)
	e := gin.New()
	SetApiRouter(e)
	server := httptest.NewServer(e)
	defer server.Close()
	call := func(method, path, bearer, body string) (int, map[string]interface{}) {
		t.Helper()
		req, er := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		require.NoError(t, er)
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, er := server.Client().Do(req)
		require.NoError(t, er)
		defer resp.Body.Close()
		var out map[string]interface{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		t.Logf("HTTP %s %s status=%d success=%v", method, path, resp.StatusCode, out["success"])
		return resp.StatusCode, out
	}
	ok := func(method, path, bearer, body string) map[string]interface{} {
		t.Helper()
		status, out := call(method, path, bearer, body)
		require.Equal(t, 200, status)
		require.Equal(t, true, out["success"], out)
		return out
	}
	status, _ := call("GET", "/api/user/identity-options", "", "")
	require.Equal(t, 401, status)
	legacyGroups := ok("GET", "/api/group/", pat, "")["data"]
	require.NotContains(t, legacyGroups, "Friend")
	options := ok("GET", "/api/user/identity-options", pat, "")["data"].(map[string]interface{})
	require.Contains(t, options["identities"], "Friend")
	require.Equal(t, "legacy", options["mode"])
	require.Len(t, options, 2)
	ok("POST", "/api/user/", pat, `{"username":"c4-friend","password":"FriendPass123","role":1,"group":"Friend"}`)
	var friend model.User
	require.NoError(t, model.DB.Where("username = ?", "c4-friend").First(&friend).Error)
	require.Equal(t, common.RoleCommonUser, friend.Role)
	require.Equal(t, "default", friend.Group)
	var count int64
	require.NoError(t, model.DB.Model(&model.Token{}).Where("user_id = ?", friend.Id).Count(&count).Error)
	require.Zero(t, count)
	login := func() string {
		out := ok("POST", "/api/user/login", "", `{"username":"c4-friend","password":"FriendPass123"}`)
		return out["data"].(map[string]interface{})["access_token"].(string)
	}
	oldSession := login()
	update := fmt.Sprintf(`{"id":%d,"username":"c4-friend","display_name":"Friend","role":1,"group":"Friend"}`, friend.Id)
	_, deniedRole := call("PUT", "/api/user/", pat, strings.Replace(update, `"role":1`, `"role":10`, 1))
	require.Equal(t, false, deniedRole["success"])
	ok("PUT", "/api/user/", pat, update)
	require.NoError(t, model.DB.First(&friend, friend.Id).Error)
	require.Equal(t, "Friend", friend.Group)
	require.Equal(t, common.RoleCommonUser, friend.Role)
	status, _ = call("GET", "/api/user/self", oldSession, "")
	require.Equal(t, 401, status)
	session := login()
	self := ok("GET", "/api/user/self", session, "")["data"].(map[string]interface{})
	require.Equal(t, "Friend", self["group"])
	require.Equal(t, float64(1), self["role"])
	status, _ = call("GET", "/api/user/identity-options", session, "")
	require.Equal(t, 403, status)
	status, _ = call("PUT", "/api/user/", session, update)
	require.Equal(t, 403, status)
	// Forged owner is ignored: creation always belongs to the authenticated caller.
	ok("POST", "/api/token/", session, fmt.Sprintf(`{"name":"friend-self","user_id":%d,"expired_time":-1,"unlimited_quota":true,"group":"default"}`, admin.Id))
	var token model.Token
	require.NoError(t, model.DB.Where("name = ?", "friend-self").First(&token).Error)
	require.Equal(t, friend.Id, token.UserId)
	require.Equal(t, "default", token.Group)
	ok("POST", fmt.Sprintf("/api/token/%d/key", token.Id), session, "")
	for _, probe := range []struct{ method, path, body string }{
		{"GET", fmt.Sprintf("/api/token/%d", token.Id), ""},
		{"POST", fmt.Sprintf("/api/token/%d/key", token.Id), ""},
		{"PUT", "/api/token/", fmt.Sprintf(`{"id":%d,"name":"stolen","unlimited_quota":true}`, token.Id)},
		{"DELETE", fmt.Sprintf("/api/token/%d", token.Id), ""},
	} {
		_, out := call(probe.method, probe.path, pat, probe.body)
		require.Equal(t, false, out["success"], out)
	}
	batch := ok("POST", "/api/token/batch/keys", pat, fmt.Sprintf(`{"ids":[%d]}`, token.Id))
	require.Empty(t, batch["data"].(map[string]interface{})["keys"])
	// Even an admin cannot issue a key owned by somebody else via this self endpoint.
	ok("POST", "/api/token/", pat, fmt.Sprintf(`{"name":"admin-self","user_id":%d,"expired_time":-1,"unlimited_quota":true}`, friend.Id))
	var adminToken model.Token
	require.NoError(t, model.DB.Where("name = ?", "admin-self").First(&adminToken).Error)
	require.Equal(t, admin.Id, adminToken.UserId)
	require.NoError(t, model.DB.First(&token, token.Id).Error)
	require.Equal(t, "friend-self", token.Name)
	require.Equal(t, friend.Id, token.UserId)
	var logs []model.Log
	require.NoError(t, model.DB.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	found := map[string]bool{}
	for _, entry := range logs {
		var other map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(entry.Other), &other))
		op, _ := other["op"].(map[string]interface{})
		action, _ := op["action"].(string)
		if action != "user.create" && action != "user.update" {
			continue
		}
		require.Equal(t, admin.Id, entry.UserId)
		params := op["params"].(map[string]interface{})
		require.Equal(t, float64(friend.Id), params["target_user_id"])
		if action == "user.update" {
			require.Equal(t, "default", params["group_before"])
			require.Equal(t, "Friend", params["group_after"])
			require.Equal(t, float64(1), params["role"])
		}
		found[action] = true
		t.Logf("audit actor=%d subject=%d action=%s params=%v", entry.UserId, friend.Id, action, params)
	}
	require.True(t, found["user.create"])
	require.True(t, found["user.update"])
	refreshedLegacyGroups := ok("GET", "/api/group/", pat, "")["data"]
	require.ElementsMatch(t, legacyGroups, refreshedLegacyGroups)
	var saved model.Option
	require.NoError(t, model.DB.Where("key = ?", identityservice.OptionKey).First(&saved).Error)
	require.Equal(t, string(raw), saved.Value)
	t.Logf("SQLite readback friend=%d role=%d group=%s token_owner=%d admin_token_owner=%d", friend.Id, friend.Role, friend.Group, token.UserId, adminToken.UserId)
}
