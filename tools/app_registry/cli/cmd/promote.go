package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/protobuf/proto"
)

// promoteIdempotencyKey returns key, or a freshly generated UUID when key is
// empty. ARCHITECTURE.md "Idempotency": CI uses a derived
// <workflow_run_id>-<attempt>[-...] key; human promotions use a
// client-generated UUID. `builds record` / `artifacts record` are CI-only
// and keep --idempotency-key required; promote/rollback are the human path,
// so they generate one rather than forcing an operator to invent one.
func promoteIdempotencyKey(key string) string {
	if key != "" {
		return key
	}
	return uuid.NewString()
}

func newPromoteCmd() *cobra.Command {
	var env, reason string
	var allowOverride, dryRun bool
	c := &cobra.Command{
		Use:   "promote <domain-name> <version>",
		Short: "Promote an artifact to an environment",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.PromoteRequest{
				EnvironmentKey: env,
				OwnerFullName:  args[0],
				Version:        args[1],
				Reason:         reason,
				AllowOverride:  allowOverride,
				DryRun:         dryRun,
				IdempotencyKey: promoteIdempotencyKey(idempotencyKeyFlag),
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Promotion.Promote(cmd.Context(), req)
				if err != nil {
					return err
				}
				// Surface these on stderr, ahead of the JSON body, so neither
				// gets lost in output a human might skim past -- a dry run or
				// an already-current no-op must never look like a write that
				// happened.
				if resp.GetDryRun() {
					fmt.Fprintln(os.Stderr, "dry run: no write performed")
				}
				if resp.GetAlreadyPromoted() {
					fmt.Fprintf(os.Stderr, "already promoted: %s is already current in %q; no new promotion recorded\n", args[0], env)
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Target environment key, e.g. prod")
	c.Flags().StringVar(&reason, "reason", "", "Required above dev rank; recorded in the audit log")
	c.Flags().BoolVar(&allowOverride, "allow-override", false, "Acknowledge promoting a VIA_CHART artifact directly")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the resulting state without writing")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Client-generated; a UUID is generated if omitted (see ARCHITECTURE.md 'Idempotency')")
	_ = c.MarkFlagRequired("env")
	return c
}

func newRollbackCmd() *cobra.Command {
	var env, reason string
	var dryRun bool
	c := &cobra.Command{
		Use:   "rollback <domain-name>",
		Short: "Re-promote whatever was previously current for this target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Promotion.Rollback(cmd.Context(), &pb.RollbackRequest{
					EnvironmentKey: env,
					OwnerFullName:  args[0],
					Reason:         reason,
					DryRun:         dryRun,
					IdempotencyKey: promoteIdempotencyKey(idempotencyKeyFlag),
				})
				if err != nil {
					return err
				}
				if resp.GetDryRun() {
					fmt.Fprintln(os.Stderr, "dry run: no write performed")
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Target environment key, e.g. prod")
	c.Flags().StringVar(&reason, "reason", "", "Required above dev rank; recorded in the audit log")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the resulting state without writing")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Client-generated; a UUID is generated if omitted (see ARCHITECTURE.md 'Idempotency')")
	_ = c.MarkFlagRequired("env")
	return c
}

func newStatusCmd() *cobra.Command {
	var domain string
	var at string
	c := &cobra.Command{
		Use:   "status <env>",
		Short: "What is running in an environment (now, or at a past instant)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.GetEnvironmentStateRequest{
				EnvironmentKey: args[0],
				Domain:         domain,
			}
			if at != "" {
				ts, err := parseRFC3339(at)
				if err != nil {
					return err
				}
				req.At = ts
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Promotion.GetEnvironmentState(cmd.Context(), req)
				if err != nil {
					return err
				}
				printDriftWarning(args[0], resp)
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&domain, "domain", "", "Filter by domain")
	c.Flags().StringVar(&at, "at", "", "RFC3339 timestamp; omit for current state")
	return c
}

// printDriftWarning renders every DriftEntry in resp prominently, on stderr,
// ahead of the JSON body on stdout. This exists because `status` is the
// command that must never let a drifted environment look clean -- see
// ARCHITECTURE.md "Promotability": an is_override image promotion that no
// longer matches its chart's pinned digest is legal but must stay visible.
// Written to stderr rather than folded into the JSON so `--format json`
// output piped to a CI parser is unaffected.
func printDriftWarning(env string, resp *pb.GetEnvironmentStateResponse) {
	var drift []*pb.DriftEntry
	for _, e := range resp.GetEntries() {
		drift = append(drift, e.GetDrift()...)
	}
	if len(drift) == 0 {
		fmt.Fprintf(os.Stderr, "status: no drift detected in %q\n", env)
		return
	}
	fmt.Fprintf(os.Stderr, "\n*** DRIFT DETECTED in %q: %d entr(y/ies) do not match their chart's pinned digest ***\n", env, len(drift))
	for _, d := range drift {
		fmt.Fprintf(os.Stderr, "  - %s: chart pins %s, but promotion %s put %s live\n",
			d.GetAppFullName(), d.GetChartPinnedDigest(), d.GetPromotionId(), d.GetPromotedDigest())
	}
	fmt.Fprintln(os.Stderr)
}

func newHistoryCmd() *cobra.Command {
	var env string
	c := &cobra.Command{
		Use:   "history <domain-name>",
		Short: "Promotion history and audit log for a target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				promotions, err := rc.Promotion.ListPromotions(cmd.Context(), &pb.ListPromotionsRequest{
					OwnerFullName:  args[0],
					EnvironmentKey: env,
					IncludeHistory: true,
				})
				if err != nil {
					return fmt.Errorf("listing promotions: %w", err)
				}
				events, err := rc.Promotion.ListPromotionEvents(cmd.Context(), &pb.ListPromotionEventsRequest{
					OwnerFullName:  args[0],
					EnvironmentKey: env,
				})
				if err != nil {
					return fmt.Errorf("listing promotion events: %w", err)
				}
				return printCombinedResponse(map[string]proto.Message{
					"promotions": promotions,
					"events":     events,
				})
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Filter by environment key")
	return c
}

func newDiffCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff <env-a> <env-b>",
		Short: "Diff what is currently running between two environments",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				stateA, err := rc.Promotion.GetEnvironmentState(cmd.Context(), &pb.GetEnvironmentStateRequest{EnvironmentKey: args[0]})
				if err != nil {
					return fmt.Errorf("fetching state for %s: %w", args[0], err)
				}
				stateB, err := rc.Promotion.GetEnvironmentState(cmd.Context(), &pb.GetEnvironmentStateRequest{EnvironmentKey: args[1]})
				if err != nil {
					return fmt.Errorf("fetching state for %s: %w", args[1], err)
				}
				return printJSON(diffEnvironmentStates(args[0], args[1], stateA, stateB))
			})
		},
	}
	return c
}

// environmentDiff is the CLI-computed comparison of two environments'
// current state. Not a wire type: GetEnvironmentState has no server-side
// diff RPC by design (AR-3d scope note in PLAN.md -- "compute client-side
// from two GetEnvironmentState calls; do not add an RPC"), so this struct
// only ever exists on the CLI side of the wire.
type environmentDiff struct {
	EnvironmentA string      `json:"environment_a"`
	EnvironmentB string      `json:"environment_b"`
	Entries      []diffEntry `json:"entries"`
}

// diffEntry describes one promotion target that differs between the two
// environments compared, or is present in only one of them. Status is one
// of "only_a", "only_b", "different". Targets with an identical digest on
// both sides are omitted entirely -- this is meant to read like a diff, not
// a full state dump (`status` already covers that).
type diffEntry struct {
	Target     string `json:"target"`
	Kind       string `json:"kind"`
	Repository string `json:"repository"`
	Status     string `json:"status"`
	VersionA   string `json:"version_a,omitempty"`
	DigestA    string `json:"digest_a,omitempty"`
	VersionB   string `json:"version_b,omitempty"`
	DigestB    string `json:"digest_b,omitempty"`
}

// diffEnvironmentStates computes environmentDiff from two
// GetEnvironmentStateResponse values already fetched by the caller. Pure and
// side-effect free so it can be unit tested without a server.
func diffEnvironmentStates(envA, envB string, a, b *pb.GetEnvironmentStateResponse) environmentDiff {
	aByKey := indexStateEntries(a)
	bByKey := indexStateEntries(b)

	keys := make(map[string]struct{}, len(aByKey)+len(bByKey))
	for k := range aByKey {
		keys[k] = struct{}{}
	}
	for k := range bByKey {
		keys[k] = struct{}{}
	}
	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	entries := make([]diffEntry, 0, len(sortedKeys))
	for _, k := range sortedKeys {
		ea, inA := aByKey[k]
		eb, inB := bByKey[k]
		switch {
		case inA && !inB:
			entries = append(entries, diffEntry{
				Target:     k,
				Kind:       artifactKindLabel(ea.GetArtifact().GetKind()),
				Repository: ea.GetArtifact().GetRepository(),
				Status:     "only_a",
				VersionA:   ea.GetArtifact().GetVersion(),
				DigestA:    ea.GetArtifact().GetDigest(),
			})
		case inB && !inA:
			entries = append(entries, diffEntry{
				Target:     k,
				Kind:       artifactKindLabel(eb.GetArtifact().GetKind()),
				Repository: eb.GetArtifact().GetRepository(),
				Status:     "only_b",
				VersionB:   eb.GetArtifact().GetVersion(),
				DigestB:    eb.GetArtifact().GetDigest(),
			})
		default:
			if ea.GetArtifact().GetDigest() != eb.GetArtifact().GetDigest() {
				entries = append(entries, diffEntry{
					Target:     k,
					Kind:       artifactKindLabel(ea.GetArtifact().GetKind()),
					Repository: ea.GetArtifact().GetRepository(),
					Status:     "different",
					VersionA:   ea.GetArtifact().GetVersion(),
					DigestA:    ea.GetArtifact().GetDigest(),
					VersionB:   eb.GetArtifact().GetVersion(),
					DigestB:    eb.GetArtifact().GetDigest(),
				})
			}
		}
	}

	return environmentDiff{EnvironmentA: envA, EnvironmentB: envB, Entries: entries}
}

// indexStateEntries keys entries by promotion target: "image:<app_id>" or
// "chart:<chart_id>". GetEnvironmentStateResponse carries no owner_full_name
// (unlike repository.TargetKey server-side), so app/chart id is the best
// available stable identity for matching the same target across two
// environments.
func indexStateEntries(resp *pb.GetEnvironmentStateResponse) map[string]*pb.EnvironmentStateEntry {
	m := make(map[string]*pb.EnvironmentStateEntry, len(resp.GetEntries()))
	for _, e := range resp.GetEntries() {
		m[stateEntryTargetKey(e)] = e
	}
	return m
}

func stateEntryTargetKey(e *pb.EnvironmentStateEntry) string {
	p := e.GetPromotion()
	if p.GetAppId() != "" {
		return "image:" + p.GetAppId()
	}
	return "chart:" + p.GetChartId()
}

func artifactKindLabel(k pb.ArtifactKind) string {
	switch k {
	case pb.ArtifactKind_ARTIFACT_KIND_IMAGE:
		return "image"
	case pb.ArtifactKind_ARTIFACT_KIND_CHART:
		return "chart"
	default:
		return "unknown"
	}
}
