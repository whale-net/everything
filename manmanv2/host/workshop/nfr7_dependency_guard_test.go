package workshop

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// TestNFR7_NoAWSSDKImport is a structural guard for issue #2183's NFR7: the S3 cache
// read path must be implemented against the standard library net/http against a
// presigned URL (exactly like manmanv2/host/backup.go's presigned PUT), never by
// importing the AWS S3 SDK (github.com/aws/aws-sdk-go-v2 or //libs/go/s3) into
// host-manager's dependency graph. Pulling the SDK in would grow the ARM64
// cross-compile surface unnecessarily and invite standing S3 credentials later.
//
// This inspects this package's own source files' import declarations directly
// (rather than shelling out to `bazel query`, which isn't available inside a
// sandboxed `bazel test` run) -- see BUILD.bazel's workshop_test "data" attribute
// for how the sources are made available to this test via the Bazel runfiles
// manifest. The full transitive-closure check
// (`bazel query 'deps(//manmanv2/host:host-manager)' | grep -i aws`) is the
// Validation-phase command that corroborates this at the whole-binary level.
func TestNFR7_NoAWSSDKImport(t *testing.T) {
	forbiddenImportPrefixes := []string{
		"github.com/aws/aws-sdk-go-v2",
		"github.com/whale-net/everything/libs/go/s3",
	}

	srcFiles := []string{"cache_client.go", "orchestrator.go"}

	for _, srcFile := range srcFiles {
		resolved, err := runfiles.Rlocation("_main/manmanv2/host/workshop/" + srcFile)
		if err != nil {
			t.Fatalf("runfiles.Rlocation(%s): %v (is %s listed in workshop_test's data attribute?)", srcFile, srcFile, srcFile)
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, resolved, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile(%s): %v", resolved, err)
		}

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, forbidden := range forbiddenImportPrefixes {
				if strings.HasPrefix(importPath, forbidden) {
					t.Errorf("%s imports %q, which pulls the AWS S3 SDK into host-manager's dependency graph -- NFR7 requires the presigned-URL transfer to use only net/http (see manmanv2/host/backup.go's presigned PUT for the existing pattern)", filepath.Base(resolved), importPath)
				}
			}
		}
	}
}
