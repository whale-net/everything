package cmd

import (
	"errors"
	"testing"
)

func TestIsSameMinorLine(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected bool
	}{
		{"v0.1.0", "v0.1.5", true},
		{"0.1.0", "0.1.5", true},
		{"v1.2.3", "v1.2.4", true},
		{"v1.2.3", "v1.3.0", false},
		{"v1.2.3", "v2.2.3", false},
		{"invalid", "v1.0.0", false},
		{"v1.0.0", "invalid", false},
		{"", "", false},
	}

	for _, tt := range tests {
		got := IsSameMinorLine(tt.v1, tt.v2)
		if got != tt.expected {
			t.Errorf("IsSameMinorLine(%q, %q) = %v; want %v", tt.v1, tt.v2, got, tt.expected)
		}
	}
}

func TestExtractVersionFromTag(t *testing.T) {
	tests := []struct {
		tag      string
		prefixes []string
		expected string
	}{
		{"demo-hello-go.v1.0.0", []string{"demo-hello-go."}, "v1.0.0"},
		{"helm-demo-hello.v0.1.0", []string{"demo-hello.", "helm-demo-hello."}, "v0.1.0"},
		{"demo-hello.v0.1.0", []string{"demo-hello.", "helm-demo-hello."}, "v0.1.0"},
		{"no-match-tag", []string{"demo-hello."}, "no-match-tag"},
		{"v1.0.0", nil, "v1.0.0"},
	}

	for _, tt := range tests {
		got := ExtractVersionFromTag(tt.tag, tt.prefixes...)
		if got != tt.expected {
			t.Errorf("ExtractVersionFromTag(%q, %v) = %q; want %q", tt.tag, tt.prefixes, got, tt.expected)
		}
	}
}

func TestGetPreviousGitTag(t *testing.T) {
	fakeGit := newFakeGit(
		fakeGitCall{
			argsContain: []string{"tag", "--sort=-version:refname", "--list"},
			output:      "demo-hello-go.v1.0.1\ndemo-hello-go.v1.0.0\ndemo-hello-go.v0.9.0\nother-app.v1.0.0",
		},
	)

	// currentTag is demo-hello-go.v1.0.1 -> previous should be demo-hello-go.v1.0.0
	prev := GetPreviousGitTag(fakeGit, "demo-hello-go.v1.0.1", []string{"demo-hello-go.*"}, []string{"demo-hello-go."})
	if prev != "demo-hello-go.v1.0.0" {
		t.Errorf("expected 'demo-hello-go.v1.0.0', got %q", prev)
	}

	// No git runner
	if got := GetPreviousGitTag(nil, "tag", []string{"*"}, []string{""}); got != "" {
		t.Errorf("expected empty string for nil GitRunner, got %q", got)
	}
}

func TestEvaluateNoOpDecision(t *testing.T) {
	// Case 1: Same minor line, content matches -> OutcomeNoOpRebuild
	res1 := EvaluateNoOpDecision("v0.1.1", "app.v0.1.1", "v0.1.0", "app.v0.1.0", true)
	if res1.Outcome != OutcomeNoOpRebuild {
		t.Errorf("expected OutcomeNoOpRebuild, got %v", res1.Outcome)
	}
	if !res1.DigestUnchanged {
		t.Errorf("expected DigestUnchanged true, got false")
	}
	if res1.EffectiveVersion != "v0.1.0" {
		t.Errorf("expected EffectiveVersion 'v0.1.0', got %q", res1.EffectiveVersion)
	}
	if res1.EffectiveTag != "app.v0.1.0" {
		t.Errorf("expected EffectiveTag 'app.v0.1.0', got %q", res1.EffectiveTag)
	}
	if res1.Published {
		t.Errorf("expected Published false, got true")
	}

	// Case 2: Minor bump, content matches -> OutcomeNewBaseline
	res2 := EvaluateNoOpDecision("v0.2.0", "app.v0.2.0", "v0.1.0", "app.v0.1.0", true)
	if res2.Outcome != OutcomeNewBaseline {
		t.Errorf("expected OutcomeNewBaseline, got %v", res2.Outcome)
	}
	if res2.DigestUnchanged {
		t.Errorf("expected DigestUnchanged false, got true")
	}
	if res2.EffectiveVersion != "v0.2.0" {
		t.Errorf("expected EffectiveVersion 'v0.2.0', got %q", res2.EffectiveVersion)
	}
	if res2.EffectiveTag != "app.v0.2.0" {
		t.Errorf("expected EffectiveTag 'app.v0.2.0', got %q", res2.EffectiveTag)
	}
	if !res2.Published {
		t.Errorf("expected Published true, got false")
	}

	// Case 3: Major bump, content matches -> OutcomeNewBaseline
	res3 := EvaluateNoOpDecision("v1.0.0", "app.v1.0.0", "v0.9.0", "app.v0.9.0", true)
	if res3.Outcome != OutcomeNewBaseline {
		t.Errorf("expected OutcomeNewBaseline, got %v", res3.Outcome)
	}
	if res3.DigestUnchanged {
		t.Errorf("expected DigestUnchanged false, got true")
	}
	if res3.EffectiveVersion != "v1.0.0" {
		t.Errorf("expected EffectiveVersion 'v1.0.0', got %q", res3.EffectiveVersion)
	}

	// Case 4: Content changed -> OutcomeProceed
	res4 := EvaluateNoOpDecision("v0.1.1", "app.v0.1.1", "v0.1.0", "app.v0.1.0", false)
	if res4.Outcome != OutcomeProceed {
		t.Errorf("expected OutcomeProceed, got %v", res4.Outcome)
	}
	if res4.DigestUnchanged {
		t.Errorf("expected DigestUnchanged false, got true")
	}
	if res4.EffectiveVersion != "v0.1.1" {
		t.Errorf("expected EffectiveVersion 'v0.1.1', got %q", res4.EffectiveVersion)
	}

	// Case 5: No previous version -> OutcomeProceed
	res5 := EvaluateNoOpDecision("v0.1.0", "app.v0.1.0", "", "", false)
	if res5.Outcome != OutcomeProceed {
		t.Errorf("expected OutcomeProceed, got %v", res5.Outcome)
	}
	if res5.DigestUnchanged {
		t.Errorf("expected DigestUnchanged false, got true")
	}
}

