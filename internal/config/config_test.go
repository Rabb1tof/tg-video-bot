package config

import (
	"testing"
	"time"
)

func TestGetEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		setValue string
		setIt    bool
		def      string
		want     string
	}{
		{"returns set value", "TEST_KEY_SET", "hello", true, "default", "hello"},
		{"returns default when not set", "TEST_KEY_UNSET", "", false, "default", "default"},
		{"empty value uses default", "TEST_KEY_EMPTY", "", true, "fallback", "fallback"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setIt {
				t.Setenv(tc.key, tc.setValue)
			}
			got := getEnv(tc.key, tc.def)
			if got != tc.want {
				t.Errorf("getEnv(%q, %q) = %q; want %q", tc.key, tc.def, got, tc.want)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		def   int
		want  int
	}{
		{"parses valid int", "TEST_INT_VALID", "42", 0, 42},
		{"uses default on empty", "TEST_INT_EMPTY", "", 7, 7},
		{"uses default on invalid", "TEST_INT_INVALID", "notanint", 99, 99},
		{"parses zero", "TEST_INT_ZERO", "0", 5, 0},
		{"parses negative", "TEST_INT_NEG", "-3", 0, -3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value != "" || tc.name == "parses zero" || tc.name == "parses negative" {
				t.Setenv(tc.key, tc.value)
			}
			got := getInt(tc.key, tc.def)
			if got != tc.want {
				t.Errorf("getInt(%q, %d) = %d; want %d", tc.key, tc.def, got, tc.want)
			}
		})
	}
}

func TestParseInt64List(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []int64
	}{
		{"empty string returns nil", "", nil},
		{"single id", "123", []int64{123}},
		{"multiple ids", "1,2,3", []int64{1, 2, 3}},
		{"with spaces", " 10 , 20 , 30 ", []int64{10, 20, 30}},
		{"skips invalid", "1,bad,3", []int64{1, 3}},
		{"negative ids", "-1,-2", []int64{-1, -2}},
		{"all invalid", "a,b,c", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInt64List(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("parseInt64List(%q) len=%d; want %d", tc.input, len(got), len(tc.want))
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Errorf("parseInt64List(%q)[%d] = %d; want %d", tc.input, i, v, tc.want[i])
				}
			}
		})
	}
}

func TestMustEnv_Panics(t *testing.T) {
	t.Run("panics when not set", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic but none occurred")
			}
		}()
		mustEnv("MUST_ENV_DEFINITELY_NOT_SET_XYZ")
	})
	t.Run("returns value when set", func(t *testing.T) {
		t.Setenv("MUST_ENV_PRESENT", "value123")
		got := mustEnv("MUST_ENV_PRESENT")
		if got != "value123" {
			t.Errorf("mustEnv = %q; want %q", got, "value123")
		}
	})
}

func TestMustInt64_Panics(t *testing.T) {
	t.Run("panics when not set", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic but none occurred")
			}
		}()
		mustInt64("MUST_INT64_NOT_SET_XYZ")
	})
	t.Run("panics on non-integer", func(t *testing.T) {
		t.Setenv("MUST_INT64_INVALID", "notanumber")
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic but none occurred")
			}
		}()
		mustInt64("MUST_INT64_INVALID")
	})
	t.Run("returns parsed int64", func(t *testing.T) {
		t.Setenv("MUST_INT64_VALID", "-9876543210")
		got := mustInt64("MUST_INT64_VALID")
		if got != -9876543210 {
			t.Errorf("mustInt64 = %d; want %d", got, -9876543210)
		}
	})
}

func TestLoad_Defaults(t *testing.T) {
	required := map[string]string{
		"TELEGRAM_TOKEN":     "test-token",
		"STORAGE_CHANNEL_ID": "-100123456789",
		"DATABASE_URL":       "postgres://localhost/test",
	}
	for k, v := range required {
		t.Setenv(k, v)
	}
	// unset optional ones to check defaults
	optionals := []string{
		"REDIS_ADDR", "WORKER_COUNT", "MAX_FILE_SIZE_MB",
		"CACHE_TTL_HOURS", "HOT_CACHE_TTL_HOURS", "RATE_LIMIT_PER_HOUR", "LOG_LEVEL",
	}
	for _, k := range optionals {
		t.Setenv(k, "")
	}

	cfg := Load()
	if cfg.TelegramToken != "test-token" {
		t.Errorf("TelegramToken = %q; want %q", cfg.TelegramToken, "test-token")
	}
	if cfg.StorageChannelID != -100123456789 {
		t.Errorf("StorageChannelID = %d", cfg.StorageChannelID)
	}
	if cfg.WorkerCount != 5 {
		t.Errorf("WorkerCount default = %d; want 5", cfg.WorkerCount)
	}
	if cfg.MaxFileSizeMB != 500 {
		t.Errorf("MaxFileSizeMB default = %d; want 500", cfg.MaxFileSizeMB)
	}
	if cfg.CacheTTL != 24*time.Hour {
		t.Errorf("CacheTTL default = %v; want 24h", cfg.CacheTTL)
	}
	if cfg.HotCacheTTL != 168*time.Hour {
		t.Errorf("HotCacheTTL default = %v; want 168h", cfg.HotCacheTTL)
	}
	if cfg.RateLimitPerHour != 5 {
		t.Errorf("RateLimitPerHour default = %d; want 5", cfg.RateLimitPerHour)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q; want info", cfg.LogLevel)
	}
}
