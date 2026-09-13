package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
	"testing"
)

// Controls distinguish exact decimal zero from positive rounding and legacy minimums.
func TestIdentityAudioZeroCompatibility(t *testing.T) {
	old, err := common.Marshal(ratio_setting.GetAudioRatioCopy())
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"q1-audio-control":0}`))
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(string(old))) })
	for _, tc := range []struct {
		name   string
		frozen bool
		ar     float64
		tokens int
		want   int
	}{
		{"frozen_zero", true, 0, 100, 0},
		{"legacy_zero_minimum", false, 0, 100, 1},
		{"frozen_tiny_positive", true, 0.001, 100, 0},
		{"frozen_positive_round", true, 0.005, 100, 2},
		{"frozen_negative_unchanged", true, 1, -1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := QuotaInfo{ModelName: "q1-audio-control", ModelRatio: 2, GroupRatio: 1.5, InputDetails: TokenDetails{AudioTokens: tc.tokens}}
			if tc.frozen {
				info.Frozen = &types.IdentityBilling{}
				info.Frozen.Base.AudioRatio = tc.ar
			}
			got, clamp := calculateAudioQuota(info)
			require.Nil(t, clamp)
			require.Equal(t, tc.want, got)
		})
	}
}
