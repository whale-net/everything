package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
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
//
// If creating or starting the new container fails -- after the old one has
// already been removed -- this makes a best-effort attempt to recreate
// whatever was running when this call started, so a transient daemon error
// (disk full, name collision, ...) doesn't leave the host with nothing
// running at all. That recovery target is "what this call started from",
// not necessarily the deploy's original version: if swapContainer is itself
// being used to roll back a failed deploy and the rollback's own create
// fails, the recovered container runs the version that just failed health
// checks, not the one before that. Still-running-something beats
// nothing-running in that scenario.
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

	id, err := createAndStart(ctx, raw, c, info, newImageRef)
	if err != nil {
		if _, recoverErr := createAndStart(ctx, raw, c, info, info.Config.Image); recoverErr != nil {
			return "", fmt.Errorf("recreating %s with %s failed (%w), AND recovering the previous container also failed -- host has no running container for %s: %v", containerName, newImageRef, err, containerName, recoverErr)
		}
		return "", fmt.Errorf("recreating %s with %s failed, recovered the previous container: %w", containerName, newImageRef, err)
	}
	return id, nil
}

// createAndStart creates and starts a container cloned from info's
// Config/HostConfig/network attachment, but with imageRef substituted for
// the image. Shared by swapContainer's primary attempt and its
// create/start-failure recovery path.
func createAndStart(ctx context.Context, raw *dockerclient.Client, c *docker.Client, info types.ContainerJSON, imageRef string) (string, error) {
	newConfig := *info.Config
	newConfig.Image = imageRef

	var netConfig *network.NetworkingConfig
	if info.NetworkSettings != nil && len(info.NetworkSettings.Networks) > 0 {
		netConfig = &network.NetworkingConfig{EndpointsConfig: info.NetworkSettings.Networks}
	}

	name := strings.TrimPrefix(info.Name, "/")
	resp, err := raw.ContainerCreate(ctx, &newConfig, info.HostConfig, netConfig, nil, name)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", name, err)
	}
	if err := c.StartContainer(ctx, resp.ID); err != nil {
		return "", fmt.Errorf("starting %s: %w", name, err)
	}
	return resp.ID, nil
}

// waitForHealthy polls containerID -- a container createAndStart just
// created, so its restart count starts at 0 -- until it looks healthy, the
// context is cancelled, or timeout elapses.
//
// If the image defines a Docker HEALTHCHECK, "healthy" means
// State.Health.Status == "healthy". Otherwise it means the container has
// stayed "running" -- without restarting at all -- for one full pollEvery
// interval: the best available signal that it isn't crash-looping when
// there's no real health endpoint to poll (host-manager reports its own
// liveness over RabbitMQ, not HTTP).
func waitForHealthy(ctx context.Context, c *docker.Client, containerID string, timeout, pollEvery time.Duration) error {
	raw := c.GetClient()
	deadline := time.Now().Add(timeout)
	var stableSince time.Time

	for {
		info, err := raw.ContainerInspect(ctx, containerID)
		if err != nil {
			return fmt.Errorf("inspecting %s: %w", containerID, err)
		}

		if info.RestartCount > 0 {
			return fmt.Errorf("container restarted (restart count %d), likely crash-looping", info.RestartCount)
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
