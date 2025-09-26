// Command line argument parsing
package main

import (
	"strconv"
	"strings"
)

// ParseArgs parses command line arguments
func ParseArgs(args []string) *Args {
	result := &Args{
		ChartValues: make(map[string]string),
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--template-dir":
			if i+1 < len(args) {
				result.TemplateDir = args[i+1]
				i++
			}
		case "--output-dir":
			if i+1 < len(args) {
				result.OutputDir = args[i+1]
				i++
			}
		case "--chart-name":
			if i+1 < len(args) {
				result.ChartName = args[i+1]
				i++
			}
		case "--version":
			if i+1 < len(args) {
				result.Version = args[i+1]
				i++
			}
		case "--description":
			if i+1 < len(args) {
				result.Description = args[i+1]
				i++
			}
		case "--domain":
			if i+1 < len(args) {
				result.Domain = args[i+1]
				i++
			}
		case "--k8s-artifacts":
			if i+1 < len(args) {
				result.K8sArtifacts = strings.Split(args[i+1], ",")
				i++
			}
		case "--chart-values":
			if i+1 < len(args) {
				for _, pair := range strings.Split(args[i+1], ",") {
					if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 {
						result.ChartValues[kv[0]] = kv[1]
					}
				}
				i++
			}
		case "--deploy-order":
			if i+1 < len(args) {
				if weight, err := strconv.Atoi(args[i+1]); err == nil {
					result.DeployOrder = weight
				}
				i++
			}
		case "--app-metadata":
			if i+1 < len(args) {
				result.AppMetadataFiles = append(result.AppMetadataFiles, args[i+1])
				i++
			}
		}
	}

	return result
}