func TestResolveCandidateCollision(t *testing.T) {
	// 1. Candidate does not exist in store
	res1, err := ResolveCandidateCollision("v0.1.0", "app.", true,
		func(v string) (bool, bool, error) {
			return false, false, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res1.DigestUnchanged || res1.CollisionFound || res1.Advanced {
		t.Errorf("expected no collision/advance, got %+v", res1)
	}
	if res1.Version != "v0.1.0" {
		t.Errorf("expected version 'v0.1.0', got %q", res1.Version)
	}

	// 2. Candidate exists with identical content (already published)
	res2, err := ResolveCandidateCollision("v0.1.0", "app.", true,
		func(v string) (bool, bool, error) {
			return true, true, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res2.DigestUnchanged {
		t.Errorf("expected DigestUnchanged true for already published, got false")
	}
	if res2.Version != "v0.1.0" {
		t.Errorf("expected version 'v0.1.0', got %q", res2.Version)
	}

	// 3. Candidate exists with different content, auto-advance enabled
	var repackaged []string
	res3, err := ResolveCandidateCollision("v0.1.0", "app.", true,
		func(v string) (bool, bool, error) {
			if v == "v0.1.0" || v == "v0.1.1" {
				return true, false, nil // different content at v0.1.0 and v0.1.1
			}
			return false, false, nil // available at v0.1.2
		},
		func(newVer string) error {
			repackaged = append(repackaged, newVer)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res3.CollisionFound || !res3.Advanced {
		t.Errorf("expected collision found and advanced, got %+v", res3)
	}
	if res3.Version != "v0.1.2" {
		t.Errorf("expected advanced to 'v0.1.2', got %q", res3.Version)
	}
	if res3.Tag != "app.v0.1.2" {
		t.Errorf("expected tag 'app.v0.1.2', got %q", res3.Tag)
	}
	if len(repackaged) != 1 || repackaged[0] != "v0.1.2" {
		t.Errorf("expected onAdvanced called for 'v0.1.2', got %v", repackaged)
	}

	// 4. Candidate exists with different content, auto-advance disabled
	res4, err := ResolveCandidateCollision("v0.1.0", "app.", false,
		func(v string) (bool, bool, error) {
			return true, false, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res4.CollisionFound || res4.Advanced {
		t.Errorf("expected CollisionFound=true and Advanced=false, got %+v", res4)
	}
	if res4.Version != "v0.1.0" {
		t.Errorf("expected version 'v0.1.0', got %q", res4.Version)
	}

	// 5. OnAdvanced error propagated
	_, err = ResolveCandidateCollision("v0.1.0", "app.", true,
		func(v string) (bool, bool, error) {
			if v == "v0.1.0" {
				return true, false, nil
			}
			return false, false, nil
		},
		func(newVer string) error {
			return errors.New("repackage error")
		},
	)
	if err == nil || err.Error() != "repackage error" {
		t.Errorf("expected 'repackage error', got %v", err)
	}
}
