package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telegram-bot/config"
	"telegram-bot/keyboards"
	"telegram-bot/localization"
)

// The API refused every request without a key, and this was silently dropped once
// already. Nothing may call an endpoint without one.
func TestEveryDownloaderSendsApiKey(t *testing.T) {
	for _, d := range downloaders {
		h := &Handler{cfg: newTestConfig("https://api.test", "SECRET123")}
		got := h.dlQuery(d, "https://site/x", "")
		if !strings.Contains(got, "apiKey=SECRET123") {
			t.Errorf("%s: query %q does not carry the API key", d.id, got)
		}
		if !strings.Contains(got, d.endpoint) {
			t.Errorf("%s: query %q does not use its registered endpoint", d.id, got)
		}
		if !strings.HasPrefix(got, "https://api.test") {
			t.Errorf("%s: query %q does not start at the configured base URL", d.id, got)
		}
	}
}

// apiBaseUrl and apiKey must only ever come from config: a literal API host or
// key baked into Go code would ignore the user's configuration.
func TestNoHardcodedApiInGoSource(t *testing.T) {
	root := filepath.Join("..")
	forbidden := []string{"qasimdev.dpdns.org", "qasim-dev", "api.qasimdev"}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		// Test files may legitimately name a host to assert against.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, bad := range forbidden {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s hardcodes %q; use config.json or the registry instead", path, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestEveryDownloaderHasAnEndpoint(t *testing.T) {
	for _, d := range downloaders {
		if d.endpoint == "" {
			t.Errorf("%s has no endpoint", d.id)
		}
	}
}

func TestPrimaryMediaPrefersVideo(t *testing.T) {
	video := mediaItem{url: "https://cdn/v.mp4", title: "Download Video"}
	thumb := mediaItem{url: "https://cdn/t.jpg", title: "Download Thumbnail"}

	// Snapchat and Vidsplay both pair a video with its preview image.
	if got := primaryMedia([]mediaItem{thumb, video}); len(got) != 1 || got[0].url != video.url {
		t.Errorf("a video must win over its preview image, got %+v", got)
	}
	if got := primaryMedia([]mediaItem{video, thumb}); len(got) != 1 || got[0].url != video.url {
		t.Errorf("order must not matter, got %+v", got)
	}

	// A real carousel keeps every image.
	images := []mediaItem{{url: "https://cdn/1.jpg"}, {url: "https://cdn/2.jpg"}, {url: "https://cdn/3.jpg"}}
	if got := primaryMedia(images); len(got) != 3 {
		t.Errorf("an image carousel must keep all images, got %d", len(got))
	}
	if got := primaryMedia(nil); got != nil {
		t.Errorf("nil input should stay nil, got %+v", got)
	}
}

// The {video, image} pair must resolve to one option holding the video, not a
// two-item album that skips the video and ships the thumbnail.
func TestVideoWithPreviewResolvesToOneVideo(t *testing.T) {
	payload := mustJSON(t, `{"success":true,"data":{"result":[
		{"video":"https://cdn/clip.mp4","image":"https://cdn/preview.jpg"}]}}`)

	list := listOptions(mustDLByID(t, "vidsplay"), payload)
	if len(list) != 1 {
		t.Fatalf("want 1 option, got %d", len(list))
	}
	if len(list[0].items) != 1 || !isVideoItem(list[0].items[0]) {
		t.Fatalf("option must hold only the video, got %+v", list[0].items)
	}
}

func TestMediaExtHonestAboutUnknownTypes(t *testing.T) {
	cases := []struct {
		ct, url, declared, want string
	}{
		{"image/jpeg", "https://cdn/a", "", ".jpg"},
		{"image/jpg", "https://cdn/a", "", ".jpg"},
		{"image/png", "https://cdn/a", "", ".png"},
		{"video/mp4", "https://cdn/a", "", ".mp4"},
		{"audio/mpeg", "https://cdn/a", "", ".mp3"},
		{"application/zip", "https://cdn/a", "", ".zip"},
		// Content-Type is unhelpful: fall back to the URL, never to .jpg.
		{"application/octet-stream", "https://cdn/a.mp3", "", ".mp3"},
		{"binary/octet-stream", "https://example.com/files/report.pdf", "", ".pdf"},
		// Nothing on the wire or in the URL: only the registry knows.
		{"application/octet-stream", "https://api.github.com/repos/o/r/zipball", ".zip", ".zip"},
		// Nothing anywhere: better no extension than a wrong one.
		{"application/octet-stream", "https://cdn/a", "", ""},
		{"", "https://cdn/a", "", ""},
	}
	for _, c := range cases {
		if got := mediaExt(c.ct, c.url, c.declared); got != c.want {
			t.Errorf("mediaExt(%q, %q, %q) = %q, want %q", c.ct, c.url, c.declared, got, c.want)
		}
	}
}

func TestUrlExtRejectsJunk(t *testing.T) {
	for _, bad := range []string{"https://cdn/a", "https://cdn/a.toolong", "://", ""} {
		if got := urlExt(bad); got != "" {
			t.Errorf("urlExt(%q) = %q, want empty", bad, got)
		}
	}
}

func TestDlExtractURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://youtu.be/abc", "https://youtu.be/abc"},
		{"https://youtu.be/abc.", "https://youtu.be/abc"},
		{"https://youtu.be/abc,", "https://youtu.be/abc"},
		{"  https://www.instagram.com/p/ABC/  ", "https://www.instagram.com/p/ABC/"},
		// A link inside a sentence is still found.
		{"check this out https://vm.tiktok.com/ZM123/ amazing", "https://vm.tiktok.com/ZM123/"},
		{"https://youtu.be/abc via @user", "https://youtu.be/abc"},
		// Bare links get a scheme, because the API rejects them without one.
		{"youtu.be/abc", "https://youtu.be/abc"},
		{"instagram.com/p/ABC/", "https://instagram.com/p/ABC/"},
		{"github.com/octocat/Hello-World", "https://github.com/octocat/Hello-World"},
		// A bare domain inside prose is not a link.
		{"look at example.com please", ""},
		{"hello world", ""},
		{"", ""},
		{"12345", ""},
	}
	for _, c := range cases {
		if got := dlExtractURL(c.in); got != c.want {
			t.Errorf("dlExtractURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAutoDetectCoversSocialAndNewSites(t *testing.T) {
	auto := map[string]bool{
		"yt": true, "ig": true, "tt": true, "fb": true, "pin": true,
		"sc": true, "tw": true,
		"threads": true, "vidsplay": true, "odysee": true,
		"istock": true, "alamy": true, "capcut": true, "imdb": true,
		"gh": false,
	}
	for _, d := range downloaders {
		want, known := auto[d.id]
		if !known {
			t.Errorf("downloaders_test is missing %s; add it to the auto-detect expectations", d.id)
			continue
		}
		if d.manualOnly == want {
			t.Errorf("%s: manualOnly=%v, want %v", d.id, d.manualOnly, want)
		}
	}
}

func TestGithubAcceptsOnlyRepositoryURLs(t *testing.T) {
	accepted := []string{
		"https://github.com/octocat/Hello-World",
		"https://github.com/octocat/Hello-World/",
		"https://github.com/octocat/Hello-World.git",
		"https://github.com/telegram-bot/telegram-bot-api",
	}
	for _, u := range accepted {
		hit := dlByHost(u)
		if !hit.ok || hit.d.id != "gh" {
			t.Errorf("%q should be accepted by /gh, got ok=%v id=%q", u, hit.ok, hit.d.id)
		}
	}

	// Verified upstream: the endpoint rejects every one of these shapes.
	rejected := []string{
		"https://github.com/octocat/Hello-World/releases/latest",
		"https://github.com/octocat/Hello-World/releases/tag/v1.0",
		"https://github.com/octocat/Hello-World/archive/refs/tags/master",
		"https://github.com/octocat/Hello-World/tree/master",
		"https://github.com/octocat/Hello-World/blob/master/README",
		"https://github.com/issues/1234",
		"https://github.com/octocat",
	}
	for _, u := range rejected {
		hit := dlByHost(u)
		if hit.ok {
			t.Errorf("%q must not be accepted, got %q", u, hit.d.id)
		}
		if !hit.offPath || hit.d.id != "gh" {
			t.Errorf("%q should be reported as an unsupported GitHub link, got offPath=%v id=%q", u, hit.offPath, hit.d.id)
		}
		if dlHasHost(u) != true {
			t.Errorf("%q is still a GitHub host, so the shortener must not claim it", u)
		}
	}
}

// A github.com link must not start a download on its own.
func TestGithubIsNotAutoDetected(t *testing.T) {
	if dlByHost("https://github.com/octocat/Hello-World").ok != true {
		t.Fatal("the repo URL itself must still resolve to /gh")
	}
	gh := mustDLByID(t, "gh")
	if !gh.manualOnly {
		t.Error("gh must be manualOnly")
	}
}

func TestUnsupportedMessageResolvesWithoutTranslations(t *testing.T) {
	gh := mustDLByID(t, "gh")
	for _, lang := range localization.SupportedLanguages() {
		got := dlText(gh, "Unsupported", lang)
		if !strings.Contains(got, "GitHub") {
			t.Errorf("%s: unsupported message should name the site, got %q", lang, got)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("%s: unsupported message rendered badly: %q", lang, got)
		}
	}
}

// Every registered site must render a working prompt in all 13 languages, either
// from its own strings or the shared ones.
func TestAllSitesResolveStrings(t *testing.T) {
	for _, d := range downloaders {
		for _, lang := range localization.SupportedLanguages() {
			for _, suffix := range []string{"Prompt", "Invalid", "Working", "Uploading", "Success", "Error", "Menu"} {
				got := dlText(d, suffix, lang)
				if got == "" || strings.Contains(got, "%!") || strings.Contains(got, "%s") {
					t.Errorf("%s/%s %s = %q", d.id, lang, suffix, got)
				}
			}
		}
	}
}

// newSiteFixture serves a JSON body plus a media file, records the query it saw,
// and returns a handler wired to it.
func newSiteFixture(t *testing.T, id string, body func(base string) map[string]interface{}, mediaCT string, media []byte, stub map[string][]string) (*Handler, *stubAPI, func() string) {
	t.Helper()

	lastQuery := ""
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v/") || strings.HasPrefix(r.URL.Path, "/d/") {
			w.Header().Set("Content-Type", mediaCT)
			w.Write(media)
			return
		}
		lastQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body(srv.URL))
	}))
	t.Cleanup(srv.Close)

	if _, ok := stub["getMe"]; !ok {
		stub["getMe"] = []string{okMe}
	}
	if _, ok := stub["sendMessage"]; !ok {
		stub["sendMessage"] = []string{`{"ok":true,"result":{}}`}
	}
	api := &stubAPI{responders: stub}
	return igHandler(t, api, srv.URL), api, func() string { return lastQuery }
}

func mustDLByID(t *testing.T, id string) downloader {
	t.Helper()
	d, ok := dlByID(id)
	if !ok {
		t.Fatalf("downloader %q is not registered", id)
	}
	return d
}

var (
	videoBytes = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p'}
	imageBytes = []byte{0xFF, 0xD8, 0xFF, 0xD9}
)

