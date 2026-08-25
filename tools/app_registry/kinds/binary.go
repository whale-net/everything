package kinds

// Binary is the artifact kind for published Go binaries. Each version publishes
// one file per platform variant (os, arch pair). The binary kind is the primary
// example of a multi-file kind for this architecture.
type Binary struct {
	// h1, h2, h3, h4, h5, h6, h7, h8 are the eight hooks this kind supplies.
	// Each hook is initialized with its actual policy value.
	h1 h1Hook
	h2 h2Hook
	h3 h3Hook
	h4 h4Hook
	h5 h5Hook
	h6 h6Hook
	h7 h7Hook
	h8 h8Hook
}

// Name returns "binary".
func (k *Binary) Name() string {
	return "binary"
}

// Hooks returns the binary kind's full HookSet.
func (k *Binary) Hooks() HookSet {
	return k
}

// H1 returns this kind's H1 hook (artifact set composition).
func (k *Binary) H1() H1 {
	return &k.h1
}

// H2 returns this kind's H2 hook (variant dimensions).
func (k *Binary) H2() H2 {
	return &k.h2
}

// H3 returns this kind's H3 hook (per-file content type).
func (k *Binary) H3() H3 {
	return &k.h3
}

// H4 returns this kind's H4 hook (compression/encoding policy).
func (k *Binary) H4() H4 {
	return &k.h4
}

// H5 returns this kind's H5 hook (consumer-facing file naming).
func (k *Binary) H5() H5 {
	return &k.h5
}

// H6 returns this kind's H6 hook (checksum manifest format).
func (k *Binary) H6() H6 {
	return &k.h6
}

// H7 returns this kind's H7 hook (app-type mapping).
func (k *Binary) H7() H7 {
	return &k.h7
}

// H8 returns this kind's H8 hook (pre-cutover naming).
func (k *Binary) H8() H8 {
	return &k.h8
}

// Stub hook implementations. Each is initialized with its actual policy value.

type h1Hook struct{}

func (h *h1Hook) Name() string                { return "H1" }
func (h *h1Hook) ValueShaped() bool           { return false } // Structural hook
func (h *h1Hook) Policy() string              { return "multi-file per-variant: one executable binary per {os, arch} pair" }

type h2Hook struct{}

func (h *h2Hook) Name() string       { return "H2" }
func (h *h2Hook) ValueShaped() bool  { return false } // Structural hook
func (h *h2Hook) Dimensions() []string {
	return []string{"os", "arch"}
}

type h3Hook struct{}

func (h *h3Hook) Name() string       { return "H3" }
func (h *h3Hook) ValueShaped() bool  { return true } // Value-shaped hook
func (h *h3Hook) ContentType() string { return "application/octet-stream" }

type h4Hook struct{}

func (h *h4Hook) Name() string      { return "H4" }
func (h *h4Hook) ValueShaped() bool { return true } // Value-shaped hook
func (h *h4Hook) Encoding() string  { return "gzip" }

type h5Hook struct{}

func (h *h5Hook) Name() string        { return "H5" }
func (h *h5Hook) ValueShaped() bool   { return true } // Value-shaped hook
func (h *h5Hook) FileNaming() string  { return "{name}-{version}-{os}-{arch}" }

type h6Hook struct{}

func (h *h6Hook) Name() string             { return "H6" }
func (h *h6Hook) ValueShaped() bool        { return true } // Value-shaped hook
func (h *h6Hook) ManifestPolicy() string   { return "checksums.txt, SHA256, one per line" }

type h7Hook struct{}

func (h *h7Hook) Name() string                  { return "H7" }
func (h *h7Hook) ValueShaped() bool             { return false } // Structural hook
func (h *h7Hook) AppTypeMapping() []string {
	return []string{"external-api", "web-api"}
}

type h8Hook struct{}

func (h *h8Hook) Name() string             { return "H8" }
func (h *h8Hook) ValueShaped() bool        { return true } // Value-shaped hook
func (h *h8Hook) PreCutoverTemplate() string {
	// Binary kind has no pre-cutover history
	return ""
}

func init() {
	Register(&Binary{})
}
