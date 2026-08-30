package main

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// TestUpsertLeafLabUser_NilOrMissingSub_ErrorsWithoutTouchingDB guards the
// upsertLeafLabUser guard clause: a nil UserInfo or one with an empty Sub
// must error out before ever reaching app.pool, so this needs no real
// database — a nil pool would panic if the guard clause were ever removed
// or reordered, which is exactly the regression this test would catch.
func TestUpsertLeafLabUser_NilOrMissingSub_ErrorsWithoutTouchingDB(t *testing.T) {
	app := &App{} // pool is deliberately nil

	cases := []struct {
		name string
		user *htmxauth.UserInfo
	}{
		{name: "nil user", user: nil},
		{name: "empty sub", user: &htmxauth.UserInfo{Sub: "", PreferredUsername: "someone"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := app.upsertLeafLabUser(context.Background(), tc.user)
			if err == nil {
				t.Fatal("upsertLeafLabUser() error = nil, want an error naming the missing OIDC sub")
			}
			if !strings.Contains(err.Error(), "sub") {
				t.Errorf("upsertLeafLabUser() error = %q, want it to name the missing sub", err.Error())
			}
		})
	}
}
