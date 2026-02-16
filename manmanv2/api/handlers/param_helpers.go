package handlers

import (
	"fmt"

	"github.com/whale-net/everything/libs/go/params"
	"github.com/whale-net/everything/manman"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// MergeAndValidateParameters merges parameters from GameConfig, ServerGameConfig, and Session
// then validates them against the GameConfig parameter definitions.
// Returns the merged parameters and any validation errors.
func MergeAndValidateParameters(
	gameConfig *manman.GameConfig,
	serverGameConfig *manman.ServerGameConfig,
	sessionParams map[string]string,
) (map[string]string, error) {
	// Convert GameConfig parameters from JSONB to params.Parameter slice
	definitions := jsonbToParamsDefinitions(gameConfig.Parameters)

	// Extract override maps from JSONB
	sgcOverrides := jsonbToStringMap(serverGameConfig.Parameters)

	// Merge: GameConfig defaults → ServerGameConfig → Session
	merged := params.MergeParams(definitions, sgcOverrides, sessionParams)

	// Validate merged parameters
	if err := params.ValidateParams(definitions, merged); err != nil {
		return nil, err
	}

	return merged, nil
}

// ValidateParametersWithDetails validates parameters and returns detailed validation issues
func ValidateParametersWithDetails(
	definitions []*params.Parameter,
	values map[string]string,
) []*pb.ValidationIssue {
	var issues []*pb.ValidationIssue

	// Check for missing required parameters
	missing := params.GetMissingRequired(definitions, values)
	for _, key := range missing {
		var description string
		for _, def := range definitions {
			if def.Key == key {
				description = def.Description
				break
			}
		}

		issues = append(issues, &pb.ValidationIssue{
			Severity:   pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR,
			Field:      "parameters." + key,
			Message:    fmt.Sprintf("Required parameter '%s' is missing", key),
			Suggestion: description,
		})
	}

	// Validate types for provided parameters
	defMap := make(map[string]*params.Parameter)
	for _, def := range definitions {
		defMap[def.Key] = def
	}

	for key, value := range values {
		def, ok := defMap[key]
		if !ok {
			// Unknown parameter - issue warning
			issues = append(issues, &pb.ValidationIssue{
				Severity:   pb.ValidationSeverity_VALIDATION_SEVERITY_WARNING,
				Field:      "parameters." + key,
				Message:    fmt.Sprintf("Unknown parameter '%s'", key),
				Suggestion: "This parameter is not defined in the game config",
			})
			continue
		}

		// Validate type
		if _, err := params.ConvertToType(value, def.Type); err != nil {
			issues = append(issues, &pb.ValidationIssue{
				Severity:   pb.ValidationSeverity_VALIDATION_SEVERITY_ERROR,
				Field:      "parameters." + key,
				Message:    fmt.Sprintf("Invalid value for parameter '%s': %v", key, err),
				Suggestion: fmt.Sprintf("Expected type: %s", def.Type),
			})
		}
	}

	return issues
}

// RenderGameConfigTemplates renders args and env templates with parameter values
func RenderGameConfigTemplates(
	gameConfig *manman.GameConfig,
	mergedParams map[string]string,
) (args string, env map[string]string) {
	// Render args template
	if gameConfig.ArgsTemplate != nil {
		args = params.RenderTemplate(*gameConfig.ArgsTemplate, mergedParams)
	}

	// Render env template
	env = make(map[string]string)
	if gameConfig.EnvTemplate != nil {
		envMap := jsonbToStringMap(gameConfig.EnvTemplate)
		for key, valueTemplate := range envMap {
			env[key] = params.RenderTemplate(valueTemplate, mergedParams)
		}
	}

	return args, env
}

// jsonbToParamsDefinitions converts manman.JSONB to []*params.Parameter
func jsonbToParamsDefinitions(j manman.JSONB) []*params.Parameter {
	pbParams := jsonbToParameters(j)
	result := make([]*params.Parameter, len(pbParams))
	for i, p := range pbParams {
		result[i] = &params.Parameter{
			Key:          p.Key,
			Value:        p.Value,
			Type:         p.Type,
			Description:  p.Description,
			Required:     p.Required,
			DefaultValue: p.DefaultValue,
		}
	}
	return result
}

// jsonbToStringMap converts JSONB to map[string]string
// Handles both direct maps and nested structures
func jsonbToStringMap(j manman.JSONB) map[string]string {
	if j == nil {
		return make(map[string]string)
	}

	result := make(map[string]string)
	for k, v := range j {
		if strVal, ok := v.(string); ok {
			result[k] = strVal
		} else if strVal, ok := v.(interface{}); ok {
			result[k] = fmt.Sprintf("%v", strVal)
		}
	}
	return result
}
