package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// ContainerConfig holds configuration for creating a container
type ContainerConfig struct {
	Image      string
	Name       string
	Command    []string
	Env        []string
	Labels     map[string]string
	NetworkID  string
	Volumes    []string          // Mount points in format "host_path:container_path"
	Ports      map[string]string // Container port -> host port mapping
	AutoRemove bool
	Privileged bool
	OpenStdin  bool
}

// CreateContainer creates a new Docker container
func (c *Client) CreateContainer(ctx context.Context, config ContainerConfig) (string, error) {
	// Build port bindings
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	for containerPort, hostPort := range config.Ports {
		// Parse port and protocol from containerPort (format: "port/protocol" or "port")
		// Parse host port and protocol from hostPort (format: "port/protocol" or "port")
		containerParts := strings.Split(containerPort, "/")
		hostParts := strings.Split(hostPort, "/")

		containerPortNum := containerParts[0]
		protocol := "tcp" // default
		if len(containerParts) > 1 {
			protocol = strings.ToLower(containerParts[1])
		}

		hostPortNum := hostParts[0]

		port, err := nat.NewPort(protocol, containerPortNum)
		if err != nil {
			return "", fmt.Errorf("invalid port %s/%s: %w", containerPortNum, protocol, err)
		}
		exposedPorts[port] = struct{}{}
		portBindings[port] = []nat.PortBinding{
			{
				HostIP:   "0.0.0.0",
				HostPort: hostPortNum,
			},
		}
	}

	// Build volume mounts
	mounts := make([]mount.Mount, 0, len(config.Volumes))
	for _, vol := range config.Volumes {
		parts := strings.SplitN(vol, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid volume format: %s (expected host:container)", vol)
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: parts[0],
			Target: parts[1],
		})
	}

	// Create container
	containerConfig := &container.Config{
		Image:        config.Image,
		Cmd:          config.Command,
		Env:          config.Env,
		Labels:       config.Labels,
		ExposedPorts: exposedPorts,
		OpenStdin:    config.OpenStdin,
		StdinOnce:    false, // stdin survives detach; needed for recovery re-attach
		Tty:          config.OpenStdin, // Allocate pseudo-TTY when stdin is enabled to prevent blocking
	}

	hostConfig := &container.HostConfig{
		PortBindings:  portBindings,
		Mounts:        mounts,
		AutoRemove:    config.AutoRemove,
		Privileged:    config.Privileged,
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	var networkingConfig *network.NetworkingConfig
	if config.NetworkID != "" {
		// If NetworkID is provided, use it in networking config
		// NetworkMode will be set automatically by Docker
		networkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				config.NetworkID: {},
			},
		}
	}

	resp, err := c.cli.ContainerCreate(ctx, containerConfig, hostConfig, networkingConfig, nil, config.Name)
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w", err)
	}

	return resp.ID, nil
}

// StartContainer starts a container
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	return c.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

// PullImage pulls a Docker image with progress logging
func (c *Client) PullImage(ctx context.Context, imageRef string) error {
	log.Printf("Pulling image %s...", imageRef)
	reader, err := c.cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageRef, err)
	}
	defer reader.Close()

	// Use a simple decoder to show progress every few seconds
	dec := json.NewDecoder(reader)
	lastLog := time.Now()
	for {
		var msg map[string]interface{}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		if time.Since(lastLog) > 5*time.Second {
			if status, ok := msg["status"].(string); ok {
				progress := ""
				if p, ok := msg["progress"].(string); ok {
					progress = " " + p
				}
				log.Printf("[docker] %s: %s%s", imageRef, status, progress)
			}
			lastLog = time.Now()
		}
	}

	log.Printf("Successfully pulled image %s", imageRef)
	return nil
}

// StopContainer stops a container gracefully
func (c *Client) StopContainer(ctx context.Context, containerID string, timeout *time.Duration) error {
	var timeoutSecs *int
	if timeout != nil {
		secs := int(timeout.Seconds())
		timeoutSecs = &secs
	} else {
		defaultSecs := 30
		timeoutSecs = &defaultSecs
	}
	return c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: timeoutSecs})
}

// RemoveContainer removes a container
func (c *Client) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	return c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: force})
}

