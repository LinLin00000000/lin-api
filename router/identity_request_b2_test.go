package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// Only the consumer after Distribute is a probe: authentication, Key lookup,
// candidate selection, product resolver and catalog handler are production code.
// This does not certify billing or any upstream protocol implementation.
func TestIdentityB2HTTPAuthorization(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B2_HTTP_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB2HTTPAuthorization$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_B2_HTTP_FIXTURE="+t.TempDir())
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "request.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Model{}, &model.Vendor{}, &model.Log{}, &model.Task{}, &model.UserSubscription{}))
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro","auto":"Auto"}`))
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["blocked","pro","default"]`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"pro":2,"Friend":1}`))
	operation_setting.SelfUseModeEnabled = true
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 10
	cfg.ServiceDefaults = map[string]float64{"default": 1, "pro": 2, "Friend": 1}
	cfg.IdentityDefaults = map[string]float64{"Friend": 0, "ordinary": 1, "VIP": 1}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{
		"default": {"friend-model": {Enabled: true}, "public-model": {Enabled: true}},
		"pro":     {"friend-model": {Enabled: true}, "pro-only": {Enabled: true}},
	}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{
		"friend-model": {Mode: "restricted", Identities: []string{"Friend"}},
		"public-model": {Mode: "public"}, "pro-only": {Mode: "public"},
	}
	s, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	var current atomic.Pointer[identityservice.Snapshot]
	current.Store(s)
	provider := func() *identityservice.Snapshot { return current.Load() }
	changedDuringIO := cfg
	changedDuringIO.Revision = 11
	changedDuringIO.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
	deniedDuringIO, err := identityservice.NewSnapshot(changedDuringIO)
	require.NoError(t, err)
	users := map[string]model.User{}
	for i, id := range []string{"Friend", "ordinary", "VIP"} {
		role := common.RoleCommonUser
		if id == "Friend" {
			role = common.RoleRootUser
		}
		u := model.User{Username: fmt.Sprintf("b2user%d", i), Group: id, Role: role, Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("b2%d", i), Quota: 1000000}
		require.NoError(t, db.Create(&u).Error)
		users[id] = u
	}
	addToken := func(name, id, group, limits string, enabled bool) model.Token {
		token := model.Token{UserId: users[id].Id, Key: name, Name: name, Group: group, Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, ModelLimitsEnabled: enabled, ModelLimits: limits, CrossGroupRetry: true}
		require.NoError(t, db.Create(&token).Error)
		return token
	}
	for _, id := range []string{"Friend", "ordinary", "VIP"} {
		for _, g := range []string{"default", "pro"} {
			addToken(id+g, id, g, "", false)
		}
	}
	addToken("autofriend", "Friend", "auto", "", false)
	addToken("limitedfriend", "Friend", "default", "public-model", true)
	addToken("identitygroup", "Friend", "Friend", "", false)
	expired := addToken("expiredfriend", "Friend", "default", "", false)
	require.NoError(t, db.Model(&expired).Update("expired_time", time.Now().Unix()-1).Error)
	ip := addToken("ipfriend", "Friend", "default", "", false)
	require.NoError(t, db.Model(&ip).Update("allow_ips", "10.0.0.1").Error)
	disabled := addToken("disabledfriend", "Friend", "default", "", false)
	require.NoError(t, db.Model(&disabled).Update("status", common.TokenStatusDisabled).Error)
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			current.Store(deniedDuringIO) // replace live config after upstream admission
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"local-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"friend-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"local-stream\"},\"finish_reason\":null}]}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"local-b2","object":"chat.completion","model":"friend-model","choices":[{"index":0,"message":{"role":"assistant","content":"local"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()
	channels := map[string]model.Channel{}
	for _, g := range []string{"default", "pro"} {
		ch := model.Channel{Name: g, Key: "synthetic-local-upstream", BaseURL: &upstream.URL, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "friend-model,public-model,pro-only,unknown", Group: g}
		require.NoError(t, ch.Insert())
		channels[g] = ch
	}
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	e := gin.New()
	auth := middleware.TokenAuthWithIdentitySnapshot(provider)
	e.GET("/v1/models", auth, func(c *gin.Context) { controller.ListModels(c, constant.ChannelTypeOpenAI) })
	e.GET("/v1/models/:model", auth, func(c *gin.Context) { controller.RetrieveModel(c, constant.ChannelTypeOpenAI) })
	probe := func(c *gin.Context) {
		r := service.RequestIdentity(c)
		group := common.GetContextKeyString(c, constant.ContextKeyUsingGroup)
		if group == "auto" {
			group = common.GetContextKeyString(c, constant.ContextKeyAutoGroup)
		}
		c.JSON(200, gin.H{"identity": r.Identity(), "revision": r.Snapshot().Config().Revision, "group": group, "channel": c.GetInt("channel_id")})
	}
	e.POST("/v1/chat/completions", auth, middleware.Distribute(), probe)
	e.GET("/v1/realtime", auth, middleware.Distribute(), probe)
	e.POST("/v1/videos", auth, middleware.Distribute(), probe)
	e.POST("/v1/responses/compact", auth, middleware.Distribute(), probe)
	for _, g := range []string{"default", "pro"} {
		task := model.Task{TaskID: "origin" + g, UserId: users["Friend"].Id, ChannelId: channels[g].Id, Properties: model.Properties{OriginModelName: "friend-model"}}
		require.NoError(t, db.Create(&task).Error)
	}
	e.POST("/v1/videos/:video_id/remix", auth, middleware.Distribute(), func(c *gin.Context) {
		info := &relaycommon.RelayInfo{UserId: c.GetInt("id"), ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}}
		if taskErr := relay.ResolveOriginTask(c, info); taskErr != nil {
			c.JSON(taskErr.StatusCode, taskErr)
			return
		}
		c.Status(204)
	})
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-"+key)
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w
	}
	for _, cache := range []bool{false, true} {
		common.MemoryCacheEnabled = cache
		if cache {
			model.InitChannelCache()
		}
		t.Run(fmt.Sprintf("cache=%t", cache), func(t *testing.T) {
			for _, g := range []string{"default", "pro"} {
				for _, id := range []string{"Friend", "ordinary", "VIP"} {
					want := 403
					if id == "Friend" {
						want = 200
					}
					for _, path := range []string{"/v1/chat/completions", "/v1/videos", "/v1/responses/compact"} {
						w := call("POST", path, id+g, `{"model":"friend-model","stream":true}`)
						require.Equal(t, want, w.Code, w.Body.String())
					}
					w := call("GET", "/v1/realtime?model=friend-model", id+g, "")
					require.Equal(t, want, w.Code, w.Body.String())
					catalog := call("GET", "/v1/models", id+g, "")
					require.Equal(t, 200, catalog.Code, catalog.Body.String())
					if id == "Friend" {
						require.Contains(t, catalog.Body.String(), `"id":"friend-model"`)
					} else {
						require.NotContains(t, catalog.Body.String(), `"id":"friend-model"`)
					}
				}
			}
			require.Equal(t, 403, call("POST", "/v1/chat/completions", "limitedfriend", `{"model":"friend-model"}`).Code)
			require.Equal(t, 403, call("POST", "/v1/chat/completions", fmt.Sprintf("limitedfriend-%d", channels["default"].Id), `{"model":"friend-model"}`).Code)
			require.Equal(t, 403, call("POST", "/v1/chat/completions", fmt.Sprintf("Frienddefault-%d", channels["pro"].Id), `{"model":"friend-model"}`).Code)
			require.Equal(t, 200, call("POST", "/v1/chat/completions", fmt.Sprintf("Frienddefault-%d", channels["default"].Id), `{"model":"friend-model"}`).Code)
			require.Equal(t, 403, call("POST", "/v1/chat/completions", "identitygroup", `{"model":"friend-model"}`).Code)
			require.Equal(t, 403, call("POST", "/v1/chat/completions", "Frienddefault", `{"model":"unknown"}`).Code)
			auto := call("POST", "/v1/chat/completions", "autofriend", `{"model":"public-model"}`)
			require.Equal(t, 200, auto.Code, auto.Body.String())
			require.Contains(t, auto.Body.String(), `"group":"default"`)
			auto = call("GET", "/v1/models", "autofriend", "")
			require.Contains(t, auto.Body.String(), `"id":"pro-only"`)
			require.Contains(t, auto.Body.String(), `"id":"public-model"`)
			require.NotContains(t, auto.Body.String(), `"id":"unknown"`)
			require.Equal(t, 404, call("GET", "/v1/models/friend-model", "ordinarydefault", "").Code)
			require.Equal(t, 200, call("GET", "/v1/models/friend-model", "Frienddefault", "").Code)
			require.Equal(t, 204, call("POST", "/v1/videos/origindefault/remix", "Frienddefault", `{}`).Code)
			require.Equal(t, 403, call("POST", "/v1/videos/originpro/remix", "Frienddefault", `{}`).Code)
			require.Equal(t, 403, call("POST", "/v1/videos/origindefault/remix", "limitedfriend", `{}`).Code)
			require.Equal(t, 400, call("POST", "/v1/videos/origindefault/remix", "ordinarydefault", `{}`).Code)
		})
	}
	for _, key := range []string{"expiredfriend", "disabledfriend", "ipfriend", "notfound"} {
		require.NotEqual(t, 200, call("POST", "/v1/chat/completions", key, `{"model":"friend-model"}`).Code)
	}
	// Exercise the genuine relay consumer with a loopback-only fake upstream.
	// Zero *legacy base ratio* avoids wallet work here; it is not a B3 free-price test.
	constant.CountToken = false
	constant.StreamingTimeout = 5
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"friend-model":0}`))
	service.InitHttpClient()
	actual := gin.New()
	actual.POST("/v1/chat/completions", auth, middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAI) })
	for _, id := range []string{"Frienddefault", "ordinarydefault"} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"friend-model","messages":[{"role":"user","content":"local"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-"+id)
		w := httptest.NewRecorder()
		actual.ServeHTTP(w, req)
		if id == "Frienddefault" {
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "local-b2")
		} else {
			require.Equal(t, 403, w.Code, w.Body.String())
		}
	}
	require.EqualValues(t, 1, upstreamCalls.Load())
	for _, key := range []string{"Frienddefault", "ordinarydefault"} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"friend-model","stream":true,"messages":[{"role":"user","content":"local"}]}`))
		req.Header.Set("Authorization", "Bearer sk-"+key)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		actual.ServeHTTP(w, req)
		if key == "Frienddefault" {
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), "local-stream")
			require.Contains(t, w.Body.String(), "[DONE]")
		} else {
			require.Equal(t, 403, w.Code, w.Body.String())
		}
	}
	require.EqualValues(t, 2, upstreamCalls.Load())
	require.Equal(t, 403, call("POST", "/v1/chat/completions", "Frienddefault", `{"model":"friend-model"}`).Code)
	current.Store(s)
	// Same genuine consumer is behind the existing request limiter.
	setting.ModelRequestRateLimitEnabled = true
	setting.ModelRequestRateLimitDurationMinutes = 1
	setting.ModelRequestRateLimitCount = 1
	setting.ModelRequestRateLimitSuccessCount = 10
	rate := gin.New()
	rate.POST("/v1/chat/completions", auth, middleware.ModelRequestRateLimit(), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAI) })
	for _, want := range []int{200, 429} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"friend-model","messages":[{"role":"user","content":"local"}]}`))
		req.Header.Set("Authorization", "Bearer sk-"+"Frienddefault")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		rate.ServeHTTP(w, req)
		require.Equal(t, want, w.Code, w.Body.String())
	}
	setting.ModelRequestRateLimitEnabled = false
	require.EqualValues(t, 3, upstreamCalls.Load())
	// Real dashboard PAT authentication must capture account identity, not body fields.
	pg := gin.New()
	pg.POST("/pg/chat/completions", middleware.PlaygroundAuthWithIdentitySnapshot(provider), middleware.Distribute(), probe)
	for _, id := range []string{"Friend", "ordinary", "VIP"} {
		pat := "b2-pat-" + id
		require.NoError(t, db.Model(&model.User{}).Where("id = ?", users[id].Id).Update("access_token", pat).Error)
		for _, group := range []string{"default", "pro", "Friend", "missing"} {
			req := httptest.NewRequest("POST", "/pg/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"friend-model","group":%q,"identity":"Friend","snapshot":{"mode":"legacy"}}`, group)))
			req.Header.Set("Authorization", "Bearer "+pat)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			pg.ServeHTTP(w, req)
			if id == "Friend" && (group == "default" || group == "pro") {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), `"identity":"Friend"`)
				require.Contains(t, w.Body.String(), `"group":"`+group+`"`)
			} else {
				require.Equal(t, 403, w.Code, w.Body.String())
			}
		}
	}
	// Real Playground consumer: session credentials work, PAT remains explicitly
	// unsupported by the existing handler (no product-policy change in B2).
	require.NoError(t, db.AutoMigrate(&model.UserSession{}))
	common.SessionSecret = "synthetic-b2-session-secret"
	pgActual := gin.New()
	pgActual.POST("/pg/chat/completions", middleware.PlaygroundAuthWithIdentitySnapshot(provider), middleware.Distribute(), controller.Playground)
	for _, id := range []string{"Friend", "ordinary"} {
		require.NoError(t, db.Model(&model.User{}).Where("id = ?", users[id].Id).Update("auth_version", 1).Error)
		bundle, err := service.CreateLoginSession(users[id].Id, "password", "127.0.0.1", "b2-local")
		require.NoError(t, err)
		for _, credential := range []string{bundle.AccessToken, "b2-pat-" + id} {
			req := httptest.NewRequest("POST", "/pg/chat/completions", strings.NewReader(`{"model":"friend-model","group":"pro","messages":[{"role":"user","content":"local"}]}`))
			req.Header.Set("Authorization", "Bearer "+credential)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			pgActual.ServeHTTP(w, req)
			if id == "ordinary" {
				require.Equal(t, 403, w.Code, w.Body.String())
			} else if credential == bundle.AccessToken {
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "local-b2")
			} else {
				require.Contains(t, w.Body.String(), "access token")
				require.NotContains(t, w.Body.String(), "local-b2")
			}
		}
	}
	require.EqualValues(t, 4, upstreamCalls.Load())
	// Genuine WebSocket upgrade and bidirectional relay, bounded to loopback.
	var wsCalls atomic.Int64
	wsUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		wsCalls.Add(1)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.created","session":{"id":"b2-local-session"}}`))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
		_, _, _ = conn.ReadMessage()
	}))
	defer wsUpstream.Close()
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels["pro"].Id).Update("base_url", wsUpstream.URL).Error)
	model.InitChannelCache()
	wsEngine := gin.New()
	wsDone := make(chan struct{}, 1)
	wsEngine.GET("/v1/realtime", auth, middleware.Distribute(), func(c *gin.Context) {
		controller.Relay(c, relaytypes.RelayFormatOpenAIRealtime)
		wsDone <- struct{}{}
	})
	wsServer := httptest.NewServer(wsEngine)
	defer wsServer.Close()
	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http") + "/v1/realtime?model=friend-model"
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer sk-" + "Friendpro"}})
	require.NoError(t, err)
	require.Equal(t, 101, response.StatusCode)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, payload, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Contains(t, string(payload), "b2-local-session")
	current.Store(deniedDuringIO)
	_, deniedResponse, deniedErr := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer sk-" + "Friendpro"}})
	require.Error(t, deniedErr)
	require.NotNil(t, deniedResponse)
	require.Equal(t, 403, deniedResponse.StatusCode)
	_ = deniedResponse.Body.Close()
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"session.update","session":{"instructions":"b2-loopback"}}`)))
	_, payload, err = conn.ReadMessage()
	require.NoError(t, err)
	require.Contains(t, string(payload), "b2-loopback")
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second))
	_ = conn.Close()
	select {
	case <-wsDone:
	case <-time.After(5 * time.Second):
		t.Fatal("real websocket relay did not finish")
	}
	_, response, err = websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer sk-" + "ordinarypro"}})
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, 403, response.StatusCode)
	_ = response.Body.Close()
	require.EqualValues(t, 1, wsCalls.Load())
	current.Store(s)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels["pro"].Id).Update("base_url", upstream.URL).Error)
	model.InitChannelCache()
	// Change both config and real account after TokenAuth but before distributor.
	stable := gin.New()
	stable.POST("/v1/chat/completions", auth, func(c *gin.Context) {
		changed := cfg
		changed.Revision = 11
		changed.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{}
		replacement, err := identityservice.NewSnapshot(changed)
		require.NoError(t, err)
		current.Store(replacement)
		require.NoError(t, db.Model(&model.User{}).Where("id = ?", users["Friend"].Id).Update("group", "ordinary").Error)
	}, middleware.Distribute(), probe)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"friend-model"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-Frienddefault")
	w := httptest.NewRecorder()
	stable.ServeHTTP(w, req)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"revision":10`)
	require.Contains(t, w.Body.String(), `"identity":"Friend"`)
	require.Equal(t, 403, call("POST", "/v1/chat/completions", "Frienddefault", `{"model":"friend-model"}`).Code)
	// Storage and the default middleware remain legacy. No config can activate B2.
	storageConfig := cfg
	storageConfig.Revision = 0
	_, err = model.IdentityServiceSettings.Save(storageConfig, 0)
	require.ErrorIs(t, err, identityservice.ErrActivationPending)
	require.Equal(t, identityservice.ModeLegacy, model.IdentityServiceSettings.Snapshot().Config().Mode)
	legacy := gin.New()
	legacy.POST("/v1/chat/completions", middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { require.Nil(t, service.RequestIdentity(c)); c.Status(204) })
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"unknown"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ordinarydefault")
	w = httptest.NewRecorder()
	legacy.ServeHTTP(w, req)
	require.Equal(t, 204, w.Code, w.Body.String())
	// Keep fixture process globals alive until the real async audit/cache workers exit.
	raw, _ := json.Marshal(map[string]any{"checks": "real TokenAuth + Distribute + SQLite + catalog", "billing": "not exercised"})
	t.Log(string(raw))
}
