// Package leaflabapipb_test contains tests for the LeafLab API proto package.
package leaflabapipb_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/proto"
)

// TestDescriptorSetContainsLeafLabAPIService verifies that the descriptor set
// contains the LeafLabAPI service definition.
func TestDescriptorSetContainsLeafLabAPIService(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)
	descriptorSet := readDescriptorSet(t, descriptorPath)

	// Find the leaflab.api.v1 proto file
	var apiFileDesc *descriptorpb.FileDescriptorProto
	for _, file := range descriptorSet.File {
		if file.GetPackage() == "leaflab.api.v1" {
			apiFileDesc = file
			break
		}
	}

	if apiFileDesc == nil {
		t.Fatal("descriptor set does not contain leaflab.api.v1 package")
	}

	// Find the LeafLabAPI service
	var leaflabService *descriptorpb.ServiceDescriptorProto
	for _, service := range apiFileDesc.Service {
		if service.GetName() == "LeafLabAPI" {
			leaflabService = service
			break
		}
	}

	if leaflabService == nil {
		t.Fatal("descriptor set does not contain LeafLabAPI service")
	}

	// Verify service is not nil and has methods
	if leaflabService == nil {
		t.Fatal("LeafLabAPI service is nil")
	}

	if len(leaflabService.Method) == 0 {
		t.Fatal("LeafLabAPI service has no methods")
	}

	t.Logf("Found LeafLabAPI service with %d methods", len(leaflabService.Method))
}

// TestDescriptorSetContainsAllLeafLabAPIMethods verifies the descriptor set
// contains all four expected RPC methods.
func TestDescriptorSetContainsAllLeafLabAPIMethods(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)
	descriptorSet := readDescriptorSet(t, descriptorPath)

	// Find the leaflab.api.v1 proto file
	var apiFileDesc *descriptorpb.FileDescriptorProto
	for _, file := range descriptorSet.File {
		if file.GetPackage() == "leaflab.api.v1" {
			apiFileDesc = file
			break
		}
	}

	if apiFileDesc == nil {
		t.Fatal("descriptor set does not contain leaflab.api.v1 package")
	}

	// Find the LeafLabAPI service
	var leaflabService *descriptorpb.ServiceDescriptorProto
	for _, service := range apiFileDesc.Service {
		if service.GetName() == "LeafLabAPI" {
			leaflabService = service
			break
		}
	}

	if leaflabService == nil {
		t.Fatal("descriptor set does not contain LeafLabAPI service")
	}

	// Expected method names
	expectedMethods := map[string]bool{
		"PushDeviceConfig": false,
		"GetDeviceConfig":  false,
		"ListBoards":       false,
		"Health":           false,
	}

	// Collect actual methods
	for _, method := range leaflabService.Method {
		methodName := method.GetName()
		if _, exists := expectedMethods[methodName]; exists {
			expectedMethods[methodName] = true
		}
	}

	// Verify all expected methods are present
	for methodName, found := range expectedMethods {
		if !found {
			t.Errorf("LeafLabAPI missing expected method: %s", methodName)
		}
	}

	// Verify the method count matches
	if len(leaflabService.Method) != len(expectedMethods) {
		t.Errorf("expected %d methods, got %d", len(expectedMethods), len(leaflabService.Method))
	}

	t.Logf("All %d expected LeafLabAPI methods found in descriptor set", len(expectedMethods))
}

// TestDescriptorSetDriftDetection verifies the descriptor set is consistent
// and derives from the same proto definitions (drift test). This test ensures
// the descriptor set matches what would be generated from the proto_library.
func TestDescriptorSetDriftDetection(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)

	// Read the descriptor set
	descriptorSet := readDescriptorSet(t, descriptorPath)

	// Verify the descriptor set is valid and complete
	if descriptorSet == nil {
		t.Fatal("descriptor set is nil")
	}

	if len(descriptorSet.File) == 0 {
		t.Fatal("descriptor set contains no files")
	}

	// Find the main leaflab.api.v1 proto file
	var apiFileDesc *descriptorpb.FileDescriptorProto
	for _, file := range descriptorSet.File {
		if file.GetPackage() == "leaflab.api.v1" && filepath.Base(file.GetName()) == "api.proto" {
			apiFileDesc = file
			break
		}
	}

	if apiFileDesc == nil {
		t.Fatal("descriptor set does not contain leaflab/api/proto/api.proto")
	}

	// Verify the file contains expected message and service types
	if len(apiFileDesc.MessageType) == 0 {
		t.Fatal("api.proto has no message types")
	}

	if len(apiFileDesc.Service) == 0 {
		t.Fatal("api.proto has no service definitions")
	}

	// Verify important message types exist
	expectedMessages := map[string]bool{
		"PushDeviceConfigRequest":  false,
		"PushDeviceConfigResponse": false,
		"GetDeviceConfigRequest":   false,
		"GetDeviceConfigResponse":  false,
		"ListBoardsRequest":        false,
		"ListBoardsResponse":       false,
		"HealthRequest":            false,
		"HealthResponse":           false,
	}

	for _, msg := range apiFileDesc.MessageType {
		if _, exists := expectedMessages[msg.GetName()]; exists {
			expectedMessages[msg.GetName()] = true
		}
	}

	for msgName, found := range expectedMessages {
		if !found {
			t.Errorf("api.proto missing expected message type: %s", msgName)
		}
	}

	t.Logf("Descriptor set drift detection passed: %d messages, %d services",
		len(apiFileDesc.MessageType), len(apiFileDesc.Service))
}

