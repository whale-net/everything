package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func newArtifactsCmd() *cobra.Command {
	artifactsCmd := &cobra.Command{
		Use:   "artifacts",
		Short: "Query published artifacts (images and charts)",
	}
	artifactsCmd.AddCommand(
		newArtifactsListCmd(),
		newArtifactsGetCmd(),
		newArtifactsResolveCmd(),
		newArtifactsRecordCmd(),
		newArtifactsBeginPublishCmd(),
		newArtifactsBeginPublishBatchCmd(),
		newArtifactsFailPublishCmd(),
	)
	return artifactsCmd
}

// containedImageJSON mirrors pb.ContainedImage's wire shape for --contains,
// a JSON array file so the AR-2c workflow can pass a chart's resolved images
// without one flag per field per image. Plain encoding/json is enough here —
// every field is a string, so there is no need for protojson's message
// handling the way newAppsReconcileCmd needs it for AppManifestSet.
type containedImageJSON struct {
	AppFullName string `json:"app_full_name"`
	Repository  string `json:"repository"`
	Version     string `json:"version"`
	Digest      string `json:"digest"`
}

// newArtifactsRecordCmd is the CI write path: called once per pushed image,
// and once per chart with --contains pointing at its resolved image digests
// (see tools/helm.ChartLockfile — AR-2c resolves each entry's tag to a
// digest after push and writes the result to a --contains file).
func newArtifactsRecordCmd() *cobra.Command {
	var buildID, kind, owner, repository, version, digest, containsFile string
	var publishedAt int64
	c := &cobra.Command{
		Use:   "record",
		Short: "Record a published artifact (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseArtifactKind(kind)
			if err != nil {
				return err
			}
			if publishedAt == 0 {
				publishedAt = time.Now().Unix()
			}
			req := &pb.RecordArtifactRequest{
				BuildId:        buildID,
				Kind:           k,
				OwnerFullName:  owner,
				Repository:     repository,
				Version:        version,
				Digest:         digest,
				PublishedAt:    publishedAt,
				IdempotencyKey: idempotencyKeyFlag,
			}
			if containsFile != "" {
				data, err := os.ReadFile(containsFile)
				if err != nil {
					return fmt.Errorf("read --contains %s: %w", containsFile, err)
				}
				var images []containedImageJSON
				if err := json.Unmarshal(data, &images); err != nil {
					return fmt.Errorf("parse --contains %s: %w", containsFile, err)
				}
				for _, img := range images {
					req.Contains = append(req.Contains, &pb.ContainedImage{
						AppFullName: img.AppFullName,
						Repository:  img.Repository,
						Version:     img.Version,
						Digest:      img.Digest,
					})
				}
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.RecordArtifact(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&buildID, "build-id", "", "Build id returned by 'builds record'")
	c.Flags().StringVar(&kind, "kind", "", "Artifact kind (image|chart)")
	c.Flags().StringVar(&owner, "owner", "", "Owning app or chart, as <domain>-<name>")
	c.Flags().StringVar(&repository, "repository", "", "Registry repository, e.g. ghcr.io/whale-net/app-registry-api")
	c.Flags().StringVar(&version, "version", "", "Semver tag, e.g. v1.2.3")
	c.Flags().StringVar(&digest, "digest", "", `Content digest, "sha256:..."`)
	c.Flags().Int64Var(&publishedAt, "published-at", 0, "Unix timestamp published (default: now)")
	c.Flags().StringVar(&containsFile, "contains", "", "kind=chart only: path to a JSON array of resolved image references")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>-<owner>-<kind>")
	_ = c.MarkFlagRequired("build-id")
	_ = c.MarkFlagRequired("kind")
	_ = c.MarkFlagRequired("owner")
	_ = c.MarkFlagRequired("repository")
	_ = c.MarkFlagRequired("digest")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

// newArtifactsBeginPublishCmd is the AR-7b (issue #558) CI write path,
// called immediately before an image/chart push -- the ∅|allocated|failed
// -> publishing transition. See ARCHITECTURE.md "Artifact lifecycle:
// allocated -> publishing -> published".
func newArtifactsBeginPublishCmd() *cobra.Command {
	var kind, owner, version, buildID string
	c := &cobra.Command{
		Use:   "begin-publish",
		Short: "Begin publishing an artifact, before the push (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseArtifactKind(kind)
			if err != nil {
				return err
			}
			req := &pb.BeginPublishRequest{
				Kind:           k,
				OwnerFullName:  owner,
				Version:        version,
				BuildId:        buildID,
				IdempotencyKey: idempotencyKeyFlag,
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.BeginPublish(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&kind, "kind", "", "Artifact kind (image|chart)")
	c.Flags().StringVar(&owner, "owner", "", "Owning app or chart, as <domain>-<name>")
	c.Flags().StringVar(&version, "version", "", "Semver tag, e.g. v1.2.3")
	c.Flags().StringVar(&buildID, "build-id", "", "Build id returned by 'builds record'")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>-<owner>-<kind>")
	_ = c.MarkFlagRequired("kind")
	_ = c.MarkFlagRequired("owner")
	_ = c.MarkFlagRequired("version")
	_ = c.MarkFlagRequired("build-id")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

// beginPublishBatchTargetJSON is the wire shape --targets expects: a JSON
// array file, one entry per target BeginPublishBatch should transition to
// "publishing". Deliberately generic (kind/owner/version) rather than
// mirroring release_helper_go's plan-matrix shape (domain/app/version)
// verbatim -- the caller (a jq transform in release.yml's plan-release job)
// derives owner_full_name = "<domain>-<app>" itself, so this file format
// stays reusable for a future chart-side batch call too, not just images.
type beginPublishBatchTargetJSON struct {
	Kind    string `json:"kind"`
	Owner   string `json:"owner"`
	Version string `json:"version"`
}

// newArtifactsBeginPublishBatchCmd is AR-7d's (issue #558) closing of the
// gap AR-7b's own scope note left open: called ONCE from the release plan
// step, before the release matrix fans out, so every intended target
// already has a "publishing" row -- and therefore shows up in
// `builds status` -- even if its own matrix leg never starts. See
// ARCHITECTURE.md "The run log" -> "As built (AR-7d)".
func newArtifactsBeginPublishBatchCmd() *cobra.Command {
	var buildID, targetsFile, idempotencyKeyPrefix string
	c := &cobra.Command{
		Use:   "begin-publish-batch",
		Short: "Begin publishing every target of a release run, before the matrix fans out (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(targetsFile)
			if err != nil {
				return fmt.Errorf("read --targets %s: %w", targetsFile, err)
			}
			var targets []beginPublishBatchTargetJSON
			if err := json.Unmarshal(data, &targets); err != nil {
				return fmt.Errorf("parse --targets %s: %w", targetsFile, err)
			}
			if len(targets) == 0 {
				return fmt.Errorf("--targets %s contains no targets", targetsFile)
			}

			req := &pb.BeginPublishBatchRequest{
				BuildId:              buildID,
				IdempotencyKeyPrefix: idempotencyKeyPrefix,
			}
			for _, t := range targets {
				k, err := parseArtifactKind(t.Kind)
				if err != nil {
					return fmt.Errorf("target %s: %w", t.Owner, err)
				}
				req.Targets = append(req.Targets, &pb.BeginPublishBatchTarget{
					Kind:          k,
					OwnerFullName: t.Owner,
					Version:       t.Version,
				})
			}

			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.BeginPublishBatch(cmd.Context(), req)
				if err != nil {
					return err
				}
				failed := 0
				for _, r := range resp.Results {
					if r.Error != "" {
						failed++
						fmt.Fprintf(os.Stderr, "::warning title=App Registry: begin-publish-batch target failed::%s %s: %s\n", r.OwnerFullName, r.Version, r.Error)
					}
				}
				if err := printResponse(resp); err != nil {
					return err
				}
				if failed > 0 {
					return fmt.Errorf("%d of %d targets failed begin-publish (see warnings above)", failed, len(resp.Results))
				}
				return nil
			})
		},
	}
	c.Flags().StringVar(&buildID, "build-id", "", "Build id returned by 'builds record'")
	c.Flags().StringVar(&targetsFile, "targets", "", `Path to a JSON array of {"kind","owner","version"} targets`)
	c.Flags().StringVar(&idempotencyKeyPrefix, "idempotency-key-prefix", "", "Required. <workflow_run_id>-<attempt> -- each target's own key is derived as <prefix>-<owner>-<kind>-intent, deliberately distinct from the per-leg BeginPublish call's key")
	_ = c.MarkFlagRequired("build-id")
	_ = c.MarkFlagRequired("targets")
	_ = c.MarkFlagRequired("idempotency-key-prefix")
	return c
}

// newArtifactsFailPublishCmd is the AR-7b (issue #558) CI write path,
// called on release.yml's error path immediately after a begin-publish for
// the same target -- the publishing -> failed transition.
func newArtifactsFailPublishCmd() *cobra.Command {
	var kind, owner, version, reason string
	c := &cobra.Command{
		Use:   "fail-publish",
		Short: "Mark an in-progress publish as failed (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := parseArtifactKind(kind)
			if err != nil {
				return err
			}
			req := &pb.FailPublishRequest{
				Kind:           k,
				OwnerFullName:  owner,
				Version:        version,
				Reason:         reason,
				IdempotencyKey: idempotencyKeyFlag,
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.FailPublish(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&kind, "kind", "", "Artifact kind (image|chart)")
	c.Flags().StringVar(&owner, "owner", "", "Owning app or chart, as <domain>-<name>")
	c.Flags().StringVar(&version, "version", "", "Semver tag, e.g. v1.2.3")
	c.Flags().StringVar(&reason, "reason", "", "Required. Why the publish failed")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>-<owner>-<kind>")
	_ = c.MarkFlagRequired("kind")
	_ = c.MarkFlagRequired("owner")
	_ = c.MarkFlagRequired("version")
	_ = c.MarkFlagRequired("reason")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

func newArtifactsListCmd() *cobra.Command {
	var kind string
	var promotableOnly bool
	c := &cobra.Command{
		Use:   "list <domain-name>",
		Short: "List artifacts for an app or chart",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.ListArtifactsRequest{
				OwnerFullName:  args[0],
				PromotableOnly: promotableOnly,
			}
			if kind != "" {
				k, err := parseArtifactKind(kind)
				if err != nil {
					return err
				}
				req.Kind = k
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.ListArtifacts(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&kind, "kind", "", "Filter by kind (image|chart)")
	c.Flags().BoolVar(&promotableOnly, "promotable", false, "Only artifacts a caller could legally promote")
	return c
}

func newArtifactsGetCmd() *cobra.Command {
	var version string
	c := &cobra.Command{
		Use:   "get <domain-name>",
		Short: "Get one artifact by owner + version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.GetArtifact(cmd.Context(), &pb.GetArtifactRequest{
					OwnerFullName: args[0],
					Version:       version,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&version, "version", "", "Version to look up, e.g. v1.2.3")
	_ = c.MarkFlagRequired("version")
	return c
}

func newArtifactsResolveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "resolve <digest-or-artifact-id>",
		Short: "Walk a chart artifact down to the image digests it pins",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.ResolveArtifactRequest{}
			if len(args[0]) > 7 && args[0][:7] == "sha256:" {
				req.Digest = args[0]
			} else {
				req.ArtifactId = args[0]
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.ResolveArtifact(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	return c
}

func parseArtifactKind(s string) (pb.ArtifactKind, error) {
	switch s {
	case "image":
		return pb.ArtifactKind_ARTIFACT_KIND_IMAGE, nil
	case "chart":
		return pb.ArtifactKind_ARTIFACT_KIND_CHART, nil
	default:
		return pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, fmt.Errorf("unknown kind %q (want image|chart)", s)
	}
}
