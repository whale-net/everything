package importer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Regression test for issue #2522: krill/render wrote a fresh "LB%d — %s"
// title line and then the decision's stored Body verbatim, and Body used to
// be seeded with that same title line -- so every rendered decision showed
// its own title twice. Body must not include the title line parseDecisions
// already captures separately as Name/ID.
func TestParseDecisions_BodyExcludesOwnTitleLine(t *testing.T) {
	body := "LB1 — Keep it simple\nAt risk: overengineering the entity model.\n\nLB2 — Ship fast\nSecond decision's body.\n"

	decisions := parseDecisions(body)

	assert.Len(t, decisions, 2)

	assert.Equal(t, "LB1", decisions[0].ID)
	assert.Equal(t, "LB1 — Keep it simple", decisions[0].Name)
	assert.Equal(t, "At risk: overengineering the entity model.", decisions[0].Body)
	assert.NotContains(t, decisions[0].Body, "LB1 — Keep it simple")

	assert.Equal(t, "LB2", decisions[1].ID)
	assert.Equal(t, "LB2 — Ship fast", decisions[1].Name)
	assert.Equal(t, "Second decision's body.", decisions[1].Body)
}
