package identityservice

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Bounded allocation observation, not a load/OOM test. The long parent key
// occurs once in the JSON, but is redundantly marshaled into discarded witnesses.
func TestRereviewDiscardedWitnessWork(t *testing.T) {
	for _, n := range []int{32, 1024} {
		in := MigrationInput{Identities: []string{"i"}, GroupRatio: map[string]float64{"g": 1}, GroupGroupRatio: map[string]map[string]float64{}}
		rows := map[string]float64{}
		for j := 0; j < n; j++ {
			rows[fmt.Sprintf("unknown%04d", j)] = 1
		}
		in.GroupGroupRatio[strings.Repeat("x", 32768)] = rows
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		// Exercise the exact transport first, then isolate allocation attributable
		// to Migrate itself (decoder cost is deliberately excluded).
		decoded, err := DecodeMigration(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !withinMigrationBudget(decoded) {
			t.Fatal("sample unexpectedly over entry budget")
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		report := Migrate(decoded)
		runtime.ReadMemStats(&after)
		out, _ := json.Marshal(report)
		used := after.TotalAlloc - before.TotalAlloc
		t.Logf("rows=%d input_bytes=%d migrate_total_alloc=%d report_bytes=%d conflicts=%d truncated=%t candidate=%t equivalent=%t", n, len(raw), used, len(out), len(report.Conflicts), report.Truncated, report.Config != nil, report.CoefficientsEquivalent)
		if report.Config != nil || report.CoefficientsEquivalent || !report.Truncated || !report.NotVerified {
			t.Fatal("unsafe outcome")
		}
		if used > 16<<20 {
			t.Errorf("discarded witness work exceeds bounded 16MiB regression guard: %d bytes for %d-byte input", used, len(raw))
		}
	}
}

func TestBudgetOmittedWitnessStopsVerification(t *testing.T) {
	assertOmitted := func(r MigrationReport) {
		t.Helper()
		if !r.Truncated || !r.NotVerified || r.Config != nil || r.CoefficientsEquivalent || r.SourceDigest != "" {
			t.Fatalf("omitted validation produced unsafe outcome: %+v", r)
		}
	}
	// Below the raw byte threshold, JSON escaping still exceeds the budget.
	in := MigrationInput{Identities: []string{"i"}, GroupRatio: map[string]float64{"g": 1}, GroupGroupRatio: map[string]map[string]float64{strings.Repeat("<", 8000): {"g": 1}}}
	assertOmitted(Migrate(in))
	// Authorization differences may be retained only with exact witnesses.
	in.GroupGroupRatio = nil
	in.ServiceModels = map[string]map[string]ServiceModel{"g": {}}
	in.ModelIdentityScopes = map[string]ModelIdentityScope{}
	for j := 0; j < 40; j++ {
		m := fmt.Sprintf("m%02d", j)
		in.ServiceModels["g"][m] = ServiceModel{Enabled: true}
		in.ModelIdentityScopes[m] = ModelIdentityScope{Mode: "public"}
		in.Matrix = append(in.Matrix, MigrationEdge{Identity: "i", Service: "g", Model: m, Allowed: false})
	}
	r := Migrate(in)
	assertOmitted(r)
	if len(r.Conflicts) != migrationWitnessLimit || len(r.AuthorizationDiff) != len(r.Conflicts) {
		t.Fatalf("lost witness/diff pairing: %d/%d", len(r.Conflicts), len(r.AuthorizationDiff))
	}
	for j, diff := range r.AuthorizationDiff {
		if diff.Model != r.Conflicts[j].Model || diff.OldAllowed || !diff.NewAllowed {
			t.Fatalf("altered authorization witness: %+v", diff)
		}
	}
	// A previous long model appears in the ratio-conflict detail, not just in
	// the current edge. Do not eagerly quote it when the witness cannot fit.
	long := "a" + strings.Repeat("x", 32768)
	in.ServiceModels = map[string]map[string]ServiceModel{"g": {long: {Enabled: true}}, "h": {"z": {Enabled: true}}}
	in.GroupRatio = map[string]float64{"g": 1, "h": 1}
	in.GroupGroupRatio = map[string]map[string]float64{"i": {"g": 1, "h": 2}}
	in.ModelIdentityScopes = map[string]ModelIdentityScope{long: {Mode: "public"}, "z": {Mode: "public"}}
	in.Matrix = []MigrationEdge{
		{Identity: "i", Service: "g", Model: long, Allowed: true, NonzeroBase: true},
		{Identity: "i", Service: "g", Model: "z", Allowed: false},
		{Identity: "i", Service: "h", Model: long, Allowed: false},
		{Identity: "i", Service: "h", Model: "z", Allowed: true, NonzeroBase: true},
	}
	assertOmitted(Migrate(in))
}
