package params

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeParams(t *testing.T) {
	definitions := []*Parameter{
		{Key: "max_players", Type: "int", Required: true, DefaultValue: "10"},
		{Key: "difficulty", Type: "string", Required: false, DefaultValue: "normal"},
		{Key: "pvp", Type: "bool", Required: false, DefaultValue: "false"},
	}

	t.Run("uses defaults when no overrides", func(t *testing.T) {
		result := MergeParams(definitions)
		assert.Equal(t, "10", result["max_players"])
		assert.Equal(t, "normal", result["difficulty"])
		assert.Equal(t, "false", result["pvp"])
	})

	t.Run("ServerGameConfig overrides defaults", func(t *testing.T) {
		sgcOverrides := map[string]string{
			"max_players": "20",
			"difficulty":  "hard",
		}
		result := MergeParams(definitions, sgcOverrides)
		assert.Equal(t, "20", result["max_players"])
		assert.Equal(t, "hard", result["difficulty"])
		assert.Equal(t, "false", result["pvp"]) // Still default
	})

	t.Run("Session overrides all", func(t *testing.T) {
		sgcOverrides := map[string]string{
			"max_players": "20",
		}
		sessionOverrides := map[string]string{
			"max_players": "30",
			"pvp":         "true",
		}
		result := MergeParams(definitions, sgcOverrides, sessionOverrides)
		assert.Equal(t, "30", result["max_players"]) // Session wins
		assert.Equal(t, "normal", result["difficulty"]) // Still default
		assert.Equal(t, "true", result["pvp"]) // Session override
	})
}

func TestValidateParams(t *testing.T) {
	definitions := []*Parameter{
		{Key: "max_players", Type: "int", Required: true},
		{Key: "server_name", Type: "string", Required: true},
		{Key: "difficulty", Type: "string", Required: false},
		{Key: "pvp", Type: "bool", Required: false},
	}

	t.Run("valid parameters pass", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
			"server_name": "MyServer",
			"difficulty":  "hard",
			"pvp":         "true",
		}
		err := ValidateParams(definitions, values)
		assert.NoError(t, err)
	})

	t.Run("missing required parameter fails", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
			// server_name is missing
		}
		err := ValidateParams(definitions, values)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "server_name")
		assert.Contains(t, err.Error(), "required")
	})

	t.Run("invalid int type fails", func(t *testing.T) {
		values := map[string]string{
			"max_players": "not_a_number",
			"server_name": "MyServer",
		}
		err := ValidateParams(definitions, values)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "max_players")
		assert.Contains(t, err.Error(), "integer")
	})

	t.Run("invalid bool type fails", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
			"server_name": "MyServer",
			"pvp":         "maybe",
		}
		err := ValidateParams(definitions, values)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "pvp")
		assert.Contains(t, err.Error(), "boolean")
	})

	t.Run("unknown parameters are allowed", func(t *testing.T) {
		values := map[string]string{
			"max_players":  "20",
			"server_name":  "MyServer",
			"unknown_param": "value",
		}
		err := ValidateParams(definitions, values)
		assert.NoError(t, err) // Unknown params don't cause errors
	})
}

func TestRenderTemplate(t *testing.T) {
	params := map[string]string{
		"server_name": "MyAwesomeServer",
		"max_players": "20",
		"world_seed":  "12345",
	}

	t.Run("renders single parameter", func(t *testing.T) {
		template := "--server-name={{server_name}}"
		result := RenderTemplate(template, params)
		assert.Equal(t, "--server-name=MyAwesomeServer", result)
	})

	t.Run("renders multiple parameters", func(t *testing.T) {
		template := "--server-name={{server_name}} --max-players={{max_players}} --seed={{world_seed}}"
		result := RenderTemplate(template, params)
		assert.Equal(t, "--server-name=MyAwesomeServer --max-players=20 --seed=12345", result)
	})

	t.Run("leaves unknown parameters unchanged", func(t *testing.T) {
		template := "--server-name={{server_name}} --password={{server_password}}"
		result := RenderTemplate(template, params)
		assert.Equal(t, "--server-name=MyAwesomeServer --password={{server_password}}", result)
	})

	t.Run("handles no parameters", func(t *testing.T) {
		template := "--fixed-arg value"
		result := RenderTemplate(template, params)
		assert.Equal(t, "--fixed-arg value", result)
	})
}

func TestConvertToType(t *testing.T) {
	t.Run("string type", func(t *testing.T) {
		val, err := ConvertToType("hello", "string")
		assert.NoError(t, err)
		assert.Equal(t, "hello", val)
	})

	t.Run("int type valid", func(t *testing.T) {
		val, err := ConvertToType("42", "int")
		assert.NoError(t, err)
		assert.Equal(t, int64(42), val)
	})

	t.Run("int type invalid", func(t *testing.T) {
		_, err := ConvertToType("not_a_number", "int")
		assert.Error(t, err)
	})

	t.Run("bool type valid", func(t *testing.T) {
		val, err := ConvertToType("true", "bool")
		assert.NoError(t, err)
		assert.Equal(t, true, val)
	})

	t.Run("bool type invalid", func(t *testing.T) {
		_, err := ConvertToType("maybe", "bool")
		assert.Error(t, err)
	})

	t.Run("secret type", func(t *testing.T) {
		val, err := ConvertToType("my_secret_password", "secret")
		assert.NoError(t, err)
		assert.Equal(t, "my_secret_password", val)
	})
}

func TestGetMissingRequired(t *testing.T) {
	definitions := []*Parameter{
		{Key: "max_players", Type: "int", Required: true},
		{Key: "server_name", Type: "string", Required: true},
		{Key: "difficulty", Type: "string", Required: false},
	}

	t.Run("no missing required", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
			"server_name": "MyServer",
		}
		missing := GetMissingRequired(definitions, values)
		assert.Empty(t, missing)
	})

	t.Run("one missing required", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
		}
		missing := GetMissingRequired(definitions, values)
		assert.Len(t, missing, 1)
		assert.Contains(t, missing, "server_name")
	})

	t.Run("multiple missing required", func(t *testing.T) {
		values := map[string]string{}
		missing := GetMissingRequired(definitions, values)
		assert.Len(t, missing, 2)
		assert.Contains(t, missing, "max_players")
		assert.Contains(t, missing, "server_name")
	})
}

func TestGetUnknownParams(t *testing.T) {
	definitions := []*Parameter{
		{Key: "max_players", Type: "int"},
		{Key: "server_name", Type: "string"},
	}

	t.Run("no unknown params", func(t *testing.T) {
		values := map[string]string{
			"max_players": "20",
			"server_name": "MyServer",
		}
		unknown := GetUnknownParams(definitions, values)
		assert.Empty(t, unknown)
	})

	t.Run("one unknown param", func(t *testing.T) {
		values := map[string]string{
			"max_players":   "20",
			"unknown_param": "value",
		}
		unknown := GetUnknownParams(definitions, values)
		assert.Len(t, unknown, 1)
		assert.Contains(t, unknown, "unknown_param")
	})

	t.Run("multiple unknown params", func(t *testing.T) {
		values := map[string]string{
			"unknown1": "value1",
			"unknown2": "value2",
		}
		unknown := GetUnknownParams(definitions, values)
		assert.Len(t, unknown, 2)
		assert.Contains(t, unknown, "unknown1")
		assert.Contains(t, unknown, "unknown2")
	})
}
