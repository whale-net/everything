package cmd

import "testing"

func TestIsTransientBazelError(t *testing.T) {
	cases := []struct {
		name string
		err  string
		want bool
	}{
		{
			name: "lost inputs (run 32689827812/job/97321529487)",
			err:  "exit status 34\nERROR: /home/runner/work/everything/everything/friendly_computing_machine/BUILD.bazel:31:12 Writing: bazel-out/.../bot_image_base_full_runfiles.tar failed: lost inputs with digests: [...]",
			want: true,
		},
		{name: "connection reset", err: "rpc error: read tcp: connection reset by peer", want: true},
		{name: "i/o timeout", err: "dial tcp: i/o timeout", want: true},
		{name: "unexpected eof", err: "unexpected EOF", want: true},
		{name: "genuine compile error", err: "ERROR: //foo:bar: undeclared name: baz", want: false},
		{name: "test failure", err: "FAIL: //foo:bar_test (see test.log)", want: false},
		{name: "empty", err: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientBazelError(tc.err); got != tc.want {
				t.Errorf("isTransientBazelError(%q) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
