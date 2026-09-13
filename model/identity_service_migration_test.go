package model

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/pkg/identityservice"
	"github.com/stretchr/testify/require"
)

func TestIdentityB1MigrationProvenance(t *testing.T) {
	input := identityservice.MigrationInput{
		Identities: []string{"Friend"}, GroupRatio: map[string]float64{"default": 1, "pro": 2},
		GroupGroupRatio:     map[string]map[string]float64{"Friend": {"default": 0, "pro": 0}},
		ServiceModels:       map[string]map[string]identityservice.ServiceModel{"default": {"m": {Enabled: true}}, "pro": {"m": {Enabled: true}}},
		ModelIdentityScopes: map[string]identityservice.Scope{"m": {Mode: "restricted", Identities: []string{"Friend"}}},
		Matrix:              []identityservice.MigrationEdge{{Identity: "Friend", Service: "default", Model: "m", Allowed: true, NonzeroBase: true}, {Identity: "Friend", Service: "pro", Model: "m", Allowed: true, NonzeroBase: true}},
	}
	report := identityservice.Migrate(input)
	require.Empty(t, report.Conflicts)
	require.NotNil(t, report.Config)
	s := NewIdentityServiceStore(identityB1DB(t))
	_, err := s.Save(*report.Config, 0)
	require.ErrorContains(t, err, "explicit migration source")
	cfg := *report.Config
	cfg.IdentityDefaults = map[string]float64{"Friend": 1}
	_, err = s.Save(cfg, 0, input)
	require.ErrorContains(t, err, "exact candidate")
	snap, err := s.Save(*report.Config, 0, input)
	require.NoError(t, err)
	require.Equal(t, report.SourceDigest, snap.Config().MigrationSourceDigest)
	input.GroupGroupRatio["Friend"]["pro"] = 1
	require.Zero(t, s.Snapshot().Config().IdentityDefaults["Friend"])
	_, err = s.Save(snap.Config(), 1, input)
	require.Error(t, err)
	require.NoError(t, s.Refresh())
	require.EqualValues(t, 1, s.Snapshot().Config().Revision)
}

func TestIdentityB1ConcurrentSnapshotRefreshSave(t *testing.T) {
	s := NewIdentityServiceStore(identityB1DB(t))
	_, err := s.Save(identityservice.DefaultConfig(), 0)
	require.NoError(t, err)
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					c := s.Snapshot().Config()
					c.ServiceDefaults["detached"] = 99
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		c := s.Snapshot().Config()
		c.ServiceDefaults["pro"] = float64(i)
		_, err = s.Save(c, c.Revision)
		require.NoError(t, err)
		require.NoError(t, s.Refresh())
	}
	close(done)
	wg.Wait()
	_, exists := s.Snapshot().Config().ServiceDefaults["detached"]
	require.False(t, exists)
}
