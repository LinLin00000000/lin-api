package identityservice

// Limits apply to preview and PUT provenance alike, before matrix enumeration.
// This is a finite configuration validator, not a partial migration mode.
const migrationCellBudget = 16384
const migrationWitnessLimit = 32
const migrationWitnessBytes = 32768

// checkedCardinality never multiplies before checking the finite budget. It is
// safe even when supplied machine-sized dimensions would overflow int.
func checkedCardinality(limit int, dimensions ...int) (int, bool) {
	product := 1
	for _, n := range dimensions {
		if n < 0 || n > limit {
			return 0, false
		}
	}
	for _, n := range dimensions {
		if n == 0 {
			return 0, true
		}
	}
	for _, n := range dimensions {
		if product > limit/n {
			return 0, false
		}
		product *= n
	}
	return product, true
}

func withinMigrationBudget(in MigrationInput) bool {
	remaining := migrationCellBudget
	take := func(n int) bool {
		if n > remaining {
			return false
		}
		remaining -= n
		return true
	}
	if !take(len(in.Identities)) || !take(len(in.GroupRatio)) || !take(len(in.Matrix)) || !take(len(in.GroupGroupRatio)) || !take(len(in.ServiceModels)) || !take(len(in.ModelIdentityScopes)) {
		return false
	}
	models := map[string]struct{}{}
	for _, rows := range in.GroupGroupRatio {
		if !take(len(rows)) {
			return false
		}
	}
	for _, rows := range in.ServiceModels {
		if !take(len(rows)) {
			return false
		}
		for m := range rows {
			models[m] = struct{}{}
		}
	}
	for m, scope := range in.ModelIdentityScopes {
		if !take(len(scope.Identities)) {
			return false
		}
		models[m] = struct{}{}
	}
	for _, edge := range in.Matrix {
		models[edge.Model] = struct{}{}
	}
	_, ok := checkedCardinality(migrationCellBudget, len(in.Identities), len(in.GroupRatio), len(models))
	return ok
}
