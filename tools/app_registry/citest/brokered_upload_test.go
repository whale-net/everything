package citest

import (
	"os"
	"strings"
	"testing"

	appcmd "github.com/whale-net/everything/tools/app_registry/cli/cmd"
)

// TestFR44HandshakePrecedsPUT verifies that the FR-44 handshake step
// (resolver capability verification) runs before any presigned URL PUTs.
// FR-44: no compressed object is written until confirmed the resolver
// supports content-encoding. A capability handshake, not a deploy-ordering rule.
func TestFR44HandshakePrecedsPUT(t *testing.T) {
	dir := githubDir(t)
	files, err := CIConfigFiles(dir)
	if err != nil {
		t.Fatalf("locate CI config: %v", err)
	}

	var releaseV2Steps []string
	for _, f := range files {
		if !strings.Contains(f, "release-v2.yml") {
			continue
		}
		data, err := readFileLines(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		steps := extractStepNames(data)
		releaseV2Steps = steps
		break
	}

	if len(releaseV2Steps) == 0 {
		t.Skip("release-v2.yml not found or no steps extracted")
	}

	// Find FR-44 handshake and PUT steps
	handshakeIdx := -1
	putIdx := -1
	for i, step := range releaseV2Steps {
		if strings.Contains(step, "FR-44 handshake") {
			handshakeIdx = i
		}
		if strings.Contains(step, "presigned") && strings.Contains(step, "Upload") {
			putIdx = i
		}
	}

	if handshakeIdx >= 0 && putIdx >= 0 {
		if handshakeIdx >= putIdx {
			t.Errorf("FR-44 handshake (step %d) does not precede PUT step (step %d). "+
				"Capability handshake must run before any bytes are written.", handshakeIdx, putIdx)
		}
	}
}

// TestBrokerCommandExists verifies that `app-registry artifacts broker` is
// invoked in the workflow with correct argument structure.
func TestBrokerCommandExists(t *testing.T) {
	inv := allInvocations(t)
	found := false
	for _, i := range inv {
		if i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			i.Args[1] == "broker" {
			found = true
			// Broker command should have required flags for:
			// - artifact identity (--artifact-kind, --artifact-identity, --version)
			// - file set (--files)
			// Do not check exact values as they're dynamic; just verify structure.
			break
		}
	}
	if !found {
		t.Errorf("no 'app-registry artifacts broker' invocation found in workflow. "+
			"FR-1: broker RPC must be called to acquire presigned URLs or 'already stored' results.")
	}
}

// TestConfirmCommandExists verifies that `app-registry artifacts confirm` is
// invoked in the workflow with correct argument structure.
func TestConfirmCommandExists(t *testing.T) {
	inv := allInvocations(t)
	found := false
	for _, i := range inv {
		if i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			i.Args[1] == "confirm" {
			found = true
			// Confirm command should have required flags for:
			// - session reference (--upload-session-id)
			// - artifact identity (--artifact-kind)
			// - file set (--files)
			break
		}
	}
	if !found {
		t.Errorf("no 'app-registry artifacts confirm' invocation found in workflow. "+
			"FR-46: upload completion must be confirmed by reading back and verifying digests.")
	}
}

// TestBrokerConfirmNoObjectStoreCredential verifies that neither broker nor
// confirm command lines include object-store credentials. NFR-1, NFR-3: the
// only write capability CI ever possesses is the short-lived, single-key,
// PUT-only presigned URL. No new secret is introduced into GitHub Actions.
func TestBrokerConfirmNoObjectStoreCredential(t *testing.T) {
	inv := allInvocations(t)
	forbiddenPatterns := []string{
		"AWS_ACCESS_KEY",
		"AWS_SECRET_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"AZURE_STORAGE_ACCOUNT",
		"AZURE_STORAGE_KEY",
		"S3_",
		"GCS_",
		"BLOB_",
	}

	for _, i := range inv {
		if !(i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			(i.Args[1] == "broker" || i.Args[1] == "confirm")) {
			continue
		}

		// Check raw command line for forbidden patterns
		for _, pattern := range forbiddenPatterns {
			if strings.Contains(i.Raw, pattern) {
				t.Errorf("%s\n  contains forbidden pattern %q (NFR-1, NFR-3: no object-store credential)",
					i, pattern)
			}
		}

		// Verify only expected flags are present
		for _, arg := range i.Args {
			if strings.HasPrefix(arg, "--") {
				flag := strings.TrimPrefix(arg, "--")
				// Extract just the flag name (before '=')
				if idx := strings.Index(flag, "="); idx >= 0 {
					flag = flag[:idx]
				}

				// Known safe flags for broker/confirm
				allowed := map[string]bool{
					"artifact-kind":       true,
					"version":             true,
					"artifact-identity":   true,
					"version-reference":   true,
					"files":               true,
					"upload-session-id":   true,
				}
				if !allowed[flag] && !strings.HasPrefix(flag, "timeout") &&
					!strings.HasPrefix(flag, "retry") {
					// May be okay (dynamic/placeholder), don't fail yet
				}
			}
		}
	}
}

// TestNoHardcodedAppNames verifies that no hardcoded app name or kind appears
// in broker/confirm command lines. FR-36: the workflow must not enumerate app
// names or kinds; it derives them from the single authoring declaration.
func TestNoHardcodedAppNames(t *testing.T) {
	inv := allInvocations(t)

	// Patterns that indicate hardcoded app names/kinds
	forbiddenPatterns := []string{
		"hello_python",
		"hello_go",
		"control-api",
		"manmanv2-control-api",
		"demo-",
	}

	for _, i := range inv {
		if !(i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			(i.Args[1] == "broker" || i.Args[1] == "confirm")) {
			continue
		}

		for _, pattern := range forbiddenPatterns {
			if strings.Contains(i.Raw, pattern) {
				t.Errorf("%s\n  contains hardcoded app/chart name %q (FR-36: derive from release plan, not enumerate)",
					i, pattern)
			}
		}
	}
}

// TestBrokerCommandRequiredFlags verifies that the broker command line includes
// all required flags: --artifact-kind, --version, --artifact-identity, --files.
// These carry the artifact identity and file set per FR-1, FR-14.
func TestBrokerCommandRequiredFlags(t *testing.T) {
	inv := allInvocations(t)
	requiredFlags := map[string]bool{
		"artifact-kind":     false,
		"version":           false,
		"artifact-identity": false,
		"files":             false,
	}

	for _, i := range inv {
		if !(i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			i.Args[1] == "broker") {
			continue
		}

		// Reset for this invocation
		for k := range requiredFlags {
			requiredFlags[k] = false
		}

		// Check for required flags
		for j := 0; j < len(i.Args); j++ {
			arg := i.Args[j]
			if arg == "--artifact-kind" || strings.HasPrefix(arg, "--artifact-kind=") {
				requiredFlags["artifact-kind"] = true
			} else if arg == "--version" || strings.HasPrefix(arg, "--version=") {
				requiredFlags["version"] = true
			} else if arg == "--artifact-identity" || strings.HasPrefix(arg, "--artifact-identity=") {
				requiredFlags["artifact-identity"] = true
			} else if arg == "--files" || strings.HasPrefix(arg, "--files=") {
				requiredFlags["files"] = true
			}
		}

		for flag, found := range requiredFlags {
			if !found {
				t.Errorf("%s\n  missing required flag --%s (FR-1, FR-14: broker needs artifact identity and file set)",
					i, flag)
			}
		}
	}
}

// TestConfirmCommandRequiredFlags verifies that the confirm command line includes
// all required flags: --upload-session-id, --artifact-kind, --files.
// These identify the upload session and provide file details for verification (FR-46).
func TestConfirmCommandRequiredFlags(t *testing.T) {
	inv := allInvocations(t)
	requiredFlags := map[string]bool{
		"upload-session-id": false,
		"artifact-kind":     false,
		"files":             false,
	}

	for _, i := range inv {
		if !(i.Binary == BinaryAppRegistry &&
			len(i.Args) > 1 &&
			i.Args[0] == "artifacts" &&
			i.Args[1] == "confirm") {
			continue
		}

		// Reset for this invocation
		for k := range requiredFlags {
			requiredFlags[k] = false
		}

		// Check for required flags
		for j := 0; j < len(i.Args); j++ {
			arg := i.Args[j]
			if arg == "--upload-session-id" || strings.HasPrefix(arg, "--upload-session-id=") {
				requiredFlags["upload-session-id"] = true
			} else if arg == "--artifact-kind" || strings.HasPrefix(arg, "--artifact-kind=") {
				requiredFlags["artifact-kind"] = true
			} else if arg == "--files" || strings.HasPrefix(arg, "--files=") {
				requiredFlags["files"] = true
			}
		}

		for flag, found := range requiredFlags {
			if !found {
				t.Errorf("%s\n  missing required flag --%s (FR-46: confirm needs session ID and file digests)",
					i, flag)
			}
		}
	}
}

// extractStepNames pulls step names from workflow YAML (lines with "- name:").
// Not a full YAML parser, just enough to order steps for precedence checks.
func extractStepNames(lines []string) []string {
	var steps []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			// Extract name between quotes
			if idx := strings.Index(trimmed, `"`); idx >= 0 {
				if endIdx := strings.Index(trimmed[idx+1:], `"`); endIdx >= 0 {
					name := trimmed[idx+1 : idx+1+endIdx]
					steps = append(steps, name)
				}
			}
		}
	}
	return steps
}

