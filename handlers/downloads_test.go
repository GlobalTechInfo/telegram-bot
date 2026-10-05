package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telegram-bot/config"
	"telegram-bot/keyboards"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestRegistryInvariants(t *testing.T) {
	if len(downloaders) == 0 {
		t.Fatal("registry is empty")
	}

	ids := make(map[string]bool)
	cmds := make(map[string]string)
	for _, d := range downloaders {
		if d.id == "" || d.name == "" {
			t.Errorf("registry entry %+v is missing an id or display name", d)
		}
		if ids[d.id] {
			t.Errorf("duplicate downloader id %q", d.id)
		}
		ids[d.id] = true

		if len(d.cmds) == 0 {
			t.Errorf("%s has no command alias", d.id)
		}
		if d.cmds[0] != d.id {
			t.Errorf("%s: first command %q should match the id so config.json lookups work", d.id, d.cmds[0])
		}
		for _, c := range d.cmds {
			if prev, ok := cmds[c]; ok {
				t.Errorf("command %q claimed by both %s and %s", c, prev, d.id)
			}
			cmds[c] = d.id
		}

		if len(d.hosts) == 0 {
			t.Errorf("%s has no hosts, so no link will ever route to it", d.id)
		}
		for _, h := range d.hosts {
			if h != strings.ToLower(h) || strings.HasPrefix(h, ".") || strings.Contains(h, "/") {
				t.Errorf("%s: host %q should be a bare lowercase suffix", d.id, h)
			}
		}
		if !strings.HasPrefix(d.endpoint, "/") {
			t.Errorf("%s: endpoint %q must start with /", d.id, d.endpoint)
		}
		if d.mode == dlFormats {
			if d.key == "" {
				t.Errorf("%s is dlFormats but has no query key", d.id)
			}
			if len(d.labels) == 0 {
				t.Errorf("%s is dlFormats but lists no formats", d.id)
			}
			for _, l := range d.labels {
				if l.value == "" || l.fallback == "" {
					t.Errorf("%s: label %+v needs a value and a fallback", d.id, l)
				}
			}
		}
	}
}

// A downloader alias must never shadow a command the bot already handles, or
// /search would silently start downloading something.
func TestRegistryCommandsDoNotShadowBuiltins(t *testing.T) {
	builtin := map[string]bool{}
	for _, name := range []string{
		"start", "help", "settings", "profile", "feedback", "about", "poll",
		"search", "bing", "meme", "translate", "convert", "weather", "qr",
		"reddit", "remind", "history", "ban", "unban", "kick", "mute",
		"promote", "demote", "del", "purge", "invite", "welcome", "groups",
		"channels", "ginfo", "gsetname", "gsetdesc", "gsettings", "lockdown",
		"antilinks", "anticaps", "moderate", "gstats", "chsettings", "chstats",
		"stream", "post", "admin",
	} {
		builtin[name] = true
	}

	for _, d := range downloaders {
		for _, c := range d.cmds {
			if builtin[c] {
				t.Errorf("downloader %s claims built-in command /%s", d.id, c)
			}
		}
	}
}

func TestRegistryAliasesAreResolvable(t *testing.T) {
	// Every alias used by the shipped config.json must exist, or /help and the
	// menu would advertise commands that answer "unknown command".
	for _, c := range []string{"yt", "instagram", "tiktok", "facebook", "pinterest", "snapchat", "twitter", "download"} {
		if _, ok := dlByCmd(c); !ok {
			t.Errorf("alias %q resolves to no downloader", c)
		}
	}
	if _, ok := dlByCmd("nonsense"); ok {
		t.Error("nonsense resolved to a downloader")
	}
	if _, ok := dlByID("nonsense"); ok {
		t.Error("nonsense resolved to a downloader id")
	}
}