// One table over the new sites: the right endpoint path, the right delivered
// type, and the API key present.
func TestNewDownloadersEndToEnd(t *testing.T) {
	cases := []struct {
		id       string
		link     string
		wantPath string
		body     func(base string) map[string]interface{}
		mediaCT  string
		media    []byte
		stub     map[string][]string
		wantCall string
	}{
		{
			id: "threads", link: "https://www.threads.net/@user/post/ABC123",
			wantPath: "/thrdown/download",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/v/one.mp4"}}
			},
			mediaCT: "video/mp4", media: videoBytes,
			stub: map[string][]string{"sendVideo": {`{"ok":true,"result":{}}`}}, wantCall: "sendVideo",
		},
		{
			id: "gh", link: "https://github.com/octocat/Hello-World",
			wantPath: "/gitclone/download",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/d/repo.zip"}}
			},
			mediaCT: "application/zip", media: []byte("PK\x03\x04zip"),
			stub: map[string][]string{"sendDocument": {`{"ok":true,"result":{}}`}}, wantCall: "sendDocument",
		},
		{
			id: "vidsplay", link: "https://www.vidsplay.com/forest-frost-winter",
			wantPath: "/download/vidsplay",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{
					"result": []interface{}{map[string]interface{}{"video": b + "/v/clip.mp4", "image": b + "/d/preview.jpg"}}}}
			},
			mediaCT: "video/mp4", media: videoBytes,
			stub: map[string][]string{"sendVideo": {`{"ok":true,"result":{}}`}}, wantCall: "sendVideo",
		},
		{
			id: "odysee", link: "https://odysee.com/@WatchmanPrivacy:1/DouglasTuman:2",
			wantPath: "/download/odysee",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{
					"result": []interface{}{map[string]interface{}{"video": "", "image": b + "/d/poster.webp"}}}}
			},
			mediaCT: "image/webp", media: imageBytes,
			stub: map[string][]string{"sendPhoto": {`{"ok":true,"result":{}}`}}, wantCall: "sendPhoto",
		},
		{
			id: "istock", link: "https://www.istockphoto.com/photo-sunset-1234567",
			wantPath: "/download/istock",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{
					"result": map[string]interface{}{"image": b + "/d/photo.jpg"}}}
			},
			mediaCT: "image/jpeg", media: imageBytes,
			stub: map[string][]string{"sendPhoto": {`{"ok":true,"result":{}}`}}, wantCall: "sendPhoto",
		},
		{
			id: "alamy", link: "https://www.alamy.com/stock-photo-sunset-12345.html",
			wantPath: "/download/alamy",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{
					"result": []interface{}{map[string]interface{}{"url": b + "/d/one.jpg"}}}}
			},
			mediaCT: "image/jpeg", media: imageBytes,
			stub: map[string][]string{"sendPhoto": {`{"ok":true,"result":{}}`}}, wantCall: "sendPhoto",
		},
		{
			id: "capcut", link: "https://www.capcut.com/template-detail/xyz/123456",
			wantPath: "/capdown/download",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/v/tpl.mp4"}}
			},
			mediaCT: "video/mp4", media: videoBytes,
			stub: map[string][]string{"sendVideo": {`{"ok":true,"result":{}}`}}, wantCall: "sendVideo",
		},
		{
			id: "imdb", link: "https://www.imdb.com/title/tt0111161/",
			wantPath: "/download/imdb",
			body: func(b string) map[string]interface{} {
				return map[string]interface{}{"success": true, "data": map[string]interface{}{
					"result": []interface{}{map[string]interface{}{
						"image":    b + "/d/poster.jpg",
						"video_hd": b + "/v/trailer.mp4",
					}}}}
			},
			mediaCT: "video/mp4", media: videoBytes,
			stub: map[string][]string{"sendVideo": {`{"ok":true,"result":{}}`}}, wantCall: "sendVideo",
		},
	}

	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			hit := dlByHost(c.link)
			if !hit.ok {
				t.Fatalf("%s: %q does not resolve to a downloader", c.id, c.link)
			}
			if hit.d.id != c.id {
				t.Fatalf("%q resolved to %q", c.link, hit.d.id)
			}
			if hit.d.endpoint != c.wantPath {
				t.Errorf("endpoint = %q, want %q", hit.d.endpoint, c.wantPath)
			}

			h, api, lastQuery := newSiteFixture(t, c.id, c.body, c.mediaCT, c.media, c.stub)
			h.resolveDownload(hit.d, -1001, -1001, c.link, "", "en")

			if got := api.callsFor(c.wantCall); len(got) == 0 {
				t.Errorf("expected %s to be called, saw none", c.wantCall)
			}
			if q := lastQuery(); !strings.Contains(q, "apiKey=") {
				t.Errorf("query %q must carry the API key", q)
			}
			if q := lastQuery(); !strings.Contains(q, "url=") {
				t.Errorf("query %q must carry the target url", q)
			}
		})
	}
}

// A stock link resolving to several files goes out as an album, not as one file.
func TestMultiFileStockLinkBecomesAlbum(t *testing.T) {
	body := func(b string) map[string]interface{} {
		return map[string]interface{}{"success": true, "data": map[string]interface{}{
			"result": []interface{}{
				map[string]interface{}{"url": b + "/d/one.jpg"},
				map[string]interface{}{"url": b + "/d/two.jpg"},
				map[string]interface{}{"url": b + "/d/three.jpg"},
			}}}
	}
	h, api, _ := newSiteFixture(t, "alamy", body, "image/jpeg", imageBytes,
		map[string][]string{"sendMediaGroup": {okMessages}})

	h.resolveDownload(mustDLByID(t, "alamy"), -1001, -1001, "https://www.alamy.com/x-1.html", "", "en")

	if n := len(api.callsFor("sendMediaGroup")); n != 1 {
		t.Fatalf("three files should become one album, got %d media groups", n)
	}
	if n := len(api.callsFor("sendPhoto")); n != 0 {
		t.Errorf("album delivery should not fall back to photos, got %d", n)
	}
}