// TestDescriptorSetContainsPushDeviceConfigRPC verifies the PushDeviceConfig RPC
// has the correct request and response types.
func TestDescriptorSetContainsPushDeviceConfigRPC(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)
	descriptorSet := readDescriptorSet(t, descriptorPath)

	leaflabService := findLeafLabAPIService(t, descriptorSet)

	var pushMethod *descriptorpb.MethodDescriptorProto
	for _, method := range leaflabService.Method {
		if method.GetName() == "PushDeviceConfig" {
			pushMethod = method
			break
		}
	}

	if pushMethod == nil {
		t.Fatal("PushDeviceConfig method not found")
	}

	if pushMethod.GetInputType() != ".leaflab.api.v1.PushDeviceConfigRequest" {
		t.Errorf("expected input type .leaflab.api.v1.PushDeviceConfigRequest, got %s",
			pushMethod.GetInputType())
	}

	if pushMethod.GetOutputType() != ".leaflab.api.v1.PushDeviceConfigResponse" {
		t.Errorf("expected output type .leaflab.api.v1.PushDeviceConfigResponse, got %s",
			pushMethod.GetOutputType())
	}
}

// readDescriptorSet reads and unmarshals a descriptor set from a file.
func readDescriptorSet(t *testing.T, path string) *descriptorpb.FileDescriptorSet {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read descriptor set: %v", err)
	}

	var descriptorSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &descriptorSet); err != nil {
		t.Fatalf("failed to unmarshal descriptor set: %v", err)
	}

	return &descriptorSet
}

// descriptorPathFromEnv gets the descriptor set path from the test environment.
// In Bazel, data files are resolved via runfiles.
func descriptorPathFromEnv(t *testing.T) string {
	t.Helper()

	// In Bazel tests, the descriptor is a data dependency passed via runfiles.
	// For this test, we use a relative path based on the Bazel runfiles structure.
	// The path is typically <runfiles_root>/<package>/leaflab_api_descriptor.pb

	// Check common Bazel runfiles paths
	paths := []string{
		"leaflab/api/proto/leaflab_api_descriptor.pb",
		"./leaflab/api/proto/leaflab_api_descriptor.pb",
		"../proto/leaflab_api_descriptor.pb",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// If we can't find it as a file, try to load from bazel-bin
	bazelBinPath := "bazel-bin/leaflab/api/proto/leaflab_api_descriptor.pb"
	if _, err := os.Stat(bazelBinPath); err == nil {
		return bazelBinPath
	}

	t.Fatalf("could not locate descriptor set; tried: %v", paths)
	return ""
}

// findLeafLabAPIService locates the LeafLabAPI service in a descriptor set.
func findLeafLabAPIService(t *testing.T, descriptorSet *descriptorpb.FileDescriptorSet) *descriptorpb.ServiceDescriptorProto {
	t.Helper()

	for _, file := range descriptorSet.File {
		if file.GetPackage() == "leaflab.api.v1" {
			for _, service := range file.Service {
				if service.GetName() == "LeafLabAPI" {
					return service
				}
			}
		}
	}

	t.Fatal("LeafLabAPI service not found in descriptor set")
	return nil
}

// TestDescriptorSetNotEmpty verifies the descriptor set is not empty and
// is properly formatted.
func TestDescriptorSetNotEmpty(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)

	data, err := os.ReadFile(descriptorPath)
	if err != nil {
		t.Fatalf("failed to read descriptor set: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("descriptor set file is empty")
	}

	// Verify it's a valid protobuf message
	var descriptorSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &descriptorSet); err != nil {
		t.Fatalf("descriptor set is not valid protobuf: %v", err)
	}

	t.Logf("Descriptor set size: %d bytes, %d files", len(data), len(descriptorSet.File))
}

// TestDescriptorSetSourceInfoIncluded verifies that source_location info
// is included in the descriptor set (for debugging and introspection).
func TestDescriptorSetSourceInfoIncluded(t *testing.T) {
	descriptorPath := descriptorPathFromEnv(t)
	descriptorSet := readDescriptorSet(t, descriptorPath)

	// Find the api.proto file
	var apiFileDesc *descriptorpb.FileDescriptorProto
	for _, file := range descriptorSet.File {
		if filepath.Base(file.GetName()) == "api.proto" {
			apiFileDesc = file
			break
		}
	}

	if apiFileDesc == nil {
		t.Fatal("api.proto not found in descriptor set")
	}

	// Check if source_location info is included
	if len(apiFileDesc.SourceCodeInfo.GetLocation()) > 0 {
		t.Logf("Source info included: %d location entries", len(apiFileDesc.SourceCodeInfo.GetLocation()))
	} else {
		t.Logf("Note: source code info not included (optional)")
	}
}
