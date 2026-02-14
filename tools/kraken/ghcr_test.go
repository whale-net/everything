package kraken

import (
	"testing"
)

func TestGHCRPackageVersionHasTag(t *testing.T) {
	v := GHCRPackageVersion{
		VersionID: 123,
		Tags:      []string{"v1.0.0", "latest"},
	}

	if !v.HasTag("v1.0.0") {
		t.Error("expected HasTag to return true for v1.0.0")
	}
	if !v.HasTag("latest") {
		t.Error("expected HasTag to return true for latest")
	}
	if v.HasTag("v2.0.0") {
		t.Error("expected HasTag to return false for v2.0.0")
	}
}

func TestGHCRPackageVersionIsUntagged(t *testing.T) {
	untagged := GHCRPackageVersion{
		VersionID: 123,
		Tags:      nil,
	}
	if !untagged.IsUntagged() {
		t.Error("expected IsUntagged to return true for nil tags")
	}

	emptyTags := GHCRPackageVersion{
		VersionID: 123,
		Tags:      []string{},
	}
	if !emptyTags.IsUntagged() {
		t.Error("expected IsUntagged to return true for empty tags")
	}

	tagged := GHCRPackageVersion{
		VersionID: 123,
		Tags:      []string{"v1.0.0"},
	}
	if tagged.IsUntagged() {
		t.Error("expected IsUntagged to return false for tagged version")
	}
}

func TestGHCRPackageVersionString(t *testing.T) {
	v := GHCRPackageVersion{
		VersionID: 12345,
		Tags:      []string{"v1.0.0", "latest"},
	}

	str := v.String()
	if str != "GHCRPackageVersion(id=12345, tags=[v1.0.0, latest])" {
		t.Errorf("unexpected string representation: %s", str)
	}
}

func TestGHCRPackageVersionStringUntagged(t *testing.T) {
	v := GHCRPackageVersion{
		VersionID: 12345,
		Tags:      nil,
	}

	str := v.String()
	if str != "GHCRPackageVersion(id=12345, tags=[untagged])" {
		t.Errorf("unexpected string representation: %s", str)
	}
}

func TestNewGHCRClientNoToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, err := NewGHCRClient("owner", "")
	if err == nil {
		t.Error("expected error when no token provided")
	}
}

func TestNewGHCRClientWithToken(t *testing.T) {
	client, err := NewGHCRClient("owner", "ghp_test_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Owner != "owner" {
		t.Errorf("expected owner 'owner', got '%s'", client.Owner)
	}
	if client.Token != "ghp_test_token" {
		t.Errorf("expected token 'ghp_test_token', got '%s'", client.Token)
	}
	if client.BaseURL != "https://api.github.com" {
		t.Errorf("unexpected base URL: %s", client.BaseURL)
	}
}

func TestNewGHCRClientFromEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_env_token")
	client, err := NewGHCRClient("owner", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Token != "ghp_env_token" {
		t.Errorf("expected token from env, got '%s'", client.Token)
	}
}
