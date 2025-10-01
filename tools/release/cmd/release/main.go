// Package main is the entry point for the release helper CLI.
package main

import (
	"fmt"
	"os"

	"github.com/whale-net/everything/tools/release/pkg/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
