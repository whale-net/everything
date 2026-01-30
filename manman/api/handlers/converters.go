package handlers

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/whale-net/everything/manman"
	pb "github.com/whale-net/everything/manman/protos"
)

// ============================================================================
// PortBinding conversions
// ============================================================================

func portBindingsToJSONB(pbs []*pb.PortBinding) manman.JSONB {
	if len(pbs) == 0 {
		return nil
	}
	result := make(map[string]interface{})
	for _, pb := range pbs {
		key := fmt.Sprintf("%d/%s", pb.ContainerPort, pb.Protocol)
		result[key] = float64(pb.HostPort) // JSON numbers are float64
	}
	return result
}

func jsonbToPortBindings(j manman.JSONB) []*pb.PortBinding {
	if j == nil {
		return nil
	}
	var bindings []*pb.PortBinding
	for key, value := range j {
		parts := strings.Split(key, "/")
		if len(parts) != 2 {
			continue
		}
		containerPort, _ := strconv.Atoi(parts[0])
		protocol := parts[1]
		hostPort := int32(value.(float64))
		bindings = append(bindings, &pb.PortBinding{
			ContainerPort: int32(containerPort),
			HostPort:      hostPort,
			Protocol:      protocol,
		})
	}
	return bindings
}

// ============================================================================
// Map<string,string> conversions (parameters, env_template)
// ============================================================================

func mapToJSONB(m map[string]string) manman.JSONB {
	if len(m) == 0 {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}

func jsonbToMap(j manman.JSONB) map[string]string {
	if j == nil {
		return nil
	}
	result := make(map[string]string)
	for k, v := range j {
		if str, ok := v.(string); ok {
			result[k] = str
		}
	}
	return result
}

// ============================================================================
// FileTemplate conversions
// ============================================================================

func filesToJSONB(files []*pb.FileTemplate) manman.JSONB {
	if len(files) == 0 {
		return nil
	}
	fileList := make([]map[string]interface{}, 0, len(files))
	for _, f := range files {
		fileList = append(fileList, map[string]interface{}{
			"path":        f.Path,
			"content":     f.Content,
			"mode":        f.Mode,
			"is_template": f.IsTemplate,
		})
	}
	return map[string]interface{}{"files": fileList}
}

func jsonbToFiles(j manman.JSONB) []*pb.FileTemplate {
	if j == nil {
		return nil
	}
	fileList, ok := j["files"].([]interface{})
	if !ok {
		return nil
	}

	result := make([]*pb.FileTemplate, 0, len(fileList))
	for _, item := range fileList {
		fileMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		path, _ := fileMap["path"].(string)
		content, _ := fileMap["content"].(string)
		mode, _ := fileMap["mode"].(string)
		isTemplate, _ := fileMap["is_template"].(bool)

		result = append(result, &pb.FileTemplate{
			Path:       path,
			Content:    content,
			Mode:       mode,
			IsTemplate: isTemplate,
		})
	}
	return result
}

// ============================================================================
// Parameter conversions
// ============================================================================

func parametersToJSONB(params []*pb.Parameter) manman.JSONB {
	if len(params) == 0 {
		return nil
	}
	paramList := make([]map[string]interface{}, 0, len(params))
	for _, p := range params {
		paramList = append(paramList, map[string]interface{}{
			"key":           p.Key,
			"value":         p.Value,
			"type":          p.Type,
			"description":   p.Description,
			"required":      p.Required,
			"default_value": p.DefaultValue,
		})
	}
	return map[string]interface{}{"parameters": paramList}
}

func jsonbToParameters(j manman.JSONB) []*pb.Parameter {
	if j == nil {
		return nil
	}
	paramList, ok := j["parameters"].([]interface{})
	if !ok {
		return nil
	}

	result := make([]*pb.Parameter, 0, len(paramList))
	for _, item := range paramList {
		pMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		key, _ := pMap["key"].(string)
		value, _ := pMap["value"].(string)
		ptype, _ := pMap["type"].(string)
		description, _ := pMap["description"].(string)
		required, _ := pMap["required"].(bool)
		defaultValue, _ := pMap["default_value"].(string)

		result = append(result, &pb.Parameter{
			Key:          key,
			Value:        value,
			Type:         ptype,
			Description:  description,
			Required:     required,
			DefaultValue: defaultValue,
		})
	}
	return result
}

// ============================================================================
// GameMetadata conversions
// ============================================================================

func metadataToJSONB(m *pb.GameMetadata) manman.JSONB {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{})
	result["genre"] = m.Genre
	result["publisher"] = m.Publisher
	result["default_players"] = float64(m.DefaultPlayers)
	result["max_players"] = float64(m.MaxPlayers)

	if m.Links != nil {
		linksMap := make(map[string]interface{})
		for k, v := range m.Links {
			linksMap[k] = v
		}
		result["links"] = linksMap
	}

	if m.Tags != nil {
		tags := make([]interface{}, len(m.Tags))
		for i, tag := range m.Tags {
			tags[i] = tag
		}
		result["tags"] = tags
	}

	return result
}

func jsonbToMetadata(j manman.JSONB) *pb.GameMetadata {
	if j == nil {
		return nil
	}

	metadata := &pb.GameMetadata{}

	if genre, ok := j["genre"].(string); ok {
		metadata.Genre = genre
	}
	if publisher, ok := j["publisher"].(string); ok {
		metadata.Publisher = publisher
	}
	if defaultPlayers, ok := j["default_players"].(float64); ok {
		metadata.DefaultPlayers = int32(defaultPlayers)
	}
	if maxPlayers, ok := j["max_players"].(float64); ok {
		metadata.MaxPlayers = int32(maxPlayers)
	}

	if linksMap, ok := j["links"].(map[string]interface{}); ok {
		links := make(map[string]string)
		for k, v := range linksMap {
			if str, ok := v.(string); ok {
				links[k] = str
			}
		}
		metadata.Links = links
	}

	if tagsList, ok := j["tags"].([]interface{}); ok {
		tags := make([]string, 0, len(tagsList))
		for _, t := range tagsList {
			if str, ok := t.(string); ok {
				tags = append(tags, str)
			}
		}
		metadata.Tags = tags
	}

	return metadata
}

// ============================================================================
// Helper functions
// ============================================================================

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
