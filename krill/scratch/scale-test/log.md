# Scale/concurrency test harness (issue #2971)

Throwaway scratch artifact used to empirically test krill-work:implement's
task-lane claim, dependency-gating, and continuous-merge mechanics under
real concurrency and width. Not a real feature -- safe to revert once
issue #2971 is resolved.

- A ran (baseline task, no deps)