func TestDlByHostMatching(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.youtube.com/watch?v=abc", "yt"},
		{"https://youtu.be/abc", "yt"},
		{"https://m.youtube.com/watch?v=abc", "yt"},
		{"https://music.youtube.com/watch?v=abc", "yt"},
		{"https://www.instagram.com/p/ABC/", "ig"},
		{"https://instagr.am/p/ABC/", "ig"},
		{"https://vm.tiktok.com/ZM123/", "tt"},
		{"https://www.tiktok.com/@user/video/123", "tt"},
		{"https://fb.watch/abc/", "fb"},
		{"https://m.facebook.com/watch/?v=1", "fb"},
		{"https://pin.it/abc", "pin"},
		{"https://www.pinterest.com/pin/1/", "pin"},
		{"https://www.snapchat.com/spot/abc", "sc"},
		{"https://t.snapchat.com/abc", "sc"},
		{"https://x.com/user/status/1", "tw"},
		{"https://t.co/abc", "tw"},
		{"https://twitter.com/user/status/1", "tw"},
		// Lookalike hosts must not match.
		{"https://notyoutube.com/watch?v=abc", ""},
		{"https://youtube.com.evil.io/watch", ""},
		{"https://evil-instagram.com/p/ABC/", ""},
		{"https://example.com/video", ""},
		{"not a url", ""},
		{"", ""},
	}

	for _, c := range cases {
		hit := dlByHost(c.url)
		got := ""
		if hit.ok {
			got = hit.d.id
		}
		if got != c.want {
			t.Errorf("dlByHost(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func TestDlPendingState(t *testing.T) {
	if _, ok := dlPendingState("awaiting_yt_url"); !ok {
		t.Error("awaiting_yt_url should resolve to a downloader")
	}
	if _, ok := dlPendingState("awaiting_nonsense_url"); ok {
		t.Error("unknown downloader state must not resolve")
	}
	for _, state := range []string{"idle", "awaiting_feedback", "awaiting_bing_query", "awaiting_poll_question"} {
		if _, ok := dlPendingState(state); ok {
			t.Errorf("state %q must not be treated as a downloader prompt", state)
		}
	}
}

func TestDlHostOKRejectsWrongSite(t *testing.T) {
	yt, _ := dlByID("yt")
	if !dlHostOK(yt, "https://youtu.be/abc") {
		t.Error("a YouTube link must be accepted at the YouTube prompt")
	}
	if dlHostOK(yt, "https://vm.tiktok.com/ZM1/") {
		t.Error("a TikTok link must be rejected at the YouTube prompt")
	}
	if dlHostOK(yt, "nonsense") {
		t.Error("garbage must be rejected")
	}
}

// Every downloader must render a prompt in every supported language, either from
// its own translations or from the shared strings.
func TestEveryDownloaderHasPromptInEveryLanguage(t *testing.T) {
	for _, d := range downloaders {
		for _, lang := range localization.SupportedLanguages() {
			for _, suffix := range []string{"Prompt", "Invalid", "Working", "Error"} {
				got := dlText(d, suffix, lang)
				if got == "" || strings.Contains(got, "!MISSING") {
					t.Errorf("%s/%s has no %s string", d.id, lang, suffix)
				}
			}
			menu := dlText(d, "Menu", lang)
			if menu == "" {
				t.Errorf("%s/%s has no menu label", d.id, lang)
			}
		}
	}
}

// A shared string must never leak the raw %s verb, and a per-site string must
// never gain a stray %!(EXTRA ...) suffix.
func TestDlTextRendersWithoutFormatErrors(t *testing.T) {
	for _, d := range downloaders {
		for _, lang := range localization.SupportedLanguages() {
			for _, suffix := range []string{"Prompt", "Invalid", "Working", "Uploading", "Success", "Error", "Menu"} {
				got := dlText(d, suffix, lang)
				if strings.Contains(got, "%!") || strings.Contains(got, "%s") {
					t.Errorf("%s/%s %s rendered badly: %q", d.id, lang, suffix, got)
				}
			}
		}
	}
}

// Existing per-site strings must keep winning over the shared fallbacks so no
// translation is lost by the migration.
func TestPerSiteStringsBeatSharedFallbacks(t *testing.T) {
	preserved := []struct {
		id, key string
	}{
		{"yt", "ytPrompt"}, {"ig", "igPrompt"}, {"tt", "ttPrompt"},
		{"fb", "fbPrompt"}, {"pin", "pinPrompt"}, {"sc", "scPrompt"}, {"tw", "twPrompt"},
	}
	for _, p := range preserved {
		if !localization.Has(p.key, "en") {
			t.Fatalf("%s is missing", p.key)
		}
	}

	// The generic wording is still available for a site with no strings of its own.
	if !localization.Has("dlPrompt", "en") {
		t.Error("shared dlPrompt is missing")
	}
	d := downloader{id: "zz", name: "Example"}
	got := dlText(d, "Prompt", "en")
	if !strings.Contains(got, "Example") {
		t.Errorf("shared prompt should name the site, got %q", got)
	}
}

func TestGenericOptionsDiscriminatesCarouselFromPicker(t *testing.T) {
	carousel := mustJSON(t, `{"success":true,"data":[
		{"title":"Download Image","url":"https://cdn/1.jpg"},
		{"title":"Download Image","url":"https://cdn/2.jpg"}]}`)
	opts := postOptions(carousel)
	if len(opts) != 1 {
		t.Fatalf("a carousel is one post: want 1 option, got %d", len(opts))
	}
	if len(opts[0].items) != 2 {
		t.Errorf("both images belong to the option, got %d", len(opts[0].items))
	}

	reel := mustJSON(t, `{"success":true,"data":[
		{"title":"Download Thumbnail","url":"https://cdn/t.jpg"},
		{"title":"Download Video","url":"https://cdn/v.mp4"}]}`)
	opts = postOptions(reel)
	if len(opts) != 1 || len(opts[0].items) != 1 {
		t.Fatalf("a reel must resolve to just the video, got %+v", opts)
	}
	if !isVideoItem(opts[0].items[0]) {
		t.Error("the surviving entry should be the video")
	}

	// Facebook nests its list under allQualities, which the generic reader does
	// not look for, so it has a dedicated extractor.
	fb, _ := dlByID("fb")
	qualities := mustJSON(t, `{"success":true,"data":{"allQualities":[
		{"quality":"720p","url":"https://cdn/720.mp4"},
		{"quality":"1080p","url":"https://cdn/1080.mp4"}]}}`)
	if got := listOptions(fb, qualities); len(got) != 0 {
		t.Errorf("the generic reader should not guess allQualities, got %d options", len(got))
	}
	list := fbOptions(fb, qualities, "en")
	if len(list) != 2 {
		t.Fatalf("two qualities are two options, got %d", len(list))
	}
	if list[0].label != "720p" || list[1].label != "1080p" {
		t.Errorf("labels should come from the API, got %q and %q", list[0].label, list[1].label)
	}

	// A conventional list needs no custom extractor.
	simple := mustJSON(t, `{"data":[{"quality":"sd","url":"https://cdn/sd.mp4"}]}`)
	if got := listOptions(fb, simple); len(got) != 1 || got[0].label != "sd" {
		t.Errorf("a plain quality list should resolve, got %+v", got)
	}
}

func TestListOptionsDeduplicates(t *testing.T) {
	fb, _ := dlByID("fb")
	dup := mustJSON(t, `{"data":[{"url":"https://cdn/a.mp4"},{"url":"https://cdn/a.mp4"}]}`)
	if got := listOptions(fb, dup); len(got) != 1 {
		t.Errorf("duplicate URLs should collapse, got %d options", len(got))
	}
	empty := mustJSON(t, `{"data":[{"quality":"720p"}]}`)
	if got := listOptions(fb, empty); len(got) != 0 {
		t.Errorf("an entry without media should be skipped, got %d", len(got))
	}
	// An unlabelled option falls back to the site's name.
	unlabelled := mustJSON(t, `{"data":[{"url":"https://cdn/a.mp4"},{"url":"https://cdn/b.mp4"}]}`)
	got := listOptions(fb, unlabelled)
	if len(got) != 2 || got[0].label != "Facebook 1" {
		t.Errorf("fallback label should name the site, got %+v", got)
	}
}

func TestDlQueryBuildsEndpointURL(t *testing.T) {
	d := downloader{id: "x", name: "X", endpoint: "/dl/x", key: "format"}
	h := &Handler{cfg: newTestConfig("https://api.test", "KEY")}
	got := h.dlQuery(d, "https://site/p/1", "720")
	for _, want := range []string{"https://api.test/dl/x?", "url=https%3A%2F%2Fsite%2Fp%2F1", "format=720"} {
		if !strings.Contains(got, want) {
			t.Errorf("query %q is missing %q", got, want)
		}
	}

	dWithParams := downloader{id: "y", name: "Y", endpoint: "/dl/y?fixed=1", params: map[string]string{"extra": "2"}}
	got = h.dlQuery(dWithParams, "https://site/p/2", "")
	if !strings.Contains(got, "fixed=1&") || !strings.Contains(got, "extra=2") {
		t.Errorf("query should append to an existing query string and add params, got %q", got)
	}
	if strings.Contains(got, "?url") {
		t.Errorf("existing query params must come first, got %q", got)
	}
}

// A yt format selection must reach the endpoint as the format parameter.
func TestFormatModeSendsChosenFormat(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendVideo":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".mp4") {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'})
			return
		}
		lastAPIQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"url": srvHost(r) + "/v/720.mp4"},
		})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	yt, _ := dlByID("yt")

	h.resolveDownload(yt, -1001, -1001, "https://youtu.be/abc", "720", "en")

	// The endpoint must have been asked for the format the user picked.
	if !strings.Contains(lastAPIQuery, "format=720") {
		t.Errorf("endpoint query should carry format=720, got %q", lastAPIQuery)
	}

	calls := api.callsFor("getMe")
	if len(calls) == 0 {
		t.Fatal("stub was never called")
	}
	if n := len(api.callsFor("sendVideo")); n != 1 {
		t.Errorf("want one video sent, got %d", n)
	}
}

