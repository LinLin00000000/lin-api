package identityservice

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestQualityAllRawRatioLeaves(t *testing.T) {
	configs := []string{
		`{"version":1,"mode":"legacy","service_defaults":{"g":%s}}`,
		`{"version":1,"mode":"legacy","identity_defaults":{"i":%s}}`,
		`{"version":1,"mode":"legacy","service_defaults":{"g":1},"service_models":{"g":{"m":{"enabled":true,"ratio":%s}}}}`,
		`{"version":1,"mode":"legacy","identity_defaults":{"i":1},"identity_model_ratios":{"i":{"m":%s}}}`,
	}
	sources := []string{
		`{"group_ratio":{"g":%s}}`,
		`{"group_group_ratio":{"i":{"g":%s}}}`,
		`{"service_models":{"g":{"m":{"enabled":true,"ratio":%s}}}}`,
	}
	for _, token := range []string{"1e-400", "-1e-400", "1e400", "-1e400", "-1", "1e-999999999999999999999999", "1e999999999999999999999999"} {
		for _, template := range configs {
			if _, err := Decode([]byte(fmt.Sprintf(template, token))); err == nil {
				t.Errorf("accepted %s in %s", token, template)
			}
		}
		for _, template := range sources {
			if _, err := DecodeMigration([]byte(fmt.Sprintf(template, token))); err == nil {
				t.Errorf("accepted %s in %s", token, template)
			}
		}
	}
	for _, token := range []string{"0", "-0", "0e-400", "-0e-400", "5e-324", "4.9406564584124654e-324", "1", "1.7976931348623157e308"} {
		for _, template := range configs {
			if _, err := Decode([]byte(fmt.Sprintf(template, token))); err != nil {
				t.Errorf("rejected %s: %v", token, err)
			}
		}
		for _, template := range sources {
			if _, err := DecodeMigration([]byte(fmt.Sprintf(template, token))); err != nil {
				t.Errorf("rejected source %s: %v", token, err)
			}
		}
	}
	c, err := Decode([]byte(fmt.Sprintf(configs[0], "5e-324")))
	if err != nil || c.ServiceDefaults["g"] == 0 {
		t.Fatalf("subnormal lost: %+v %v", c, err)
	}
}

func TestQualityMigrationWitnessAndOverflowBounds(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, dims := range [][]int{{maxInt, maxInt, 2}, {16384, 2, 1}, {-1, 1, 1}} {
		if _, ok := checkedCardinality(16384, dims...); ok {
			t.Fatalf("accepted dimensions %v", dims)
		}
	}
	if n, ok := checkedCardinality(16384, 128, 128, 1); !ok || n != 16384 {
		t.Fatalf("boundary %d %v", n, ok)
	}
	in := MigrationInput{Identities: []string{"i"}, GroupRatio: map[string]float64{"g": 1}, ModelIdentityScopes: map[string]ModelIdentityScope{}}
	for i := 0; i < 40; i++ {
		in.ModelIdentityScopes[fmt.Sprintf("m%02d", i)] = ModelIdentityScope{Mode: "public"}
	}
	r := Migrate(in)
	raw, _ := json.Marshal(r)
	if !r.Truncated || !r.NotVerified || len(r.Conflicts) != migrationWitnessLimit || r.Config != nil || r.CoefficientsEquivalent || len(raw) > 65536 {
		t.Fatalf("invalid bounded report: %s", raw)
	}
	if r.Conflicts[0].Identity != "i" || r.Conflicts[0].Service != "g" || r.Conflicts[0].Model != "m00" {
		t.Fatalf("lost exact witness %+v", r.Conflicts[0])
	}
	// Oversized exact keys cannot bypass the byte budget or yield false success.
	in.Identities = []string{strings.Repeat("x", 40000)}
	r = Migrate(in)
	raw, _ = json.Marshal(r)
	if !r.Truncated || r.Config != nil || r.CoefficientsEquivalent || len(raw) > 65536 {
		t.Fatalf("long-key report bytes=%d", len(raw))
	}
}

func TestQualityTransportNonzeroUnderflow(t *testing.T) {
	for _, token := range []string{"1e-400", "-1e-400"} {
		raw := fmt.Sprintf(`{"version":1,"mode":"legacy","service_defaults":{"Pro":%s},"identity_defaults":{"VIP":1}}`, token)
		c, err := Decode([]byte(raw))
		if err != nil {
			continue
		}
		s, err := NewSnapshot(c)
		if err != nil {
			t.Fatal(err)
		}
		q, err := s.Freeze(QuoteInput{Identity: "VIP", Service: "Pro", PhysicalModel: "m", BillingModel: "m", QuotaPerUnit: 1, Rounding: "unit", Components: map[string]BaseComponent{"base": {Value: 1, Unit: "call", Source: "synthetic"}}})
		t.Errorf("nonzero input %s accepted as S=%g; quote=%g quote_error=%v storage_validation=%v", token, c.ServiceDefaults["Pro"], q.Components["base"].Effective, err, ValidateForStorage(c))
	}
}
func TestQualityMigrationTransportUnderflow(t *testing.T) {
	raw := []byte(`{"identities":["VIP"],"group_ratio":{"Pro":1},"group_group_ratio":{"VIP":{"Pro":-1e-400}},"service_models":{"Pro":{"m":{"enabled":true}}},"model_identity_scopes":{"m":{"mode":"public"}},"matrix":[{"identity":"VIP","service":"Pro","model":"m","allowed":true,"nonzero_base":true}]}`)
	in, err := DecodeMigration(raw)
	if err != nil {
		return
	}
	r := Migrate(in)
	if r.Config != nil {
		t.Errorf("negative nonzero legacy pair accepted: equivalent=%v D=%g digest=%s", r.CoefficientsEquivalent, r.Config.IdentityDefaults["VIP"], r.SourceDigest)
	}
}
func TestQualityBoundedMatrixAmplification(t *testing.T) {
	in := MigrationInput{GroupRatio: map[string]float64{}, ModelIdentityScopes: map[string]ModelIdentityScope{}}
	for i := 0; i < 30; i++ {
		in.Identities = append(in.Identities, fmt.Sprintf("i%d", i))
		in.GroupRatio[fmt.Sprintf("g%d", i)] = 1
		in.ModelIdentityScopes[fmt.Sprintf("m%d", i)] = ModelIdentityScope{Mode: "public"}
	}
	raw, _ := json.Marshal(in)
	r := Migrate(in)
	out, _ := json.Marshal(r)
	if len(r.Conflicts) > 64 || len(out) > 65536 || r.Config != nil || r.CoefficientsEquivalent {
		t.Errorf("unbounded or falsely successful report: conflicts=%d bytes=%d", len(r.Conflicts), len(out))
	}
	t.Logf("bounded synthetic input_bytes=%d identities=30 services=30 models=30 conflicts=%d response_bytes=%d", len(raw), len(r.Conflicts), len(out))
}