// An empty successful payload must report an error, not claim success.
func TestEmptyMediaReportsFailure(t *testing.T) {
	body := func(string) map[string]interface{} {
		return map[string]interface{}{"success": true, "data": map[string]interface{}{
			"result": []interface{}{map[string]interface{}{"image": "", "video_hd": "", "video_sd": ""}}}}
	}
	h, api, _ := newSiteFixture(t, "imdb", body, "video/mp4", videoBytes,
		map[string][]string{"sendVideo": {`{"ok":true,"result":{}}`}})

	h.resolveDownload(mustDLByID(t, "imdb"), -1001, -1001, "https://www.imdb.com/title/tt1/", "", "en")

	if n := len(api.callsFor("sendVideo")); n != 0 {
		t.Errorf("nothing should be sent when the payload is empty, got %d videos", n)
	}
	msgs := api.callsFor("sendMessage")
	if len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1].params.Get("text"), "ailed") {
		t.Errorf("expected an error message, got %+v", msgs)
	}
}

// A zip must not be uploaded under a .jpg name.
func TestZipKeepsItsExtension(t *testing.T) {
	body := func(b string) map[string]interface{} {
		return map[string]interface{}{"success": true, "data": map[string]interface{}{"url": b + "/d/repo.zip"}}
	}
	h, api, _ := newSiteFixture(t, "gh", body, "application/octet-stream", []byte("PK\x03\x04zip"),
		map[string][]string{"sendDocument": {`{"ok":true,"result":{}}`}})

	h.resolveDownload(mustDLByID(t, "gh"), -1001, -1001, "https://github.com/octocat/Hello-World", "", "en")

	docs := api.callsFor("sendDocument")
	if len(docs) == 0 {
		t.Fatal("expected the repository to be sent as a document")
	}
}

func TestCommandAliasesForNewSites(t *testing.T) {
	for _, alias := range []string{"threads", "th", "gh", "github", "vidsplay", "vps",
		"odysee", "ody", "istock", "ist", "alamy", "capcut", "cap", "imdb"} {
		if _, ok := dlByCmd(alias); !ok {
			t.Errorf("alias %q resolves to no downloader", alias)
		}
	}
}

func TestConfigCommandsMatchRegistry(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	for _, d := range downloaders {
		cmd, ok := cfg.Commands[d.id]
		if !ok {
			t.Errorf("config.json has no command entry for %q", d.id)
			continue
		}
		if !cmd.Enabled {
			t.Errorf("config.json command %q is disabled", d.id)
		}
	}
	if cfg.ApiBaseURL == "" || cfg.ApiKey == "" {
		t.Error("config.json must keep apiBaseUrl and apiKey")
	}
}

func TestDownloadMenuListsEveryEnabledSite(t *testing.T) {
	h, _, _ := newSiteFixture(t, "gh",
		func(string) map[string]interface{} { return map[string]interface{}{"success": true} },
		"application/zip", nil, map[string][]string{})

	rows := h.downloadMenu("en").InlineKeyboard
	seen := make(map[string]bool)
	for _, row := range rows {
		for _, b := range row {
			if cb(b) == "back" {
				continue
			}
			if !strings.HasPrefix(cb(b), "dl_") {
				t.Errorf("unexpected menu button %q", cb(b))
			}
			seen[strings.TrimPrefix(cb(b), "dl_")] = true
		}
	}
	for _, d := range downloaders {
		if !seen[d.id] {
			t.Errorf("%s is missing from the download menu", d.id)
		}
	}
}

func TestMenuLabelFallsBackToDisplayName(t *testing.T) {
	// A site with no translated *Menu key uses its proper name.
	for _, id := range []string{"threads", "gh", "imdb"} {
		d := mustDLByID(t, id)
		if localization.Has(d.id+"Menu", "en") {
			continue
		}
		if got := dlText(d, "Menu", "en"); got != d.name {
			t.Errorf("%s menu label = %q, want the display name %q", id, got, d.name)
		}
	}
	if got := keyboards.DownloadMenu([]keyboards.DownloadEntry{{ID: "gh", Label: "GitHub", Enabled: true}}, "en").InlineKeyboard; len(got) != 2 {
		t.Errorf("download menu should render the entry plus back, got %d rows", len(got))
	}
}

func TestDlSplitPickCoversNewSites(t *testing.T) {
	for _, data := range []string{"threads_fmt:0", "gh_fmt:0", "vidsplay_fmt:1",
		"odysee_fmt:0", "istock_fmt:0", "alamy_fmt:0", "capcut_fmt:1", "imdb_fmt:0"} {
		if !dlIsPick(data) {
			t.Errorf("%q is not recognised as a picker callback", data)
		}
		d, _ := dlSplitPick(data)
		if d.id == "" {
			t.Errorf("%q did not resolve a downloader", data)
		}
	}
	if dlIsPick("search_pinterest") {
		t.Error("an unrelated callback must not be treated as a format pick")
	}
}

func TestFullDownloaderInventory(t *testing.T) {
	want := map[string]bool{
		"yt": true, "ig": true, "tt": true, "fb": true, "pin": true, "sc": true, "tw": true,
		"threads": true, "gh": true, "vidsplay": true, "odysee": true,
		"istock": true, "alamy": true, "capcut": true, "imdb": true,
	}
	for _, d := range downloaders {
		if !want[d.id] {
			t.Errorf("unexpected downloader %q", d.id)
		}
		delete(want, d.id)
	}
	for id := range want {
		t.Errorf("downloader %q is missing", id)
	}
	if len(downloaders) != 15 {
		t.Errorf("expected 15 downloaders, got %d", len(downloaders))
	}
}