// An mp3 format must go out as audio even when the CDN answers octet-stream.
func TestAudioFormatForcesAudioDelivery(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendAudio":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/instagram/download") || strings.Contains(r.URL.Path, "loaderto") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"url": srvHost(r) + "/a/song.mp3"},
			})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0xFF, 0xFB, 0x90, 0x00})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	yt, _ := dlByID("yt")

	h.resolveDownload(yt, -1001, -1001, "https://youtu.be/abc", "mp3", "en")

	if n := len(api.callsFor("sendAudio")); n != 1 {
		t.Errorf("mp3 should be sent as audio, got %d sendAudio calls", n)
	}
	if n := len(api.callsFor("sendDocument")); n != 0 {
		t.Errorf("mp3 must not fall back to a document, got %d", n)
	}
}

// Facebook keeps its image-as-document quirk and its info block.
func TestFacebookKeepsInfoBlockAndDocumentDelivery(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":        {okMe},
		"sendDocument": {`{"ok":true,"result":{}}`},
		"sendMessage":  {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".jpg") {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"title":    "Clip",
				"download": srvHost(r) + "/d/one.jpg",
			},
		})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	fb, _ := dlByID("fb")

	h.resolveDownload(fb, -1001, -1001, "https://fb.watch/x/", "", "en")

	if n := len(api.callsFor("sendDocument")); n != 1 {
		t.Errorf("Facebook images are documents, got %d sendDocument calls", n)
	}
	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("Facebook must not send photos, got %d", n)
	}

}

