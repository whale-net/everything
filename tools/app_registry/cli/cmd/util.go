package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// parseRFC3339 parses a --at flag value into a Unix timestamp, the wire
// format GetEnvironmentStateRequest.at expects.
func parseRFC3339(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, fmt.Errorf("invalid --at %q, want RFC3339 (e.g. 2026-01-15T00:00:00Z): %w", s, err)
	}
	return t.Unix(), nil
}

// printJSON formats a plain Go value (not a proto message) the same way
// printResponse formats one -- used by CLI-side computations that have no
// wire type of their own, e.g. `diff`'s client-side comparison (AR-3d:
// GetEnvironmentState offers no server-side diff RPC, so the CLI computes it
// from two calls -- see promote.go's diffEnvironmentStates).
func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format response: %w", err)
	}
	if format == "table" {
		fmt.Println("# table format not implemented yet, showing json")
	}
	fmt.Println(string(b))
	return nil
}

// printCombinedResponse formats several proto responses as one JSON object,
// keyed by name. Each part is protojson-formatted individually so enums
// still print as names (encoding/json alone would print raw ints), then
// combined -- used by `history`, which calls both ListPromotions and
// ListPromotionEvents and has no single response message to hand to
// printResponse.
func printCombinedResponse(parts map[string]proto.Message) error {
	raw := make(map[string]json.RawMessage, len(parts))
	for k, v := range parts {
		b, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(v)
		if err != nil {
			return fmt.Errorf("failed to format response: %w", err)
		}
		raw[k] = b
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to format response: %w", err)
	}
	if format == "table" {
		fmt.Println("# table format not implemented yet, showing json")
	}
	fmt.Println(string(b))
	return nil
}
