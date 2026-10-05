package main

import (
	"os"
	"path/filepath"
	"testing"

	"telegram-bot/config"
)

// .env has to reach the process environment, because that is where every
// Effective* lookup looks first. Forwarding a fixed list of variable names
// instead meant AI_KEY was read from .env into a local map and then dropped, so
// the agent looked unconfigured while the file plainly had the key.
func TestEnvFileReachesProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "" +
		"# comment line\n" +
		"\n" +
		"BOT_TOKEN=123456:ABC\n" +
		"AI_KEY=key-from-dot-env\n" +
		"AI_BASE_URL=https://ai.test\n" +
		"DB_PATH=./data/bot.db\n" +
		`QUOTED="value with spaces"` + "\n" +
		"MEM_LIMIT_MB=400\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"AI_KEY", "AI_BASE_URL", "DB_PATH", "MEM_LIMIT_MB", "QUOTED"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	env := loadEnv(path)
	applyEnv(env)

	for k, want := range map[string]string{
		"AI_KEY":       "key-from-dot-env",
		"AI_BASE_URL":  "https://ai.test",
		"DB_PATH":      "./data/bot.db",
		"MEM_LIMIT_MB": "400",
		"QUOTED":       "value with spaces",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("os.Getenv(%q) = %q, want %q", k, got, want)
		}
	}
	if os.Getenv("BOT_TOKEN") == "" {
		t.Error("BOT_TOKEN was not forwarded either")
	}
}

// The agent's own resolution: .env wins, and a key that exists only in .env is
// what makes it ready, since config.json ships blank on purpose.
func TestAiKeyFromEnvFileMakesAgentReady(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("AI_KEY=key-from-dot-env\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AI_KEY", "")
	os.Unsetenv("AI_KEY")

	cfg, err := config.Load("../telegram-bot/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.ApiKey != "" {
		t.Skip("config.json now ships a key; the override cannot be observed")
	}

	applyEnv(loadEnv(path))

	if got := cfg.EffectiveAiKey(); got != "key-from-dot-env" {
		t.Errorf("EffectiveAiKey() = %q, want the .env value", got)
	}
	if !cfg.AiReady() {
		t.Error("agent should be ready once AI_KEY is in the environment")
	}
}

// An empty value in .env must not blank out a variable that is already set by
// the process environment, or a blank AI_KEY would erase a real one.
func TestEmptyEnvValueDoesNotClobberProcessEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("AI_KEY=\nAPI_KEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AI_KEY", "from-process-env")
	applyEnv(loadEnv(path))

	if got := os.Getenv("AI_KEY"); got != "from-process-env" {
		t.Errorf("a blank .env line overwrote the process value: %q", got)
	}
}