// Facebook without a quality list must use the direct download field.
func TestFacebookDirectDownloadFallback(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendVideo":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".mp4") {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'})
			return
		}
		lastAPIQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"download": srvHost(r) + "/d/only.mp4"},
		})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	fb, _ := dlByID("fb")

	h.resolveDownload(fb, -1001, -1001, "https://fb.watch/x/", "", "en")

	if n := len(api.callsFor("sendVideo")); n != 1 {
		t.Errorf("the direct URL should be downloaded and sent, got %d videos", n)
	}
}

// Twitter keeps its caption on both the picker and the delivered media.
func TestTwitterCaptionIsAttached(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendPhoto":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/twitter/") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"text":   "hello world",
					"user":   map[string]interface{}{"name": "Someone", "username": "someone"},
					"medias": []interface{}{map[string]interface{}{"type": "photo", "url": srvHost(r) + "/m/1.jpg"}},
				},
			})
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	tw, _ := dlByID("tw")

	h.resolveDownload(tw, -1001, -1001, "https://x.com/a/status/1", "", "en")

	photos := api.callsFor("sendPhoto")
	if len(photos) != 1 {
		t.Fatalf("want one photo, got %d", len(photos))
	}
	if photos[0].params.Get("caption") == "" {
		t.Error("the tweet caption should be attached to the photo")
	}
}

