package cmd

import (
	"fmt"

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
	)
	return artifactsCmd
}

// newArtifactsRecordCmd is the CI write path. The exact flag shape depends on
// how tools/helm's chart -> image lockfile is threaded through CI, which
// lands with the ArtifactRegistry implementation in AR-2 — command structure
// only for now.
func newArtifactsRecordCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "record",
		Short: "Record a published artifact (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("not implemented: flag shape lands with RecordArtifact in AR-2")
		},
	}
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
