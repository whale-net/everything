package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FR5 tests (root plan #2080, task #2089): server-side env rendering,
// rendered_env wire presence semantics, and legible render failure.

func TestRenderEffectiveEnv(t *testing.T) {
	t.Run("empty template and no patches yields empty rendered env", func(t *testing.T) {
		rendered, err := renderEffectiveEnv(nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rendered == nil {
			t.Fatal("expected non-nil (authoritative) rendered env")
		}
		if len(rendered) != 0 {
			t.Errorf("expected empty rendered env, got %v", rendered)
		}
	})

	t.Run("template is the base layer", func(t *testing.T) {
		rendered, err := renderEffectiveEnv(map[string]string{"MOTD": "hello"}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rendered["MOTD"] != "hello" {
			t.Errorf("expected template value kept, got %v", rendered)
		}
	})

	t.Run("merge order: template then gc patches then sgc patches, later wins", func(t *testing.T) {
		template := map[string]string{"MOTD": "template", "SHARED": "template"}
		gcPatch := "SHARED=gc\nGC_ONLY=1\n# comment line\n\n"
		sgcPatch := "SHARED=sgc\nSGC_ONLY=2"
		rendered, err := renderEffectiveEnv(template, []string{gcPatch, sgcPatch})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rendered["MOTD"] != "template" {
			t.Errorf("template value should survive, got %q", rendered["MOTD"])
		}
		if rendered["SHARED"] != "sgc" {
			t.Errorf("expected sgc patch to win merge order, got %q", rendered["SHARED"])
		}
		if rendered["GC_ONLY"] != "1" || rendered["SGC_ONLY"] != "2" {
			t.Errorf("expected patch-only vars present, got %v", rendered)
		}
	})

	t.Run("invalid override line fails legibly", func(t *testing.T) {
		_, err := renderEffectiveEnv(map[string]string{}, []string{"NOT_A_KV_LINE"})
		if err == nil {
			t.Fatal("expected error for line without '='")
		}
		if !strings.Contains(err.Error(), "KEY=VALUE") {
			t.Errorf("expected legible error mentioning KEY=VALUE, got %v", err)
		}
	})

	t.Run("empty key fails legibly", func(t *testing.T) {
		_, err := renderEffectiveEnv(map[string]string{}, []string{"=value"})
		if err == nil {
			t.Fatal("expected error for empty key")
		}
	})
}

func TestBuildStartSessionCommandRenderedEnvWire(t *testing.T) {
	session := &manman.Session{SessionID: 7, SGCID: 100}
	sgc := &manman.ServerGameConfig{SGCID: 100, ServerID: 1}
	gc := &manman.GameConfig{ConfigID: 1}

	t.Run("nil rendered env is absent from the wire (host falls back)", func(t *testing.T) {
		cmd := buildStartSessionCommand(session, sgc, gc, false, nil, nil)
		if _, present := cmd["rendered_env"]; present {
			t.Errorf("expected rendered_env to be absent for nil map, got %v", cmd["rendered_env"])
		}
	})

	t.Run("empty rendered env is present and authoritative on the wire", func(t *testing.T) {
		cmd := buildStartSessionCommand(session, sgc, gc, false, nil, map[string]string{})
		v, present := cmd["rendered_env"]
		if !present {
			t.Fatal("expected rendered_env key to be present for empty map")
		}
		m, ok := v.(map[string]string)
		if !ok || len(m) != 0 {
			t.Errorf("expected empty map on the wire, got %v", v)
		}
	})

	t.Run("populated rendered env is published as-is", func(t *testing.T) {
		cmd := buildStartSessionCommand(session, sgc, gc, false, nil, map[string]string{"A": "1"})
		m, ok := cmd["rendered_env"].(map[string]string)
		if !ok || m["A"] != "1" {
			t.Errorf("expected rendered_env {A:1} on the wire, got %v", cmd["rendered_env"])
		}
	})
}

// startSessionEnvHarness builds a SessionHandler wired with mocks that can
// serve env_vars strategies and patches for the FR5 rendering path.
func startSessionEnvHarness(t *testing.T) (*SessionHandler, *MockSessionRepo, *MockPatchRepo) {
	t.Helper()
	sessionRepo := &MockSessionRepo{}
	strategyRepo := &MockStrategyRepo{}
	patchRepo := &MockPatchRepo{}

	repo := &repository.Repository{
		Sessions:                sessionRepo,
		ServerGameConfigs:       &MockSGCRepo{},
		GameConfigs:             &MockGCRepo{},
		ConfigurationStrategies: strategyRepo,
		ConfigurationPatches:    patchRepo,
		ServerPorts:             &MockServerPortRepo{},
		GameConfigVolumes:       &MockGameConfigVolumeRepo{},
	}

	h := &SessionHandler{
		repo:        repo,
		sessionRepo: sessionRepo,
		sgcRepo:     repo.ServerGameConfigs,
		gcRepo:      repo.GameConfigs,
		publisher:   nil, // Publisher is optional in StartSession
	}
	return h, sessionRepo, patchRepo
}

func TestStartSession_EnvRenderFailureFailsLegibly(t *testing.T) {
	h, sessionRepo, patchRepo := startSessionEnvHarness(t)

	// An env_vars strategy exists with a patch whose content cannot render
	// (line without '='), simulating operator error in override content.
	h.repo.ConfigurationStrategies = &MockStrategyRepo{
		strategies: []*manman.ConfigurationStrategy{
			{StrategyID: 5, GameID: 1, StrategyType: manman.StrategyTypeEnvVars},
		},
	}
	patchRepo.patches = []*manman.ConfigurationPatch{
		{
			PatchID:       1,
			StrategyID:    5,
			PatchLevel:    manman.PatchLevelServerGameConfig,
			EntityID:      100,
			PatchContent:  strPtr("BROKEN LINE NO EQUALS"),
			PatchOrder:    0,
		},
	}

	req := &pb.StartSessionRequest{ServerGameConfigId: 100}
	resp, err := h.StartSession(context.Background(), req)
	if err == nil {
		t.Fatalf("expected legible failure, got resp %+v", resp)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Errorf("expected Internal status, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to render session environment") {
		t.Errorf("expected legible render-failure message, got %v", err)
	}

	// The session must have been marked crashed synchronously (same failure
	// path as other start errors, e.g. port allocation).
	crashed := false
	for _, s := range sessionRepo.updated {
		if s.Status == manman.SessionStatusCrashed {
			crashed = true
		}
	}
	if !crashed {
		t.Errorf("expected session to be marked crashed, updated=%v", sessionRepo.updated)
	}
}

func TestStartSession_RenderedEnvHappyPathConsultsBothLevels(t *testing.T) {
	h, _, patchRepo := startSessionEnvHarness(t)

	h.repo.ConfigurationStrategies = &MockStrategyRepo{
		strategies: []*manman.ConfigurationStrategy{
			{StrategyID: 5, GameID: 1, StrategyType: manman.StrategyTypeEnvVars},
		},
	}
	h.repo.GameConfigs = &MockGCRepo{gc: &manman.GameConfig{
		ConfigID:    1,
		GameID:      1,
		EnvTemplate: manman.JSONB{"MOTD": "hello"},
	}}
	patchRepo.patches = []*manman.ConfigurationPatch{
		{
			PatchID:       1,
			StrategyID:    5,
			PatchLevel:    manman.PatchLevelGameConfig,
			EntityID:      1,
			PatchContent:  strPtr("GC_ONLY=1"),
			PatchOrder:    0,
		},
		{
			PatchID:       2,
			StrategyID:    5,
			PatchLevel:    manman.PatchLevelServerGameConfig,
			EntityID:      100,
			PatchContent:  strPtr("SGC_ONLY=2"),
			PatchOrder:    0,
		},
	}

	req := &pb.StartSessionRequest{ServerGameConfigId: 100}
	if _, err := h.StartSession(context.Background(), req); err != nil {
		t.Fatalf("expected start to succeed with renderable overrides, got %v", err)
	}

	// Both deployment levels must have been consulted for overrides.
	joined := strings.Join(patchRepo.listCalls, ";")
	if !strings.Contains(joined, "level=game_config") || !strings.Contains(joined, "level=server_game_config") {
		t.Errorf("expected both patch levels consulted, got %v", patchRepo.listCalls)
	}
}
