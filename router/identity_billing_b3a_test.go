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
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// Each child owns all globals and its SQLite ledger. No external model/DB/cache.
func TestIdentityB3aSyncLedger(t *testing.T) {
	fixture := os.Getenv("IDENTITY_B3A_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB3aSyncLedger$", "-test.count=1", "-test.timeout=120s", "-test.v")
		cmd.Env = append(os.Environ(), "IDENTITY_B3A_FIXTURE="+t.TempDir())
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
	var attempt int
	var scenario string
	var activeGroup string
	var reserves []int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		attempt++
		var u model.User
		var k model.Token
		if db.First(&u, activeUser).Error != nil || db.First(&k, activeToken).Error != nil {
			w.WriteHeader(500)
			return
		}
		reserved = 100000 - u.Quota
		reserves = append(reserves, reserved)
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
		if scenario != "violation" {
			common.QuotaPerUnit = 999999
		}
		next := cfg
		next.Revision = 32
		next.IdentityDefaults = map[string]float64{"ordinary": 9, "Friend": 9}
		s, _ := identityservice.NewSnapshot(next)
		current.Store(s)
		if scenario == "refund" || (scenario == "retry" && (attempt == 1 || (activeGroup == "auto" && attempt == 2))) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"message":"local retry","type":"server_error"}}`)
			return
		}
		if scenario == "gemini" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"local"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"totalTokenCount":120,"promptTokensDetails":[{"modality":"TEXT","tokenCount":90},{"modality":"AUDIO","tokenCount":10}]}}`)
			return
		}
		if scenario == "speech" {
			w.Header().Set("Content-Type", "audio/pcm")
			_, _ = w.Write(make([]byte, 48000))
			return
		}
		if scenario == "realtime" {
			conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			for i := 0; i < 2; i++ {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.done","response":{"usage":{"total_tokens":120,"input_tokens":100,"output_tokens":20,"input_token_details":{"text_tokens":90,"audio_tokens":10},"output_token_details":{"text_tokens":15,"audio_tokens":5}}}}`))
			}
			_, _, _ = conn.ReadMessage()
			return
		}
		if scenario == "image" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"local"},{"b64_json":"local"}]}`)
			return
		}
		if scenario == "cache" || scenario == "chat-audio" {
			w.Header().Set("Content-Type", "application/json")
			details := `"prompt_tokens_details":{"cached_tokens":20,"image_tokens":10}`
			if scenario == "chat-audio" {
				details = `"prompt_tokens_details":{"text_tokens":90,"audio_tokens":10},"completion_tokens_details":{"text_tokens":15,"audio_tokens":5}`
			}
			fmt.Fprintf(w, `{"id":"local","model":"b3a-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,%s}}`, details)
			return
		}
		if scenario == "violation" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Failed check: SAFETY_CHECK_TYPE","type":"invalid_request_error"}}`)
			return
		}
		if scenario == "compact" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"b3a-compact","object":"response.compaction","output":[],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120}}`)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"b3a\",\"object\":\"chat.completion.chunk\",\"model\":\"b3a-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"total_tokens\":120}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"b3a","object":"chat.completion","model":"b3a-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`)
	}))
	defer upstream.Close()
	ch := model.Channel{Name: "local", Key: "synthetic-local", BaseURL: &upstream.URL, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Models: "b3a-model,gemini-2.5-flash", Group: "default,pro", AutoBan: common.GetPointer(0)}
	require.NoError(t, ch.Insert())
	e := gin.New()
	e.POST("/v1/chat/completions", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAI) })
	e.POST("/v1/responses/compact", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAIResponsesCompaction) })
	e.POST("/v1/images/generations", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAIImage) })
	e.POST("/v1/audio/speech", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAIAudio) })
	e.POST("/v1beta/models/*path", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatGemini) })
	wsDone := make(chan struct{}, 1)
	e.GET("/v1/realtime", middleware.TokenAuthWithIdentitySnapshot(func() *identityservice.Snapshot { return current.Load() }), middleware.Distribute(), func(c *gin.Context) { controller.Relay(c, relaytypes.RelayFormatOpenAIRealtime); wsDone <- struct{}{} })
	wsServer := httptest.NewServer(e)
	defer wsServer.Close()
	gate := gin.New()
	gate.POST("/validate", controller.ValidateIdentityServiceSetting)
	fee := model_setting.GetGrokSettings()
	fee.ViolationDeductionEnabled = true
	fee.ViolationDeductionAmount = 0.001
	payload, err := json.Marshal(map[string]any{"config": cfg})
	require.NoError(t, err)
	gateReq := httptest.NewRequest("POST", "/validate", strings.NewReader(string(payload)))
	gateReq.Header.Set("Content-Type", "application/json")
	gateRec := httptest.NewRecorder()
	gate.ServeHTTP(gateRec, gateReq)
	require.Equal(t, 422, gateRec.Code)
	require.Contains(t, gateRec.Body.String(), "violation_fee_free_conflict")
	require.Contains(t, gateRec.Body.String(), "identity_service activation remains disabled by storage policy")
	require.Contains(t, gateRec.Body.String(), "production authorization, backup/restore")
	require.NotContains(t, gateRec.Body.String(), "(B3b)")
	require.Contains(t, gateRec.Body.String(), `"activation_ready":false`)
	common.RetryTimes = 1
	for _, id := range []string{"ordinary", "Friend"} {
		for _, g := range []string{"default", "pro", "auto"} {
			for _, kind := range []string{"chat", "stream", "compact", "retry", "refund", "violation", "cache", "chat-audio", "image", "expression", "speech", "realtime", "gemini"} {
				stream := kind == "stream"
				t.Run(fmt.Sprintf("%s/%s/%s", id, g, kind), func(t *testing.T) {
					current.Store(snap)
					channelType := constant.ChannelTypeOpenAI
					if kind == "gemini" {
						channelType = constant.ChannelTypeGemini
					}
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", ch.Id).Update("type", channelType).Error)
					common.QuotaPerUnit = 500000
					model_setting.GetGrokSettings().ViolationDeductionEnabled = true
					model_setting.GetGrokSettings().ViolationDeductionAmount = 0.001
					require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"b3a-model":2,"gemini-2.5-flash":2}`))
					require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"b3a-model":2,"gemini-2.5-flash":2}`))
					require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
					require.NoError(t, ratio_setting.UpdateCacheRatioByJSONString(`{"b3a-model":0.5}`))
					require.NoError(t, ratio_setting.UpdateImageRatioByJSONString(`{"b3a-model":3}`))
					require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{}`))
					require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{}`))
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`, "billing_setting.billing_expr": `{}`}))
					if kind == "image" {
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"b3a-model":0.01}`))
					}
					if kind == "chat-audio" || kind == "realtime" || kind == "speech" {
						require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"b3a-model":3}`))
						require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"b3a-model":4}`))
					}
					if kind == "expression" {
						require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{"b3a-model":"tiered_expr"}`, "billing_setting.billing_expr": `{"b3a-model":"p * 4 + c * 8"}`}))
					}
					name := fmt.Sprintf("b3a%d", calls.Load())
					u := model.User{Username: name, AffCode: name, Group: id, Status: common.UserStatusEnabled, Role: common.RoleCommonUser, Quota: 100000, Setting: `{"billing_preference":"wallet_only"}`}
					require.NoError(t, db.Create(&u).Error)
					k := model.Token{UserId: u.Id, Key: name, Name: name, Group: g, CrossGroupRetry: g == "auto" && kind == "retry", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100000}
					require.NoError(t, db.Create(&k).Error)
					activeUser = u.Id
					activeToken = k.Id
					reserved = -1
					attempt = 0
					scenario = kind
					activeGroup = g
					reserves = nil
					req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"b3a-model","messages":[{"role":"user","content":"hi"}],"max_tokens":20,"stream":%t}`, stream)))
					if kind == "compact" {
						req = httptest.NewRequest("POST", "/v1/responses/compact", strings.NewReader(`{"model":"b3a-model","input":"hi"}`))
					}
					if kind == "image" {
						req = httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"b3a-model","prompt":"local","n":2,"size":"1024x1024"}`))
					}
					if kind == "speech" {
						req = httptest.NewRequest("POST", "/v1/audio/speech", strings.NewReader(`{"model":"b3a-model","input":"hi","voice":"alloy","response_format":"pcm"}`))
					}
					if kind == "gemini" {
						req = httptest.NewRequest("POST", "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
					}
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
					if kind == "violation" {
						require.Equal(t, 400, rec.Code, rec.Body.String())
					} else if kind == "refund" {
						require.Equal(t, 500, rec.Code, rec.Body.String())
					} else {
						require.Equal(t, 200, rec.Code, rec.Body.String())
					}
					base := 280
					if kind == "image" {
						base = 10000
					}
					if kind == "cache" {
						base = 300
					}
					if kind == "chat-audio" {
						base = 420
					}
					if kind == "realtime" {
						base = 840
					}
					// CountToken=false fast metadata has empty input; PCM gives 17 audio tokens.
					if kind == "speech" {
						base = 408
					}
					if kind == "gemini" {
						base = 265
					}
					want := (base*3 + 1) / 2
					if g == "pro" || (g == "auto" && kind == "retry") {
						want = (base*5 + 1) / 2
					}
					if id == "Friend" || kind == "refund" {
						want = 0
					}
					if kind == "violation" {
						want = 3500
						if g == "pro" {
							want = 4500
						}
						require.Eventually(t, func() bool {
							var kk model.Token
							var uu model.User
							return db.First(&kk, k.Id).Error == nil && db.First(&uu, u.Id).Error == nil && 100000-kk.RemainQuota == want && 100000-uu.Quota == want
						}, time.Second, 10*time.Millisecond)
					}
					if kind == "refund" {
						require.Eventually(t, func() bool {
							var k2 model.Token
							var u2 model.User
							return db.First(&k2, k.Id).Error == nil && db.First(&u2, u.Id).Error == nil && k2.RemainQuota == 100000 && u2.Quota == 100000
						}, time.Second, 10*time.Millisecond)
					}
					if kind == "retry" {
						if g == "auto" {
							require.Equal(t, 3, attempt)
							if id != "Friend" {
								require.Greater(t, reserves[2], reserves[0])
							}
						} else {
							require.Equal(t, 2, attempt)
						}
					}
					require.NoError(t, db.First(&u, u.Id).Error)
					require.NoError(t, db.First(&k, k.Id).Error)
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