// A picker with more than one option must store them and render buttons.
func TestListModeStoresOptionsAndRendersPicker(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"allQualities": []interface{}{
					map[string]interface{}{"quality": "720p", "url": srvHost(r) + "/q/720.mp4"},
					map[string]interface{}{"quality": "1080p", "url": srvHost(r) + "/q/1080.mp4"},
				},
			},
		})
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	fb, _ := dlByID("fb")

	h.resolveDownload(fb, -1001, -1001, "https://fb.watch/x/", "", "en")

	if n := len(api.callsFor("sendVideo")); n != 0 {
		t.Errorf("nothing should be downloaded before a choice is made, got %d videos", n)
	}
	msgs := api.callsFor("sendMessage")
	if len(msgs) == 0 {
		t.Fatal("no picker message sent")
	}
	last := msgs[len(msgs)-1].params.Get("text")
	if !strings.Contains(last, "Choose quality") {
		t.Errorf("the Facebook metadata block should stay above the picker, got %q", last)
	}
	if reply := msgs[len(msgs)-1].params.Get("reply_markup"); !strings.Contains(reply, "fb_fmt:1") {
		t.Errorf("picker should offer fb_fmt:1, got %q", reply)
	}

	// The stored options must survive the session round trip through bbolt.
	sess := h.store.GetOrCreate(-1001)
	opts := loadOptions(sess.Data["fb_options"])
	if len(opts) != 2 {
		t.Fatalf("options must survive the session round trip, got %d", len(opts))
	}
	if opts[1].label != "1080p" || opts[1].mediaURL == "" {
		t.Errorf("persisted option lost data: %+v", opts[1])
	}
}

func TestHandleDownloadPickRejectsOutOfRange(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")
	fb, _ := dlByID("fb")

	h.handleDownloadPick(fb, -1001, -1001, "7", "en")

	if n := len(api.callsFor("sendVideo")); n != 0 {
		t.Errorf("an out-of-range pick must not download, got %d", n)
	}
	msgs := api.callsFor("sendMessage")
	if len(msgs) == 0 {
		t.Fatal("expected an error message")
	}
	if !strings.Contains(msgs[len(msgs)-1].params.Get("text"), "ailed") {
		t.Errorf("expected the site's error string, got %q", msgs[len(msgs)-1].params.Get("text"))
	}
}

func TestDlTextFallbackForNewSiteNeedsNoLocalization(t *testing.T) {
	d := downloader{id: "soundcloud", name: "SoundCloud"}
	if got := dlText(d, "Prompt", "en"); !strings.Contains(got, "SoundCloud") {
		t.Errorf("shared prompt should name the site, got %q", got)
	}
	if got := dlText(d, "Error", "en"); got == "" {
		t.Error("shared error string missing")
	}
	for _, lang := range localization.SupportedLanguages() {
		if got := dlText(d, "Prompt", lang); strings.Contains(got, "!") && !strings.Contains(got, "Send") && lang == "en" {
			t.Errorf("%s prompt looks broken: %q", lang, got)
		}
	}
}

