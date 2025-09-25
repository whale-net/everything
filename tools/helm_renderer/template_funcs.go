// Template functions for Helm chart rendering
package main

import (
	"fmt"
	"strings"
	"text/template"
)

// GetTemplateFuncs returns the template function map available to templates
// These functions provide Helm-compatible functionality including Sprig-like functions
func GetTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"default": defaultFunc,
		"quote":   quoteFunc,
		"indent":  indentFunc,
		"nindent": nindentFunc,
		"trunc":   truncFunc,
		"trimSuffix": func(suffix, text string) string {
			return strings.TrimSuffix(text, suffix)
		},
		"dict":       dictFunc,
		"include":    includeFunc,
		"toYaml":     toYamlFunc,
		"upper":      strings.ToUpper,
		"lower":      strings.ToLower,
		"contains":   strings.Contains,
		"printf":     fmt.Sprintf,
		"typeOf":     typeOfFunc,
		"index":      indexFunc,
		"replace":    strings.ReplaceAll,
		"split":      strings.Split,
		"join":       joinFunc,
		"trim":       strings.Trim,
		"trimPrefix": strings.TrimPrefix,
		"hasPrefix":  strings.HasPrefix,
		"hasSuffix":  strings.HasSuffix,
		"len":        lenFunc,
		"hasKey":     hasKeyFunc,
		"int":        intFunc,
		"gt":         gtFunc,
		"lt":         ltFunc,
		"eq":         eqFunc,
		"ne":         neFunc,
		"and":        andFunc,
		"or":         orFunc,
		"not":        notFunc,
		"kindIs":     kindIsFunc,
	}
}

// defaultFunc returns defaultVal if value is nil or empty, otherwise returns value
func defaultFunc(defaultVal interface{}, value interface{}) interface{} {
	if value == nil || value == "" {
		return defaultVal
	}
	return value
}

// quoteFunc wraps a string in double quotes
func quoteFunc(str string) string {
	return fmt.Sprintf(`"%s"`, str)
}

// indentFunc indents each line of text by the specified number of spaces
func indentFunc(spaces int, text string) string {
	padding := strings.Repeat(" ", spaces)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = padding + line
		}
	}
	return strings.Join(lines, "\n")
}

// nindentFunc indents text with a newline prefix
func nindentFunc(spaces int, text string) string {
	return "\n" + strings.Repeat(" ", spaces) + strings.ReplaceAll(text, "\n", "\n"+strings.Repeat(" ", spaces))
}

// truncFunc truncates text to the specified length
func truncFunc(length int, text string) string {
	if len(text) <= length {
		return text
	}
	return text[:length]
}

// dictFunc creates a map from alternating key-value arguments
func dictFunc(values ...interface{}) map[string]interface{} {
	if len(values)%2 != 0 {
		panic("dict requires an even number of arguments")
	}
	result := make(map[string]interface{})
	for i := 0; i < len(values); i += 2 {
		key := fmt.Sprintf("%v", values[i])
		result[key] = values[i+1]
	}
	return result
}

// includeFunc is a placeholder for Helm's include function
func includeFunc(name string, data interface{}) (string, error) {
	// Return a comment indicating this is a placeholder - avoids nested template syntax
	return fmt.Sprintf("# Template include placeholder for '%s'", name), nil
}

// toYamlFunc provides simple YAML-like formatting for basic objects
func toYamlFunc(obj interface{}) string {
	switch v := obj.(type) {
	case map[string]interface{}:
		var lines []string
		for k, val := range v {
			lines = append(lines, fmt.Sprintf("%s: %v", k, val))
		}
		return strings.Join(lines, "\n")
	case []interface{}:
		var lines []string
		for _, val := range v {
			lines = append(lines, fmt.Sprintf("- %v", val))
		}
		return strings.Join(lines, "\n")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// typeOfFunc returns the type of an object as a string
func typeOfFunc(obj interface{}) string {
	return fmt.Sprintf("%T", obj)
}

// indexFunc provides nested map/slice access with multiple keys
func indexFunc(obj interface{}, keys ...interface{}) interface{} {
	current := obj
	for _, key := range keys {
		switch o := current.(type) {
		case map[string]interface{}:
			current = o[fmt.Sprintf("%v", key)]
		case []interface{}:
			if i, ok := key.(int); ok && i >= 0 && i < len(o) {
				current = o[i]
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return current
}

// joinFunc joins string slices with a separator
func joinFunc(sep string, elems []string) string {
	return strings.Join(elems, sep)
}

// lenFunc returns the length of various collection types
func lenFunc(obj interface{}) int {
	switch o := obj.(type) {
	case []interface{}:
		return len(o)
	case []App:
		return len(o)
	case []Artifact:
		return len(o)
	case map[string]interface{}:
		return len(o)
	case string:
		return len(o)
	default:
		return 0
	}
}

// hasKeyFunc checks if a map contains a specific key
func hasKeyFunc(obj interface{}, key string) bool {
	switch o := obj.(type) {
	case map[string]interface{}:
		_, exists := o[key]
		return exists
	default:
		return false
	}
}

// intFunc converts values to integers
func intFunc(obj interface{}) int {
	switch v := obj.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var i int
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

// gtFunc compares if a > b
func gtFunc(a, b interface{}) bool {
	return compareValues(a, b) > 0
}

// ltFunc compares if a < b
func ltFunc(a, b interface{}) bool {
	return compareValues(a, b) < 0
}

// eqFunc compares if a == b
func eqFunc(a, b interface{}) bool {
	return compareValues(a, b) == 0
}

// neFunc compares if a != b
func neFunc(a, b interface{}) bool {
	return compareValues(a, b) != 0
}

// andFunc performs logical AND
func andFunc(args ...interface{}) bool {
	for _, arg := range args {
		if !isTruthy(arg) {
			return false
		}
	}
	return true
}

// orFunc performs logical OR
func orFunc(args ...interface{}) bool {
	for _, arg := range args {
		if isTruthy(arg) {
			return true
		}
	}
	return false
}

// notFunc performs logical NOT
func notFunc(arg interface{}) bool {
	return !isTruthy(arg)
}

// kindIsFunc checks if object is of a specific kind
func kindIsFunc(kind string, obj interface{}) bool {
	switch kind {
	case "string":
		_, ok := obj.(string)
		return ok
	case "map":
		_, ok := obj.(map[string]interface{})
		return ok
	case "slice":
		_, ok := obj.([]interface{})
		return ok
	case "int":
		_, ok := obj.(int)
		return ok
	default:
		return false
	}
}

// Helper function to compare values
func compareValues(a, b interface{}) int {
	aInt := intFunc(a)
	bInt := intFunc(b)
	if aInt < bInt {
		return -1
	} else if aInt > bInt {
		return 1
	}
	return 0
}

// Helper function to check truthiness
func isTruthy(obj interface{}) bool {
	switch v := obj.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case string:
		return v != ""
	case nil:
		return false
	default:
		return true
	}
}
