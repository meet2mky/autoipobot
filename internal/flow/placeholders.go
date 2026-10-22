package flow

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	placeholderReference  = regexp.MustCompile(`(^|[^A-Za-z0-9_.+-])@([A-Za-z_][A-Za-z0-9_-]*)`)
	placeholderDefinition = regexp.MustCompile(`^\s*@([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*(.*?)\s*$`)
)

// ParsePlaceholders parses "@name=value" lines. Values may be quoted with
// Go/JSON string syntax to include spaces or special characters.
func ParsePlaceholders(text string) (map[string]string, error) {
	placeholders := make(map[string]string)
	for lineNumber, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		match := placeholderDefinition.FindStringSubmatch(line)
		if len(match) != 3 {
			return nil, fmt.Errorf("placeholder line %d must look like @name=value", lineNumber+1)
		}
		value := strings.TrimSpace(match[2])
		if strings.HasPrefix(value, "\"") {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("placeholder line %d has an invalid quoted value: %w", lineNumber+1, err)
			}
			value = unquoted
		}
		placeholders[match[1]] = value
	}
	return placeholders, nil
}

// FormatPlaceholders renders placeholders as sorted "@name=value" lines.
func FormatPlaceholders(placeholders map[string]string) string {
	keys := make([]string, 0, len(placeholders))
	for key := range placeholders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		value := placeholders[key]
		if value == "" || strings.ContainsAny(value, " \t\"\\\n") || value != strings.TrimSpace(value) {
			value = strconv.Quote(value)
		}
		lines = append(lines, "@"+key+"="+value)
	}
	return strings.Join(lines, "\n")
}

// Resolve substitutes @name references with values. Unknown references are
// left untouched.
func Resolve(text string, values map[string]string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	return placeholderReference.ReplaceAllStringFunc(text, func(reference string) string {
		match := placeholderReference.FindStringSubmatch(reference)
		if len(match) < 3 {
			return reference
		}
		value, ok := values[match[2]]
		if !ok {
			return reference
		}
		return match[1] + value
	})
}

// References returns the placeholder names referenced in text, in order of
// first appearance.
func References(text string) []string {
	var names []string
	seen := map[string]bool{}
	for _, match := range placeholderReference.FindAllStringSubmatch(text, -1) {
		if len(match) > 2 && !seen[match[2]] {
			seen[match[2]] = true
			names = append(names, match[2])
		}
	}
	return names
}

// ReferencedPlaceholders returns every placeholder name used by the flow's
// command arguments and qualifier expressions, sorted.
func (f Flow) ReferencedPlaceholders() []string {
	seen := map[string]bool{}
	var names []string
	add := func(text string) {
		for _, name := range References(text) {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	for _, node := range f.Nodes {
		for _, arg := range node.Args {
			add(arg)
		}
		add(node.Expression)
	}
	sort.Strings(names)
	return names
}

// DefaultValues returns the values defined on the start node.
func (f Flow) DefaultValues() map[string]string {
	values := map[string]string{}
	start := f.StartNode()
	if start == nil {
		return values
	}
	if strings.TrimSpace(start.PlaceholderText) != "" {
		if parsed, err := ParsePlaceholders(start.PlaceholderText); err == nil {
			for key, value := range parsed {
				values[key] = value
			}
			return values
		}
	}
	for key, value := range start.Placeholders {
		values[key] = value
	}
	return values
}

// MergeValues layers maps left to right; later maps win.
func MergeValues(maps ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, m := range maps {
		for key, value := range m {
			merged[key] = value
		}
	}
	return merged
}

// SecretPlaceholders are redacted in logs.
var SecretPlaceholders = map[string]bool{"password": true, "pin": true, "otp": true, "mpin": true, "token": true}

// RedactValues returns a copy of values with secret-looking entries masked.
func RedactValues(values map[string]string) map[string]string {
	redacted := make(map[string]string, len(values))
	for key, value := range values {
		if SecretPlaceholders[strings.ToLower(key)] || strings.Contains(strings.ToLower(key), "password") {
			if value != "" {
				value = "[redacted]"
			}
		}
		redacted[key] = value
	}
	return redacted
}
