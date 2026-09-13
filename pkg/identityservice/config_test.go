package identityservice

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func pointer(v float64) *float64 { return &v }
func fixture() Config {
	c := DefaultConfig()
	c.Revision = 9
	c.ServiceDefaults = map[string]float64{"Default": 1, "Pro": 2}
	c.IdentityDefaults = map[string]float64{"ordinary": 1, "VIP": 0.8, "Friend": 0}
	c.ServiceModels = map[string]map[string]ServiceModel{"Default": {"m": {Enabled: true}}, "Pro": {"m": {Enabled: true}}}
	c.ModelIdentityScopes = map[string]ModelIdentityScope{"m": {Mode: "public"}}
	return c
}
func snapshot(t *testing.T, c Config) *Snapshot {
	t.Helper()
	s, e := NewSnapshot(c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestDefaultValidationAndStorageGate(t *testing.T) {
	c := DefaultConfig()
	if c.Mode != ModeLegacy || c.Version != 1 || c.Revision != 0 {
		t.Fatalf("default %+v", c)
	}
	if e := ValidateForStorage(c); e != nil {
		t.Fatal(e)
	}
	c.Mode = ModeIdentityService
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	if e := ValidateForStorage(c); !errors.Is(e, ErrActivationPending) {
		t.Fatalf("activation accepted: %v", e)
	}
	if _, e := NewSnapshot(c); e != nil {
		t.Fatal("structural preview rejected", e)
	}
}
func TestValidateRejectsInvalidValuesAndReferences(t *testing.T) {
	cases := map[string]func(*Config){
		"negative":          func(c *Config) { c.ServiceDefaults["Pro"] = -1 },
		"nan":               func(c *Config) { c.IdentityDefaults["VIP"] = math.NaN() },
		"inf":               func(c *Config) { c.ServiceModels["Pro"]["m"] = ServiceModel{Ratio: pointer(math.Inf(1))} },
		"override-negative": func(c *Config) { c.IdentityModelRatios["VIP"] = map[string]float64{"m": -1} },
		"identity":          func(c *Config) { c.IdentityModelRatios["ghost"] = map[string]float64{"m": 1} },
		"service":           func(c *Config) { c.ServiceModels["ghost"] = map[string]ServiceModel{} },
		"scope-identity":    func(c *Config) { c.ModelIdentityScopes["m"] = Scope{Mode: "restricted", Identities: []string{"ghost"}} },
		"scope-mode":        func(c *Config) { c.ModelIdentityScopes["m"] = Scope{Mode: "contains"} },
		"public-list":       func(c *Config) { c.ModelIdentityScopes["m"] = Scope{Mode: "public", Identities: []string{"VIP"}} },
		"duplicate-identities": func(c *Config) {
			c.ModelIdentityScopes["m"] = Scope{Mode: "restricted", Identities: []string{"VIP", "VIP"}}
		},
		"model-whitespace":   func(c *Config) { c.ServiceModels["Pro"][" m "] = ServiceModel{} },
		"empty-model":        func(c *Config) { c.ModelIdentityScopes[""] = Scope{Mode: "public"} },
		"control-model":      func(c *Config) { c.ModelIdentityScopes["m\nx"] = Scope{Mode: "public"} },
		"service-whitespace": func(c *Config) { c.ServiceDefaults[" Pro"] = 1 },
		"version":            func(c *Config) { c.Version = 2 },
		"mode":               func(c *Config) { c.Mode = "new" },
		"digest":             func(c *Config) { c.MigrationSourceDigest = "fake" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := fixture()
			mutate(&c)
			if e := Validate(c); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	c := fixture()
	c.ServiceDefaults["Pro"] = 0
	c.IdentityDefaults["VIP"] = 0
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
}
func TestDecodeStrict(t *testing.T) {
	raw, e := json.Marshal(fixture())
	if e != nil {
		t.Fatal(e)
	}
	c, e := Decode(raw)
	if e != nil || c.Revision != 9 {
		t.Fatalf("decode: %+v %v", c, e)
	}
	for _, bad := range []string{"", "null", "[]", `{"version":1,"mode":"legacy","typo":1}`, string(raw) + ` {}`, string(raw) + ` true`, `{"version":1,"mode":"legacy","version":1}`, `{"Version":1,"mode":"legacy"}`, `{"version":1,"mode":"legacy","revision":null}`, `{"version":1,"mode":"legacy","service_defaults":{"x":null}}`, `{"version":1,"mode":"legacy","service_defaults":{"x":1,"x":2}}`, `{"version":1,"mode":"legacy","service_defaults":{"x":1},"service_models":{"x":{"m":{"enabled":true,"ratio":null}}}}`, `{"version":1,"mode":"legacy","service_defaults":{"x":1e999}}`, `{"version":1,"mode":"legacy","identity_model_ratios":{"x":{"m":{"allowed":true}}}}`} {
		if _, e := Decode([]byte(bad)); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestSnapshotDeepCopiesEveryLayer(t *testing.T) {
	c := fixture()
	c.ServiceModels["Pro"]["m"] = ServiceModel{Enabled: true, Ratio: pointer(3)}
	c.IdentityModelRatios["VIP"] = map[string]float64{"m": 0.5}
	c.ModelIdentityScopes["m"] = Scope{Mode: "restricted", Identities: []string{"VIP"}}
	s := snapshot(t, c)
	want := s.Config()
	c.ServiceDefaults["Pro"] = 100
	*c.ServiceModels["Pro"]["m"].Ratio = 100
	c.IdentityModelRatios["VIP"]["m"] = 100
	c.ModelIdentityScopes["m"].Identities[0] = "ordinary"
	got := s.Config()
	got.IdentityDefaults["VIP"] = 100
	*got.ServiceModels["Pro"]["m"].Ratio = 100
	got.ModelIdentityScopes["m"].Identities[0] = "ordinary"
	delete(got.ServiceModels, "Default")
	if !reflect.DeepEqual(s.Config(), want) {
		t.Fatal("snapshot alias")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				x := s.Config()
				x.IdentityDefaults["VIP"] = float64(j)
				_, _ = s.ResolveRatios("VIP", "Pro", "m")
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(s.Config(), want) {
		t.Fatal("concurrent alias")
	}
}
func TestRatiosReplacementPresenceAndPermissionIndependence(t *testing.T) {
	c := fixture()
	c.ServiceModels["Pro"]["m"] = ServiceModel{Enabled: false, Ratio: pointer(0)}
	c.IdentityModelRatios["VIP"] = map[string]float64{"m": 1}
	delete(c.ModelIdentityScopes, "m")
	s := snapshot(t, c)
	r, e := s.ResolveRatios("VIP", "Pro", "m")
	if e != nil || r.ServiceFactor.Value != 0 || r.IdentityFactor.Value != 1 || !strings.Contains(r.ServiceFactor.Source, "service_models") || !strings.Contains(r.IdentityFactor.Source, "identity_model_ratios") {
		t.Fatalf("%+v %v", r, e)
	}
	if e := s.Authorize("VIP", "Pro", "m"); e == nil {
		t.Fatal("price granted permission")
	}
	delete(c.IdentityModelRatios["VIP"], "m")
	c.ServiceModels["Pro"]["m"] = ServiceModel{}
	r, e = snapshot(t, c).ResolveRatios("VIP", "Pro", "m")
	if e != nil || r.ServiceFactor.Value != 2 || r.IdentityFactor.Value != 0.8 {
		t.Fatalf("inheritance: %+v %v", r, e)
	}
	for _, args := range [][3]string{{"ghost", "Pro", "m"}, {"VIP", "ghost", "m"}, {"VIP", "Pro", " m"}} {
		if _, e := s.ResolveRatios(args[0], args[1], args[2]); e == nil {
			t.Fatal("unknown silently fell back", args)
		}
	}
}
func TestAuthorizationExactFailClosed(t *testing.T) {
	c := fixture()
	c.ModelIdentityScopes["m"] = Scope{Mode: "restricted", Identities: []string{"Friend"}}
	s := snapshot(t, c)
	for _, g := range []string{"Default", "Pro"} {
		if e := s.Authorize("Friend", g, "m"); e != nil {
			t.Fatal(e)
		}
		for _, id := range []string{"ordinary", "VIP", "ghost"} {
			if e := s.Authorize(id, g, "m"); e == nil {
				t.Fatal("unauthorized", id)
			}
		}
	}
	for _, m := range []string{"M", "m-suffix", "prefix-m", " m", "new"} {
		if e := s.Authorize("Friend", "Pro", m); e == nil {
			t.Fatal("non-exact permission", m)
		}
	}
	for _, scope := range []Scope{{Mode: "restricted"}, {Mode: "public"}} {
		c.ModelIdentityScopes["m"] = scope
		s = snapshot(t, c)
		err := s.Authorize("VIP", "Pro", "m")
		if (scope.Mode == "public") != (err == nil) {
			t.Fatal(scope, err)
		}
	}
	delete(c.ModelIdentityScopes, "m")
	if e := snapshot(t, c).Authorize("VIP", "Pro", "m"); e == nil {
		t.Fatal("missing scope")
	}
	c.ModelIdentityScopes["m"] = Scope{Mode: "public"}
	delete(c.ServiceModels["Pro"], "m")
	if e := snapshot(t, c).Authorize("VIP", "Pro", "m"); e == nil {
		t.Fatal("missing service row")
	}
}
func TestFreezeEachBaseComponentOnceAndDetached(t *testing.T) {
	c := fixture()
	c.ServiceModels["Pro"]["m"] = ServiceModel{Enabled: true, Ratio: pointer(3)}
	c.IdentityModelRatios["VIP"] = map[string]float64{"m": 0.5}
	s := snapshot(t, c)
	in := QuoteInput{Identity: "VIP", Service: "Pro", PhysicalModel: "m", BillingModel: "billing-m", QuotaPerUnit: 500000, Rounding: "caller-policy-v1", Components: map[string]BaseComponent{"input": {Value: 2, Unit: "token", Source: "base-v1"}, "tool": {Value: 4, Unit: "call", Source: "tool-v1"}}}
	q, e := s.Freeze(in)
	if e != nil {
		t.Fatal(e)
	}
	if q.Components["input"].Effective != 3 || q.Components["tool"].Effective != 6 || q.Ratios.Revision != 9 || q.BillingModel != "billing-m" || q.Ratios.PhysicalModel != "m" || q.Rounding != in.Rounding {
		t.Fatalf("%+v", q)
	}
	in.Components["input"] = BaseComponent{Value: 10, Unit: "token", Source: "base-v2"}
	c.IdentityDefaults["VIP"] = 9
	if q.Components["input"].Base.Value != 2 {
		t.Fatal("base changed")
	}
	next, e := s.Freeze(in)
	if e != nil || next.Components["input"].Effective != 15 {
		t.Fatal("base update not propagated to new quote", next, e)
	}
	in.Identity = "Friend"
	free, e := s.Freeze(in)
	if e != nil || free.Components["input"].Effective != 0 {
		t.Fatal("free", free, e)
	}
	in.Components["input"] = BaseComponent{Value: math.Inf(1), Unit: "token", Source: "base"}
	if _, e := s.Freeze(in); e == nil {
		t.Fatal("invalid base accepted even for free")
	}
	in.Identity = "ordinary"
	in.Components["input"] = BaseComponent{Value: math.MaxFloat64, Unit: "token", Source: "base"}
	if _, e := s.Freeze(in); e == nil {
		t.Fatal("overflow")
	}
	in.Components["input"] = BaseComponent{Value: math.SmallestNonzeroFloat64, Unit: "token", Source: "base"}
	in.Identity = "VIP"
	c.ServiceModels["Pro"]["m"] = ServiceModel{Ratio: pointer(0.1)}
	if _, e := snapshot(t, c).Freeze(in); e == nil {
		t.Fatal("underflow became free")
	}
}
