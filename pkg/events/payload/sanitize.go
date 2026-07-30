// Package payload owns the security boundary for externally delivered events.
package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
)

var sensitiveKeys = map[string]struct{}{
	"apikey":        {},
	"authorization": {},
	"globalapikey":  {},
	"instancetoken": {},
	"password":      {},
	"passwd":        {},
	"refreshtoken":  {},
	"secret":        {},
	"token":         {},
	"tokendigest":   {},
	"accesstoken":   {},
}

// SanitizeJSON returns an independent JSON payload with bearer credentials
// removed recursively. External producer adapters must call this immediately
// before accepting a payload so new event call sites cannot bypass the policy.
func SanitizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("external event payload is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("external event payload must be valid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("external event payload must contain one JSON value")
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		return nil, errors.New("external event payload must be an object or array")
	}
	sanitize(value)
	result, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("external event payload could not be encoded")
	}
	return result, nil
}

func sanitize(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if _, sensitive := sensitiveKeys[normalizedKey(key)]; sensitive {
				delete(current, key)
				continue
			}
			sanitize(nested)
		}
	case []any:
		for _, nested := range current {
			sanitize(nested)
		}
	}
}

func normalizedKey(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, value)
}
