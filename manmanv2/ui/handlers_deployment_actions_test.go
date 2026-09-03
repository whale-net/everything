package main

// TODO(#1627 Implementation/Testing): table-driven tests for
// handleDeploymentAction (Start/Stop/Restart), renderDeploymentRow, and
// buildDeploymentRowData, following the fake-gRPC-client pattern in
// handlers_sgc_test.go / handlers_sessions_deployment_row_test.go -- embed
// the nil manmanpb.ManManAPIClient and override only the RPCs each scenario
// reaches. See #1627's Testing section for the full scenario list
// (Start/Stop/Restart happy paths, inline-error paths, method/id/verb
// validation, and the non-HTMX redirect + routing-unaffected guards).
