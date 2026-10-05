package handlers

import (
	"os"
	"testing"

	"telegram-bot/config"
)

func liveKey(t *testing.T) string {
	k := os.Getenv("AI_KEY")
	if k == "" {
		t.Skip("AI_KEY not set")
	}
	return k
}

// TestLiveAgent is an opt-in smoke test against the real agent endpoint. It is
// skipped unless AI_KEY is exported, so the suite stays offline by default:
//
//	AI_KEY=... go test ./handlers/ -run TestLiveAgent -v
//
// The endpoint lives in this test rather than in the registry because it is not
// a downloader; config.json holds the same base URL for the bot itself.
func TestLiveAgent(t *testing.T) {
	h := &Handler{cfg: &config.Config{AI: config.AIConfig{
		Enabled: true, Name: "Kraken", Owner: "Qasim",
		ApiBaseURL:    "https://mistral.stacktoy.workers.dev",
		ApiKey:        liveKey(t),
		MaxInputChars: 500,
	}}}
	for _, c := range []struct{ text, lang string }{
		{"What is the capital of France? One short sentence.", "en"},
		{"Say hello in one short sentence.", "hi"},
		{"I am nervous about my exam tomorrow. Be encouraging.", "en"},
	} {
		got, err := h.aiReply(c.text, c.lang)
		if err != nil {
			t.Fatalf("%s: %v", c.lang, err)
		}
		t.Logf("[%s] %s", c.lang, got)
	}
}
