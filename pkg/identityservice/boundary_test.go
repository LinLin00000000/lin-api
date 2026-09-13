package identityservice

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestPureBoundaryNumericAndQuoteContract(t *testing.T) {
	c := fixture()
	c.ServiceDefaults["Pro"] = math.MaxFloat64
	c.IdentityDefaults["VIP"] = math.SmallestNonzeroFloat64
	s := snapshot(t, c)
	in := QuoteInput{Identity: "VIP", Service: "Pro", PhysicalModel: "m", BillingModel: "m", QuotaPerUnit: 1, Rounding: "exact-unit-quote", Components: map[string]BaseComponent{"base": {Value: 2, Unit: "call", Source: "price-v1"}}}
	q, e := s.Freeze(in)
	if e != nil || q.Components["base"].Effective <= 0 || math.IsInf(q.Components["base"].Effective, 0) {
		t.Fatalf("intermediate overflow/underflow: %+v %v", q, e)
	}
	bad := []func(*QuoteInput){
		func(q *QuoteInput) { q.BillingModel = "" }, func(q *QuoteInput) { q.Rounding = "" },
		func(q *QuoteInput) { q.QuotaPerUnit = 0 }, func(q *QuoteInput) { q.QuotaPerUnit = math.NaN() },
		func(q *QuoteInput) { q.Components = nil },
		func(q *QuoteInput) {
			q.Components = map[string]BaseComponent{"x": {Value: 1, Unit: "", Source: "base"}}
		},
		func(q *QuoteInput) {
			q.Components = map[string]BaseComponent{"x": {Value: 1, Unit: "call", Source: ""}}
		},
		func(q *QuoteInput) {
			q.Components = map[string]BaseComponent{"x": {Value: -1, Unit: "call", Source: "base"}}
		},
	}
	for i, change := range bad {
		input := in
		change(&input)
		if _, e := s.Freeze(input); e == nil {
			t.Fatalf("bad contract %d", i)
		}
	}
	var empty *Snapshot
	if _, e := empty.ResolveRatios("VIP", "Pro", "m"); e == nil {
		t.Fatal("nil snapshot accepted")
	}
	if e := (&Snapshot{}).Authorize("VIP", "Pro", "m"); e == nil {
		t.Fatal("zero snapshot accepted")
	}
}

func TestPureStorageGateCannotBeCertifiedByDigest(t *testing.T) {
	r := Migrate(migrationFixture())
	if r.Config == nil {
		t.Fatal(r)
	}
	c := *r.Config
	c.Mode = ModeIdentityService
	raw, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := Decode(raw)
	if e != nil {
		t.Fatal(e)
	}
	if e := ValidateForStorage(decoded); !errors.Is(e, ErrActivationPending) {
		t.Fatalf("digest bypass: %v", e)
	}
	c.Mode = "bogus"
	if e := ValidateForStorage(c); e == nil {
		t.Fatal("invalid structure accepted")
	}
}

func TestPureMigrationDraftOwnsInputAndModelConstraints(t *testing.T) {
	in := migrationFixture()
	r := Migrate(in)
	if r.Config == nil {
		t.Fatal(r)
	}
	want := clone(*r.Config)
	in.GroupRatio["Pro"] = 99
	in.ServiceModels["Pro"]["m"] = ServiceModel{}
	if !reflect.DeepEqual(*r.Config, want) {
		t.Fatal("migration output aliases input")
	}
	// An identity cannot use separate global D defaults for different models.
	// Minimal conversion reports that conflict rather than deleting allowed edges.
	in = migrationFixture()
	in.ServiceModels["Default"]["n"] = ServiceModel{Enabled: true}
	in.ModelIdentityScopes["n"] = Scope{Mode: "public"}
	for _, id := range in.Identities {
		for _, g := range []string{"Default", "Pro"} {
			in.Matrix = append(in.Matrix, MigrationEdge{Identity: id, Service: g, Model: "n", Allowed: g == "Default", NonzeroBase: true})
		}
	}
	in.ServiceModels["Default"]["m"] = ServiceModel{Enabled: false}
	for i := range in.Matrix {
		if in.Matrix[i].Model == "m" && in.Matrix[i].Service == "Default" {
			in.Matrix[i].Allowed = false
		}
	}
	in.GroupGroupRatio["Friend"]["Pro"] = 1.7
	r = Migrate(in)
	if r.Config != nil || !conflictCode(r, "inconsistent_identity_ratio") {
		t.Fatalf("model conflict escaped %+v", r)
	}
}

func TestPureDecodeNestedUnknownAndNumericShape(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"mode":"legacy","revision":-1}`,
		`{"version":1,"mode":"legacy","revision":1.5}`,
		`{"version":1,"mode":"legacy","service_defaults":{"Pro":2},"service_models":{"Pro":{"m":{"enabled":true,"allowed":true}}}}`,
		`{"version":1,"mode":"legacy","model_identity_scopes":{"m":{"mode":"public","extra":true}}}`,
		`{"version":1,"mode":"legacy","service_defaults":{"Pro":2},"service_models":{"Pro":{"m":{"enabled":null}}}}`,
		`{"version":1,"mode":"legacy","model_identity_scopes":{"m":null}}`,
		`{"version":1,"mode":"legacy","identity_defaults":{"VIP":1},"identity_model_ratios":{"VIP":{"m":null}}}`,
		`{"version":1,"mode":"legacy","service_defaults":{"Pro":2},"service_models":{"Pro":{"m":{"enabled":"true"}}}}`,
		`{"version":1,"mode":"legacy","model_identity_scopes":[]}`,
		`{"version":1,"mode":"legacy","model_identity_scopes":{"m":{"mode":"public","identities":{}}}}`,
	} {
		if _, e := Decode([]byte(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
