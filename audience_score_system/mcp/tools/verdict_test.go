package tools

// Pure-Go, table-driven coverage for verdict.go's two pure functions:
// parseVerdictValue (the CHECK-constraint mirror that rejects an invalid
// verdict enum value before a write tool ever opens a transaction) and
// excerpt (CitedNoteOutput.TextExcerpt's truncation rule). No Postgres or
// MCP transport needed here -- that end-to-end coverage (save_
// viability_verdict/get_viability_verdict against a real database and a
// real in-process MCP client, including the FR12 immutability and NFR2
// replay assertions) is verdict_integration_test.go's job (build tag
// "integration").
import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

func TestParseVerdictValue_AcceptsExactlyTheThreeEnumValues(t *testing.T) {
	cases := []struct {
		raw  string
		want store.VerdictValue
	}{
		{"viable", store.VerdictViable},
		{"not-viable", store.VerdictNotViable},
		{"needs-more-research", store.VerdictNeedsMoreResearch},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseVerdictValue(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseVerdictValue_RejectsAnythingElse(t *testing.T) {
	for _, raw := range []string{
		"",
		"Viable",     // wrong case
		"not_viable", // wrong separator
		"maybe",
		"needs-more-research ", // trailing whitespace
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseVerdictValue(raw)
			assert.Error(t, err, "must reject %q rather than silently accepting a value the viability_verdict.verdict CHECK constraint would reject", raw)
			assert.Empty(t, got)
		})
	}
}

func TestExcerpt_ShortTextPassesThroughUnchanged(t *testing.T) {
	short := "a short note"
	assert.Equal(t, short, excerpt(short))

	exact := strings.Repeat("x", citationExcerptRunes)
	assert.Equal(t, exact, excerpt(exact), "text exactly at the limit must not be truncated")
}

func TestExcerpt_LongTextTruncatedWithEllipsisAtExactRuneBound(t *testing.T) {
	long := strings.Repeat("y", citationExcerptRunes+50)
	got := excerpt(long)

	assert.True(t, strings.HasSuffix(got, "..."), "truncated excerpt must end with an ellipsis marker")
	runes := []rune(got)
	assert.Len(t, runes, citationExcerptRunes+3, "truncated excerpt must be exactly citationExcerptRunes runes plus the 3-rune ellipsis")
	assert.Equal(t, strings.Repeat("y", citationExcerptRunes), string(runes[:citationExcerptRunes]))
}

func TestExcerpt_TruncatesByRuneNotByte_MultiByteCharactersNotSplit(t *testing.T) {
	// Each "é" is a single rune but 2 bytes in UTF-8; a byte-based
	// truncation at citationExcerptRunes bytes would split one of these in
	// half and corrupt the excerpt.
	long := strings.Repeat("é", citationExcerptRunes+10)
	got := excerpt(long)

	require.True(t, strings.HasSuffix(got, "..."))
	body := strings.TrimSuffix(got, "...")
	runes := []rune(body)
	assert.Len(t, runes, citationExcerptRunes)
	for _, r := range runes {
		assert.Equal(t, 'é', r, "truncation must never split a multi-byte rune")
	}
}
