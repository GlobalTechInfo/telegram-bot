package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telegram-bot/config"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// igServer serves a stand-in for the Instagram endpoint plus the media files it
// points at, so the whole collect -> chunk -> album -> count path can run
// without touching the real API or Telegram. dataFor builds the response body.
func igServer(t *testing.T, dataFor func(base string) interface{}) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/instagram/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dataFor(srv.URL))
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	})
	mux.HandleFunc("/vid/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write([]byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70})
	})
	return srv.URL
}

func igDownloader(t *testing.T) downloader {
	t.Helper()
	d, ok := dlByID("ig")
	if !ok {
		t.Fatal("ig is not registered")
	}
	return d
}

func igHandler(t *testing.T, api *stubAPI, baseURL string) *Handler {
	t.Helper()
	tgSrv := httptest.NewServer(api.handler())
	t.Cleanup(tgSrv.Close)

	bot, err := tgbotapi.NewBotAPIWithClient("123:TEST", tgSrv.URL+"/bot%s/%s", tgSrv.Client())
	if err != nil {
		t.Fatalf("stub bot: %v", err)
	}
	store := session.NewStore(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(store.Close)

	cfg := &config.Config{ApiBaseURL: baseURL, ApiKey: "test"}
	return New(bot, cfg, store, "123:TEST")
}

func imageEntries(n int, base string) []map[string]string {
	out := make([]map[string]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]string{
			"title": "Download Image",
			"url":   fmt.Sprintf("%s/img/%d.jpg", base, i),
		})
	}
	return out
}

func lastMessageText(t *testing.T, api *stubAPI) string {
	t.Helper()
	calls := api.callsFor("sendMessage")
	if len(calls) == 0 {
		t.Fatal("no sendMessage calls recorded")
	}
	return calls[len(calls)-1].params.Get("text")
}

func TestDownloadInstagramSendsEveryCarouselImage(t *testing.T) {
	base := igServer(t, func(base string) interface{} {
		return map[string]interface{}{"success": true, "data": imageEntries(26, base)}
	})

	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"sendMediaGroup": {okMessages},
		"sendMessage":    {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, base)

	h.resolveDownload(igDownloader(t), -1001, -1001, "https://www.instagram.com/p/Dd_0i2OE8ZL/", "", "en")

	groups := api.callsFor("sendMediaGroup")
	if len(groups) != 3 {
		t.Fatalf("want 3 albums for 26 images, got %d", len(groups))
	}
	wantSizes := []int{10, 10, 6}
	for i, g := range groups {
		var media []map[string]interface{}
		if err := json.Unmarshal([]byte(g.params.Get("media")), &media); err != nil {
			t.Fatalf("album %d media param: %v", i, err)
		}
		if len(media) != wantSizes[i] {
			t.Errorf("album %d has %d items, want %d", i, len(media), wantSizes[i])
		}
	}
	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("carousel should not need individual photos, saw %d", n)
	}

	final := lastMessageText(t, api)
	if !strings.Contains(final, "26") {
		t.Errorf("final message should report 26 items, got %q", final)
	}
}

func TestDownloadInstagramSingleImage(t *testing.T) {
	base := igServer(t, func(base string) interface{} {
		return map[string]interface{}{"success": true, "data": imageEntries(1, base)}
	})

	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendPhoto":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, base)

	h.resolveDownload(igDownloader(t), -1001, -1001, "https://www.instagram.com/p/SINGLE12345/", "", "en")

	if n := len(api.callsFor("sendPhoto")); n != 1 {
		t.Errorf("single image should be one sendPhoto, got %d", n)
	}
	if n := len(api.callsFor("sendMediaGroup")); n != 0 {
		t.Errorf("single image must not use sendMediaGroup, got %d", n)
	}
}

func TestDownloadInstagramReelSendsVideoOnly(t *testing.T) {
	base := igServer(t, func(base string) interface{} {
		return map[string]interface{}{
			"success": true,
			"data": []map[string]string{
				{"title": "Download Thumbnail", "url": base + "/img/thumb.jpg"},
				{"title": "Download Video", "url": base + "/vid/reel.mp4"},
			},
		}
	})

	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendVideo":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, base)

	h.resolveDownload(igDownloader(t), -1001, -1001, "https://www.instagram.com/reel/DbNCGmgFWfr/", "", "en")

	if n := len(api.callsFor("sendVideo")); n != 1 {
		t.Errorf("reel should send exactly one video, got %d", n)
	}
	if n := len(api.callsFor("sendMediaGroup")); n != 0 {
		t.Errorf("reel must not send an album, got %d calls", n)
	}
	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("reel must not send the thumbnail, got %d photos", n)
	}
}

func TestDownloadInstagramCapsItems(t *testing.T) {
	base := igServer(t, func(base string) interface{} {
		return map[string]interface{}{"success": true, "data": imageEntries(120, base)}
	})

	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"sendMediaGroup": {okMessages},
		"sendMessage":    {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, base)

	h.resolveDownload(igDownloader(t), -1001, -1001, "https://www.instagram.com/p/BIGPOST123/", "", "en")

	groups := api.callsFor("sendMediaGroup")
	if len(groups) != 5 {
		t.Fatalf("want 5 albums for a capped 50 items, got %d", len(groups))
	}
	var last []map[string]interface{}
	json.Unmarshal([]byte(groups[4].params.Get("media")), &last)
	if len(last) != 10 {
		t.Errorf("last album should be full, got %d items", len(last))
	}
	if final := lastMessageText(t, api); !strings.Contains(final, "50") {
		t.Errorf("final message should report the 50 item cap, got %q", final)
	}
}

func TestMain(m *testing.M) {
	// EffectiveApiBaseURL prefers the environment; keep tests independent of it.
	os.Unsetenv("API_BASE_URL")
	os.Unsetenv("API_KEY")
	os.Exit(m.Run())
}
