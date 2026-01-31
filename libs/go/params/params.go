package params

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parameter represents a typed configuration parameter
type Parameter struct {
	Key          string
	Value        string
	Type         string // "string" | "int" | "bool" | "secret"
	Description  string
	Required     bool
	DefaultValue string
}

// ValidationError represents a parameter validation error
type ValidationError struct {
	Parameter string
	Message   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("parameter %q: %s", e.Parameter, e.Message)
}

// MergeParams merges parameters from multiple levels (GameConfig → ServerGameConfig → Session)
// Later levels override earlier levels.
func MergeParams(definitions []*Parameter, overrides ...map[string]string) map[string]string {
	result := make(map[string]string)

	// Start with default values from definitions
	for _, def := range definitions {
		if def.DefaultValue != "" {
			result[def.Key] = def.DefaultValue
		}
	}

	// Apply overrides in order (ServerGameConfig, then Session)
	for _, override := range overrides {
		for k, v := range override {
			result[k] = v
		}
	}

	return result
}

// ValidateParams validates parameters against their definitions
func ValidateParams(definitions []*Parameter, values map[string]string) error {
	defMap := make(map[string]*Parameter)
	for _, def := range definitions {
		defMap[def.Key] = def
	}

	// Check required parameters
	for _, def := range definitions {
		if def.Required {
			if _, ok := values[def.Key]; !ok {
				return &ValidationError{
					Parameter: def.Key,
					Message:   "required parameter is missing",
				}
			}
		}
	}

	// Validate types
	for key, value := range values {
		def, ok := defMap[key]
		if !ok {
			// Unknown parameter (warning, not error)
			continue
		}

		if err := validateType(value, def.Type); err != nil {
			return &ValidationError{
				Parameter: key,
				Message:   err.Error(),
			}
		}
	}

	return nil
}

// validateType validates a value against a type
func validateType(value, paramType string) error {
	switch paramType {
	case "string", "secret":
		// Any string is valid
		return nil
	case "int":
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("must be an integer, got %q", value)
		}
	case "bool":
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("must be a boolean (true/false), got %q", value)
		}
	default:
		return fmt.Errorf("unknown parameter type %q", paramType)
	}
	return nil
}

// RenderTemplate replaces {{param_name}} placeholders in a template string
// with values from the parameters map.
func RenderTemplate(template string, params map[string]string) string {
	re := regexp.MustCompile(`\{\{([a-zA-Z0-9_]+)\}\}`)

	return re.ReplaceAllStringFunc(template, func(match string) string {
		// Extract parameter name from {{param_name}}
		paramName := strings.TrimPrefix(strings.TrimSuffix(match, "}}"), "{{")

		if value, ok := params[paramName]; ok {
			return value
		}

		// If parameter not found, leave placeholder as-is
		return match
	})
}

// ConvertToType converts a string value to the appropriate type
// Returns the value as interface{} for use in templates
func ConvertToType(value, paramType string) (interface{}, error) {
	switch paramType {
	case "string", "secret":
		return value, nil
	case "int":
		return strconv.ParseInt(value, 10, 64)
	case "bool":
		return strconv.ParseBool(value)
	default:
		return nil, fmt.Errorf("unknown parameter type %q", paramType)
	}
}

// FormatErrors formats multiple validation errors into a single message
func FormatErrors(errors []*ValidationError) string {
	if len(errors) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("parameter validation failed:\n")
	for _, err := range errors {
		sb.WriteString(fmt.Sprintf("  - %s\n", err.Error()))
	}
	return sb.String()
}

// GetMissingRequired returns a list of required parameters that are missing
func GetMissingRequired(definitions []*Parameter, values map[string]string) []string {
	var missing []string
	for _, def := range definitions {
		if def.Required {
			if _, ok := values[def.Key]; !ok {
				missing = append(missing, def.Key)
			}
		}
	}
	return missing
}

// GetUnknownParams returns a list of parameters in values that are not defined
func GetUnknownParams(definitions []*Parameter, values map[string]string) []string {
	defMap := make(map[string]bool)
	for _, def := range definitions {
		defMap[def.Key] = true
	}

	var unknown []string
	for key := range values {
		if !defMap[key] {
			unknown = append(unknown, key)
		}
	}
	return unknown
}
