package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIdentityB1HTTPRootCASAndLegacyBypass(t *testing.T) {
	// The existing RootAuth audit pool outlives HTTP handlers. Isolate fixture
	// globals in this test binary's child; parent removes its temp DB only after
	// child exit. No logger changes, sleeps, audit mocks or data-race suppression.
	fixture := os.Getenv("IDENTITY_B1_HTTP_FIXTURE")
	if fixture == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIdentityB1HTTPRootCASAndLegacyBypass$", "-test.count=1", "-test.timeout=120s")
		cmd.Env = append(os.Environ(), "IDENTITY_B1_HTTP_FIXTURE="+t.TempDir())
		output, err := cmd.CombinedOutput()
		t.Log(string(output))
		require.NoError(t, err)
		return
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(fixture, "http.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}, &model.User{}, &model.Log{}))
	model.DB = db
	model.LOG_DB = db
	model.IdentityServiceSettings = model.NewIdentityServiceStore(db)
	common.RedisEnabled = false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	for _, x := range []struct {
		name string
		role int
	}{{"root", common.RoleRootUser}, {"admin", common.RoleAdminUser}, {"ordinary", common.RoleCommonUser}} {
		token := "synthetic-b1-" + x.name
		require.NoError(t, db.Create(&model.User{Username: "identity-b1-" + x.name, Role: x.role, Status: common.UserStatusEnabled, Group: "default", AccessToken: &token, AuthVersion: 1, AffCode: "b1-" + x.name}).Error)
	}
	gin.SetMode(gin.TestMode)
	e := gin.New()
	g := e.Group("/api/option", middleware.RootAuth())
	registerIdentityServiceRoutes(g)
	g.PUT("/", controller.UpdateOption)
	call := func(method, path, actor string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if text, ok := body.(string); ok {
			raw = []byte(text)
		} else {
			raw, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req := httptest.NewRequest(method, "/api/option"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if actor != "" {
			req.Header.Set("Authorization", "Bearer synthetic-b1-"+actor)
		}
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w
	}
	path := "/identity_service"
	for _, method := range []string{"GET", "PUT", "POST"} {
		p := path
		if method == "POST" {
			p += "/validate"
		}
		require.Equal(t, 401, call(method, p, "", nil).Code)
		for _, actor := range []string{"ordinary", "admin"} {
			require.Equal(t, 403, call(method, p, actor, nil).Code)
		}
	}
	w := call("GET", path, "root", nil)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"mode":"legacy"`)
	var count int64
	require.NoError(t, db.Model(&model.Option{}).Count(&count).Error)
	require.Zero(t, count)

	// Raw numeric tokens must be rejected before PUT Save or validate can lose them.
	for _, token := range []string{"1e-400", "-1e-400", "1e400", "-1e400"} {
		for _, leaf := range []string{
			`"service_defaults":{"g":%s}`,
			`"identity_defaults":{"i":%s}`,
			`"service_defaults":{"g":1},"service_models":{"g":{"m":{"enabled":true,"ratio":%s}}}`,
			`"identity_defaults":{"i":1},"identity_model_ratios":{"i":{"m":%s}}`,
		} {
			payload := `{"expected_revision":0,"config":{"version":1,"mode":"legacy",` + fmt.Sprintf(leaf, token) + `}}`
			require.Equal(t, 400, call("PUT", path, "root", payload).Code, payload)
			require.Equal(t, 400, call("POST", path+"/validate", "root", payload).Code, payload)
		}
		for _, leaf := range []string{`"group_ratio":{"g":%s}`, `"group_group_ratio":{"i":{"g":%s}}`, `"service_models":{"g":{"m":{"ratio":%s}}}`} {
			payload := `{"expected_revision":0,"config":{"version":1,"mode":"legacy"},"migration_source":{` + fmt.Sprintf(leaf, token) + `}}`
			require.Equal(t, 400, call("PUT", path, "root", payload).Code, payload)
			require.Equal(t, 400, call("POST", path+"/validate", "root", payload).Code, payload)
		}
	}
	source := identityservice.MigrationInput{GroupRatio: map[string]float64{}, ModelIdentityScopes: map[string]identityservice.ModelIdentityScope{}}
	for i := 0; i < 30; i++ {
		source.Identities = append(source.Identities, fmt.Sprintf("i%d", i))
		source.GroupRatio[fmt.Sprintf("g%d", i)] = 1
		source.ModelIdentityScopes[fmt.Sprintf("m%d", i)] = identityservice.ModelIdentityScope{Mode: "public"}
	}
	boundedBody := map[string]any{"expected_revision": 0, "config": identityservice.DefaultConfig(), "migration_source": source}
	inputBytes, _ := json.Marshal(boundedBody)
	start := time.Now()
	response := call("POST", path+"/validate", "root", boundedBody)
	require.Equal(t, 400, response.Code, response.Body.String())
	require.Less(t, response.Body.Len(), 65536)
	require.Contains(t, response.Body.String(), `"not_verified":true`)
	require.Contains(t, response.Body.String(), `"coefficients_equivalent":false`)
	require.NotContains(t, response.Body.String(), `"config":`)
	require.Less(t, time.Since(start), 5*time.Second)
	require.Equal(t, 400, call("PUT", path, "root", boundedBody).Code)
	require.NoError(t, db.Model(&model.Option{}).Count(&count).Error)
	require.Zero(t, count)
	require.Zero(t, model.IdentityServiceSettings.Snapshot().Config().Revision)
	t.Logf("bounded HTTP input_bytes=%d response_bytes=%d elapsed=%s", len(inputBytes), response.Body.Len(), time.Since(start))

	cfg := identityservice.DefaultConfig()
	cfg.IdentityDefaults = map[string]float64{"Friend": 0}
	cfg.ServiceDefaults = map[string]float64{"default": 1, "pro": 2}
	body := map[string]any{"config": cfg, "expected_revision": 0}
	w = call("PUT", path, "root", body)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"revision":1`)
	require.Equal(t, 409, call("PUT", path, "root", body).Code)
	cfg.Revision = 1
	cfg.Mode = "identity_service"
	body["config"] = cfg
	body["expected_revision"] = 1
	w = call("PUT", path, "root", body)
	require.Equal(t, 422, w.Code)
	require.Contains(t, w.Body.String(), "pending runtime/activation verification")
	w = call("POST", path+"/validate", "root", map[string]any{"config": cfg})
	require.Equal(t, 422, w.Code)
	cfg.Mode = "legacy"
	body["config"] = cfg
	w = call("POST", path+"/validate", "root", map[string]any{"config": cfg})
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"activation_ready":false`)
	w = call("PUT", "/", "root", map[string]any{"key": identityservice.OptionKey, "value": "{}"})
	require.Equal(t, 400, w.Code, w.Body.String())
	w = call("PUT", path, "root", map[string]any{"config": cfg})
	require.Equal(t, 400, w.Code)
	w = call("PUT", path, "root", `{"expected_revision":1,"config":{"version":1,"mode":"legacy","revision":1,"service_defaults":{"pro":null}}}`)
	require.Equal(t, 400, w.Code)
	w = call("PUT", path, "root", `{"expected_revision":1,"config":{"version":1,"mode":"legacy","revision":1,"evil":true}}`)
	require.Equal(t, 400, w.Code)
	w = call("PUT", path, "root", `{"expected_revision":1,"config":{"version":1,"mode":"legacy","revision":1}} {}`)
	require.Equal(t, 400, w.Code)
	w = call("GET", path, "root", nil)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"revision":1`)
	require.Contains(t, w.Body.String(), `"Friend":0`)
	require.NotContains(t, w.Body.String(), `"mode":"identity_service"`)
	// Editing after an audited migration must not retain an unverified digest.
	cfg.MigrationSourceDigest = strings.Repeat("a", 64)
	body["config"] = cfg
	require.Equal(t, 400, call("PUT", path, "root", body).Code)
	var persisted model.Option
	require.NoError(t, db.First(&persisted, "key = ?", identityservice.OptionKey).Error)
	stored, err := identityservice.Decode([]byte(persisted.Value))
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.Revision)
	require.Zero(t, stored.IdentityDefaults["Friend"])

	// A representable subnormal and explicit zero survive real SQLite Save/readback.
	for _, token := range []string{"5e-324", "0", "-0"} {
		rev := model.IdentityServiceSettings.Snapshot().Config().Revision
		payload := fmt.Sprintf(`{"expected_revision":%d,"config":{"version":1,"mode":"legacy","revision":%d,"service_defaults":{"g":%s}}}`, rev, rev, token)
		require.Equal(t, 200, call("POST", path+"/validate", "root", payload).Code)
		saved := call("PUT", path, "root", payload)
		require.Equal(t, 200, saved.Code, saved.Body.String())
		got := call("GET", path, "root", nil)
		require.Equal(t, 200, got.Code)
		var row model.Option
		require.NoError(t, db.First(&row, "key = ?", identityservice.OptionKey).Error)
		decoded, e := identityservice.Decode([]byte(row.Value))
		require.NoError(t, e)
		if token == "5e-324" {
			require.Equal(t, math.SmallestNonzeroFloat64, decoded.ServiceDefaults["g"])
		} else {
			require.Zero(t, decoded.ServiceDefaults["g"])
		}
		require.Equal(t, "legacy", decoded.Mode)
	}

}
