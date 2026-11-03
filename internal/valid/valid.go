// Package valid carries per-field validation errors from the domain
// packages back to forms.
package valid

import (
	"sort"
	"strings"
)

// Errors maps a form field name to a human-readable message.
type Errors map[string]string

// Add records msg for field, keeping the first message per field.
func (e Errors) Add(field, msg string) {
	if _, ok := e[field]; !ok {
		e[field] = msg
	}
}

// Err returns e as an error, or nil if there are no errors.
func (e Errors) Err() error {
	if len(e) == 0 {
		return nil
	}
	return e
}

func (e Errors) Error() string {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e[k])
	}
	return "invalid input: " + strings.Join(parts, "; ")
}
