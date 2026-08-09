// Command app-registry is the thin gRPC client for the App Registry. No
// business logic lives here — see cmd/README.md "Thin means thin". Every
// command validates argument shape, calls exactly one RPC, and formats the
// response.
package main

import "github.com/whale-net/everything/tools/app_registry/cli/cmd"

func main() {
	cmd.Execute()
}
