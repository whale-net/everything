package kinds

// Firmware is the artifact kind for published firmware binaries. Each version
// publishes one file per board/target variant. The firmware kind demonstrates
// kind genericity: it is neither OS nor architecture, proving that the system
// supports kinds beyond the typical binary categorization.
type Firmware struct {
	// h1-h8 are the eight hooks this kind supplies.
	h1 firmwareH1Hook
	h2 firmwareH2Hook
	h3 firmwareH3Hook
	h4 firmwareH4Hook
	h5 firmwareH5Hook
	h6 firmwareH6Hook
	h7 firmwareH7Hook
	h8 firmwareH8Hook
}

// Name returns "firmware".
func (k *Firmware) Name() string {
	return "firmware"
}

// Hooks returns the firmware kind's full HookSet.
func (k *Firmware) Hooks() HookSet {
	return k
}

// H1 returns this kind's H1 hook (artifact set composition).
func (k *Firmware) H1() H1 {
	return &k.h1
}

// H2 returns this kind's H2 hook (variant dimensions).
func (k *Firmware) H2() H2 {
	return &k.h2
}

// H3 returns this kind's H3 hook (per-file content type).
func (k *Firmware) H3() H3 {
	return &k.h3
}

// H4 returns this kind's H4 hook (compression/encoding policy).
func (k *Firmware) H4() H4 {
	return &k.h4
}

// H5 returns this kind's H5 hook (consumer-facing file naming).
func (k *Firmware) H5() H5 {
	return &k.h5
}

// H6 returns this kind's H6 hook (checksum manifest format).
func (k *Firmware) H6() H6 {
	return &k.h6
}

// H7 returns this kind's H7 hook (app-type mapping).
func (k *Firmware) H7() H7 {
	return &k.h7
}

// H8 returns this kind's H8 hook (pre-cutover naming).
func (k *Firmware) H8() H8 {
	return &k.h8
}

// Stub hook implementations for firmware kind.

type firmwareH1Hook struct{}

func (h *firmwareH1Hook) Name() string       { return "H1" }
func (h *firmwareH1Hook) ValueShaped() bool  { return false } // Structural hook
func (h *firmwareH1Hook) Policy() string {
	return "single-file per variant: one firmware image per {board, target} pair"
}

type firmwareH2Hook struct{}

func (h *firmwareH2Hook) Name() string { return "H2" }
func (h *firmwareH2Hook) ValueShaped() bool {
	return false
} // Structural hook
func (h *firmwareH2Hook) Dimensions() []string {
	return []string{"board", "target"}
}

type firmwareH3Hook struct{}

func (h *firmwareH3Hook) Name() string        { return "H3" }
func (h *firmwareH3Hook) ValueShaped() bool   { return true } // Value-shaped hook
func (h *firmwareH3Hook) ContentType() string { return "application/x-firmware" }

type firmwareH4Hook struct{}

func (h *firmwareH4Hook) Name() string      { return "H4" }
func (h *firmwareH4Hook) ValueShaped() bool { return true } // Value-shaped hook
func (h *firmwareH4Hook) Encoding() string  { return "none" }

type firmwareH5Hook struct{}

func (h *firmwareH5Hook) Name() string       { return "H5" }
func (h *firmwareH5Hook) ValueShaped() bool  { return true } // Value-shaped hook
func (h *firmwareH5Hook) FileNaming() string { return "{name}-{version}-{board}-{target}.bin" }

type firmwareH6Hook struct{}

func (h *firmwareH6Hook) Name() string             { return "H6" }
func (h *firmwareH6Hook) ValueShaped() bool        { return true } // Value-shaped hook
func (h *firmwareH6Hook) ManifestPolicy() string   { return "manifest.json, SHA256" }

type firmwareH7Hook struct{}

func (h *firmwareH7Hook) Name() string              { return "H7" }
func (h *firmwareH7Hook) ValueShaped() bool         { return false } // Structural hook
func (h *firmwareH7Hook) AppTypeMapping() []string { return []string{} }

type firmwareH8Hook struct{}

func (h *firmwareH8Hook) Name() string                 { return "H8" }
func (h *firmwareH8Hook) ValueShaped() bool            { return true } // Value-shaped hook
func (h *firmwareH8Hook) PreCutoverTemplate() string   { return "" }   // No pre-cutover history

func init() {
	Register(&Firmware{})
}
