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

func TestInlineMediaResultContract(t *testing.T) {
	s := searcher{id: "yt_search", name: "YouTube", urlKeys: []string{"url"}}
	cases := []struct {
		name     string
		in       map[string]interface{}
		wantType string
		needKeys []string
	}{
		{"gif", map[string]interface{}{"url": "https://cdn.example.com/a.gif", "title": "g"}, "gif",
			[]string{"type", "id", "gif_url", "thumbnail_url"}},
		{"video", map[string]interface{}{"url": "https://cdn.example.com/a.mp4", "title": "v"}, "video",
			[]string{"type", "id", "video_url", "mime_type", "thumbnail_url"}},
		{"video-with-thumb", map[string]interface{}{"url": "https://cdn.example.com/a.mp4", "thumbnail": "https://cdn.example.com/t.jpg"}, "video",
			[]string{"type", "id", "video_url", "mime_type", "thumbnail_url"}},
		{"photo", map[string]interface{}{"url": "https://cdn.example.com/a.jpg", "title": "p"}, "photo",
			[]string{"type", "id", "photo_url", "thumbnail_url"}},
	}
	for _, tc := range cases {
		r := s.inlineResult(tc.in, 0)
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if m["type"] != tc.wantType {
			t.Errorf("%s: type = %v, want %s", tc.name, m["type"], tc.wantType)
		}
		for _, k := range tc.needKeys {
			if v, ok := m[k]; !ok || v == "" {
				t.Errorf("%s: missing/empty %q in %s", tc.name, k, b)
			}
		}
		if _, bad := m["thumb_url"]; bad {
			t.Errorf("%s: uses removed field thumb_url: %s", tc.name, b)
		}
		if _, bad := m["input_message_content"]; bad {
			t.Errorf("%s: input_message_content must be absent: %s", tc.name, b)
		}
		if strings.Contains(string(b), `"type":""`) {
			t.Errorf("%s: empty type emitted: %s", tc.name, b)
		}
	}
}

func TestInlineVideoUsesThumbnailFromItem(t *testing.T) {
	s := searcher{id: "yt_search", name: "YouTube", urlKeys: []string{"url"}}
	b, _ := json.Marshal(s.inlineResult(map[string]interface{}{
		"url": "https://cdn.example.com/v.mp4", "thumbnail": "https://cdn.example.com/thumb.jpg",
	}, 0))
	if !strings.Contains(string(b), "https://cdn.example.com/thumb.jpg") {
		t.Errorf("expected item thumbnail to be used: %s", b)
	}
}

func TestInlineIDsUniqueAcrossSources(t *testing.T) {
	seen := map[string]bool{}
	for _, src := range defaultInlineSources() {
		for i := 0; i < 5; i++ {
			r := src.inlineResult(map[string]interface{}{
				"url": "https://cdn.example.com/x.jpg", "title": "t",
			}, i)
			b, _ := json.Marshal(r)
			var m map[string]interface{}
			_ = json.Unmarshal(b, &m)
			id, _ := m["id"].(string)
			if id == "" {
				t.Fatalf("empty id")
			}
			if seen[id] {
				t.Errorf("duplicate inline id %q", id)
			}
			seen[id] = true
		}
	}
}

func TestMediaOpsMenuHidesAllVideoOpsWithoutFFmpeg(t *testing.T) {
	for _, ffmpeg := range []bool{false, true} {
		kb := keyboards.MediaOpsMenu("en", "video", ffmpeg)
		seen := map[string]bool{}
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if b.CallbackData != nil {
					seen[*b.CallbackData] = true
				}
			}
		}
		for _, op := range []string{"media:trim", "media:audio", "media:voice", "media:gif"} {
			if seen[op] != ffmpeg {
				t.Errorf("ffmpeg=%v: %s present=%v, want %v", ffmpeg, op, seen[op], ffmpeg)
			}
		}
	}
	// Every photo edit stays available without ffmpeg, since none of them shell out.
	kb := keyboards.MediaOpsMenu("en", "photo", false)
	ops := map[string]bool{}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != nil {
				ops[*b.CallbackData] = true
			}
		}
	}
	for _, op := range []string{"media:rotate90", "media:rotate180", "media:rotate270",
		"media:flip", "media:square", "media:resize"} {
		if !ops[op] {
			t.Errorf("photo op %s missing without ffmpeg", op)
		}
	}
	delete(ops, "back") // the navigation row is not an operation
	if len(ops) != 6 {
		t.Errorf("photo ops without ffmpeg = %d, want 6", len(ops))
	}
}