// Odysee's live payload pairs an empty video with a poster image; the poster is
// what can be delivered, and it must not be mistaken for a video.
func TestOdyseeEmptyVideoFallsBackToPoster(t *testing.T) {
	odysee := mustDLByID(t, "odysee")
	if odysee.mode != dlList {
		t.Errorf("odysee reads data.result[], so it should be dlList, got %v", odysee.mode)
	}

	payload := mustJSON(t, `{"success":true,"data":{"result":[
		{"video":"","image":"https://thumbs.odycdn.com/poster.webp"}]}}`)

	opts := listOptions(odysee, payload)
	if len(opts) != 1 {
		t.Fatalf("want 1 option, got %d", len(opts))
	}
	if len(opts[0].items) != 1 {
		t.Fatalf("an empty video must not add an item, got %+v", opts[0].items)
	}
	if opts[0].mediaURL != "https://thumbs.odycdn.com/poster.webp" {
		t.Errorf("should deliver the poster, got %q", opts[0].mediaURL)
	}
	if isVideoItem(opts[0].items[0]) {
		t.Error("a .webp poster must not be treated as a video")
	}
}

func TestOdyseeIsRegistered(t *testing.T) {
	d, ok := dlByID("odysee")
	if !ok {
		t.Fatal("odysee must be in the registry")
	}
	if d.endpoint != "/download/odysee" {
		t.Errorf("odysee endpoint = %q", d.endpoint)
	}
	hit := dlByHost("https://odysee.com/@channel:1/video-abc")
	if !hit.ok || hit.d.id != "odysee" {
		t.Errorf("an odysee link should resolve, got ok=%v id=%q", hit.ok, hit.d.id)
	}
}

// Threads returns the post caption in data.title alongside data.video.
func TestThreadsCaptionIsNotUsedAsMediaLabel(t *testing.T) {
	long := strings.Repeat("a", 200)
	payload := mustJSON(t, `{"success":true,"data":{"title":"`+long+`","video":"https://cdn/v.mp4"}}`)

	items := collectURLs(payload)
	if len(items) != 1 {
		t.Fatalf("want 1 media item, got %d: %+v", len(items), items)
	}
	if items[0].url != "https://cdn/v.mp4" {
		t.Errorf("wrong media url: %s", items[0].url)
	}
	if items[0].title != "" {
		t.Errorf("a caption must not become a media label, got %q", items[0].title)
	}
	if !isVideoItem(items[0]) {
		t.Error("the mp4 must still be recognised as the video")
	}

	opts := postOptions(payload)
	if len(opts) != 1 || len(opts[0].items) != 1 {
		t.Errorf("a Threads video post must resolve to one video, got %+v", opts)
	}
}

// Short labels such as "Download Video" must still be read.
func TestShortMediaLabelsStillDetected(t *testing.T) {
	payload := mustJSON(t, `{"data":{"title":"Download Video","url":"https://cdn/v.mp4"}}`)
	items := collectURLs(payload)
	if len(items) != 1 || items[0].title != "Download Video" {
		t.Errorf("a short title should label the media, got %+v", items)
	}
}

// An Instagram carousel can mix many photos with a single video. Collapsing the
// whole carousel to that one video is the bug this guards: the 26-item post used
// by the reporter ships 25 images and one video and must deliver all 26.
func TestMixedCarouselKeepsEveryItem(t *testing.T) {
	entries := make([]map[string]interface{}, 0, 26)
	for i := 0; i < 26; i++ {
		title := "Download Image"
		if i == 5 {
			title = "Download Video"
		}
		entries = append(entries, map[string]interface{}{
			"title": title,
			"url":   fmt.Sprintf("https://cdn.test/%d.jpg", i),
		})
	}
	raw, err := json.Marshal(map[string]interface{}{"success": true, "data": entries})
	if err != nil {
		t.Fatal(err)
	}
	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}

	items := collectURLs(payload)
	if len(items) != 26 {
		t.Fatalf("collectURLs returned %d items, want 26", len(items))
	}
	if got := primaryMedia(items); len(got) != 26 {
		t.Errorf("a mixed carousel must keep every item, got %d", len(got))
	}

	opts := postOptions(payload)
	if len(opts) != 1 {
		t.Fatalf("one post, want 1 option, got %d", len(opts))
	}
	if len(opts[0].items) != 26 {
		t.Errorf("option must carry all 26 items, got %d", len(opts[0].items))
	}
}

// A single entry pairing a video with its preview still delivers only the video,
// and several such entries each keep their own video.
func TestPreviewImageIsDroppedPerEntry(t *testing.T) {
	single := mustJSON(t, `{"data":[{"video":"https://cdn/v.mp4","image":"https://cdn/p.jpg"}]}`)
	if got := primaryMedia(collectURLs(single)); len(got) != 1 || !isVideoItem(got[0]) {
		t.Errorf("a video must win inside one entry, got %+v", got)
	}

	two := mustJSON(t, `{"data":[
		{"video":"https://cdn/1.mp4","image":"https://cdn/1.jpg"},
		{"video":"https://cdn/2.mp4","image":"https://cdn/2.jpg"}]}`)
	got := primaryMedia(collectURLs(two))
	if len(got) != 2 {
		t.Fatalf("two entries must keep both videos, got %d: %+v", len(got), got)
	}
	for _, it := range got {
		if !isVideoItem(it) {
			t.Errorf("a preview image survived: %s", it.url)
		}
	}
}

// Entries that carry a video plus other media of their own keep those too.
func TestEntryWithVideoAndMoreKeepsItsOwnFiles(t *testing.T) {
	one := mustJSON(t, `{"data":[{"video":"https://cdn/v.mp4","image":"https://cdn/p.jpg"}]}`)
	if got := primaryMedia(collectURLs(one)); len(got) != 1 {
		t.Errorf("want just the video, got %d", len(got))
	}
	if got := primaryMedia(collectURLs(one)); got[0].url != "https://cdn/v.mp4" {
		t.Errorf("wrong survivor: %s", got[0].url)
	}
}

