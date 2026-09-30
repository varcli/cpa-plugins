// Package clinenx holds the small, dependency-free helpers the Cline provider
// reuses across auth, models, executor and management code: string
// normalization, truncation for error messages, and defensive JSON decoding.
//
// Keeping them here (rather than in provider/) means the helpers stay testable
// on their own and the provider package reads as protocol logic, not utility
// glue.
package clinenx

import (
	"encoding/json"
	"strings"
)

// NonEmpty returns value unless it is blank, in which case it returns fallback.
func NonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// StringValue reads a string field from a decoded JSON object.
func StringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// Truncate clips value for an error message so an upstream HTML body cannot
// blow up a log line or a management response.
func Truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

// CloneJSON returns a standalone copy so callers never hold an alias into an RPC
// response buffer that the host may reuse.
func CloneJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return append([]byte(nil), raw...)
}

// TrimList trims every entry and drops the blanks, preserving order. It is the
// forgiving counterpart to the strict model-ID validator: used for values that
// come from the host config block rather than from the panel.
func TrimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		if id := strings.TrimSpace(raw); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// AppendUnique appends value unless it is already present.
func AppendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

// RemoveString removes every occurrence of value, preserving the order of the
// rest. The input slice is reused, so callers must not keep an alias of it.
func RemoveString(items []string, value string) []string {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

// MoveBefore moves value so it sits directly before the "before" entry. An empty
// before (or one that is absent) appends value instead.
func MoveBefore(items []string, value, before string) []string {
	if value == "" {
		return items
	}
	items = RemoveString(items, value)
	if before == "" {
		return append(items, value)
	}
	for i, item := range items {
		if item != before {
			continue
		}
		out := append([]string{}, items[:i]...)
		out = append(out, value)
		return append(out, items[i:]...)
	}
	return append(items, value)
}
