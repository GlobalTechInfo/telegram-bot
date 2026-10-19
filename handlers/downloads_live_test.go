package handlers

import (
	"os"
	"testing"

	"telegram-bot/config"
)

// TestLiveSoundCloud is an opt-in smoke test against the real media endpoint. It
// is skipped unless LIVE_DOWNLOAD is exported, so the suite stays offline:
//
//	LIVE_DOWNLOAD=1 go test ./handlers/ -run TestLiveSoundCloud -v
//
// The aggregator needs no bot token, so this runs the whole delivery path
// against the live API with a stub Telegram server: endpoint, playlist and
// ffmpeg. The base URL and key come from config.json rather than being written
// out here, so it follows the deployment.
func TestLiveSoundCloud(t *testing.T) {
	if os.Getenv("LIVE_DOWNLOAD") == "" {
		t.Skip("LIVE_DOWNLOAD not set")
	}
	bin := resolveFFmpeg("")
	if bin == "" {
		t.Skip("ffmpeg is not installed")
	}
	old := ffmpegPath
	ffmpegPath = bin
	defer func() { ffmpegPath = old }()

	cfg, err := config.Load("../config.json")
	if err != nil {
		t.Skipf("config.json: %v", err)
	}

	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendAudio":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, cfg.EffectiveApiBaseURL())
	h.cfg.ApiKey = cfg.EffectiveApiKey()

	d, _ := dlByID("soundcloud")
	h.resolveDownload(d, -1001, -1001, "https://soundcloud.com/forss/flickermood", "", "en")

	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("artwork must not be sent, got %d photos", n)
	}
	calls := api.callsFor("sendAudio")
	if len(calls) != 1 {
		t.Fatalf("want one sendAudio, got %d", len(calls))
	}
	t.Logf("sent as audio, %d file field(s) in the request", calls[0].files)
}
