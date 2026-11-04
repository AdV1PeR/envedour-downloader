package config

import (
	"os"
	"testing"
)

// Load() must surface REDIS_PASSWORD into Config.RedisPassword. The env
// variable is the only source: godotenv and systemd EnvironmentFile both
// end up in os.Getenv before Load() runs, so env manipulation here covers
// all real entry paths.
func TestLoadRedisPassword(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token") // required by Load()

	t.Run("set value reaches config", func(t *testing.T) {
		t.Setenv("REDIS_PASSWORD", "secret123")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.RedisPassword != "secret123" {
			t.Fatalf("RedisPassword = %q, want %q", cfg.RedisPassword, "secret123")
		}
	})

	t.Run("empty value means no auth", func(t *testing.T) {
		t.Setenv("REDIS_PASSWORD", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.RedisPassword != "" {
			t.Fatalf("RedisPassword = %q, want empty", cfg.RedisPassword)
		}
	})

	t.Run("unset variable means no auth", func(t *testing.T) {
		old, had := os.LookupEnv("REDIS_PASSWORD")
		if err := os.Unsetenv("REDIS_PASSWORD"); err != nil {
			t.Fatalf("Unsetenv: %v", err)
		}
		t.Cleanup(func() {
			if had {
				os.Setenv("REDIS_PASSWORD", old)
			}
		})

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.RedisPassword != "" {
			t.Fatalf("RedisPassword = %q, want empty for unset variable", cfg.RedisPassword)
		}
	})
}

// Without REDIS_PASSWORD the behavior must be identical to the
// pre-authentication build: empty password, no auth attempt.
func TestLoadRedisPasswordBackwardCompatible(t *testing.T) {
	t.Setenv("BOT_TOKEN", "test-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.RedisPassword != "" {
		t.Fatalf("RedisPassword = %q, want empty for backward compatibility", cfg.RedisPassword)
	}
	if cfg.RedisAddr == "" {
		t.Fatal("RedisAddr must keep its default")
	}
}
