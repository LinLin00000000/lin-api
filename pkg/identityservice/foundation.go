package identityservice

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrActivationPending = errors.New("identity_service activation pending runtime/activation verification")
var ErrDenied = errors.New("identity/service product authorization denied")

func DefaultConfig() Config {
	return Config{Version: Version, Mode: ModeLegacy, ServiceDefaults: map[string]float64{}, IdentityDefaults: map[string]float64{}, ServiceModels: map[string]map[string]ServiceModel{}, IdentityModelRatios: map[string]map[string]float64{}, ModelIdentityScopes: map[string]ModelIdentityScope{}}
}

func finiteNonnegative(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
func validKey(s string) bool {
	if s == "" || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func validateDefaults(name string, m map[string]float64) error {
	for _, k := range keys(m) {
		if !validKey(k) {
			return fmt.Errorf("%s[%q]: invalid exact key", name, k)
		}
		if !finiteNonnegative(m[k]) {
			return fmt.Errorf("%s[%q]: ratio must be finite and nonnegative", name, k)
		}
	}
	return nil
}

// Validate checks structure, not permission completeness or activation readiness.
// Missing product rows/scopes remain valid deny-by-default configuration.
func Validate(c Config) error {
	if c.Version != Version {
		return fmt.Errorf("version: unsupported %d", c.Version)
	}
	if c.Mode != ModeLegacy && c.Mode != ModeIdentityService {
		return fmt.Errorf("mode: unsupported %q", c.Mode)
	}
	if c.MigrationSourceDigest != "" {
		if len(c.MigrationSourceDigest) != 64 || strings.ToLower(c.MigrationSourceDigest) != c.MigrationSourceDigest {
			return errors.New("migration_source_digest: require lowercase sha256 hex")
		}
		if _, e := hex.DecodeString(c.MigrationSourceDigest); e != nil {
			return errors.New("migration_source_digest: require lowercase sha256 hex")
		}
	}
	if e := validateDefaults("service_defaults", c.ServiceDefaults); e != nil {
		return e
	}
	if e := validateDefaults("identity_defaults", c.IdentityDefaults); e != nil {
		return e
	}
	for _, g := range keys(c.ServiceModels) {
		if _, ok := c.ServiceDefaults[g]; !ok {
			return fmt.Errorf("service_models[%q]: unknown service", g)
		}
		for _, m := range keys(c.ServiceModels[g]) {
			row := c.ServiceModels[g][m]
			if !validKey(m) {
				return fmt.Errorf("service_models[%q][%q]: invalid exact model key", g, m)
			}
			if row.Ratio != nil && !finiteNonnegative(*row.Ratio) {
				return fmt.Errorf("service_models[%q][%q].ratio: must be finite and nonnegative", g, m)
			}
		}
	}
	for _, id := range keys(c.IdentityModelRatios) {
		if _, ok := c.IdentityDefaults[id]; !ok {
			return fmt.Errorf("identity_model_ratios[%q]: unknown identity", id)
		}
		if e := validateDefaults(fmt.Sprintf("identity_model_ratios[%q]", id), c.IdentityModelRatios[id]); e != nil {
			return e
		}
	}
	for _, m := range keys(c.ModelIdentityScopes) {
		if !validKey(m) {
			return fmt.Errorf("model_identity_scopes[%q]: invalid exact model key", m)
		}
		scope := c.ModelIdentityScopes[m]
		if scope.Mode != "public" && scope.Mode != "restricted" {
			return fmt.Errorf("model_identity_scopes[%q]: unknown scope mode %q", m, scope.Mode)
		}
		if scope.Mode == "public" && len(scope.Identities) > 0 {
			return fmt.Errorf("model_identity_scopes[%q]: public scope cannot contain identity list", m)
		}
		seen := map[string]bool{}
		for _, id := range scope.Identities {
			if _, ok := c.IdentityDefaults[id]; !ok {
				return fmt.Errorf("model_identity_scopes[%q]: unknown identity %q", m, id)
			}
			if seen[id] {
				return fmt.Errorf("model_identity_scopes[%q]: duplicate identity %q", m, id)
			}
			seen[id] = true
		}
	}
	return nil
}

// ValidateForStorage deliberately blocks activation, regardless of a digest or
// migration report. Pure coefficient proofs cannot validate live billing paths.
func ValidateForStorage(c Config) error {
	if e := Validate(c); e != nil {
		return e
	}
	if c.Mode == ModeIdentityService {
		return ErrActivationPending
	}
	return nil
}

// Decode rejects duplicate fields, case aliases, unknown nested fields, null
// scalar/ratio values, non-objects, and trailing JSON. Omitted maps are empty
// deny-by-default maps; the explicit version/mode are never inferred here.
func Decode(raw []byte) (Config, error) {
	if !utf8.Valid(raw) {
		return Config{}, errors.New("config: invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, e := readJSON(d)
	if e != nil {
		return Config{}, e
	}
	if _, e = d.Token(); e != io.EOF {
		if e == nil {
			e = errors.New("trailing JSON")
		}
		return Config{}, e
	}
	if e = checkShape(value, reflect.TypeOf(Config{}), "config"); e != nil {
		return Config{}, e
	}
	var c Config
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if e = strict.Decode(&c); e != nil {
		return Config{}, e
	}
	if e = Validate(c); e != nil {
		return Config{}, e
	}
	return c, nil
}

func readJSON(d *json.Decoder) (any, error) {
	tok, e := d.Token()
	if e != nil {
		return nil, e
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		out := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			name, ok := k.(string)
			if !ok {
				return nil, errors.New("object key must be string")
			}
			if _, exists := out[name]; exists {
				return nil, fmt.Errorf("duplicate JSON key %q", name)
			}
			v, e := readJSON(d)
			if e != nil {
				return nil, e
			}
			out[name] = v
		}
		_, e = d.Token()
		return out, e
	case '[':
		out := []any{}
		for d.More() {
			v, e := readJSON(d)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		_, e = d.Token()
		return out, e
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func checkShape(v any, t reflect.Type, path string) error {
	if v == nil {
		if t.Kind() == reflect.Map || t.Kind() == reflect.Slice {
			return nil
		}
		return fmt.Errorf("%s: null not permitted", path)
	}
	if t.Kind() == reflect.Pointer {
		return checkShape(v, t.Elem(), path)
	}
	switch t.Kind() {
	case reflect.Float64:
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: expected number", path)
		}
		// Inspect the mantissa, not a big.Rat of an attacker-controlled exponent.
		// Negative zero retains the existing numeric semantics; any nonzero
		// negative or any nonzero value rounded to zero must never become free.
		raw := n.String()
		nonzero := false
		for _, ch := range raw {
			if ch == 'e' || ch == 'E' {
				break
			}
			if ch >= '1' && ch <= '9' {
				nonzero = true
			}
		}
		if nonzero && strings.HasPrefix(raw, "-") {
			return fmt.Errorf("%s: ratio must be nonnegative", path)
		}
		f, err := n.Float64()
		if err != nil || !finiteNonnegative(f) || (nonzero && f == 0) {
			return fmt.Errorf("%s: ratio outside representable nonnegative float64 range", path)
		}
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", path)
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
		}
		for _, k := range keys(obj) {
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("%s: unknown field %q", path, k)
			}
			if e := checkShape(obj[k], ft, path+"."+k); e != nil {
				return e
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected map", path)
		}
		for _, k := range keys(obj) {
			if e := checkShape(obj[k], t.Elem(), fmt.Sprintf("%s[%q]", path, k)); e != nil {
				return e
			}
		}
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", path)
		}
		for _, x := range a {
			if e := checkShape(x, t.Elem(), path+"[]"); e != nil {
				return e
			}
		}
	}
	return nil // encoding/json performs scalar type and range validation.
}

func clone(c Config) Config {
	out := c
	out.ServiceDefaults = make(map[string]float64, len(c.ServiceDefaults))
	for k, v := range c.ServiceDefaults {
		out.ServiceDefaults[k] = v
	}
	out.IdentityDefaults = make(map[string]float64, len(c.IdentityDefaults))
	for k, v := range c.IdentityDefaults {
		out.IdentityDefaults[k] = v
	}
	out.ServiceModels = make(map[string]map[string]ServiceModel, len(c.ServiceModels))
	for g, models := range c.ServiceModels {
		out.ServiceModels[g] = make(map[string]ServiceModel, len(models))
		for m, row := range models {
			if row.Ratio != nil {
				v := *row.Ratio
				row.Ratio = &v
			}
			out.ServiceModels[g][m] = row
		}
	}
	out.IdentityModelRatios = make(map[string]map[string]float64, len(c.IdentityModelRatios))
	for id, models := range c.IdentityModelRatios {
		out.IdentityModelRatios[id] = make(map[string]float64, len(models))
		for m, v := range models {
			out.IdentityModelRatios[id][m] = v
		}
	}
	out.ModelIdentityScopes = make(map[string]ModelIdentityScope, len(c.ModelIdentityScopes))
	for m, scope := range c.ModelIdentityScopes {
		scope.Identities = append([]string(nil), scope.Identities...)
		out.ModelIdentityScopes[m] = scope
	}
	return out
}

func NewSnapshot(c Config) (*Snapshot, error) {
	if e := Validate(c); e != nil {
		return nil, e
	}
	return &Snapshot{config: clone(c)}, nil
}
func (s *Snapshot) Config() Config { return clone(s.config) }
