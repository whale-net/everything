package migrate

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	// Clear env vars that might interfere
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSL_MODE"} {
		os.Unsetenv(key)
	}

	cfg := DefaultConfig()
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5432, cfg.Port)
	assert.Equal(t, "postgres", cfg.User)
	assert.Equal(t, "", cfg.Password)
	assert.Equal(t, "postgres", cfg.Database)
	assert.Equal(t, "disable", cfg.SSLMode)
	assert.Equal(t, 25, cfg.MaxOpenConns)
	assert.Equal(t, 5, cfg.MaxIdleConns)
	assert.Equal(t, 5*time.Minute, cfg.ConnMaxLifetime)
}

func TestDefaultConfig_WithEnvVars(t *testing.T) {
	os.Setenv("DB_HOST", "db.example.com")
	os.Setenv("DB_PORT", "5433")
	os.Setenv("DB_USER", "admin")
	os.Setenv("DB_PASSWORD", "secret123")
	os.Setenv("DB_NAME", "mydb")
	os.Setenv("DB_SSL_MODE", "require")
	defer func() {
		for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSL_MODE"} {
			os.Unsetenv(key)
		}
	}()

	cfg := DefaultConfig()
	assert.Equal(t, "db.example.com", cfg.Host)
	assert.Equal(t, 5433, cfg.Port)
	assert.Equal(t, "admin", cfg.User)
	assert.Equal(t, "secret123", cfg.Password)
	assert.Equal(t, "mydb", cfg.Database)
	assert.Equal(t, "require", cfg.SSLMode)
}

func TestGetEnv(t *testing.T) {
	t.Run("returns env var when set", func(t *testing.T) {
		os.Setenv("TEST_GET_ENV_KEY", "myvalue")
		defer os.Unsetenv("TEST_GET_ENV_KEY")

		assert.Equal(t, "myvalue", getEnv("TEST_GET_ENV_KEY", "default"))
	})

	t.Run("returns default when not set", func(t *testing.T) {
		os.Unsetenv("TEST_GET_ENV_MISSING")
		assert.Equal(t, "default", getEnv("TEST_GET_ENV_MISSING", "default"))
	})

	t.Run("returns env var even when empty string", func(t *testing.T) {
		// Empty string means not set for this implementation
		os.Setenv("TEST_GET_ENV_EMPTY", "")
		defer os.Unsetenv("TEST_GET_ENV_EMPTY")

		assert.Equal(t, "default", getEnv("TEST_GET_ENV_EMPTY", "default"))
	})
}

func TestGetEnvInt(t *testing.T) {
	t.Run("returns parsed int when set", func(t *testing.T) {
		os.Setenv("TEST_GET_ENV_INT", "9999")
		defer os.Unsetenv("TEST_GET_ENV_INT")

		assert.Equal(t, 9999, getEnvInt("TEST_GET_ENV_INT", 42))
	})

	t.Run("returns default when not set", func(t *testing.T) {
		os.Unsetenv("TEST_GET_ENV_INT_MISSING")
		assert.Equal(t, 42, getEnvInt("TEST_GET_ENV_INT_MISSING", 42))
	})

	t.Run("returns default when value is not a number", func(t *testing.T) {
		os.Setenv("TEST_GET_ENV_INT_BAD", "not_a_number")
		defer os.Unsetenv("TEST_GET_ENV_INT_BAD")

		assert.Equal(t, 42, getEnvInt("TEST_GET_ENV_INT_BAD", 42))
	})

	t.Run("handles zero value", func(t *testing.T) {
		os.Setenv("TEST_GET_ENV_INT_ZERO", "0")
		defer os.Unsetenv("TEST_GET_ENV_INT_ZERO")

		assert.Equal(t, 0, getEnvInt("TEST_GET_ENV_INT_ZERO", 42))
	})

	t.Run("handles negative value", func(t *testing.T) {
		os.Setenv("TEST_GET_ENV_INT_NEG", "-5")
		defer os.Unsetenv("TEST_GET_ENV_INT_NEG")

		assert.Equal(t, -5, getEnvInt("TEST_GET_ENV_INT_NEG", 42))
	})
}

