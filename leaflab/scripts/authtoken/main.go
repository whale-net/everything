// Command authtoken is the human-facing wrapper around
// libs/go/grpcauth's OIDC device authorization grant (FR81, credential
// half) for LeafLab terminal callers -- push-config.sh in particular, which
// cannot call Go APIs directly and needs a bearer token string to hand to
// grpcurl.
//
// Two modes:
//
//	authtoken login   -- one-time interactive approval: prints a
//	                      verification URL/code to stderr, polls until
//	                      approved, and caches the resulting refresh token.
//	authtoken         -- non-interactive: loads the cached token (silently
//	                      refreshing it if near expiry) and prints the
//	                      access token to stdout. Fails with an actionable
//	                      message -- never blocks waiting for approval --
//	                      when no cached credential exists.
//
// Only the bare access token is ever written to stdout; everything else
// (prompts, errors) goes to stderr, so `TOKEN=$(authtoken)` in a script
// captures exactly the token (NFR13: never logging token material applies
// here too -- this command itself never logs the token, only forwards it).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "authtoken: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	interactive := false
	if len(args) > 0 {
		switch args[0] {
		case "login":
			interactive = true
		case "-h", "--help", "help":
			printUsage()
			return nil
		default:
			printUsage()
			return fmt.Errorf("unknown argument %q", args[0])
		}
	}

	config, err := configFromEnv()
	if err != nil {
		return err
	}

	token, err := grpcauth.DeviceFlowAccessToken(context.Background(), config, interactive)
	if err != nil {
		return err
	}

	fmt.Println(token)
	return nil
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage:
  authtoken login   One-time interactive device authorization approval.
  authtoken         Print a cached (silently refreshed) access token, or
                     fail with an actionable message if none is cached.

Environment:
  LEAFLAB_API_OIDC_ISSUER      Keycloak realm URL (required)
  LEAFLAB_DEVICE_FLOW_CLIENT_ID  Public Keycloak client id with the device
                                  authorization grant enabled (required) --
                                  see libs/go/grpcauth/KEYCLOAK.md
  LEAFLAB_DEVICE_FLOW_SCOPES     Comma-separated scopes (optional; "openid"
                                  is always included implicitly)
  LEAFLAB_DEVICE_FLOW_TOKEN_CACHE  Token cache file path (optional; defaults
                                    to <user config dir>/grpcauth/device-flow-token.json)`)
}

func configFromEnv() (grpcauth.DeviceFlowConfig, error) {
	issuer := os.Getenv("LEAFLAB_API_OIDC_ISSUER")
	if issuer == "" {
		return grpcauth.DeviceFlowConfig{}, fmt.Errorf("LEAFLAB_API_OIDC_ISSUER is required")
	}
	clientID := os.Getenv("LEAFLAB_DEVICE_FLOW_CLIENT_ID")
	if clientID == "" {
		return grpcauth.DeviceFlowConfig{}, fmt.Errorf("LEAFLAB_DEVICE_FLOW_CLIENT_ID is required")
	}

	var scopes []string
	if raw := os.Getenv("LEAFLAB_DEVICE_FLOW_SCOPES"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				scopes = append(scopes, s)
			}
		}
	}

	return grpcauth.DeviceFlowConfig{
		IssuerURL:      issuer,
		ClientID:       clientID,
		Scopes:         scopes,
		TokenCachePath: os.Getenv("LEAFLAB_DEVICE_FLOW_TOKEN_CACHE"),
	}, nil
}
