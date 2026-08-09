package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

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
				IdempotencyKey: idempotencyKeyFlag,
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Promotion.Promote(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Target environment key, e.g. prod")
	c.Flags().StringVar(&reason, "reason", "", "Required above dev rank; recorded in the audit log")
	c.Flags().BoolVar(&allowOverride, "allow-override", false, "Acknowledge promoting a VIA_CHART artifact directly")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the resulting state without writing")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. Client-generated, makes retries safe")
	_ = c.MarkFlagRequired("env")
	_ = c.MarkFlagRequired("idempotency-key")
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
					IdempotencyKey: idempotencyKeyFlag,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Target environment key, e.g. prod")
	c.Flags().StringVar(&reason, "reason", "", "Required above dev rank; recorded in the audit log")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the resulting state without writing")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. Client-generated, makes retries safe")
	_ = c.MarkFlagRequired("env")
	_ = c.MarkFlagRequired("idempotency-key")
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
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&domain, "domain", "", "Filter by domain")
	c.Flags().StringVar(&at, "at", "", "RFC3339 timestamp; omit for current state")
	return c
}

func newHistoryCmd() *cobra.Command {
	var env string
	c := &cobra.Command{
		Use:   "history <domain-name>",
		Short: "Promotion history for a target",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Promotion.ListPromotions(cmd.Context(), &pb.ListPromotionsRequest{
					OwnerFullName:  args[0],
					EnvironmentKey: env,
					IncludeHistory: true,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&env, "env", "", "Filter by environment key")
	return c
}

func newDiffCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "diff <env-a> <env-b>",
		Short: "Diff what is running between two environments",
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
				// AR-1: no diffing logic yet, print both states side by side
				// via two calls. Real diffing is server-side once
				// GetEnvironmentState is implemented (AR-3).
				if err := printResponse(stateA); err != nil {
					return err
				}
				return printResponse(stateB)
			})
		},
	}
	return c
}
