package workshop

import (
	"go/parser"
	"go/token"
	"os"
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
// (a glob() of every non-test .go file in this package) for how the sources are
// made available to this test via the Bazel runfiles manifest. The file list below
// is discovered at test time from that runfiles directory, rather than hardcoded,
// so a future source file added to this package (e.g. a new S3/cache-adjacent
// helper) is automatically checked -- no second list to remember to update when the
// package grows. The full transitive-closure check
// (`bazel query 'deps(//manmanv2/host:host-manager)' | grep -i aws`) is the
// Validation-phase command that corroborates this at the whole-binary level.
func TestNFR7_NoAWSSDKImport(t *testing.T) {
	forbiddenImportPrefixes := []string{
		"github.com/aws/aws-sdk-go-v2",
		"github.com/whale-net/everything/libs/go/s3",
	}

	// Resolve a known-stable anchor file to find this package's runfiles directory,
	// then discover every other non-test .go file alongside it. This avoids a second
	// hardcoded file list that would need to stay in sync with BUILD.bazel's glob().
	anchor, err := runfiles.Rlocation("_main/manmanv2/host/workshop/cache_client.go")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(cache_client.go): %v (is the workshop package's data glob still present in workshop_test's BUILD.bazel?)", err)
	}
	dir := filepath.Dir(anchor)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir(%s): %v", dir, err)
	}

	var srcFiles []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		srcFiles = append(srcFiles, name)
	}
	if len(srcFiles) == 0 {
		t.Fatalf("no non-test .go files discovered in %s -- guard is not checking anything", dir)
	}

	for _, srcFile := range srcFiles {
		resolved := filepath.Join(dir, srcFile)

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