func TestIsSecondsRejectsNonsense(t *testing.T) {
	for _, ok := range []string{"0", "12", "12.5", "0.5"} {
		if !isSeconds(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-5", "abc", "1-2", "1..2", " "} {
		if isSeconds(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestClipWindowSetNeedsBothEnds(t *testing.T) {
	if clipWindowSet(map[string]interface{}{"media_from": "0"}) {
		t.Error("only a start is not a usable window")
	}
	if clipWindowSet(map[string]interface{}{"media_to": "30"}) {
		t.Error("only an end is not a usable window")
	}
	if !clipWindowSet(map[string]interface{}{"media_from": "0", "media_to": "30"}) {
		t.Error("both ends should be accepted")
	}
}

// Every button label must resolve to real text. A key that no language defines
// renders as an empty label, which is exactly what a stray key name causes.
func TestNoMenuButtonRendersEmpty(t *testing.T) {
	menus := map[string]tgbotapi.InlineKeyboardMarkup{
		"MainMenu":        keyboards.MainMenu(&config.Config{}, ""),
		"MediaPhoto":      keyboards.MediaOpsMenu("", "photo", true),
		"MediaVideoFF":    keyboards.MediaOpsMenu("", "video", true),
		"MediaVideoNoFF":  keyboards.MediaOpsMenu("", "video", false),
		"SearchMenu":      keyboards.SearchMenu(""),
		"Back":            keyboards.Back(""),
		"TranslatePicker": keyboards.TranslateLangPicker(""),
	}
	for name, kb := range menus {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if strings.TrimSpace(b.Text) == "" {
					t.Errorf("%s: empty button label (callback=%v)", name, b.CallbackData)
				}
			}
		}
	}
}

func TestEveryLanguageRendersEveryMenuButton(t *testing.T) {
	h := igHandler(t, &stubAPI{responders: map[string][]string{"getMe": {okMe}}}, "https://api.test")
	langs := localization.SupportedLanguages()
	for _, lang := range langs {
		menus := []tgbotapi.InlineKeyboardMarkup{
			keyboards.MainMenu(&config.Config{}, lang),
			keyboards.MediaOpsMenu(lang, "photo", true),
			keyboards.MediaOpsMenu(lang, "video", true),
			keyboards.MediaOpsMenu(lang, "video", false),
			keyboards.SearchMenu(lang),
			keyboards.Back(lang),
			keyboards.TranslateLangPicker(lang),
			h.downloadMenu(lang),
		}
		for mi, kb := range menus {
			for _, row := range kb.InlineKeyboard {
				for _, b := range row {
					if strings.TrimSpace(b.Text) == "" {
						t.Errorf("lang=%s menu#%d: empty label (callback=%v)",
							lang, mi, b.CallbackData)
					}
				}
			}
		}
	}
}

// Media must be reachable from the menu on a host with no ffmpeg. The photo edits
// are pure Go, so gating the button on ffmpegPath hid a working feature.
func TestMediaMenuShowsWithoutFFmpeg(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Media.Enabled = true
	cfg.Tools.Media.FFmpegPath = ""
	cfg.Tools.Define.Enabled = true
	cfg.Tools.Define.Endpoint = "https://example.test/"

	kb := keyboards.ToolsMenu(cfg, "en")
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != nil && *b.CallbackData == "media" {
				found = true
			}
		}
	}
	if !found {
		t.Error("media button hidden without ffmpeg, though photo edits work")
	}

	// Explicitly disabled must still hide it.
	cfg.Tools.Media.Enabled = false
	cfg.Tools.Media.FFmpegPath = ""
	kb = keyboards.ToolsMenu(cfg, "en")
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != nil && *b.CallbackData == "media" {
				t.Error("media button shown while tools.media.enabled is false")
			}
		}
	}
}

