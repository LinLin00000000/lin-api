package identityservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
)

// decimalRat treats a float configuration value as its shortest round-trip JSON
// decimal. Migration equality is exact over those decimals, not epsilon-based.
func decimalRat(v float64) *big.Rat {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', -1, 64))
	if !ok {
		panic("decimalRat requires validated finite input")
	}
	return r
}

type edgeKey struct{ identity, service, model string }

// Migrate preserves S=GroupRatio and solves one global default D per identity.
// Pair values REPLACE GroupRatio. This deliberately narrow automatic converter
// rejects any need for per-model reparameterization or pair exceptions. It uses
// caller-supplied product scopes, never infers permissions from discount tables.
// A returned legacy Config is a draft only. There is no activation certificate.
func Migrate(in MigrationInput) MigrationReport {
	r := MigrationReport{Conflicts: []MigrationConflict{}, AuthorizationDiff: []AuthorizationChange{}, UnconstrainedIdentities: []string{}, PendingVerification: []string{
		"production source provenance and complete old request authorization (token groups/auto/whitelists, user restrictions, disabled recoverable routes)",
		"all billing families, components, actual quota rounding, minimum charges and wallet/subscription/token settlement",
		"runtime authorization entry points and frozen request/async/retry billing contracts",
		"inflight legacy task drain/rollback and old-instance writer isolation",
		"violation-fee policy conflicts and separate activation approval",
	}}
	witnessBytes := 0
	omit := func() bool {
		r.Truncated = true
		r.NotVerified = true
		return false
	}
	// Raw bytes lower-bound JSON encoding. Subtract to avoid overflow and
	// reject even the first enormous key before allocating its encoding.
	room := func(fields ...string) bool {
		if r.Truncated || len(r.Conflicts) >= migrationWitnessLimit {
			return omit()
		}
		remaining := migrationWitnessBytes - witnessBytes
		if remaining <= 0 {
			return omit()
		}
		for _, field := range fields {
			if len(field) > remaining {
				return omit()
			}
			remaining -= len(field)
		}
		return true
	}
	add := func(code, id, g, m, detail string) bool {
		if !room(code, id, g, m, detail) {
			return false
		}
		conflict := MigrationConflict{Code: code, Identity: id, Service: g, Model: m, Detail: detail}
		raw, _ := json.Marshal(conflict)
		if len(raw) > migrationWitnessBytes-witnessBytes {
			return omit()
		}
		witnessBytes += len(raw)
		r.Conflicts = append(r.Conflicts, conflict)
		return true
	}
	if !withinMigrationBudget(in) {
		r.NotVerified = true
		if !add("migration_budget_exceeded", "", "", "", "migration projection exceeds finite entry or identity x service x model budget (16384); no equivalence verified") {
			return r
		}
		return r
	}
	c := DefaultConfig()
	for _, id := range in.Identities {
		if !validKey(id) {
			if !add("invalid_identity", id, "", "", "invalid exact identity key") {
				return r
			}
			continue
		}
		if _, ok := c.IdentityDefaults[id]; ok {
			if !add("duplicate_identity", id, "", "", "duplicate registered identity") {
				return r
			}
			continue
		}
		c.IdentityDefaults[id] = 1
	}
	for g, v := range in.GroupRatio {
		c.ServiceDefaults[g] = v
	}
	c.ServiceModels = in.ServiceModels
	c.ModelIdentityScopes = in.ModelIdentityScopes
	if e := Validate(c); e != nil {
		if !add("invalid_config", "", "", "", e.Error()) {
			return r
		}
	}
	if len(c.IdentityDefaults) == 0 || len(c.ServiceDefaults) == 0 {
		if !add("empty_universe", "", "", "", "explicit identities and services required") {
			return r
		}
	}
	for _, id := range keys(in.GroupGroupRatio) {
		if _, ok := c.IdentityDefaults[id]; !ok {
			if !add("unknown_pair_identity", id, "", "", "pair references unregistered identity") {
				return r
			}
		}
		for _, g := range keys(in.GroupGroupRatio[id]) {
			v := in.GroupGroupRatio[id][g]
			if _, ok := c.ServiceDefaults[g]; !ok {
				if !add("unknown_pair_service", id, g, "", "pair references unknown GroupRatio service") {
					return r
				}
			}
			if !finiteNonnegative(v) {
				if !add("invalid_pair_ratio", id, g, "", "legacy pair must be finite and nonnegative") {
					return r
				}
			}
		}
	}
	for _, g := range keys(c.ServiceModels) {
		for _, m := range keys(c.ServiceModels[g]) {
			if c.ServiceModels[g][m].Ratio != nil {
				if !add("service_override_not_supported", "", g, m, "minimal migration preserves GroupRatio without model S reparameterization") {
					return r
				}
			}
		}
	}
	if len(r.Conflicts) > 0 || r.Truncated {
		return r
	}

	models := map[string]bool{}
	for m := range c.ModelIdentityScopes {
		models[m] = true
	}
	for _, rows := range c.ServiceModels {
		for m := range rows {
			models[m] = true
		}
	}
	edges := map[edgeKey]MigrationEdge{}
	for _, edge := range in.Matrix {
		id, g, m := edge.Identity, edge.Service, edge.Model
		if !validKey(m) {
			if !add("invalid_model", id, g, m, "invalid exact model key") {
				return r
			}
			continue
		}
		if _, ok := c.IdentityDefaults[id]; !ok {
			if !add("unknown_edge_identity", id, g, m, "matrix identity not registered") {
				return r
			}
			continue
		}
		if _, ok := c.ServiceDefaults[g]; !ok {
			if !add("unknown_edge_service", id, g, m, "matrix service not registered") {
				return r
			}
			continue
		}
		models[m] = true
		k := edgeKey{id, g, m}
		if _, ok := edges[k]; ok {
			if !add("duplicate_edge", id, g, m, "matrix edge specified more than once") {
				return r
			}
			continue
		}
		edges[k] = edge
	}
	if len(models) == 0 {
		if !add("empty_universe", "", "", "", "explicit model matrix required") {
			return r
		}
	}
	identityKeys, serviceKeys, modelKeys := keys(c.IdentityDefaults), keys(c.ServiceDefaults), keys(models)
	for _, id := range identityKeys {
		for _, g := range serviceKeys {
			for _, m := range modelKeys {
				if _, ok := edges[edgeKey{id, g, m}]; !ok {
					if !add("incomplete_matrix", id, g, m, "explicit allowed/denied edge is required") {
						r.NotVerified = true
						return r
					}
				}
			}
		}
	}
	if len(r.Conflicts) > 0 || r.Truncated {
		return r
	}

	s, _ := NewSnapshot(c)
	constraints := map[string]*big.Rat{}
	first := map[string]MigrationEdge{}
	// Sort instead of trusting map or input ordering: conflicts and witnesses are
	// stable, including which incompatible service/model relation is reported.
	ordered := make([]MigrationEdge, 0, len(edges))
	for _, edge := range edges {
		ordered = append(ordered, edge)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Model < b.Model
	})
	for _, edge := range ordered {
		id, g, m := edge.Identity, edge.Service, edge.Model
		allowed := s.Authorize(id, g, m) == nil
		if edge.Allowed != allowed {
			if add("authorization_diff", id, g, m, fmt.Sprintf("old allowed=%t; proposed allowed=%t", edge.Allowed, allowed)) {
				r.AuthorizationDiff = append(r.AuthorizationDiff, AuthorizationChange{Identity: id, Service: g, Model: m, OldAllowed: edge.Allowed, NewAllowed: allowed})
			} else {
				return r
			}
		}
		if !edge.Allowed || !edge.NonzeroBase {
			continue
		}
		old := in.GroupRatio[g]
		if pair, ok := in.GroupGroupRatio[id][g]; ok {
			old = pair
		}
		service := c.ServiceDefaults[g]
		if service == 0 {
			if old != 0 {
				if !add("zero_service_nonzero_legacy", id, g, m, fmt.Sprintf("S=0 cannot reproduce legacy replacement R=%s", decimalRat(old).RatString())) {
					return r
				}
			}
			continue
		}
		d := new(big.Rat).Quo(decimalRat(old), decimalRat(service))
		if prior, ok := constraints[id]; ok {
			if prior.Cmp(d) != 0 {
				w := first[id]
				// Prior witness keys also occur in detail: preflight before fmt
				// can allocate an oversized quoted string.
				if !room("inconsistent_identity_ratio", id, g, m, w.Service, w.Model) {
					return r
				}
				if !add("inconsistent_identity_ratio", id, g, m, fmt.Sprintf("required D=%s differs from D=%s at service=%q model=%q (R replaces, not multiplies S)", d.RatString(), prior.RatString(), w.Service, w.Model)) {
					return r
				}
			}
		} else {
			constraints[id] = d
			first[id] = edge
		}
	}
	for _, id := range keys(c.IdentityDefaults) {
		d, ok := constraints[id]
		if !ok {
			r.UnconstrainedIdentities = append(r.UnconstrainedIdentities, id)
			continue
		}
		f, _ := d.Float64()
		if !finiteNonnegative(f) || decimalRat(f).Cmp(d) != 0 {
			w := first[id]
			if !add("unrepresentable_identity_ratio", id, w.Service, w.Model, fmt.Sprintf("exact D=%s cannot round-trip through float64 JSON decimal without coefficient drift", d.RatString())) {
				return r
			}
			continue
		}
		c.IdentityDefaults[id] = f
	}
	if len(r.Conflicts) > 0 || r.Truncated {
		return r
	}
	// Hash only the supplied non-production projection, never imply provenance.
	raw, e := json.Marshal(in)
	if e != nil {
		if !add("source_encoding", "", "", "", e.Error()) {
			return r
		}
		return r
	}
	digest := sha256.Sum256(raw)
	r.SourceDigest = hex.EncodeToString(digest[:])
	c.MigrationSourceDigest = r.SourceDigest
	c = clone(c)
	r.Config = &c
	r.CoefficientsEquivalent = true
	return r
}
