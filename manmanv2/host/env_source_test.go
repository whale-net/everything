package main

import (
	"testing"

	"github.com/whale-net/everything/manmanv2/host/rmq"
)

// FR5 presence semantics (root plan #2080):
//   - nil rendered_env        -> absent: fall back to env_template
//   - empty rendered_env      -> present and authoritative: env is empty
//   - populated rendered_env  -> used as-is, template ignored

func TestResolveSessionEnv_FallsBackToTemplateWhenAbsent(t *testing.T) {
	cmd := &rmq.StartSessionCommand{
		GameConfig: rmq.GameConfigMessage{
			EnvTemplate: map[string]string{"MOTD": "hello", "MAX_PLAYERS": "20"},
		},
	}
	env, source := resolveSessionEnv(cmd)
	if source != "env_template_fallback" {
		t.Errorf("expected env_template_fallback source, got %q", source)
	}
	if len(env) != 2 || env["MOTD"] != "hello" || env["MAX_PLAYERS"] != "20" {
		t.Errorf("expected template env to be used, got %v", env)
	}
}

func TestResolveSessionEnv_EmptyRenderedEnvIsAuthoritative(t *testing.T) {
	cmd := &rmq.StartSessionCommand{
		GameConfig: rmq.GameConfigMessage{
			EnvTemplate: map[string]string{"MOTD": "hello"},
		},
		RenderedEnv: map[string]string{},
	}
	env, source := resolveSessionEnv(cmd)
	if source != "rendered_env" {
		t.Errorf("expected rendered_env source, got %q", source)
	}
	if len(env) != 0 {
		t.Errorf("expected empty rendered env to be honored (authoritative-empty), got %v", env)
	}
}

func TestResolveSessionEnv_PopulatedRenderedEnvUsedAsIs(t *testing.T) {
	cmd := &rmq.StartSessionCommand{
		GameConfig: rmq.GameConfigMessage{
			EnvTemplate: map[string]string{"MOTD": "hello", "OVERRIDE_ME": "template"},
		},
		RenderedEnv: map[string]string{"MOTD": "hello", "OVERRIDE_ME": "override", "EXTRA": "added"},
	}
	env, source := resolveSessionEnv(cmd)
	if source != "rendered_env" {
		t.Errorf("expected rendered_env source, got %q", source)
	}
	if env["OVERRIDE_ME"] != "override" {
		t.Errorf("expected override to win, got %q", env["OVERRIDE_ME"])
	}
	if _, ok := env["EXTRA"]; !ok {
		t.Errorf("expected added override var to be present, env %v", env)
	}
}