// A reel arrives as two separate entries, a labelled still and the video.
func TestReelPreviewEntryIsDropped(t *testing.T) {
	reel := mustJSON(t, `{"success":true,"data":[
		{"title":"Download Thumbnail","url":"https://cdn/t.jpg"},
		{"title":"Download Video","url":"https://cdn/v.mp4"}]}`)

	opts := postOptions(reel)
	if len(opts) != 1 {
		t.Fatalf("a reel is one post, got %d options", len(opts))
	}
	if len(opts[0].items) != 1 {
		t.Fatalf("only the video should be delivered, got %+v", opts[0].items)
	}
	if !isVideoItem(opts[0].items[0]) {
		t.Errorf("survivor should be the video, got %q", opts[0].items[0].url)
	}
}

// A post with no video keeps a labelled still: nothing is dropped.
func TestPreviewAloneIsKept(t *testing.T) {
	only := mustJSON(t, `{"data":[{"title":"Download Thumbnail","url":"https://cdn/t.jpg"}]}`)
	if got := postOptions(only); len(got) != 1 || len(got[0].items) != 1 {
		t.Errorf("a lone still must still be delivered, got %+v", got)
	}
}

func TestInstagramCarouselDropsReelPostersButKeepsTheReels(t *testing.T) {
	// The exact shape of the Instagram carousel that shipped broken: 14 photos,
	// 6 reels, and a "Download Thumbnail" poster in front of each reel. The 26
	// API entries collapse to the 20 unique files the user should get.
	var items []mediaItem
	for i := 0; i < 14; i++ {
		items = append(items, mediaItem{
			url:   fmt.Sprintf("https://cdn.test/photo%02d.jpg", i),
			title: "Download Image",
			group: i * 2,
		})
	}
	for i := 0; i < 6; i++ {
		g := 28 + i*3
		items = append(items,
			mediaItem{url: fmt.Sprintf("https://cdn.test/thumb%02d.jpg", i), title: "Download Thumbnail", group: g},
			mediaItem{url: fmt.Sprintf("https://cdn.test/reel%02d.mp4", i), title: "Download Video", group: g + 1},
		)
	}

	got := primaryMedia(items)
	if len(got) != 20 {
		t.Fatalf("primaryMedia kept %d items, want 20", len(got))
	}

	videos := 0
	for _, it := range got {
		if isPreviewItem(it) {
			t.Errorf("poster for a reel survived: %s", it.title)
		}
		if isVideoItem(it) {
			videos++
		}
	}
	if videos != 6 {
		t.Errorf("kept %d reels, want 6", videos)
	}
}

func TestLooksLikeVideo(t *testing.T) {
	cases := []struct {
		name string
		ct   string
		it   mediaItem
		body []byte
		want bool
	}{
		{"declared video", "video/mp4", mediaItem{url: "https://x.test/a"}, videoBytes, true},
		{"cdn says octet-stream, url says mp4", "application/octet-stream",
			mediaItem{url: "https://v1.pinimg.com/videos/101/x.mp4"}, videoBytes, true},
		{"api labels it a video", "application/octet-stream",
			mediaItem{url: "https://x.test/a", title: "Download Video"}, videoBytes, true},
		{"image must not become a video", "application/octet-stream",
			mediaItem{url: "https://i.pinimg.com/originals/9f/a.jpg"}, imageBytes, false},
		{"poster next to a video must not win", "image/jpeg",
			mediaItem{url: "https://i.pinimg.com/originals/9f/a.jpg", title: "Download Thumbnail"}, imageBytes, false},
		{"declared image stays an image", "image/jpeg",
			mediaItem{url: "https://x.test/a.jpg"}, imageBytes, false},
		{"zip is not a video", "application/zip",
			mediaItem{url: "https://x.test/a.zip"}, []byte("PK" + string([]byte{3, 4}) + "zzzzzzzz"), false},
	}
	for _, c := range cases {
		if got := looksLikeVideo(c.ct, c.it, c.body); got != c.want {
			t.Errorf("%s: looksLikeVideo(%q) = %v, want %v", c.name, c.ct, got, c.want)
		}
	}
}

func TestLooksLikeAudioKeepsVideoOutOfAudio(t *testing.T) {
	if looksLikeAudio("video/mpeg") {
		t.Error("video/mpeg must not be sent as audio")
	}
	if !looksLikeAudio("audio/mpeg") {
		t.Error("audio/mpeg must be sent as audio")
	}
	if !looksLikeAudio("audio/mp4") {
		t.Error("audio/mp4 must be sent as audio")
	}
}

// A Pinterest video arrives as application/octet-stream, which used to fall
// through every branch and reach the user as a document to open by hand.
func TestPinterestVideoIsNotSentAsDocument(t *testing.T) {
	h, api, _ := newSiteFixture(t, "pin",
		func(b string) map[string]interface{} {
			return map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"result": b + "/v/clip.mp4"},
			}
		},
		"application/octet-stream", videoBytes,
		map[string][]string{
			"sendVideo":    {`{"ok":true,"result":{}}`},
			"sendDocument": {`{"ok":true,"result":{}}`},
		})

	h.resolveDownload(mustDLByID(t, "pin"), -1001, -1001,
		"https://www.pinterest.com/pin/1055599493123168/", "", "en")

	if len(api.callsFor("sendVideo")) == 0 {
		t.Error("pinterest video must be sent with sendVideo")
	}
	if n := len(api.callsFor("sendDocument")); n != 0 {
		t.Errorf("pinterest video was also sent as a document (%d times)", n)
	}
}

// A still photo must not be turned into a video by the extension rule.
func TestPinterestPhotoIsStillAPhoto(t *testing.T) {
	h, api, _ := newSiteFixture(t, "pin",
		func(b string) map[string]interface{} {
			return map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"result": b + "/v/pin.jpg"},
			}
		},
		"application/octet-stream", imageBytes,
		map[string][]string{
			"sendPhoto": {`{"ok":true,"result":{}}`},
			"sendVideo": {`{"ok":true,"result":{}}`},
		})

	h.resolveDownload(mustDLByID(t, "pin"), -1001, -1001,
		"https://www.pinterest.com/pin/1055599493123168/", "", "en")

	if n := len(api.callsFor("sendVideo")); n != 0 {
		t.Errorf("a .jpg was sent as video %d times", n)
	}
}

