package kinds

// Dispatch provides the mechanism for common pipeline components to look up
// kind-specific policies via hooks, rather than branching on kind identity.
//
// This is the pattern all common mechanisms follow:
//   1. Receive a kind identifier from the caller
//   2. Use GetKind() to retrieve the kind implementation
//   3. Use kind.Hooks() to access the full hook set
//   4. Call the specific hook method to retrieve the policy value
//   5. Implement the common mechanism using the policy value
//
// See FR-35 and FR-63(b).

// GetKind retrieves a kind by name. Returns nil if not found. This is the
// entry point for common mechanisms doing dispatch.
func GetKind(name string) Kind {
	return Get(name)
}

// Example: How a common mechanism uses dispatch to access hook policies.
//
// This pattern appears in every common mechanism — it never branches on kind
// identity directly:
//
//   func UploadFile(kindName string, contentToUpload []byte) error {
//       kind := GetKind(kindName)
//       if kind == nil {
//           return fmt.Errorf("unknown kind %q", kindName)
//       }
//       hooks := kind.Hooks()
//
//       // Dispatch to H3 (content type)
//       contentType := hooks.H3().ContentType()
//
//       // Dispatch to H4 (compression)
//       encoding := hooks.H4().Encoding()
//
//       // Now implement the common "file upload" mechanism using these policies
//       return uploadWithPolicy(contentType, encoding, contentToUpload)
//   }
//
// The upload mechanism itself is agnostic to kind — it uses dispatch to adapt
// its behavior per kind.
