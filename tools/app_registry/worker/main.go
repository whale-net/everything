// Command app-registry-worker is the AR-4b/AR-5 Temporal worker: it drains
// tools/app_registry's writeback_outbox table and runs WritebackWorkflow
// executions against a stub Writeback activity implementation that renders
// promotion state and writes it to a local path -- publishing nowhere -- and
// (issue #889) also runs ReleaseWorkflow executions on the same task queue.
// See ../ARCHITECTURE.md "Writeback: outbox -> Temporal" and ./README.md.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/postgres"
	"github.com/whale-net/everything/tools/app_registry/worker/outbox"
	"github.com/whale-net/everything/tools/app_registry/worker/reaper"
	"github.com/whale-net/everything/tools/app_registry/worker/release"
	"github.com/whale-net/everything/tools/app_registry/worker/writeback"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Fatal error: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName:   "app-registry-worker",
		Domain:        "app-registry",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck
	logger := logging.Get("app-registry-worker")

	// Direct Postgres connection for draining the outbox -- see
	// outbox.Drainer's Store field doc comment for why this is not routed
	// through the gRPC API.
	logger.Info("connecting to database")
	pool, err := db.NewPool(ctx, "")
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	repo := postgres.NewRepository(pool)

	// gRPC client to the App Registry API, used only by StubActivities to
	// read state via GetEnvironmentState -- see ARCHITECTURE.md "The API
	// is the write path; git is the delivery path". Any authenticated
	// credential works; GetEnvironmentState only requires
	// auth.RequireAuthenticated.
	registryAddr := getEnv("APP_REGISTRY_ADDRESS", "localhost:50051")
	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         grpcauth.AuthMode(getEnv("GRPC_AUTH_MODE", "none")),
		TokenURL:     os.Getenv("GRPC_AUTH_TOKEN_URL"),
		ClientID:     os.Getenv("GRPC_AUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("GRPC_AUTH_CLIENT_SECRET"),
	})
	if err != nil {
		return fmt.Errorf("create auth dial option: %w", err)
	}
	conn, err := grpcclient.NewClient(ctx, registryAddr, authOpt)
	if err != nil {
		return fmt.Errorf("connect to app-registry-api at %s: %w", registryAddr, err)
	}
	defer conn.Close() //nolint:errcheck
	registryClient := pb.NewPromotionRegistryClient(conn.GetConnection())
	appClient := pb.NewAppRegistryClient(conn.GetConnection())

	// Temporal client + worker, via libs/go/temporal -- see AR-4a.
	temporalCfg := temporallib.ConfigFromEnv()
	if temporalCfg.TaskQueue == "" {
		temporalCfg.TaskQueue = writeback.TaskQueue
	}
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace, "task_queue", temporalCfg.TaskQueue)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("app-registry-worker"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	w := temporallib.NewWorker(temporalClient, temporalCfg.TaskQueue, worker.Options{})
	w.RegisterWorkflow(writeback.WritebackWorkflow)

	// Real (gitops) Writeback implementation is opt-in: only selected when
	// WRITEBACK_GITOPS_REPO is set, so `bazel test`/local dev/Tilt keep
	// working with zero config against StubActivities -- see issue #798's
	// sequencing and worker/writeback/gitops.go's package doc comment.
	if gitopsRepo := os.Getenv("WRITEBACK_GITOPS_REPO"); gitopsRepo != "" {
		gitops, gerr := writeback.NewGitOpsActivities(registryClient, appClient, writeback.GitOpsConfig{
			Repo:           gitopsRepo,
			Branch:         os.Getenv("WRITEBACK_GITOPS_BRANCH"),
			AppID:          os.Getenv("WRITEBACK_GITHUB_APP_ID"),
			InstallationID: os.Getenv("WRITEBACK_GITHUB_APP_INSTALLATION_ID"),
			PrivateKeyPEM:  os.Getenv("WRITEBACK_GITHUB_APP_PRIVATE_KEY"),
			AuthorName:     os.Getenv("WRITEBACK_GIT_AUTHOR_NAME"),
			AuthorEmail:    os.Getenv("WRITEBACK_GIT_AUTHOR_EMAIL"),
		})
		if gerr != nil {
			return fmt.Errorf("configure gitops writeback activities: %w", gerr)
		}
		logger.Info("using real gitops Writeback implementation", "repo", gitopsRepo, "branch", gitops.Config.Branch)
		w.RegisterActivityWithOptions(gitops.RenderEnvironmentState, activityOptions(writeback.ActivityRenderEnvironmentState))
		w.RegisterActivityWithOptions(gitops.Publish, activityOptions(writeback.ActivityPublish))
	} else {
		outDir := getEnv("WRITEBACK_OUTPUT_DIR", "/tmp/app-registry-writeback")
		stub := writeback.NewStubActivities(registryClient, outDir)
		w.RegisterActivityWithOptions(stub.RenderEnvironmentState, activityOptions(writeback.ActivityRenderEnvironmentState))
		w.RegisterActivityWithOptions(stub.Publish, activityOptions(writeback.ActivityPublish))
	}

	// RecordWritebackResult (FR7a, issue #1029) is shared by both the
	// gitops and stub branches above -- see writeback.Recorder's doc
	// comment. Same direct-Postgres repo (repo) the outbox drainer and
	// release.Activities below use, for the same "no mutating gRPC RPC for
	// this write" reason.
	recorder := &writeback.Recorder{Registry: repo}
	w.RegisterActivityWithOptions(recorder.RecordWritebackResult, activityOptions(writeback.ActivityRecordWritebackResult))

	// ReleaseWorkflow (issue #889), registered on the same task queue as
	// WritebackWorkflow -- release.TaskQueue == writeback.TaskQueue, see
	// that constant's doc comment. Registry is the same direct-Postgres repo
	// the outbox drainer uses (see release/record.go's package doc comment
	// for why VerifyPublished/RecordTargetState bypass the gRPC API).
	// GitHub is opt-in, like WRITEBACK_GITOPS_REPO above: DispatchBuild/
	// PollBuild return a clear "not configured" error rather than running
	// with a silently-defaulted credential when unset. PlanBinaryPath/
	// WorkspaceRoot are optional overrides, not requirements -- ResolvePlan
	// and FinalizePublish both work with them unset (a PATH-resolved
	// release_helper_go binary and a per-invocation scratch/cloned
	// directory respectively; see release/plan.go and
	// release/finalize.go's package doc comments) -- so `bazel test`/local
	// dev/Tilt keep working with zero config either way.
	releaseActivities := &release.Activities{
		Registry:       repo,
		PlanBinaryPath: os.Getenv("RELEASE_PLAN_BINARY_PATH"),
		WorkspaceRoot:  os.Getenv("RELEASE_WORKSPACE_ROOT"),
		// ChartMuseum credentials for FinalizePublish's finalize-chart
		// shell-out (issue #928) -- credential-locality move: ChartMuseum
		// write access now lives on this worker, not in release-v2.yml's
		// merged build job. Optional like the vars above: a batch with no
		// chart targets never needs them.
		ChartRepoURL:  getEnv("RELEASE_CHART_REPO_URL", "https://charts.whalenet.dev"),
		ChartRepoUser: os.Getenv("RELEASE_CHART_REPO_USER"),
		ChartRepoPass: os.Getenv("RELEASE_CHART_REPO_PASS"),
		// GHCR retag credential for FinalizePublish's finalize-app shell-out
		// (issue #996) -- a static bot-account PAT, not minted from the
		// RELEASE_GITHUB_APP_* App below: App installation tokens cannot
		// write to organization-owned GHCR packages outside a GitHub
		// Actions run. Optional like the chart vars: a batch with no app
		// targets never needs it (see Activities.GHCRToken doc comment).
		GHCRToken: os.Getenv("RELEASE_GHCR_TOKEN"),
		// S3 credentials for FinalizePublish's CLI-binary publish step
		// (issue #984) -- same credential-locality reasoning as
		// RELEASE_CHART_REPO_* above: this worker holds write access to the
		// dedicated RELEASE_TOOLS_S3_BUCKET, no GHA job does. Optional like
		// the chart vars: a batch with no release_helper_go/app-registry
		// target never needs them (see Activities.ReleaseToolsS3* doc
		// comment, activities.go).
		ReleaseToolsS3Bucket:    os.Getenv("RELEASE_TOOLS_S3_BUCKET"),
		ReleaseToolsS3Endpoint:  os.Getenv("RELEASE_TOOLS_S3_ENDPOINT"),
		ReleaseToolsS3Region:    os.Getenv("RELEASE_TOOLS_S3_REGION"),
		ReleaseToolsS3AccessKey: os.Getenv("RELEASE_TOOLS_S3_ACCESS_KEY"),
		ReleaseToolsS3SecretKey: os.Getenv("RELEASE_TOOLS_S3_SECRET_KEY"),
	}
	if appID := os.Getenv("RELEASE_GITHUB_APP_ID"); appID != "" {
		dispatcher, derr := release.NewGitHubDispatcher(release.GitHubDispatcherConfig{
			App: writeback.GitHubAppConfig{
				AppID:          appID,
				InstallationID: os.Getenv("RELEASE_GITHUB_APP_INSTALLATION_ID"),
				PrivateKeyPEM:  os.Getenv("RELEASE_GITHUB_APP_PRIVATE_KEY"),
			},
			Owner:        getEnv("RELEASE_GITHUB_REPO_OWNER", "whale-net"),
			Repo:         getEnv("RELEASE_GITHUB_REPO_NAME", "everything"),
			WorkflowFile: getEnv("RELEASE_GITHUB_WORKFLOW_FILE", "release-v2.yml"),
			Ref:          getEnv("RELEASE_GITHUB_REF", "main"),
		})
		if derr != nil {
			return fmt.Errorf("configure release github dispatcher: %w", derr)
		}
		logger.Info("using real GitHub dispatcher for ReleaseWorkflow", "owner", dispatcher.Config.Owner, "repo", dispatcher.Config.Repo, "workflow_file", dispatcher.Config.WorkflowFile)
		releaseActivities.GitHub = dispatcher
	}
	w.RegisterWorkflow(release.ReleaseWorkflow)
	w.RegisterActivityWithOptions(releaseActivities.CheckApproval, activityOptions(release.ActivityCheckApproval))
	w.RegisterActivityWithOptions(releaseActivities.ResolvePlan, activityOptions(release.ActivityResolvePlan))
	w.RegisterActivityWithOptions(releaseActivities.RecordResolvedPlan, activityOptions(release.ActivityRecordResolvedPlan))
	w.RegisterActivityWithOptions(releaseActivities.DispatchBuild, activityOptions(release.ActivityDispatchBuild))
	w.RegisterActivityWithOptions(releaseActivities.PollBuild, activityOptions(release.ActivityPollBuild))
	w.RegisterActivityWithOptions(releaseActivities.FinalizePublish, activityOptions(release.ActivityFinalizePublish))
	w.RegisterActivityWithOptions(releaseActivities.VerifyPublished, activityOptions(release.ActivityVerifyPublished))
	w.RegisterActivityWithOptions(releaseActivities.RecordTargetState, activityOptions(release.ActivityRecordTargetState))

	// Outbox drain loop, running alongside the Temporal worker in this same
	// process -- see outbox.Drainer.
	drainer := &outbox.Drainer{
		Store:        repo.Writeback(),
		Temporal:     temporalClient,
		TaskQueue:    temporalCfg.TaskQueue,
		WorkerID:     workerID(),
		BatchSize:    getEnvInt("WRITEBACK_BATCH_SIZE", 20),
		StaleAfter:   getEnvDuration("WRITEBACK_CLAIM_STALE_AFTER", 2*time.Minute),
		PollInterval: getEnvDuration("WRITEBACK_POLL_INTERVAL", 5*time.Second),
		Logger:       logger,
	}

	// Stale-artifact reaper (AR-7b, issue #558), running alongside the
	// outbox drain loop in this same process -- see reaper.Reaper and
	// ARCHITECTURE.md "The reaper is not optional".
	artifactReaper := &reaper.Reaper{
		Store:        repo.Artifacts(),
		Timeout:      getEnvDuration("ARTIFACT_REAPER_TIMEOUT", 30*time.Minute),
		PollInterval: getEnvDuration("ARTIFACT_REAPER_POLL_INTERVAL", 5*time.Minute),
		Logger:       logger,
	}

	done := make(chan error, 3)
	go func() {
		if err := w.Run(worker.InterruptCh()); err != nil {
			done <- fmt.Errorf("temporal worker: %w", err)
			return
		}
		done <- nil
	}()
	go func() {
		if err := drainer.Run(ctx); err != nil && ctx.Err() == nil {
			done <- fmt.Errorf("outbox drainer: %w", err)
			return
		}
		done <- nil
	}()
	go func() {
		if err := artifactReaper.Run(ctx); err != nil && ctx.Err() == nil {
			done <- fmt.Errorf("artifact reaper: %w", err)
			return
		}
		done <- nil
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		logger.Info("shutting down gracefully")
		cancel()
	case err := <-done:
		cancel()
		return err
	}
	<-done
	return nil
}

// activityOptions names an activity registration -- see
// writeback.ActivityRenderEnvironmentState/ActivityPublish's doc comment on
// why WritebackWorkflow dispatches by string name rather than a Go method
// value: the workflow depends on the Writeback interface, not on
// StubActivities being the implementation behind it.
func activityOptions(name string) activity.RegisterOptions {
	return activity.RegisterOptions{Name: name}
}

func workerID() string {
	if id := os.Getenv("WORKER_ID"); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "app-registry-worker"
	}
	return "app-registry-worker-" + host
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return defaultValue
	}
	return n
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultValue
	}
	return d
}