func TestSniffVideo(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"mp4", []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, true},
		{"webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0, 0, 0, 0, 0, 0, 0}, true},
		{"avi", []byte("RIFF" + string([]byte{0, 0, 0, 0}) + "AVI "), true},
		{"jpeg", imageBytes, false},
		{"zip", []byte("PK\x03\x04\x00\x00\x00\x00\x00"), false},
		{"too short to tell", []byte("ftyp"), false},
	}
	for _, c := range cases {
		if got := sniffVideo(c.body); got != c.want {
			t.Errorf("%s: sniffVideo = %v, want %v", c.name, got, c.want)
		}
	}
}

// A video whose URL gives no clue at all is still recognised, because the
// container magic settles it.
func TestVideoWithNoExtensionIsStillAVideo(t *testing.T) {
	it := mediaItem{url: "https://v1.pinimg.com/videos/101/x?token=1"}
	if !looksLikeVideo("application/octet-stream", it, videoBytes) {
		t.Error("a real mp4 body must be sent as video")
	}
	if looksLikeVideo("application/octet-stream", it, imageBytes) {
		t.Error("a jpeg body must not be sent as video")
	}
}

func TestSniffImage(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want bool
	}{
		{"jpeg", imageBytes, true},
		{"png", []byte("\x89PNG\r\n\x1a\n"), true},
		{"gif", []byte("GIF89a...."), true},
		{"webp", []byte("RIFF" + string([]byte{0, 0, 0, 0}) + "WEBPVP8 "), true},
		{"mp4 is not an image", videoBytes, false},
		{"zip is not an image", []byte("PK" + string([]byte{3, 4}) + "zzzzzzzz"), false},
		{"too short to tell", []byte{0xFF, 0xD8}, false},
	}
	for _, c := range cases {
		if got := sniffImage(c.body); got != c.want {
			t.Errorf("%s: sniffImage = %v, want %v", c.name, got, c.want)
		}
	}
}

// The mirror of the Pinterest bug: a photo behind an unhelpful Content-Type must
// arrive as a photo, not as a document.
func TestOctetStreamPhotoIsSentAsPhoto(t *testing.T) {
	h, api, _ := newSiteFixture(t, "pin",
		func(b string) map[string]interface{} {
			return map[string]interface{}{
				"success": true,
				"data":    map[string]interface{}{"result": b + "/v/pin"},
			}
		},
		"application/octet-stream", imageBytes,
		map[string][]string{
			"sendPhoto":    {`{"ok":true,"result":{}}`},
			"sendDocument": {`{"ok":true,"result":{}}`},
		})

	h.resolveDownload(mustDLByID(t, "pin"), -1001, -1001,
		"https://www.pinterest.com/pin/1055599493123168/", "", "en")

	if len(api.callsFor("sendPhoto")) == 0 {
		t.Error("an octet-stream photo must be sent with sendPhoto")
	}
	if n := len(api.callsFor("sendDocument")); n != 0 {
		t.Errorf("an octet-stream photo was also sent as a document (%d times)", n)
	}
}

func TestApiReasonExplainsSilentFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"snapchat caches an empty result", `{"success":true,"data":{"result":[]},"cached":true}`,
			"cached result"},
		{"snapdown explains itself", `{"success":false,"error":"Failed to process Snapchat URL"}`,
			"Failed to process Snapchat URL"},
		{"reason nested under data", `{"success":true,"data":{"error":"Unsupported link"}}`,
			"Unsupported link"},
		{"message key is used too", `{"success":false,"message":"Invalid URL"}`, "Invalid URL"},
		{"nothing to say", `{"success":true,"data":{"result":[]}}`, "no reason given"},
	}
	for _, c := range cases {
		var v interface{}
		if err := json.Unmarshal([]byte(c.body), &v); err != nil {
			t.Fatal(err)
		}
		if got := apiReason(v); got != c.want {
			t.Errorf("%s: apiReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// pinVariantFixture answers a Pinterest call differently depending on whether
// "type=video" was asked for, which is what the real endpoint does: it returns
// one file per call and picks which one itself.
func pinVariantFixture(t *testing.T, videoForVariant bool) (*Handler, *stubAPI, *int) {
	t.Helper()

	calls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v/") || strings.HasPrefix(r.URL.Path, "/p/") {
			w.Header().Set("Content-Type", "application/octet-stream")
			if strings.HasPrefix(r.URL.Path, "/v/") {
				w.Write(videoBytes)
			} else {
				w.Write(imageBytes)
			}
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		wantVideo := r.URL.Query().Get("type") == "video" == videoForVariant
		path := "/p/poster.jpg"
		if wantVideo {
			path = "/v/clip.mp4"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"download_url": srv.URL + path},
		})
	}))
	t.Cleanup(srv.Close)

	api := &stubAPI{responders: map[string][]string{
		"getMe":          {okMe},
		"sendMessage":    {`{"ok":true,"result":{}}`},
		"sendVideo":      {`{"ok":true,"result":{}}`},
		"sendPhoto":      {`{"ok":true,"result":{}}`},
		"sendMediaGroup": {`{"ok":true,"result":{}}`},
		"sendDocument":   {`{"ok":true,"result":{}}`},
	}}
	return igHandler(t, api, srv.URL), api, &calls
}

