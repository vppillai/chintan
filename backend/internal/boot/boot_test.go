package boot

import (
	"log/slog"
	"testing"
)

// The env helpers that do not fatal, over the environment the test sets.
func TestEnvHelpersReadTrimmedValuesAndDefaults(t *testing.T) {
	t.Setenv("LOG_LEVEL", " Warn ")
	t.Setenv("BOOT_TEST_STR", "  value ")
	t.Setenv("BOOT_TEST_INT", " 42 ")
	t.Setenv("BOOT_TEST_EMPTY", "   ")
	if got := LogLevel(); got != slog.LevelWarn {
		t.Errorf("LogLevel = %v, want warn", got)
	}
	if got := EnvOr("BOOT_TEST_STR", "x"); got != "value" {
		t.Errorf("EnvOr = %q", got)
	}
	if got := EnvOr("BOOT_TEST_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("EnvOr(blank) = %q, want the fallback", got)
	}
	if got := EnvInt64("BOOT_TEST_INT", 0); got != 42 {
		t.Errorf("EnvInt64 = %d", got)
	}
	if got := EnvInt64("BOOT_TEST_EMPTY", 7); got != 7 {
		t.Errorf("EnvInt64(blank) = %d, want the fallback", got)
	}
	if got := MustEnv("BOOT_TEST_STR"); got != "value" {
		t.Errorf("MustEnv = %q", got)
	}
}
