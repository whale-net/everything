package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/whale-net/everything/libs/go/docker"
	"github.com/whale-net/everything/libs/go/logging"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal error", "error", err)
		os.Exit(1)
	}
}

type config struct {
	registry registryConfig

	dockerSocket  string
	containerName string

	envFilePath string
	versionVar  string

	pollInterval       time.Duration
	healthCheckTimeout time.Duration
	healthCheckPoll    time.Duration
}

func loadConfig() (config, error) {
	cfg := config{
		registry: registryConfig{
			Address:      getEnv("APP_REGISTRY_ADDRESS", "localhost:50051"),
			Environment:  os.Getenv("APP_REGISTRY_ENVIRONMENT"),
			Repository:   os.Getenv("APP_REGISTRY_REPOSITORY"),
			AuthMode:     getEnv("GRPC_AUTH_MODE", "none"),
			AuthTokenURL: os.Getenv("GRPC_AUTH_TOKEN_URL"),
			ClientID:     os.Getenv("GRPC_AUTH_CLIENT_ID"),
			ClientSecret: os.Getenv("GRPC_AUTH_CLIENT_SECRET"),
		},
		dockerSocket:  getEnv("DOCKER_SOCKET", "/var/run/docker.sock"),
		containerName: os.Getenv("CONTAINER_NAME"),
		envFilePath:   getEnv("ENV_FILE_PATH", "/workspace/.env"),
		versionVar:    getEnv("VERSION_ENV_VAR", "IMAGE_VERSION"),
	}
	if cfg.registry.Environment == "" {
		return config{}, fmt.Errorf("APP_REGISTRY_ENVIRONMENT must be set (e.g. \"prod\")")
	}
	if cfg.registry.Repository == "" {
		return config{}, fmt.Errorf("APP_REGISTRY_REPOSITORY must be set (e.g. \"ghcr.io/whale-net/manmanv2-host-manager\")")
	}
	if cfg.containerName == "" {
		return config{}, fmt.Errorf("CONTAINER_NAME must be set to the name of the compose service's container")
	}

	var err error
	if cfg.pollInterval, err = getEnvDuration("POLL_INTERVAL", 60*time.Second); err != nil {
		return config{}, err
	}
	if cfg.healthCheckTimeout, err = getEnvDuration("HEALTH_CHECK_TIMEOUT", 60*time.Second); err != nil {
		return config{}, err
	}
	if cfg.healthCheckPoll, err = getEnvDuration("HEALTH_CHECK_POLL_INTERVAL", 5*time.Second); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName: getEnv("LOG_SERVICE_NAME", "compose-resolver"),
		Domain:      os.Getenv("LOG_DOMAIN"),
		JSONFormat:  getEnv("LOG_FORMAT", "json") == "json",
	})
	defer logging.Shutdown(ctx)
	logger := logging.Get("main")

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	dockerClient, err := docker.NewClient(cfg.dockerSocket)
	if err != nil {
		return fmt.Errorf("connecting to Docker: %w", err)
	}
	defer dockerClient.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(cfg.pollInterval)
	defer ticker.Stop()

	logger.Info("compose-resolver starting",
		"environment", cfg.registry.Environment,
		"repository", cfg.registry.Repository,
		"container", cfg.containerName,
		"poll_interval", cfg.pollInterval)

	for {
		if err := reconcile(ctx, cfg, dockerClient, logger); err != nil {
			logger.Error("reconcile failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-sigCh:
			logger.Info("shutting down")
			return nil
		case <-ticker.C:
		}
	}
}

// reconcile resolves what app-registry says should be running and, if it
// differs from the target container's current image tag, deploys it: pull,
// swap the container, wait for health, roll back on failure.
//
// Current version is read from the running container's own image tag, not
// from .env -- .env is written for operator visibility only (see
// writeEnvValue) -- so drift between the two can never cause a spurious
// redeploy or a stuck "already up to date" false negative.
func reconcile(ctx context.Context, cfg config, dockerClient *docker.Client, logger *slog.Logger) error {
	resolved, err := resolveCurrentVersion(ctx, cfg.registry)
	if err != nil {
		return fmt.Errorf("resolving version from app-registry: %w", err)
	}
	if resolved == nil {
		logger.Warn("no promotion found for repository in environment; nothing to deploy",
			"repository", cfg.registry.Repository, "environment", cfg.registry.Environment)
		return nil
	}

	info, err := dockerClient.GetClient().ContainerInspect(ctx, cfg.containerName)
	if err != nil {
		return fmt.Errorf("inspecting %s (has `docker compose up -d` brought it up yet?): %w", cfg.containerName, err)
	}

	currentVersion := imageTag(info.Config.Image)
	if currentVersion == resolved.Version {
		return nil
	}

	oldRef := info.Config.Image
	newRef := cfg.registry.Repository + ":" + resolved.Version

	logger.Info("new version promoted, deploying", "from", currentVersion, "to", resolved.Version, "digest", resolved.Digest)

	newID, err := swapContainer(ctx, dockerClient, cfg.containerName, newRef)
	if err != nil {
		return fmt.Errorf("deploying %s: %w", newRef, err)
	}

	if err := waitForHealthy(ctx, dockerClient, newID, cfg.healthCheckTimeout, cfg.healthCheckPoll); err != nil {
		logger.Error("new version failed health check, rolling back", "version", resolved.Version, "error", err)
		if _, rbErr := swapContainer(ctx, dockerClient, cfg.containerName, oldRef); rbErr != nil {
			return fmt.Errorf("deploy of %s failed health check (%v), and rollback to %s also failed: %w", resolved.Version, err, oldRef, rbErr)
		}
		return fmt.Errorf("deploy of %s failed health check, rolled back to %s: %w", resolved.Version, oldRef, err)
	}

	if err := writeEnvValue(cfg.envFilePath, cfg.versionVar, resolved.Version); err != nil {
		logger.Warn("deploy succeeded but failed to update .env for operator visibility", "error", err)
	}

	logger.Info("deploy succeeded", "version", resolved.Version)
	return nil
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s=%q: %w", key, v, err)
	}
	return d, nil
}