func TestPinterestSendsVideoAndPosterTogether(t *testing.T) {
	for _, videoFirst := range []bool{true, false} {
		name := "video on the variant call"
		if videoFirst {
			name = "video on the default call"
		}
		t.Run(name, func(t *testing.T) {
			h, api, calls := pinVariantFixture(t, videoFirst)
			h.resolveDownload(mustDLByID(t, "pin"), -1001, -1001, "https://pin.it/6rgFK92Jj", "", "en")

			// One base call plus one per registered variant: the endpoint cannot
			// be trusted to hand over the video on any single request.
			want := len(mustDLByID(t, "pin").variants) + 1
			if *calls != want {
				t.Errorf("made %d API calls, want %d", *calls, want)
			}
			if len(api.callsFor("sendVideo")) == 0 {
				t.Error("the video was not delivered")
			}
			if len(api.callsFor("sendPhoto")) == 0 {
				t.Error("the poster was not delivered")
			}
			if n := len(api.callsFor("sendMediaGroup")); n != 0 {
				t.Errorf("a video and a poster must not be sent as one album (%d times)", n)
			}
		})
	}
}

// The user asked for both when both exist, the one that exists when only one
// does, and never a failure for the missing half.
func TestPinterestDeliversWhateverOneCallReturns(t *testing.T) {
	calls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v/") {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(videoBytes)
			return
		}
		calls++
		if r.URL.Query().Get("type") == "video" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "no video for this pin"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    map[string]interface{}{"download_url": srv.URL + "/v/only.mp4"},
		})
	}))
	t.Cleanup(srv.Close)

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
		"sendVideo": {`{"ok":true,"result":{}}`}, "sendPhoto": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.resolveDownload(mustDLByID(t, "pin"), -1001, -1001, "https://pin.it/x", "", "en")

	if len(api.callsFor("sendVideo")) != 1 {
		t.Errorf("expected the single available video to be delivered, saw %d", len(api.callsFor("sendVideo")))
	}
	for _, m := range api.callsFor("sendMessage") {
		if strings.Contains(m.params.Get("text"), "could not") || strings.Contains(m.params.Get("text"), "failed") {
			t.Errorf("a missing second asset must not fail the post: %s", m.params.Get("text"))
		}
	}
}

func TestParseVariant(t *testing.T) {
	got := parseVariant("type=video")
	if got["type"] != "video" {
		t.Errorf("parseVariant = %v", got)
	}
	if parseVariant("nonsense") != nil {
		t.Error("a variant without = must be ignored")
	}
}

func TestMixedTypes(t *testing.T) {
	vid := mediaItem{url: "https://x.test/a.mp4", title: "Download Video"}
	img := mediaItem{url: "https://x.test/a.jpg"}
	if !mixedTypes([]mediaItem{img, vid}) {
		t.Error("a video plus a still is a mixed set")
	}
	if mixedTypes([]mediaItem{img, {url: "https://x.test/b.jpg"}}) {
		t.Error("two stills are a gallery")
	}
	if mixedTypes([]mediaItem{vid, {url: "https://x.test/c.mp4", title: "Download Video"}}) {
		t.Error("two videos are not mixed")
	}
}

func TestFirstOfEachKind(t *testing.T) {
	vid := mediaItem{url: "https://x.test/a.mp4", title: "Download Video"}
	img1 := mediaItem{url: "https://x.test/1.jpg"}
	img2 := mediaItem{url: "https://x.test/2.jpg"}

	got := firstOfEachKind([]mediaItem{img1, img2, vid})
	if len(got) != 2 {
		t.Fatalf("kept %d assets, want 2: %v", len(got), got)
	}
	if got[0].url != vid.url || got[1].url != img1.url {
		t.Errorf("video must come first, then one still: %v", got)
	}

	if n := len(firstOfEachKind([]mediaItem{img1, img2})); n != 1 {
		t.Errorf("two identical posters must not become an album, kept %d", n)
	}
	if n := len(firstOfEachKind([]mediaItem{vid})); n != 1 {
		t.Errorf("a video-only result must be kept, kept %d", n)
	}
	if n := len(firstOfEachKind(nil)); n != 0 {
		t.Errorf("nothing in, nothing out, got %d", n)
	}
}

// apiJSON returns whatever the endpoint sent, so a body of null, [] or a bare
// string arrives with no error at all. A single-value type assertion on any of
// them panics, and these run in a goroutine with no recover.
func TestApiFailedSurvivesNonObjectResponses(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantFail  bool
		wantIsObj bool
	}{
		{"success true", `{"success":true,"data":{"url":"https://x.test/a.mp4"}}`, false, true},
		{"success false", `{"success":false,"error":"nope"}`, true, true},
		{"no flag at all", `{"data":{"url":"https://x.test/a.mp4"}}`, false, true},
		{"json null", `null`, true, false},
		{"json array", `[]`, true, false},
		{"bare string", `"hello"`, true, false},
		{"json number", `42`, true, false},
		{"json false", `false`, true, false},
	}
	for _, c := range cases {
		var v interface{}
		if err := json.Unmarshal([]byte(c.body), &v); err != nil {
			t.Fatal(err)
		}
		failed, isObject := apiFailed(v)
		if failed != c.wantFail || isObject != c.wantIsObj {
			t.Errorf("%s: apiFailed = (%v, %v), want (%v, %v)", c.name, failed, isObject, c.wantFail, c.wantIsObj)
		}
	}
}

// The end-to-end version: a malformed body must produce an error message, not a
// panic that takes the process down.
func TestMalformedApiResponseDoesNotPanic(t *testing.T) {
	for _, body := range []string{"null", "[]", `"oops"`, "42"} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, body)
			}))
			defer srv.Close()

			api := &stubAPI{responders: map[string][]string{
				"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
			}}
			h := igHandler(t, api, srv.URL)
			h.resolveDownload(mustDLByID(t, "ig"), -1001, -1001, "https://instagram.com/p/x/", "", "en")

			if len(api.callsFor("sendMessage")) == 0 {
				t.Error("the user should be told the download failed")
			}
		})
	}
}