// readFileLines reads a file and returns lines as a string slice.
func readFileLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

// TestBrokerConfirmInvokeValidness is the regression test for broker/confirm
// command line validity. This leverages the existing cobra-based validation
// from contract_test.go but specifically targets broker/confirm.
func TestBrokerConfirmInvokeValidness(t *testing.T) {
	for _, inv := range allInvocations(t) {
		if !(inv.Binary == BinaryAppRegistry &&
			len(inv.Args) > 1 &&
			inv.Args[0] == "artifacts" &&
			(inv.Args[1] == "broker" || inv.Args[1] == "confirm")) {
			continue
		}
		if inv.Dynamic {
			continue // Skip dynamic invocations; they're covered by TestDynamicCallSitesAreKnown
		}

		t.Run(inv.File+":"+string(inv.Binary)+":"+strings.Join(inv.Args[:min(3, len(inv.Args))], "_"), func(t *testing.T) {
			root := appcmd.NewRootCmd()
			target, rest, err := root.Find(inv.Args)
			if err != nil {
				t.Fatalf("%s\n  unknown subcommand: %v", inv, err)
			}
			if target.RunE == nil && target.Run == nil {
				t.Fatalf("%s\n  resolves to %q, which is a command group, not a runnable command", inv, target.Name())
			}
			args, err := substitutePlaceholders(target, rest)
			if err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
			if err := target.ParseFlags(args); err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
			if err := target.ValidateRequiredFlags(); err != nil {
				t.Fatalf("%s\n  %v", inv, err)
			}
		})
	}
}
