package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Real historical consumers, isolated from process-global DB/settings/registry.
// No PAT, public MJ image route, billing, worker or production activation is used.
// Media covers the persisted legacy video descriptor, not plugin artifact storage.
func TestIdentityB2HistoryConsumers(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B2_HISTORY_CONSUMERS_FIXTURE")
	if fixture == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestIdentityB2HistoryConsumers$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_B2_HISTORY_CONSUMERS_FIXTURE="+t.TempDir())
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "history-consumers.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Task{}, &model.Midjourney{}, &model.Channel{}, &model.Ability{}, &model.Option{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.IdentityDefaults = map[string]float64{"Friend": 1}
	cfg.ServiceDefaults = map[string]float64{"default": 1, "retired": 1}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{
		"retired": {"model": {Enabled: true}},
	}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
	snapshot, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	auth := middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return snapshot })
	owner := model.User{Username: "consumerowner", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "consumerowner"}
	other := model.User{Username: "consumerother", Group: "Friend", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "consumerother"}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	addKey := func(key string, userID int, group string) model.Token {
		token := model.Token{UserId: userID, Key: key, Name: key, Group: group, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, ModelLimitsEnabled: true, ModelLimits: "unrelated-model"}
		require.NoError(t, db.Create(&token).Error)
		return token
	}
	original := addKey("consumeroriginal", owner.Id, "retired")
	addKey("consumersecond", owner.Id, "retired")
	addKey("consumercurrent", owner.Id, "default")
	addKey("consumerother", other.Id, "retired")
	expired := addKey("consumerexpired", owner.Id, "retired")
	require.NoError(t, db.Model(&expired).Update("expired_time", time.Now().Unix()-1).Error)
	disabled := addKey("consumerdisabled", owner.Id, "retired")
	require.NoError(t, db.Model(&disabled).Update("status", common.TokenStatusDisabled).Error)
	ip := addKey("consumerip", owner.Id, "retired")
	require.NoError(t, db.Model(&ip).Update("allow_ips", "10.0.0.1").Error)
	exhausted := addKey("consumerexhausted", owner.Id, "retired")
	require.NoError(t, db.Model(&exhausted).Updates(map[string]any{"unlimited_quota": false, "remain_quota": 0}).Error)

	type upstreamRequest struct{ Method, Path, Authorization, Secret, Range string }
	requests := make(chan upstreamRequest, 64)
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case requests <- upstreamRequest{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("mj-api-secret"), r.Header.Get("Range")}:
		default:
			http.Error(w, "fixture request bound exceeded", http.StatusTooManyRequests)
			return
		}
		switch r.URL.Path {
		case "/media":
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "4")
			w.Header().Set("Accept-Ranges", "bytes")
			if r.Header.Get("Range") != "" {
				w.Header().Set("Content-Range", "bytes 0-3/4")
				w.WriteHeader(http.StatusPartialContent)
			}
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("data"))
			}
		case "/mj/task/historymj/image-seed", "/fast/mj/task/historymj/image-seed":
			// A genuine protected upstream rejects missing channel credentials.
			// This exposes the historical image-seed channel-context regression.
			if r.Header.Get("mj-api-secret") != "history-channel-key" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":4,"description":"missing channel credential"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":1,"description":"success","result":"owner-seed-314159"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	system_setting.GetFetchSetting().EnableSSRFProtection = true
	system_setting.GetFetchSetting().AllowPrivateIp = true
	system_setting.GetFetchSetting().AllowedPorts = []string{upstreamURL.Port()}
	service.InitHttpClient()
	channel := model.Channel{Name: "history-consumer-channel", Type: constant.ChannelTypeMidjourney, Key: "history-channel-key", BaseURL: &upstream.URL, Status: common.ChannelStatusEnabled, Group: "retired", Models: "model"}
	require.NoError(t, channel.Insert())
	video := model.Task{TaskID: "historyvideo", UserId: owner.Id, ChannelId: channel.Id, Group: "retired", Status: model.TaskStatusSuccess, Action: constant.TaskActionTextToVideo, Properties: model.Properties{OriginModelName: "model"}, PrivateData: model.TaskPrivateData{TokenId: original.Id, ResultURL: upstream.URL + "/media"}}
	require.NoError(t, db.Create(&video).Error)
	mj := model.Midjourney{MjId: "historymj", UserId: owner.Id, ChannelId: channel.Id, Status: "SUCCESS", Prompt: "private-history-prompt"}
	require.NoError(t, db.Create(&mj).Error)

	// Compile and register a real Responses renderer; no dependency injection of
	// lookup, ownership, protocol resolution or completed rendering is permitted.
	source := routerPluginSource("history-responses", "1.0.0", `[]`)
	source = strings.Replace(source, `fetchMode: "per_task",`, `fetchMode: "per_task", protocols: [{name:"openai_responses",supports:["sync","background"]}],`, 1)
	source += `
export const protocols = {openai_responses: {
 decodeRequest: function(ctx) { return {model:"model",requestBody:ctx.body.value}; },
 renderFinal: function() { return {output:[{type:"message",status:"completed",role:"assistant",content:[{type:"output_text",text:"owner-history-final",annotations:[],logprobs:[]}]}]}; }
}};`
	plugin, err := jsplugin.CompilePlugin(source, jsplugin.Options{Key: "history-responses", Version: "1.0.0"})
	require.NoError(t, err)
	registry := jsplugin.NewRegistry()
	require.NoError(t, registry.ReplaceOverrides([]*jsplugin.LoadedPlugin{plugin}))
	jsplugin.DefaultRegistry = registry
	responseTask := model.Task{TaskID: "task_historyresponse", UserId: owner.Id, ChannelId: channel.Id, Group: "retired", Platform: constant.TaskPlatform("history-responses"), Status: model.TaskStatusSuccess, Properties: model.Properties{OriginModelName: "model"}, PrivateData: model.TaskPrivateData{TokenId: original.Id}}
	require.NoError(t, db.Create(&responseTask).Error)

	// Revoke service/model after durable history exists, only in the injected
	// snapshot. Default storage remains deliberately unable to activate mode.
	delete(cfg.ServiceDefaults, "retired")
	delete(cfg.ServiceModels, "retired")
	cfg.Revision++
	snapshot, err = identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	persistedCandidate := cfg
	persistedCandidate.Revision = 0
	_, err = model.IdentityServiceSettings.Save(persistedCandidate, 0)
	require.ErrorIs(t, err, identityservice.ErrActivationPending)

	actual := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		actual.Handle(method, "/v1/videos/:task_id/content", auth, controller.VideoProxy)
		actual.Handle(method, "/v1/tasks/:key/artifacts/:artifact_key/content", auth, controller.TaskArtifactContent)
	}
	actual.GET("/v1/responses/:response_id", auth, controller.RetrieveTaskPluginResponse)
	actual.GET("/mj/task/:id/image-seed", auth, middleware.Distribute(), controller.RelayMidjourney)
	actual.GET("/:mode/mj/task/:id/image-seed", auth, middleware.Distribute(), controller.RelayMidjourney)
	call := func(method, path, key, byteRange string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer sk-"+key)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Range", byteRange)
		r.RemoteAddr = "192.0.2.10:2345"
		w := httptest.NewRecorder()
		actual.ServeHTTP(w, r)
		return w
	}
	routes := []struct{ method, path, kind string }{
		{"GET", "/v1/videos/historyvideo/content", "media"},
		{"HEAD", "/v1/videos/historyvideo/content", "media"},
		{"GET", "/v1/tasks/historyvideo/artifacts/video/content", "media"},
		{"HEAD", "/v1/tasks/historyvideo/artifacts/video/content", "media"},
		{"GET", "/v1/responses/resp_historyresponse", "response"},
		{"GET", "/mj/task/historymj/image-seed", "mj"},
		{"GET", "/fast/mj/task/historymj/image-seed", "mj"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			// Key validity/IP/quota constraints survive service retirement, on
			// every real consumer; the creating Key is not an ownership boundary.
			for _, key := range []string{"consumerexpired", "consumerdisabled", "consumerip", "consumerexhausted", "missing"} {
				before := calls.Load()
				w := call(route.method, route.path, key, "")
				require.Contains(t, []int{401, 403}, w.Code, "%s: %s", key, w.Body.String())
				require.Equal(t, before, calls.Load(), "invalid Key reached upstream")
			}
			before := calls.Load()
			w := call(route.method, route.path, "consumerother", "")
			if route.kind == "mj" {
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "task_no_found")
			} else {
				require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
			}
			require.NotContains(t, w.Body.String(), "owner-history-final")
			require.NotContains(t, w.Body.String(), "owner-seed-314159")
			require.Equal(t, before, calls.Load(), "another owner reached upstream")
			for _, key := range []string{"consumeroriginal", "consumersecond", "consumercurrent"} {
				t.Run(key, func(t *testing.T) {
					before := calls.Load()
					w := call(route.method, route.path, key, "")
					if route.kind != "response" {
						require.Equal(t, before+1, calls.Load())
						select {
						case observed := <-requests:
							require.Equal(t, route.method, observed.Method)
							require.Empty(t, observed.Authorization, "client Key must never be forwarded")
							if route.kind == "mj" {
								require.Equal(t, route.path, observed.Path)
								require.Equal(t, "history-channel-key", observed.Secret)
							} else {
								require.Equal(t, "/media", observed.Path)
								require.Empty(t, observed.Secret)
							}
						default:
							t.Fatal("consumer did not reach loopback upstream")
						}
					} else {
						require.Equal(t, before, calls.Load())
					}
					require.Equal(t, http.StatusOK, w.Code, w.Body.String())
					switch route.kind {
					case "media":
						require.Equal(t, "video/mp4", w.Header().Get("Content-Type"))
						if route.method == http.MethodHead {
							require.Empty(t, w.Body.String())
						} else {
							require.Equal(t, "data", w.Body.String())
						}
					case "response":
						var response map[string]any
						require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
						require.Equal(t, "resp_historyresponse", response["id"])
						require.Equal(t, "completed", response["status"])
						require.Equal(t, "model", response["model"])
						require.Contains(t, w.Body.String(), "owner-history-final")
					case "mj":
						require.Contains(t, w.Body.String(), "owner-seed-314159")
					}
				})
			}
		})
	}
	for _, path := range []string{"/v1/videos/historyvideo/content", "/v1/tasks/historyvideo/artifacts/video/content"} {
		t.Run("range"+path, func(t *testing.T) {
			before := calls.Load()
			w := call(http.MethodGet, path, "consumeroriginal", "bytes=0-3")
			require.Equal(t, http.StatusPartialContent, w.Code, w.Body.String())
			require.Equal(t, "data", w.Body.String())
			require.Equal(t, "bytes 0-3/4", w.Header().Get("Content-Range"))
			require.Equal(t, before+1, calls.Load())
			select {
			case observed := <-requests:
				require.Equal(t, "bytes=0-3", observed.Range)
				require.Equal(t, "/media", observed.Path)
			default:
				t.Fatal("range request not observed")
			}
		})
	}
	// Revoking the creating Key does not transfer or destroy user ownership:
	// that Key is rejected everywhere, but a second valid owner Key can retrieve.
	require.NoError(t, db.Model(&original).Update("status", common.TokenStatusDisabled).Error)
	for _, route := range routes {
		before := calls.Load()
		w := call(route.method, route.path, "consumeroriginal", "")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		require.Equal(t, before, calls.Load())
	}
	w := call(http.MethodGet, "/v1/responses/resp_historyresponse", "consumersecond", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "owner-history-final")
	require.NoError(t, db.Model(&original).Update("status", common.TokenStatusEnabled).Error)

	// Disabling the owner remains authoritative for every historical consumer.
	require.NoError(t, db.Model(&owner).Update("status", common.UserStatusDisabled).Error)
	for _, route := range routes {
		before := calls.Load()
		w := call(route.method, route.path, "consumeroriginal", "")
		require.Equal(t, http.StatusForbidden, w.Code, fmt.Sprintf("%s %s: %s", route.method, route.path, w.Body.String()))
		require.Equal(t, before, calls.Load())
	}
	require.Empty(t, requests)
}
