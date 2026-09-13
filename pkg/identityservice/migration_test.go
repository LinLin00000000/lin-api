package identityservice

import (
	"math"
	"reflect"
	"testing"
)

func migrationFixture() MigrationInput {
	return MigrationInput{Identities: []string{"ordinary", "Friend"}, GroupRatio: map[string]float64{"Default": 1, "Pro": 2}, GroupGroupRatio: map[string]map[string]float64{"Friend": {"Default": 0.8, "Pro": 1.6}}, ServiceModels: map[string]map[string]ServiceModel{"Default": {"m": {Enabled: true}}, "Pro": {"m": {Enabled: true}}}, ModelIdentityScopes: map[string]Scope{"m": {Mode: "public"}}, Matrix: []MigrationEdge{{Identity: "ordinary", Service: "Default", Model: "m", Allowed: true, NonzeroBase: true}, {Identity: "ordinary", Service: "Pro", Model: "m", Allowed: true, NonzeroBase: true}, {Identity: "Friend", Service: "Default", Model: "m", Allowed: true, NonzeroBase: true}, {Identity: "Friend", Service: "Pro", Model: "m", Allowed: true, NonzeroBase: true}}}
}
func conflictCode(r MigrationReport, code string) bool {
	for _, c := range r.Conflicts {
		if c.Code == code {
			return true
		}
	}
	return false
}
func TestMigrationReplacementExactAndNeverActivation(t *testing.T) {
	in := migrationFixture()
	r := Migrate(in)
	if !r.CoefficientsEquivalent || r.Config == nil || len(r.Conflicts) > 0 || len(r.AuthorizationDiff) > 0 || r.ActivationReady || len(r.PendingVerification) == 0 {
		t.Fatalf("%+v", r)
	}
	if !reflect.DeepEqual(r.Config.ServiceDefaults, in.GroupRatio) || r.Config.IdentityDefaults["Friend"] != 0.8 || r.Config.IdentityDefaults["ordinary"] != 1 || r.Config.Mode != ModeLegacy || len(r.SourceDigest) != 64 || r.Config.MigrationSourceDigest != r.SourceDigest {
		t.Fatalf("%+v", r.Config)
	}
	if e := ValidateForStorage(*r.Config); e != nil {
		t.Fatal(e)
	}
	if next := Migrate(in); !reflect.DeepEqual(r, next) {
		t.Fatal("nondeterministic report")
	}
	in.GroupGroupRatio["Friend"]["Pro"] = 1.7
	r = Migrate(in)
	if r.Config != nil || r.CoefficientsEquivalent || !conflictCode(r, "inconsistent_identity_ratio") {
		t.Fatalf("nonseparable %+v", r)
	}
	for _, c := range r.Conflicts {
		if c.Code == "inconsistent_identity_ratio" && (c.Identity != "Friend" || c.Model != "m" || c.Service == "") {
			t.Fatalf("imprecise conflict %+v", c)
		}
	}
}
func TestMigrationZerosAndUnconstrained(t *testing.T) {
	in := migrationFixture()
	in.GroupRatio["Pro"] = 0
	in.GroupGroupRatio["Friend"]["Pro"] = 0
	r := Migrate(in)
	if r.Config == nil || r.Config.ServiceDefaults["Pro"] != 0 {
		t.Fatalf("zero column %+v", r)
	}
	in.GroupGroupRatio["Friend"]["Pro"] = 0.1
	r = Migrate(in)
	if !conflictCode(r, "zero_service_nonzero_legacy") {
		t.Fatalf("%+v", r)
	}
	in = migrationFixture()
	in.GroupGroupRatio["Friend"]["Default"] = 0
	in.GroupGroupRatio["Friend"]["Pro"] = 0
	r = Migrate(in)
	if r.Config == nil || r.Config.IdentityDefaults["Friend"] != 0 {
		t.Fatalf("free %+v", r)
	}
	for g := range in.GroupRatio {
		in.GroupRatio[g] = 0
	}
	r = Migrate(in)
	if r.Config == nil || r.Config.IdentityDefaults["Friend"] != 1 || len(r.UnconstrainedIdentities) != 2 || r.ActivationReady {
		t.Fatalf("unconstrained %+v", r)
	}
}
func TestMigrationExactProofNoCoarseFloatTolerance(t *testing.T) {
	in := migrationFixture()
	in.GroupGroupRatio["Friend"]["Pro"] = math.Nextafter(1.6, 2)
	if r := Migrate(in); r.Config != nil || !conflictCode(r, "inconsistent_identity_ratio") {
		t.Fatalf("ULP silently accepted %+v", r)
	}
	in = migrationFixture()
	in.GroupRatio["Default"] = 3
	in.GroupRatio["Pro"] = 6
	in.GroupGroupRatio["Friend"]["Default"] = 1
	in.GroupGroupRatio["Friend"]["Pro"] = 2
	if r := Migrate(in); r.Config != nil || !conflictCode(r, "unrepresentable_identity_ratio") {
		t.Fatalf("inexact 1/3 silently accepted %+v", r)
	}
}
func TestMigrationExplicitAuthorizationDiffAndCompleteMatrix(t *testing.T) {
	in := migrationFixture()
	in.ModelIdentityScopes["m"] = Scope{Mode: "restricted", Identities: []string{"Friend"}}
	r := Migrate(in)
	if r.Config != nil || len(r.AuthorizationDiff) != 2 || !conflictCode(r, "authorization_diff") {
		t.Fatalf("%+v", r)
	}
	in = migrationFixture()
	in.Matrix[0].Allowed = false
	r = Migrate(in)
	if r.Config != nil || len(r.AuthorizationDiff) != 1 || !r.AuthorizationDiff[0].NewAllowed {
		t.Fatalf("expansion %+v", r)
	}
	in = migrationFixture()
	in.Matrix = in.Matrix[:3]
	if r := Migrate(in); r.Config != nil || !conflictCode(r, "incomplete_matrix") {
		t.Fatalf("incomplete %+v", r)
	}
	in = migrationFixture()
	in.Matrix = append(in.Matrix, in.Matrix[0])
	if r := Migrate(in); r.Config != nil || !conflictCode(r, "duplicate_edge") {
		t.Fatalf("duplicate %+v", r)
	}
	in = migrationFixture()
	in.Matrix[0].Model = " m"
	if r := Migrate(in); r.Config != nil {
		t.Fatalf("normalized bad key %+v", r)
	}
	in = migrationFixture()
	in.GroupGroupRatio["ghost"] = map[string]float64{"Pro": 1}
	if r := Migrate(in); r.Config != nil {
		t.Fatalf("unknown pair identity %+v", r)
	}
	in = migrationFixture()
	in.GroupGroupRatio["Friend"]["ghost"] = 1
	if r := Migrate(in); r.Config != nil {
		t.Fatalf("unknown pair service %+v", r)
	}
	in = migrationFixture()
	in.GroupGroupRatio["Friend"]["Pro"] = math.NaN()
	if r := Migrate(in); r.Config != nil {
		t.Fatalf("NaN %+v", r)
	}
	in = migrationFixture()
	in.ServiceModels["Pro"]["m"] = ServiceModel{Enabled: true, Ratio: pointer(1)}
	if r := Migrate(in); r.Config != nil {
		t.Fatalf("reparameterization %+v", r)
	}
}
func TestMigrationSparseEdgesDoNotInventPermissions(t *testing.T) {
	in := migrationFixture()
	in.ServiceModels["Pro"]["m"] = ServiceModel{Enabled: false}
	in.Matrix[1].Allowed = false
	in.Matrix[3].Allowed = false
	in.GroupGroupRatio["Friend"]["Pro"] = 1.7
	r := Migrate(in)
	if r.Config == nil || len(r.AuthorizationDiff) > 0 || r.Config.IdentityDefaults["Friend"] != 0.8 {
		t.Fatalf("sparse %+v", r)
	}
}
