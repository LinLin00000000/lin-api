package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewA1ParseErrorMustNotRevokeCommittedMembership(t *testing.T) {
	for _, which := range []string{"settings", "setting"} {
		t.Run(which, func(t *testing.T) {
			routeCoreDB(t)
			raw := "{broken"
			c := Channel{Key: "synthetic", Models: "a", Group: "default", Status: common.ChannelStatusEnabled}
			if which == "settings" {
				c.OtherSettings = raw
			} else {
				c.Setting = &raw
			}
			require.NoError(t, c.Insert())
			stale, err := GetChannelById(c.Id, true)
			require.NoError(t, err)
			require.NoError(t, AddChannelModels(c.Id, []string{"b"}))
			if which == "settings" {
				_ = stale.GetOtherSettings()
			} else {
				_ = stale.GetSetting()
			}
			fresh, err := GetChannelById(c.Id, true)
			require.NoError(t, err)
			var count int64
			require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ? AND model = ?", c.Id, "b").Count(&count).Error)
			t.Logf("after parse getter: models=%q ability_b=%d", fresh.Models, count)
			require.Equal(t, "a,b", fresh.Models, "read/parse path must not erase committed additive update")
			require.EqualValues(t, 1, count)
			if which == "settings" {
				require.Equal(t, raw, stale.OtherSettings)
				require.Equal(t, raw, fresh.OtherSettings)
				_, parseErr := stale.ParseOtherSettings()
				require.ErrorContains(t, parseErr, "field=settings")
			} else {
				require.Equal(t, raw, *stale.Setting)
				require.Equal(t, raw, *fresh.Setting)
				_, parseErr := stale.ParseSetting()
				require.ErrorContains(t, parseErr, "field=setting")
			}
		})
	}
}

func TestReviewA1ReportEmptyMembership(t *testing.T) {
	routeCoreDB(t)
	c := Channel{Key: "synthetic", Models: "a,,b", Group: "default"}
	require.NoError(t, c.Insert())
	success, failures, err := FixAbility()
	require.Error(t, err)
	require.Zero(t, success)
	require.Equal(t, 1, failures)
	var validation *RouteMembershipValidationError
	require.ErrorAs(t, err, &validation)
	require.Equal(t, []RouteMembershipDiagnostic{{ChannelID: c.Id, Field: "models", Key: "", Position: 1}}, validation.Diagnostics)
	require.ErrorContains(t, err, `field=models key="" position=1`)
	fresh, readErr := GetChannelById(c.Id, true)
	require.NoError(t, readErr)
	require.Equal(t, "a,,b", fresh.Models)

	var n int64
	require.NoError(t, DB.Model(&Ability{}).Where("channel_id = ? AND model = ?", c.Id, "").Count(&n).Error)
	t.Logf("malformed membership accepted; empty-model abilities=%d; FixAbility success=%d failures=%d error=%v", n, success, failures, err)
	require.EqualValues(t, 1, n)
}

func TestReviewA1MembershipDiagnosticsAndAtomicAbort(t *testing.T) {
	routeCoreDB(t)
	valid := Channel{Key: "synthetic", Models: "a, exact ,A", Group: "default"}
	require.Empty(t, valid.ValidateRouteMembership())
	require.NoError(t, valid.Insert())
	invalid := Channel{Key: "synthetic", Models: ",a,,", Group: "default,,pro"}
	require.NoError(t, invalid.Insert())
	require.NoError(t, DB.Where("channel_id = ?", valid.Id).Delete(&Ability{}).Error)
	var before []Ability
	require.NoError(t, DB.Order("channel_id, model").Find(&before).Error)
	for i := 0; i < 2; i++ {
		success, failures, err := FixAbility()
		require.Zero(t, success)
		require.Equal(t, 1, failures)
		var validation *RouteMembershipValidationError
		require.ErrorAs(t, err, &validation)
		require.Equal(t, []RouteMembershipDiagnostic{
			{invalid.Id, "models", "", 0}, {invalid.Id, "models", "", 2}, {invalid.Id, "models", "", 3}, {invalid.Id, "group", "", 1},
		}, validation.Diagnostics)
		var after []Ability
		require.NoError(t, DB.Order("channel_id, model").Find(&after).Error)
		require.Equal(t, before, after)
		fresh, err := GetChannelById(valid.Id, true)
		require.NoError(t, err)
		require.Equal(t, valid.Models, fresh.Models)
	}
	require.Empty(t, (&Channel{Models: "", Group: ""}).ValidateRouteMembership())
}
