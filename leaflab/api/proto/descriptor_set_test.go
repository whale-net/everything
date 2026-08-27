// Package proto_test exercises the published FileDescriptorSet Bazel
// artifact //leaflab/api/proto:leaflabapi_descriptor_set (FR81 contract
// half, NFR11): a programmatic caller must be able to learn
// leaflab.api.v1.LeafLabAPI's contract from this artifact alone, with no
// server reflection (FR11.1 turns that off) and no drift from the protos
// the server actually compiles and registers.
package proto_test

import (
	"os"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// loadDescriptorSet locates the Bazel-built FileDescriptorSet via the
// standard rules_go runfiles discovery process (works under `bazel test`,
// `bazel run`, and a plain `go test` invoked from within a runfiles tree)
// and parses it.
func loadDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()

	rf, err := runfiles.New()
	if err != nil {
		t.Fatalf("runfiles.New: %v", err)
	}
	path, err := rf.Rlocation("_main/leaflab/api/proto/leaflabapi_descriptor_set.pb")
	if err != nil {
		t.Fatalf("Rlocation(leaflabapi_descriptor_set.pb): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading descriptor set at %s: %v", path, err)
	}

	var fdSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(data, &fdSet); err != nil {
		t.Fatalf("unmarshaling descriptor set: %v", err)
	}
	return &fdSet
}

// TestDescriptorSet_ResolvesServiceWithImports asserts the published
// descriptor set is self-contained: parsing it alone (no server reflection,
// no separately-fetched imports) resolves leaflab.api.v1.LeafLabAPI and,
// transitively via --include_imports, firmware.SensorConfig from
// firmware/proto/config.proto.
func TestDescriptorSet_ResolvesServiceWithImports(t *testing.T) {
	fdSet := loadDescriptorSet(t)

	files, err := protodesc.NewFiles(fdSet)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v (descriptor set is not self-contained/resolvable)", err)
	}

	svcDesc, err := files.FindDescriptorByName("leaflab.api.v1.LeafLabAPI")
	if err != nil {
		t.Fatalf("FindDescriptorByName(leaflab.api.v1.LeafLabAPI): %v", err)
	}
	svc, ok := svcDesc.(protoreflect.ServiceDescriptor)
	if !ok {
		t.Fatalf("leaflab.api.v1.LeafLabAPI resolved to %T, want a service descriptor", svcDesc)
	}
	if got, want := svc.Methods().Len(), 4; got != want {
		t.Errorf("LeafLabAPI has %d methods, want %d (PushDeviceConfig, GetDeviceConfig, ListBoards, GetHealth)", got, want)
	}

	msgDesc, err := files.FindDescriptorByName("firmware.SensorConfig")
	if err != nil {
		t.Fatalf("FindDescriptorByName(firmware.SensorConfig): %v (transitive import not included)", err)
	}
	if _, ok := msgDesc.(protoreflect.MessageDescriptor); !ok {
		t.Fatalf("firmware.SensorConfig resolved to %T, want a message descriptor", msgDesc)
	}
}

// TestDescriptorSet_MatchesCompiledServerProtos asserts the published
// descriptor set's service/method shape is identical to
// leaflabapipb.File_leaflab_api_proto_api_proto -- the exact compiled-in
// FileDescriptor that //leaflab/api:api_lib (the server binary) embeds via
// its go_proto_library dependency on the very same proto_library this
// descriptor set is built from. A caller resolving the contract from this
// artifact must see the same RPCs, in the same order, with the same
// request/response types, as the server that will actually serve them --
// not a second, potentially stale copy of the same protos.
func TestDescriptorSet_MatchesCompiledServerProtos(t *testing.T) {
	fdSet := loadDescriptorSet(t)

	files, err := protodesc.NewFiles(fdSet)
	if err != nil {
		t.Fatalf("protodesc.NewFiles: %v", err)
	}
	descSetService, err := files.FindDescriptorByName("leaflab.api.v1.LeafLabAPI")
	if err != nil {
		t.Fatalf("FindDescriptorByName(leaflab.api.v1.LeafLabAPI): %v", err)
	}
	fromDescSet, ok := descSetService.(protoreflect.ServiceDescriptor)
	if !ok {
		t.Fatalf("leaflab.api.v1.LeafLabAPI resolved to %T, want a service descriptor", descSetService)
	}

	// The compiled-in FileDescriptor the server (and this test binary, via
	// its own import of leaflabapipb) actually links against.
	compiledFile := leaflabapipb.File_leaflab_api_proto_api_proto
	compiledService := compiledFile.Services().Get(0)
	if string(compiledService.FullName()) != "leaflab.api.v1.LeafLabAPI" {
		t.Fatalf("compiled-in service full name = %q, want leaflab.api.v1.LeafLabAPI", compiledService.FullName())
	}

	if got, want := fromDescSet.Methods().Len(), compiledService.Methods().Len(); got != want {
		t.Fatalf("method count mismatch: descriptor set has %d, compiled server protos have %d", got, want)
	}
	for i := 0; i < compiledService.Methods().Len(); i++ {
		want := compiledService.Methods().Get(i)
		got := fromDescSet.Methods().Get(i)

		if got.Name() != want.Name() {
			t.Errorf("method[%d] name = %q, want %q", i, got.Name(), want.Name())
		}
		if got.Input().FullName() != want.Input().FullName() {
			t.Errorf("method %q input type = %q, want %q", want.Name(), got.Input().FullName(), want.Input().FullName())
		}
		if got.Output().FullName() != want.Output().FullName() {
			t.Errorf("method %q output type = %q, want %q", want.Name(), got.Output().FullName(), want.Output().FullName())
		}
		if got.IsStreamingClient() != want.IsStreamingClient() || got.IsStreamingServer() != want.IsStreamingServer() {
			t.Errorf("method %q streaming shape mismatch: got client=%v server=%v, want client=%v server=%v",
				want.Name(), got.IsStreamingClient(), got.IsStreamingServer(), want.IsStreamingClient(), want.IsStreamingServer())
		}
	}

	// Belt-and-suspenders: the compiled file must itself already be
	// registered in the global registry (proof leaflabapipb was actually
	// imported/linked, not merely referenced by type), and must be the same
	// file compiled from the same .proto path as the descriptor set.
	registered, err := protoregistry.GlobalFiles.FindFileByPath(string(compiledFile.Path()))
	if err != nil {
		t.Fatalf("compiled file %q not found in global registry: %v", compiledFile.Path(), err)
	}
	if registered != compiledFile {
		t.Fatalf("global registry file for %q is not the same instance as leaflabapipb.File_leaflab_api_proto_api_proto", compiledFile.Path())
	}
}