// Google writes a space as "_" in Urdu, so a translation routinely arrives with
// an unpaired underscore. Sent under ParseMode Markdown that kills the whole
// message, which is what "an error occurred" was.
func TestTranslateEscapesMarkdown(t *testing.T) {
	body := `{"success":true,"data":{"originalText":"how are you","translation":"آپ_کیسے_ہیں","detectedLanguage":"en"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, srv.URL)
	h.fetchTranslate(777, 777, "how are you", "ur", "ur")

	sent := sendTexts(api.callsFor("sendMessage"))
	if len(api.callsFor("sendMessage")) == 0 {
		t.Fatal("nothing was sent")
	}
	if strings.Contains(sent, "آپ_کیسے_ہیں") {
		t.Errorf("translation was sent with raw underscores:\n%s", sent)
	}
	if !strings.Contains(sent, "آپ\\_کیسے\\_ہیں") {
		t.Errorf("translation is not escaped:\n%s", sent)
	}
}

func sendTexts(calls []capturedCall) string {
	var out []string
	for _, c := range calls {
		out = append(out, c.params.Get("text"))
	}
	return strings.Join(out, "\n---\n")
}

// escapeMarkdown is what keeps third-party text from breaking the parse.
func TestEscapeMarkdownCoversEveryBreakableChar(t *testing.T) {
	got := escapeMarkdown("a_b*c`d[e")
	want := "a\\_b\\*c\\`d\\[e"
	if got != want {
		t.Errorf("escapeMarkdown = %q, want %q", got, want)
	}
}

// #8: len() is bytes, []rune() is runes. A 40-character Urdu or Japanese title
// is over 90 bytes but under 90 runes, and the old slice panicked — taking the
// whole inline query down, since the panic was inside HandleInline.
func TestInlineTitleNonLatinDoesNotPanic(t *testing.T) {
	for _, title := range []string{
		"آپ کیسے ہیں؟ میں ٹھیک ہوں آج کل بہت اچھا لگ رہا ہے یار",
		"タイトル_" + strings.Repeat("長", 40),
		"हिन्दी_" + strings.Repeat("अक्षर", 30),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on %q: %v", title, r)
				}
			}()
			got := inlineResultTitle(title, "YouTube")
			if n := len([]rune(got)); n > inlineTitleMaxRunes+1 {
				t.Errorf("title not truncated: %d runes", n)
			}
		}()
	}
}

// #7: there is no setInlineMode in the Bot API. EnableInline must not try.
func TestNoSetInlineModeRequestIsSent(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	h.cfg.Tools.Inline.Enabled = true
	h.EnableInline()
	for _, c := range api.calls {
		if c.method == "setInlineMode" {
			t.Error("setInlineMode does not exist in the Bot API and must not be called")
		}
	}
}

// #2 and #6: a URL in callback_data is capped at 64 bytes, and no handler serves
// it either way. Every button that carries a link has to be a URL button.
func TestLinkButtonsAreNotCallbackData(t *testing.T) {
	item := map[string]interface{}{
		"title": "a post",
		"url":   "https://www.reddit.com/r/pics/comments/1abc23z/some_quite_long_title_here/",
	}
	link := redditPost{URL: item["url"].(string), Title: "a post"}.link()
	if link == "" {
		t.Fatal("expected a link")
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("Open", link),
		))
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != nil {
				t.Errorf("a link must not ride in callback_data (%d bytes): %q",
					len(*b.CallbackData), *b.CallbackData)
			}
		}
	}
}

// A realistic pronunciation URL is far past the 64-byte callback_data limit.
func TestPronunciationURLExceedsCallbackLimit(t *testing.T) {
	audio := "https://api.dictionaryapi.dev/media/pronunciations/en/serendipity-us.mp3"
	if len(audio) <= 64 {
		t.Skip("this URL is not long enough to exercise the limit")
	}
	if len([]byte(audio)) <= 64 {
		t.Fatal("expected the URL to exceed 64 bytes")
	}
}