func TestAbs(t *testing.T) {
	assert.Equal(t, 5, abs(5))
	assert.Equal(t, 5, abs(-5))
	assert.Equal(t, 0, abs(0))
	assert.Equal(t, 1, abs(1))
	assert.Equal(t, 1, abs(-1))
	assert.Equal(t, 100, abs(100))
	assert.Equal(t, 100, abs(-100))
}

func TestTruncate(t *testing.T) {
	t.Run("returns string unchanged when shorter than max", func(t *testing.T) {
		assert.Equal(t, "hello", truncate("hello", 10))
	})

	t.Run("returns string unchanged when equal to max", func(t *testing.T) {
		assert.Equal(t, "hello", truncate("hello", 5))
	})

	t.Run("truncates with ellipsis when longer than max", func(t *testing.T) {
		result := truncate("hello world this is long", 10)
		assert.Equal(t, "hello w...", result)
		assert.Len(t, result, 10)
	})

	t.Run("handles empty string", func(t *testing.T) {
		assert.Equal(t, "", truncate("", 10))
	})

	t.Run("handles maxLen of 3 (minimum for ellipsis)", func(t *testing.T) {
		assert.Equal(t, "...", truncate("hello", 3))
	})

	t.Run("truncates when one character over limit", func(t *testing.T) {
		assert.Equal(t, "he...", truncate("hello!", 5))
	})
}

func TestPrintHistory(t *testing.T) {
	t.Run("prints no history message when empty", func(t *testing.T) {
		output := captureStdout(func() {
			printHistory([]HistoryEntry{})
		})
		assert.Contains(t, output, "No migration history found")
	})

	t.Run("prints formatted table with entries", func(t *testing.T) {
		now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		durationMs := 150
		entries := []HistoryEntry{
			{
				HistoryID:    1,
				Version:      1,
				Direction:    "up",
				Status:       "success",
				StartedAt:    now,
				CompletedAt:  &now,
				DurationMs:   &durationMs,
				ErrorMessage: nil,
				AppliedBy:    "migration-binary",
				CreatedAt:    now,
			},
		}

		output := captureStdout(func() {
			printHistory(entries)
		})
		assert.Contains(t, output, "Migration History:")
		assert.Contains(t, output, "Version")
		assert.Contains(t, output, "Direction")
		assert.Contains(t, output, "Status")
		assert.Contains(t, output, "success")
		assert.Contains(t, output, "150ms")
		assert.Contains(t, output, "10:30:45")
	})

	t.Run("prints dash for nil duration", func(t *testing.T) {
		now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		entries := []HistoryEntry{
			{
				HistoryID:  2,
				Version:    2,
				Direction:  "up",
				Status:     "started",
				StartedAt:  now,
				DurationMs: nil,
				AppliedBy:  "migration-binary",
				CreatedAt:  now,
			},
		}

		output := captureStdout(func() {
			printHistory(entries)
		})
		assert.Contains(t, output, "-")
		assert.Contains(t, output, "started")
	})

	t.Run("prints truncated error message", func(t *testing.T) {
		now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		durationMs := 50
		longError := "this is a very long error message that should be truncated to fit in the table"
		entries := []HistoryEntry{
			{
				HistoryID:    3,
				Version:      3,
				Direction:    "up",
				Status:       "failed",
				StartedAt:    now,
				CompletedAt:  &now,
				DurationMs:   &durationMs,
				ErrorMessage: &longError,
				AppliedBy:    "migration-binary",
				CreatedAt:    now,
			},
		}

		output := captureStdout(func() {
			printHistory(entries)
		})
		assert.Contains(t, output, "failed")
		assert.Contains(t, output, "...")
	})

	t.Run("handles empty error message", func(t *testing.T) {
		now := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
		durationMs := 50
		emptyErr := ""
		entries := []HistoryEntry{
			{
				HistoryID:    4,
				Version:      4,
				Direction:    "up",
				Status:       "failed",
				StartedAt:    now,
				CompletedAt:  &now,
				DurationMs:   &durationMs,
				ErrorMessage: &emptyErr,
				AppliedBy:    "migration-binary",
				CreatedAt:    now,
			},
		}

		// Should not panic
		output := captureStdout(func() {
			printHistory(entries)
		})
		assert.Contains(t, output, "failed")
	})
}

// captureStdout captures stdout output from a function call.
func captureStdout(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}
