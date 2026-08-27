// Command faketoken is a test double for leaflab/scripts/authtoken: it
// implements the same "print a bearer token to stdout, everything else on
// stderr" contract push-config.sh's obtain_access_token() relies on
// (LEAFLAB_AUTHTOKEN_BIN is documented in push-config.sh as "path to a
// pre-built authtoken binary ... mainly for tests"), but returns a fixed
// token from FAKETOKEN_VALUE instead of performing a real OIDC device
// authorization grant. Used by pushconfig_test to exercise push-config.sh's
// authenticated-request path against a real, in-process gRPC server without
// a real Keycloak instance.
package main

import (
	"fmt"
	"os"
)

func main() {
	token := os.Getenv("FAKETOKEN_VALUE")
	if token == "" {
		fmt.Fprintln(os.Stderr, "faketoken: FAKETOKEN_VALUE is not set")
		os.Exit(1)
	}
	fmt.Println(token)
}
