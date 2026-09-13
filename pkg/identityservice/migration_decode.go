package identityservice

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"unicode/utf8"
)

// DecodeMigration preserves the same strict transport contract as Decode.
// Semantic conflicts remain reportable through Migrate rather than being
// silently converted (notably null ratios must never decode as free zero).
func DecodeMigration(raw []byte) (MigrationInput, error) {
	var in MigrationInput
	if !utf8.Valid(raw) {
		return in, errors.New("migration source: invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := readJSON(d)
	if err != nil {
		return in, err
	}
	if _, err = d.Token(); err != io.EOF {
		return in, errors.New("trailing JSON")
	}
	if err = checkShape(value, reflect.TypeOf(MigrationInput{}), "migration_source"); err != nil {
		return in, err
	}
	obj := value.(map[string]any)
	if rows, ok := obj["matrix"].([]any); ok {
		for _, item := range rows {
			row := item.(map[string]any)
			for _, field := range []string{"identity", "service", "model", "allowed", "nonzero_base"} {
				if _, exists := row[field]; !exists {
					return in, errors.New("migration matrix requires explicit " + field)
				}
			}
		}
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err = strict.Decode(&in); err != nil {
		return in, err
	}
	return in, nil
}