func TestDownloadPickerCallbackFormat(t *testing.T) {
	rows := keyboards.DownloadPicker("zz", []string{"A", "B", "C"}, "en").InlineKeyboard
	if len(rows) != 3 {
		t.Fatalf("want 2 button rows plus back, got %d", len(rows))
	}
	if cb(rows[0][0]) != "zz_fmt:0" || cb(rows[0][1]) != "zz_fmt:1" {
		t.Errorf("first row callbacks: %q, %q", cb(rows[0][0]), cb(rows[0][1]))
	}
	if len(rows[1]) != 1 || cb(rows[1][0]) != "zz_fmt:2" {
		t.Errorf("second row should hold the last option, got %q", cb(rows[1][0]))
	}
	if cb(rows[2][0]) != "back" {
		t.Errorf("last row should be the back button, got %q", cb(rows[2][0]))
	}
}

func TestDownloadFormatPickerCarriesValues(t *testing.T) {
	d := downloader{id: "yt", name: "YouTube", labels: []dlLabel{
		{"mp3", "ytFormatMp3", "MP3"},
		{"360", "ytFormat360", "360p"},
	}}
	rows := keyboards.DownloadFormatPicker(d.id, dlLabelTexts(d, "en"), dlLabelValues(d), "en").InlineKeyboard

	if cb(rows[0][0]) != "yt_fmt:mp3" {
		t.Errorf("format callbacks must carry the value, got %q", cb(rows[0][0]))
	}
	if rows[0][0].Text != localization.Get("ytFormatMp3", "en") {
		t.Errorf("label should use the site's existing translation, got %q", rows[0][0].Text)
	}
}

func TestDlLabelFallbackNeedsNoTranslation(t *testing.T) {
	d := downloader{id: "new", name: "New", labels: []dlLabel{{"720", "newFormat720", "720p"}}}
	if got := dlLabelTexts(d, "en")[0]; got != "720p" {
		t.Errorf("missing key should fall back to readable text, got %q", got)
	}
}

func mustJSON(t *testing.T, raw string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return v
}

// lastAPIQuery captures the most recent request the fake endpoint saw.
var lastAPIQuery string

func cb(b tgbotapi.InlineKeyboardButton) string {
	if b.CallbackData == nil {
		return ""
	}
	return *b.CallbackData
}

func newTestConfig(base, key string) *config.Config {
	return &config.Config{ApiBaseURL: base, ApiKey: key}
}

func srvHost(r *http.Request) string { return "http://" + r.Host }

// The migration must not silently swap a site's own wording for the shared one.
// Every concept the engine uses has to resolve back to the key the hand-written
// downloader used before, in every language.
func TestEngineResolvesExistingPerSiteKeys(t *testing.T) {
	expect := map[string]map[string]string{
		"Prompt":       {"yt": "ytPrompt", "ig": "igPrompt", "tt": "ttPrompt", "fb": "fbPrompt", "pin": "pinPrompt", "sc": "scPrompt", "tw": "twPrompt"},
		"Invalid":      {"yt": "ytInvalid", "ig": "igInvalid", "tt": "ttInvalid", "fb": "fbInvalid", "pin": "pinInvalid", "sc": "scInvalid", "tw": "twInvalid"},
		"Working":      {"yt": "ytDownloading", "ig": "igDownloading", "tt": "ttDownloading", "fb": "fbDownloading", "pin": "pinDownloading", "sc": "scDownloading", "tw": "twDownloading"},
		"Uploading":    {"yt": "ytUploading", "ig": "igUploading", "tt": "ttUploading", "fb": "fbUploading", "pin": "pinUploading", "sc": "scUploading", "tw": "twUploading"},
		"Success":      {"yt": "ytSuccess", "ig": "igSuccess", "tt": "ttSuccess", "fb": "fbSuccess", "pin": "pinSuccess", "sc": "scSuccess", "tw": "twSuccess"},
		"Error":        {"yt": "ytError", "ig": "igError", "tt": "ttError", "fb": "fbError", "pin": "pinError", "sc": "scError", "tw": "twError"},
		"ChooseFormat": {"yt": "ytFormat", "tt": "ttFormat", "fb": "fbFormat", "sc": "scFormat"},
		"Menu":         {"yt": "ytMenu", "ig": "igMenu", "tt": "ttMenu", "fb": "fbMenu", "pin": "pinMenu", "sc": "scMenu", "tw": "twMenu"},
	}

	for concept, byID := range expect {
		for id, key := range byID {
			d, ok := dlByID(id)
			if !ok {
				t.Fatalf("%s is not registered", id)
			}
			if !localization.Has(key, "en") {
				t.Errorf("%s: expected key %s to exist", id, key)
				continue
			}
			for _, lang := range localization.SupportedLanguages() {
				want := localization.Get(key, lang)
				if got := dlText(d, concept, lang); got != want {
					t.Errorf("%s/%s %s resolves to %q, want the site's own %q", id, lang, concept, got, want)
				}
			}
		}
	}
}

