package router

import (
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
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// Each child owns all globals and its SQLite ledger. No external model/DB/cache.
func TestIdentityAudioZeroProtocol(t *testing.T) {
	fixture := os.Getenv("IDENTITY_AUDIO_ZERO_PROTOCOL")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityAudioZeroProtocol$", "-test.count=1", "-test.timeout=120s", "-test.v")
		cmd.Env = append(os.Environ(), "IDENTITY_AUDIO_ZERO_PROTOCOL="+t.TempDir())
		out, err := cmd.CombinedOutput()
		t.Log(string(out))
		require.NoError(t, err)
		return
	}
	require.Empty(t, os.Getenv("SQL_DSN"))
	require.Empty(t, os.Getenv("REDIS_CONN_STRING"))
	common.SQLitePath = filepath.Join(fixture, "billing.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.MemoryCacheEnabled = false
	common.BatchUpdateEnabled = false
	constant.CountToken = false
	common.PreConsumedQuota = 10
	constant.StreamingTimeout = 30
	require.NoError(t, model.InitDB())
	db := model.DB
	model.LOG_DB = db
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Option{}, &model.Log{}, &model.UserSubscription{}))
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	require.NoError(t, setting.UpdateAutoGroupsByJsonString(`["default","pro"]`))
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{"default":"Default","pro":"Pro","auto":"Auto"}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":7,"pro":9}`))
	cfg := identityservice.DefaultConfig()
	cfg.Mode = identityservice.ModeIdentityService
	cfg.Revision = 31
	cfg.IdentityDefaults = map[string]float64{"ordinary": 0.5, "Friend": 0}
	cfg.ServiceDefaults = map[string]float64{"default": 3, "pro": 5}
	cfg.ServiceModels = map[string]map[string]identityservice.ServiceModel{"default": {"b3a-model": {Enabled: true}, "gemini-2.5-flash": {Enabled: true}}, "pro": {"b3a-model": {Enabled: true}, "gemini-2.5-flash": {Enabled: true}}}
	cfg.ModelIdentityScopes = map[string]identityservice.ModelIdentityScope{"b3a-model": {Mode: "public"}, "gemini-2.5-flash": {Mode: "public"}}
	snap, err := identityservice.NewSnapshot(cfg)
	require.NoError(t, err)
	var current atomic.Pointer[identityservice.Snapshot]
	current.Store(snap)
	var calls atomic.Int64
	var activeUser, activeToken int
	var reserved int
	var scenario string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var u model.User
		var k model.Token
		if db.First(&u, activeUser).Error != nil || db.First(&k, activeToken).Error != nil {
			w.WriteHeader(500)
			return
		}
		reserved = 100000 - u.Quota
		if reserved != 100000-k.RemainQuota {
			w.WriteHeader(500)
			return
		}
		// Update base prices and product revision only after the actual precharge.
		_ = ratio_setting.UpdateModelRatioByJSONString(`{"b3a-model":99}`)
		_ = ratio_setting.UpdateCompletionRatioByJSONString(`{"b3a-model":99,"gemini-2.5-flash":99}`)
		_ = ratio_setting.UpdateModelPriceByJSONString(`{"b3a-model":99}`)
		_ = ratio_setting.UpdateCacheRatioByJSONString(`{"b3a-model":99}`)
		_ = ratio_setting.UpdateImageRatioByJSONString(`{"b3a-model":99}`)
		_ = ratio_setting.UpdateAudioRatioByJSONString(`{"b3a-model":99}`)
		_ = ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"b3a-model":99}`)
		_ = config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": `{"b3a-model":"p * 999"}`})
		next := cfg
		next.Revision = 32
		next.IdentityDefaults = map[string]float64{"ordinary": 9, "Friend": 9}
		s, _ := identityservice.NewSnapshot(next)
		current.Store(s)
		if scenario == "realtime" {
			conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for i := 0; i < 2; i++ {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"usage":{"total_tokens":120,"input_tokens":100,"output_tokens":20,"input_token_details":{"text_tokens":0,"audio_tokens":100},"output_token_details":{"text_tokens":0,"audio_tokens":20}}}}`))
			}
			_, _, _ = conn.ReadMessage()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		details := `"prompt_tokens_details":{"text_tokens":0,"audio_tokens":100},"completion_tokens_details":{"text_tokens":0,"audio_tokens":20}`
		fmt.Fprintf(w, `{"id":"local","model":"b3a-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,%s}}`, details)
		return
	}))
	defer upstream.Close()
	ch := model.Channel{Name: "local", Key: "synthetic-local", BaseURL: &upstream.URL, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "b3a-model,gemini-2.5-flash", Group: "default,pro", AutoBan: common.GetPointer(0)}
	require.NoError(t, ch.Insert())
	e := gin.New()
	e.POST("/v1/chat/completions", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAI) })
	wsDone := make(chan struct{}, 1)
	e.GET("/v1/realtime", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAIRealtime); wsDone <- struct{}{} })
	wsServer := httptest.NewServer(e)
	defer wsServer.Close()
	for _, id := range []string{"ordinary", "Friend"} {
		for _, g := range []string{"default"} {
			for _, kind := range []string{"chat-audio", "realtime"} {
				t.Run(fmt.Sprintf("%s/%s/%s", id, g, kind), func(t *testing.T) {
					current.Store(snap)
					common.QuotaPerUnit = 500000
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"b3a-model":2,"gemini-2.5-flash":2}`))
					require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"b3a-model":2,"gemini-2.5-flash":2}`))
					require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}))
					require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"b3a-model":0}`))
					require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"b3a-model":4}`))
					name := fmt.Sprintf("b3a%d", calls.Load())
					u := model.User{Username: name, AffCode: name, Group: id, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}
					require.NoError(t, db.Create(&u).Error)
					k := model.Token{UserId: u.Id, Key: name, Name: name, Group: g, CrossGroupRetry: g == "auto" && kind == "retry", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}
					require.NoError(t, db.Create(&k).Error)
					activeUser = u.Id
					activeToken = k.Id
					reserved = -1
					scenario = kind
					req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"b3a-model","messages":[{"role":"user","content":"hi"}],"max_tokens":20,"stream":%t}`, false)))
					req.Header.Set("Authorization", "Bearer sk-"+k.Key)
					req.Header.Set("Content-Type", "application/json")
					rec := httptest.NewRecorder()
					if kind == "realtime" {
						conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(wsServer.URL, "http")+"/v1/realtime?model=b3a-model", http.Header{"Authorization": []string{"Bearer sk-" + k.Key}})
						require.NoError(t, err)
						require.Equal(t, 101, resp.StatusCode)
						_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
						for i := 0; i < 2; i++ {
							_, msg, err := conn.ReadMessage()
							require.NoError(t, err)
							require.Contains(t, string(msg), "response.done")
						}
						_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(time.Second))
						_ = conn.Close()
						select {
						case <-wsDone:
						case <-time.After(5 * time.Second):
							t.Fatal("realtime relay timeout")
						}
					} else {
						e.ServeHTTP(rec, req)
					}
					require.Equal(t, 200, rec.Code, rec.Body.String())
					want := 0
					require.NoError(t, db.First(&u, u.Id).Error)
					require.NoError(t, db.First(&k, k.Id).Error)
					t.Logf("LEDGER identity=%s kind=%s wallet=%d token=%d used=%d expected=%d", id, kind, 100000-u.Quota, 100000-k.RemainQuota, k.UsedQuota, want)
					require.Equal(t, want, 100000-u.Quota, "wallet final")
					require.Equal(t, want, 100000-k.RemainQuota, "Key final")
					require.Equal(t, want, k.UsedQuota)
					if id == "Friend" {
						require.Zero(t, reserved)
					} else {
						require.Positive(t, reserved)
					}
				})
			}
		}
	}
}
