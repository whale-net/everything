package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	serverAddr string
	format     string
)

var rootCmd = &cobra.Command{
	Use:          "app-registry",
	Short:        "Thin gRPC client for the App Registry",
	Long:         "app-registry is a thin gRPC client for the App Registry. It validates argument shape, calls one RPC, and formats the response — no business logic lives here.",
	SilenceUsage: true,
}

// Execute runs the CLI. Called from main.go.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.PersistentFlags().StringVar(&serverAddr, "address", defaultServerAddr(), "app-registry-api address (host:port)")
	rootCmd.PersistentFlags().StringVar(&format, "format", "json", "Output format: json or table")

	rootCmd.AddCommand(
		newAppsCmd(),
		newArtifactsCmd(),
		newBuildsCmd(),
		newPromoteCmd(),
		newRollbackCmd(),
		newStatusCmd(),
		newHistoryCmd(),
		newDiffCmd(),
		newEnvCmd(),
	)
}

// defaultServerAddr reads APP_REGISTRY_ADDRESS, matching the ADDRESS
// convention other CLIs in this repo use for their target service.
func defaultServerAddr() string {
	if addr := os.Getenv("APP_REGISTRY_ADDRESS"); addr != "" {
		return addr
	}
	return "localhost:50051"
}