// A site that never had a picker must fall back to the shared wording.
func TestSitesWithoutOwnFormatStringUseSharedOne(t *testing.T) {
	for _, id := range []string{"ig", "pin", "tw"} {
		d, _ := dlByID(id)
		got := dlText(d, "ChooseFormat", "en")
		if got != localization.Get("dlChooseFormat", "en") {
			t.Errorf("%s should use the shared picker prompt, got %q", id, got)
		}
	}
}

// Format button labels must keep their existing translations too.
func TestFormatPickerLabelsKeepTranslations(t *testing.T) {
	yt, _ := dlByID("yt")
	tt, _ := dlByID("tt")
	for _, lang := range localization.SupportedLanguages() {
		ytLabels := dlLabelTexts(yt, lang)
		for i, key := range []string{"ytFormatMp3", "ytFormat360", "ytFormat720", "ytFormat1080"} {
			if ytLabels[i] != localization.Get(key, lang) {
				t.Errorf("%s yt label %d = %q, want %q", lang, i, ytLabels[i], localization.Get(key, lang))
			}
		}
		ttLabels := dlLabelTexts(tt, lang)
		for i, key := range []string{"ttFormatWM", "ttFormatNoWM", "ttFormatHD", "ttFormatMusic"} {
			if ttLabels[i] != localization.Get(key, lang) {
				t.Errorf("%s tt label %d = %q, want %q", lang, i, ttLabels[i], localization.Get(key, lang))
			}
		}
	}
}

// Every site must end a successful download with its own success string.
func TestSuccessfulDownloadReportsCompletion(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendVideo":   {`{"ok":true,"result":{}}`},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	bodies := map[string]func(base string) map[string]interface{}{
		"pin": func(b string) map[string]interface{} {
			return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/v/one.mp4"}}
		},
		"fb": func(b string) map[string]interface{} {
			return map[string]interface{}{"success": true, "data": map[string]interface{}{"download": b + "/v/one.mp4"}}
		},
		"sc": func(b string) map[string]interface{} {
			return map[string]interface{}{"success": true, "data": map[string]interface{}{
				"result": []interface{}{map[string]interface{}{"video": b + "/v/one.mp4"}}}}
		},
		"tw": func(b string) map[string]interface{} {
			return map[string]interface{}{"success": true, "data": map[string]interface{}{
				"text":   "tweet",
				"medias": []interface{}{map[string]interface{}{"type": "video", "url": b + "/v/one.mp4"}}}}
		},
		"yt": func(b string) map[string]interface{} {
			return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/v/one.mp4"}}
		},
	}

	var current string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".mp4") {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(bodies[current](srvHost(r)))
	}))
	defer srv.Close()

	h := igHandler(t, api, srv.URL)
	for _, id := range []string{"pin", "fb", "sc", "tw", "yt"} {
		api.mu.Lock()
		api.calls = nil
		api.mu.Unlock()
		current = id

		d, _ := dlByID(id)
		h.resolveDownload(d, -1001, -1001, "https://site/x", "", "en")

		msgs := api.callsFor("sendMessage")
		if len(msgs) < 2 {
			t.Fatalf("%s: expected an uploading notice and a completion notice, got %d messages", id, len(msgs))
		}
		final := msgs[len(msgs)-1].params.Get("text")
		want := localization.Get(d.id+"Success", "en")
		if d.id == "yt" {
			want = localization.Get("ytSuccess", "en")
		}
		if !strings.Contains(final, "Download") {
			t.Errorf("%s: completion message %q does not look like %q", id, final, want)
		}
	}
}