// GetContainerStatus returns the status of a container by ID or name
func (c *Client) GetContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error) {
	info, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect container: %w", err)
	}

	status := &ContainerStatus{
		ContainerID: info.ID,
		ID:          info.ID,
		Name:        info.Name,
		Status:      info.State.Status,
		Running:     info.State.Running,
		ExitCode:    info.State.ExitCode,
		Labels:      info.Config.Labels,
	}

	if info.State.StartedAt != "" {
		startedAt, err := time.Parse(time.RFC3339Nano, info.State.StartedAt)
		if err == nil {
			status.StartedAt = &startedAt
		}
	}

	if info.State.FinishedAt != "" {
		finishedAt, err := time.Parse(time.RFC3339Nano, info.State.FinishedAt)
		if err == nil {
			status.FinishedAt = &finishedAt
		}
	}

	return status, nil
}

// GetContainerStatusByName returns the status of a container by name
// This is an alias for GetContainerStatus since Docker's inspect accepts both ID and name
func (c *Client) GetContainerStatusByName(ctx context.Context, containerName string) (*ContainerStatus, error) {
	return c.GetContainerStatus(ctx, containerName)
}

// ContainerStatus represents the status of a container
type ContainerStatus struct {
	ContainerID string            // Full container ID
	ID          string            // Alias for ContainerID (for backwards compatibility)
	Name        string            // Container name
	Status      string            // "created", "running", "exited", etc.
	Running     bool              // Whether container is currently running
	ExitCode    int               // Exit code if stopped
	StartedAt   *time.Time        // When container started
	FinishedAt  *time.Time        // When container finished
	Labels      map[string]string // Container labels
}

// ListContainers lists containers matching the given filters
func (c *Client) ListContainers(ctx context.Context, labelFilters map[string]string) ([]ContainerStatus, error) {
	// Convert filters to Docker format
	filterArgs := filters.NewArgs()
	for key, value := range labelFilters {
		filterArgs.Add("label", fmt.Sprintf("%s=%s", key, value))
	}

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	statuses := make([]ContainerStatus, 0, len(containers))
	for _, cnt := range containers {
		name := ""
		if len(cnt.Names) > 0 {
			name = cnt.Names[0]
		}
		statuses = append(statuses, ContainerStatus{
			ID:      cnt.ID,
			Name:    name,
			Status:  cnt.Status,
			Running: cnt.State == "running",
		})
	}

	return statuses, nil
}

// CreateNetwork creates a Docker network
func (c *Client) CreateNetwork(ctx context.Context, name string, labels map[string]string) (string, error) {
	resp, err := c.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Labels: labels,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create network: %w", err)
	}
	return resp.ID, nil
}

// GetNetworkIDByName returns the ID of a network by its name
func (c *Client) GetNetworkIDByName(ctx context.Context, name string) (string, error) {
	networks, err := c.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}

	for _, n := range networks {
		if n.Name == name {
			return n.ID, nil
		}
	}

	return "", fmt.Errorf("network %s not found", name)
}

// RemoveNetwork removes a Docker network
func (c *Client) RemoveNetwork(ctx context.Context, networkID string) error {
	return c.cli.NetworkRemove(ctx, networkID)
}

// NetworkInfo represents basic information about a Docker network
type NetworkInfo struct {
	ID        string
	Name      string
	Labels    map[string]string
	CreatedAt time.Time
}

// ListNetworks lists networks matching the given label filters
func (c *Client) ListNetworks(ctx context.Context, labelFilters map[string]string) ([]NetworkInfo, error) {
	filterArgs := filters.NewArgs()
	for key, value := range labelFilters {
		filterArgs.Add("label", fmt.Sprintf("%s=%s", key, value))
	}

	networks, err := c.cli.NetworkList(ctx, network.ListOptions{
		Filters: filterArgs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	result := make([]NetworkInfo, 0, len(networks))
	for _, n := range networks {
		result = append(result, NetworkInfo{
			ID:        n.ID,
			Name:      n.Name,
			Labels:    n.Labels,
			CreatedAt: n.Created,
		})
	}

	return result, nil
}

// GetContainerLogs returns the logs from a container
func (c *Client) GetContainerLogs(ctx context.Context, containerID string, follow bool, tail string) (io.ReadCloser, error) {
	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tail,
	}
	return c.cli.ContainerLogs(ctx, containerID, options)
}

// AttachToContainer attaches to a running container for stdin/stdout/stderr
func (c *Client) AttachToContainer(ctx context.Context, containerID string) (types.HijackedResponse, error) {
	options := container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	}
	return c.cli.ContainerAttach(ctx, containerID, options)
}
