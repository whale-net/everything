package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
	"github.com/whale-net/everything/libs/go/docker"
)

// imageTag returns the tag portion of an image reference such as
// "ghcr.io/whale-net/manmanv2-host-manager:v1.4.0" -- everything after the
// last colon that comes after the last slash, so a registry port (as in
// "host:5000/repo:tag") is never mistaken for the tag separator.
func imageTag(ref string) string {
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon <= slash {
		return ""
	}
	return ref[colon+1:]
}

// swapContainer replaces the container currently named containerName with a
// new one running newImageRef, cloning every other setting (env, mounts,
// network attachment, restart policy, ...) from the container it replaces.
// The new image is pulled before anything is torn down, so a failed pull
// leaves the running container untouched. Returns the new container's ID.
func swapContainer(ctx context.Context, c *docker.Client, containerName, newImageRef string) (string, error) {
	raw := c.GetClient()

	info, err := raw.ContainerInspect(ctx, containerName)
	if err != nil {
		return "", fmt.Errorf("inspecting %s: %w", containerName, err)
	}

	if err := c.PullImage(ctx, newImageRef); err != nil {
		return "", err
	}

	timeout := 30 * time.Second
	if err := c.StopContainer(ctx, info.ID, &timeout); err != nil {
		return "", fmt.Errorf("stopping %s: %w", containerName, err)
	}
	if err := c.RemoveContainer(ctx, info.ID, false); err != nil {
		return "", fmt.Errorf("removing %s: %w", containerName, err)
	}

	newConfig := *info.Config
	newConfig.Image = newImageRef

	var netConfig *network.NetworkingConfig
	if info.NetworkSettings != nil && len(info.NetworkSettings.Networks) > 0 {
		netConfig = &network.NetworkingConfig{EndpointsConfig: info.NetworkSettings.Networks}
	}

	name := strings.TrimPrefix(info.Name, "/")
	resp, err := raw.ContainerCreate(ctx, &newConfig, info.HostConfig, netConfig, nil, name)
	if err != nil {
		return "", fmt.Errorf("recreating %s: %w", name, err)
	}
	if err := c.StartContainer(ctx, resp.ID); err != nil {
		return "", fmt.Errorf("starting %s: %w", name, err)
	}
	return resp.ID, nil
}

// waitForHealthy polls containerID until it looks healthy, the context is
// cancelled, or timeout elapses.
//
// If the image defines a Docker HEALTHCHECK, "healthy" means
// State.Health.Status == "healthy". Otherwise it means the container has
// stayed "running" -- without restarting beyond baselineRestarts -- for one
// full pollEvery interval: the best available signal that it isn't
// crash-looping when there's no real health endpoint to poll (host-manager
// reports its own liveness over RabbitMQ, not HTTP).
func waitForHealthy(ctx context.Context, c *docker.Client, containerID string, baselineRestarts int, timeout, pollEvery time.Duration) error {
	raw := c.GetClient()
	deadline := time.Now().Add(timeout)
	var stableSince time.Time

	for {
		info, err := raw.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspecting %s: %w", containerID, err)
		}

		if info.RestartCount > baselineRestarts {
			return fmt.Errorf("container restarted (restart count %d > baseline %d), likely crash-looping", info.RestartCount, baselineRestarts)
		}

		switch {
		case info.State.Health != nil && info.State.Health.Status == "healthy":
			return nil
		case info.State.Health != nil && info.State.Health.Status == "unhealthy":
			return fmt.Errorf("container reported unhealthy")
		case info.State.Health == nil && info.State.Running:
			if stableSince.IsZero() {
				stableSince = time.Now()
			}
			if time.Since(stableSince) >= pollEvery {
				return nil
			}
		default:
			stableSince = time.Time{}
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for container to become healthy (status=%s)", info.State.Status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}
