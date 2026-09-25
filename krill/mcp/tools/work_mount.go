package tools

import (
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// expectedWorkSurfaceToolNames is exactly what RegisterWorkAll mounts at
// /mcp/work. It lives here rather than only in the registration test so the
// two stay a single list to update.
var expectedWorkSurfaceToolNames = []string{
	// init_session plus the ungated product/milestone discovery reads
	// (issue #3028): a krill-work persona holds milestone and task ids, but
	// its plugin manifest no longer mounts /mcp/design, so without these it
	// cannot resolve the product or read the milestone it is working on.
	"init_session",
	"list_products",
	"get_milestone",
	"list_milepebbles",
	"list_product_delivery",
	"get_milestone_status",
	"get_milestone_status_history",
	// The work-axis task-lifecycle surface (M4, issues #2719-#2727;
	// transition_note_lifecycle #2874).
	"create_task",
	"declare_task_dependencies",
	"get_task",
	"list_tasks",
	"claim_task",
	"heartbeat_task",
	"complete_task",
	"abandon_task",
	"record_note",
	"transition_note_lifecycle",
}

// RegisterWorkAll registers krill's entire work-axis MCP surface against
// reg -- the same call ../main.go wires at boot onto the mount
// server/transport.go's workMountPath (/mcp/work).
//
// It is a bundle rather than the individual Register* calls inlined in
// main.go for the same reason RegisterAll and RegisterDesignAll are: so a
// test can pin the mount's exact tool set by exercising this one function.
// Inlined, the mount was unpinned -- the six discovery reads added for
// #3028 could be deleted from main.go with every other test still green,
// because the existing registration tests exercise the design mount, not
// this one. The failure mode is silent in production too: a krill-work
// persona simply stops being able to find a Product, with nothing in a log
// saying so.
//
// The surface is split from /mcp/design deliberately (main.go's package doc
// comment): krill-work personas must not reach design-session authoring or
// milestone/delivery WRITES. Every dual-mounted tool here is either ungated
// (the discovery reads, which take no krill_session_id) or one of the four
// already shared with designReg for a stated reason (get_task, list_tasks,
// abandon_task, init_session). No work-axis write that designReg does not
// already carry is reachable from this mount.
func RegisterWorkAll(reg *server.Registry, entities *store.Store, sessions store.SessionStore, assembler *work.Assembler, querier *slice.Querier) {
	// Ungated discovery. A persona holding a task or milestone id cannot use
	// it without resolving the product it belongs to (issue #3028).
	RegisterInitSession(reg, sessions, entities.Scopes())
	RegisterListProducts(reg, entities.Products())
	RegisterGetMilestone(reg, entities.MilestoneAuthoring())
	RegisterListMilepebbles(reg, entities.MilestoneAuthoring())
	RegisterListProductDelivery(reg, entities.Products(), querier)
	RegisterGetMilestoneStatus(reg, entities.MilestoneStatus())
	RegisterGetMilestoneStatusHistory(reg, entities.MilestoneStatus())

	// The task-lifecycle axis proper.
	RegisterCreateTask(reg, sessions, entities.Tasks())
	RegisterDeclareTaskDependencies(reg, sessions, entities.Tasks())
	RegisterGetTaskPayload(reg, entities.Tasks(), assembler)
	RegisterListTasks(reg, entities.Tasks())
	RegisterClaimTask(reg, sessions, entities.Tasks(), assembler)
	RegisterHeartbeatTask(reg, sessions, entities.Tasks())
	RegisterCompleteTask(reg, sessions, entities.Tasks(), assembler)
	RegisterAbandonTask(reg, sessions, entities.Tasks(), assembler)
	RegisterRecordNote(reg, sessions, entities.Tasks())
	RegisterTransitionNoteLifecycle(reg, sessions, entities.Tasks())
}

// ExpectedWorkSurfaceToolNames exposes the mount's exact tool set to the
// registration test, so the assertion and the registration cannot drift
// apart.
func ExpectedWorkSurfaceToolNames() []string {
	out := make([]string, len(expectedWorkSurfaceToolNames))
	copy(out, expectedWorkSurfaceToolNames)
	return out
}
