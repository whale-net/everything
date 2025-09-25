// Helm template renderer for Bazel-generated charts
// Uses Go's text/template package (same engine as Helm)
package main

import (
	"fmt"
	"os"
)

func main() {
	// Parse command line arguments
	args := ParseArgs(os.Args[1:])

	// Collect chart data
	data, err := CollectChartData(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error collecting chart data: %v\n", err)
		os.Exit(1)
	}

	// Render templates
	if err := RenderTemplates(args.TemplateDir, args.OutputDir, data); err != nil {
		fmt.Fprintf(os.Stderr, "Error rendering templates: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully generated Helm chart '%s' in %s\n", args.ChartName, args.OutputDir)
}


