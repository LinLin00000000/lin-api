package jsplugin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/pkg/identityservice"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTaskIdentityCompletionBoundaryPreservesLegacyContract(t *testing.T) {
	source := strings.Replace(mockPlugin, `export function extractUsageOnComplete(task, result) { return {upstreamUnits: 23}; }`, `export function extractUsageOnComplete(ctx,result,body){ return {seconds: body ? body.seconds : 2}; }`, 1)
	plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
	require.NoError(t, err)
	adaptor := New(plugin)
	adaptor.Init(&relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}})
	task := &model.Task{PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{IdentityQuote: &identityservice.FrozenQuote{}}}}
	result, err := adaptor.ParseTaskResult(task, nil, []byte(`{"seconds":8}`))
	require.NoError(t, err)
	require.Equal(t, float64(8), result.UsageFacts["seconds"])
	for i := 0; i < 2; i++ {
		_, present := adaptor.AdjustBillingOnComplete(task, result)
		require.False(t, present)
		require.Equal(t, float64(8), result.UsageFacts["seconds"], "a second body-less hook would overwrite with 2")
	}
	zero := 0
	result.ActualQuota = &zero
	quota, present := adaptor.AdjustBillingOnComplete(task, result)
	require.True(t, present)
	require.Zero(t, quota)
	result.ActualQuota = nil
	// Legacy consumers retain the old two-argument call, even after parsing.
	_, present = adaptor.AdjustBillingOnComplete(&model.Task{}, result)
	require.False(t, present)
	require.Equal(t, float64(2), result.UsageFacts["seconds"])

}

func TestTaskIdentityImmediateUsageProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, immediate, hook string
		want                  float64
		rejected              bool
	}{
		{"hook-body", `{status:"SUCCESS"}`, `return {seconds:body.seconds};`, 8, false},
		{"explicit-facts-precede-hook", `{status:"SUCCESS",usageFacts:{seconds:8}}`, `throw new Error("must not call");`, 8, false},
		{"explicit-zero-fact", `{status:"SUCCESS",usageFacts:{seconds:0}}`, ``, 0, false},
		{"explicit-zero-quota-without-facts", `{status:"SUCCESS",actualQuota:0}`, ``, 0, false},
		{"missing-facts-no-hook", `{status:"SUCCESS"}`, ``, 0, true},
		{"empty-hook", `{status:"SUCCESS"}`, `return {};`, 0, true},
		{"throwing-hook", `{status:"SUCCESS"}`, `throw new Error("bounded");`, 0, true},
		{"negative-facts", `{status:"SUCCESS",usageFacts:{seconds:-1}}`, ``, 0, true},
		{"non-object-facts", `{status:"SUCCESS",usageFacts:"invalid"}`, ``, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(mockPlugin, `taskData: {accepted: true, status: resp.statusCode},`, `immediate:`+tc.immediate+`,`, 1)
			hook := ""
			if tc.hook != "" {
				hook = `export function extractUsageOnComplete(ctx,result,body){` + tc.hook + `}`
			}
			source = strings.Replace(source, `export function extractUsageOnComplete(task, result) { return {upstreamUnits: 23}; }`, hook, 1)
			plugin, err := pluginruntime.NewRegistry().Register(source, pluginruntime.Options{})
			require.NoError(t, err)
			adaptor := New(plugin)
			info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, TaskRelayInfo: &relaycommon.TaskRelayInfo{}, IdentityBilling: &hosttypes.IdentityBilling{}, TieredBillingSnapshot: &billingexpr.BillingSnapshot{}}
			adaptor.Init(info)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/videos", nil)
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"upstream","seconds":8}`))}
			parsed, taskErr := adaptor.ParseResponse(c, response, info)
			if tc.rejected {
				require.NotNil(t, taskErr)
				require.NotNil(t, parsed)
				require.NotNil(t, parsed.Immediate)
				require.Equal(t, "upstream", parsed.Immediate.TaskID)
				return
			}
			require.Nil(t, taskErr)
			require.NotNil(t, parsed.Immediate)
			require.True(t, parsed.Immediate.CompletionUsageCaptured)
			if tc.name == "explicit-zero-quota-without-facts" {
				require.NotNil(t, parsed.Immediate.ActualQuota)
				require.Zero(t, *parsed.Immediate.ActualQuota)
			} else {
				require.Equal(t, tc.want, parsed.Immediate.UsageFacts["seconds"])
			}
		})
	}
}
