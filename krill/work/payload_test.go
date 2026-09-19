// Scaffold-phase placeholder (issue #2721) -- full red/green coverage of
// Assemble's real behavior (NFR4's byte-equal slice regression test, the
// empty-dependency-list case, etc.) lands in this task's Testing phase.
// This file only proves the Scaffold-phase stub's own contract: Assemble
// returns ErrNotImplemented, and the package compiles/links against
// //krill/slice and //krill/store as //krill/work:work.
package work_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/work"
)

func TestAssemble_ScaffoldStubNotImplemented(t *testing.T) {
	a := work.NewAssembler(nil, nil)

	_, err := a.Assemble(context.Background(), uuid.New(), uuid.New())

	assert.ErrorIs(t, err, work.ErrNotImplemented)
}
