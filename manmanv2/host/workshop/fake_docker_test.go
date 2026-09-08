package workshop

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/whale-net/everything/libs/go/docker"
)

// fakeDockerClient is a full in-memory fake of DockerContainerClient (#2184's interface
// seam, added specifically so these tests don't need a live Docker daemon) used across
// orchestrator_test.go and verify_test.go to drive both the SteamCMD verify container
// lifecycle (verify.go) and the SteamCMD download/helper container lifecycle
// (orchestrator.go).
//
// It classifies every container by name prefix -- getVerifyContainerName's
// "workshop-verify-" vs getDownloadContainerName's "workshop-download-" -- and, on
// StartContainer, simulates SteamCMD's on-disk side effects by writing directly into the
// bind-mounted host path taken from the container's Volumes spec. All tests using this
// fake set internalDataDir == hostDataDir, so no host/container path translation is
// needed: the "container" writes exactly where the orchestrator itself later reads from.
type fakeDockerClient struct {
	mu sync.Mutex

	nextID     int
	containers map[string]*fakeContainerRecord

	// verifyContentVersions maps workshopID -> the content version a verify container
	// should report by writing it into appworkshop_<steamAppID>.acf. A missing/empty
	// entry leaves the manifest directory empty, so readWorkshopManifestVersion fails to
	// find the item's block at all (VerifyWorkshopItem's "manifest missing" failure mode).
	verifyContentVersions map[string]string
	// verifyExitCode is the exit code every verify container reports (default 0). A
	// nonzero value simulates a SteamCMD verify failure, independent of the download path.
	verifyExitCode int

	// downloadFiles maps filename -> content to deposit under the download container's
	// steamapps/workshop/content/<appid>/<workshopid>/ directory once "started".
	downloadFiles map[string]string
	// downloadExitCode is the exit code every download/helper container reports (default 0).
	downloadExitCode int

	pullErr error

	createdContainers []docker.ContainerConfig // every CreateContainer call, in order
}

type fakeContainerRecord struct {
	config   docker.ContainerConfig
	exitCode int
}

func newFakeDockerClient() *fakeDockerClient {
	return &fakeDockerClient{containers: map[string]*fakeContainerRecord{}}
}

func (f *fakeDockerClient) CreateContainer(ctx context.Context, config docker.ContainerConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("fake-container-%d", f.nextID)

	exitCode := f.downloadExitCode
	if strings.HasPrefix(config.Name, "workshop-verify-") {
		exitCode = f.verifyExitCode
	}
	f.containers[id] = &fakeContainerRecord{config: config, exitCode: exitCode}
	f.createdContainers = append(f.createdContainers, config)
	return id, nil
}

func (f *fakeDockerClient) StartContainer(ctx context.Context, containerID string) error {
	f.mu.Lock()
	rec, ok := f.containers[containerID]
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("fake docker: unknown container %s", containerID)
	}

	switch {
	case strings.HasPrefix(rec.config.Name, "workshop-verify-"):
		return f.simulateVerify(rec.config)
	case strings.HasPrefix(rec.config.Name, "workshop-download-"):
		return f.simulateDownload(rec.config)
	default:
		// Helper containers (e.g. the named-volume copy step): no filesystem side
		// effect is needed for any scenario these tests exercise (all use bind mounts).
		return nil
	}
}

func (f *fakeDockerClient) simulateVerify(config docker.ContainerConfig) error {
	steamAppID, workshopID, ok := steamAppAndWorkshopIDFromCommand(config.Command)
	if !ok || len(config.Volumes) == 0 {
		return nil
	}
	f.mu.Lock()
	version := f.verifyContentVersions[workshopID]
	f.mu.Unlock()
	if version == "" {
		return nil
	}
	hostDir := hostPathFromVolume(config.Volumes[0])
	return writeWorkshopManifest(hostDir, steamAppID, workshopID, version)
}

func (f *fakeDockerClient) simulateDownload(config docker.ContainerConfig) error {
	steamAppID, workshopID, ok := steamAppAndWorkshopIDFromCommand(config.Command)
	if !ok || len(config.Volumes) == 0 {
		return nil
	}
	// The temp download dir mount is always appended last -- HandleDownloadCommand
	// appends it to volumeMounts after resolveVolumeMounts's bind mounts.
	hostDir := hostPathFromVolume(config.Volumes[len(config.Volumes)-1])
	contentDir := filepath.Join(hostDir, "steamapps", "workshop", "content", steamAppID, workshopID)
	if err := os.MkdirAll(contentDir, 0777); err != nil {
		return err
	}
	f.mu.Lock()
	files := f.downloadFiles
	f.mu.Unlock()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(contentDir, name), []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeDockerClient) PullImage(ctx context.Context, imageRef string) error {
	return f.pullErr
}

func (f *fakeDockerClient) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, containerID)
	return nil
}

func (f *fakeDockerClient) GetContainerStatus(ctx context.Context, containerID string) (*docker.ContainerStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.containers[containerID]
	if !ok {
		return nil, fmt.Errorf("fake docker: container not found: %s", containerID)
	}
	return &docker.ContainerStatus{ContainerID: containerID, Running: false, ExitCode: rec.exitCode}, nil
}

func (f *fakeDockerClient) GetContainerLogs(ctx context.Context, containerID string, follow bool, tail string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

// downloadContainerCount returns how many SteamCMD *download* containers (not verify
// containers) have been created -- the FR8 cost-saving assertion ("no SteamCMD download
// container started" on a cache hit) needs to distinguish these from the verify
// container that always runs first, per #2184's verify-then-serve sequencing.
func (f *fakeDockerClient) downloadContainerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, cfg := range f.createdContainers {
		if strings.HasPrefix(cfg.Name, "workshop-download-") {
			n++
		}
	}
	return n
}

func (f *fakeDockerClient) verifyContainerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, cfg := range f.createdContainers {
		if strings.HasPrefix(cfg.Name, "workshop-verify-") {
			n++
		}
	}
	return n
}

// steamAppAndWorkshopIDFromCommand extracts the steamAppID/workshopID pair common to both
// buildSteamCMDCommand's and buildVerifySteamCMDCommand's
// "... +workshop_download_item <appid> <itemid> [validate] +quit" argument list -- both
// build their Command slice with these two values at the same fixed indices (5, 6).
func steamAppAndWorkshopIDFromCommand(cmd []string) (steamAppID, workshopID string, ok bool) {
	if len(cmd) < 7 {
		return "", "", false
	}
	return cmd[5], cmd[6], true
}

// hostPathFromVolume returns the host-side half of a "host_path:container_path" bind
// mount spec, as used throughout docker.ContainerConfig.Volumes.
func hostPathFromVolume(v string) string {
	parts := strings.SplitN(v, ":", 2)
	return parts[0]
}

// writeWorkshopManifest writes a minimal appworkshop_<steamAppID>.acf under
// hostDir/steamapps/workshop/, in the shape readWorkshopManifestVersion (verify.go)
// parses: a block keyed by workshopID containing a "manifest" field.
func writeWorkshopManifest(hostDir, steamAppID, workshopID, version string) error {
	dir := filepath.Join(hostDir, "steamapps", "workshop")
	if err := os.MkdirAll(dir, 0777); err != nil {
		return err
	}
	content := fmt.Sprintf(`"AppWorkshop"
{
	"WorkshopItemsInstalled"
	{
		"%s"
		{
			"manifest"		"%s"
		}
	}
}
`, workshopID, version)
	return os.WriteFile(filepath.Join(dir, fmt.Sprintf("appworkshop_%s.acf", steamAppID)), []byte(content), 0644)
}
