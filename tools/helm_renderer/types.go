// Package helm_renderer provides types and functionality for Helm chart generation
package main

// Release represents Helm release information
type Release struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
}

// Chart represents Helm chart metadata
type Chart struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// Values represents Helm values.yaml content (using map for flexible field access)
type Values map[string]interface{}

// ChartData represents the data available to templates during rendering (Helm-compatible)
type ChartData struct {
	// Helm standard objects
	Release Release `json:"Release"`
	Chart   Chart   `json:"Chart"`
	Values  Values  `json:"Values"`

	// Our custom data
	ChartName         string            `json:"chartName"`
	Version           string            `json:"version"`
	Description       string            `json:"description"`
	Domain            string            `json:"domain"`
	DeployOrderWeight int               `json:"deployOrderWeight"`
	ChartValues       map[string]string `json:"chartValues"`
	Apps              []App             `json:"apps"`
	Artifacts         []Artifact        `json:"artifacts"`
}

// App represents a release_app metadata
type App struct {
	Name     string `json:"name"`
	RepoName string `json:"repo_name"`
	Registry string `json:"registry"`
	Version  string `json:"version"`
	Language string `json:"language"`
	Domain   string `json:"domain"`
}

// Artifact represents a k8s_artifact metadata
type Artifact struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	ManifestPath     string `json:"manifest_path"`
	HookWeight       int    `json:"hook_weight"`
	HookDeletePolicy string `json:"hook_delete_policy"`
}

// Args represents parsed command line arguments
type Args struct {
	TemplateDir      string
	OutputDir        string
	ChartName        string
	Version          string
	Description      string
	Domain           string
	DeployOrder      int
	ChartValues      map[string]string
	AppMetadataFiles []string
	K8sArtifacts     []string
}