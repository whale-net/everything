package main

import "testing"

func TestImageTag(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"ghcr.io/whale-net/manmanv2-host-manager:v1.4.0", "v1.4.0"},
		{"ghcr.io/whale-net/manmanv2-host-manager:latest", "latest"},
		{"registry.example.com:5000/manmanv2-host-manager:v1.4.0", "v1.4.0"},
		{"ghcr.io/whale-net/manmanv2-host-manager", ""},
		{"registry.example.com:5000/manmanv2-host-manager", ""},
	}
	for _, tc := range cases {
		if got := imageTag(tc.ref); got != tc.want {
			t.Errorf("imageTag(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}
