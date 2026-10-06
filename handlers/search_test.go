package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every lookup source is declared as data, so these invariants are what stop a
// new entry from being registered wrong.
func TestSearcherRegistryInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range searchers {
		if s.id == "" || s.endpoint == "" || s.name == "" {
			t.Errorf("incomplete entry: %+v", s)
			continue
		}
		if seen[s.id] {
			t.Errorf("duplicate id %q", s.id)
		}
		seen[s.id] = true

		if !strings.HasPrefix(s.endpoint, "/") {
			t.Errorf("%s: endpoint %q must start with a slash", s.id, s.endpoint)
		}
		// A source that carries a query must say where; one that does not must not.
		if s.param == "" && !s.noQuery && s.kind != kindShortURL {
			t.Errorf("%s: no param and not marked noQuery", s.id)
		}
		if s.kind != kindShortURL && len(s.listPath) == 0 {
			t.Errorf("%s: list sources need a listPath", s.id)
		}
		// A shortener returns exactly one link, so it has no count.
		if s.kind != kindShortURL && s.count <= 0 {
			t.Errorf("%s: count must be positive", s.id)
		}
	}
}

func TestSearcherByIDFindsEveryRegisteredSource(t *testing.T) {
	for _, s := range searchers {
		got, ok := searcherByID(s.id)
		if !ok {
			t.Errorf("%s is not resolvable by id", s.id)
			continue
		}
		if got.endpoint != s.endpoint {
			t.Errorf("%s: endpoint = %q, want %q", s.id, got.endpoint, s.endpoint)
		}
	}
	if _, ok := searcherByID("nope"); ok {
		t.Error("an unknown id must not resolve")
	}
}

// digList has to find the list at whatever depth the endpoint nests it.
func TestDigListFindsNestedLists(t *testing.T) {
	cases := []struct {
		name string
		body map[string]interface{}
		path []string
		want int
	}{
		{"one level", map[string]interface{}{"data": map[string]interface{}{"results": []interface{}{1, 2}}},
			[]string{"data", "results"}, 2},
		{"four levels", map[string]interface{}{"data": map[string]interface{}{
			"result": map[string]interface{}{"data": map[string]interface{}{"data": []interface{}{1}}}}},
			[]string{"data", "result", "data", "data"}, 1},
		{"missing key", map[string]interface{}{"data": map[string]interface{}{}},
			[]string{"data", "results"}, 0},
		{"path runs out", map[string]interface{}{"data": 5},
			[]string{"data", "results"}, 0},
		{"list where an object was expected", map[string]interface{}{"data": []interface{}{1}},
			[]string{"data", "results"}, 0},
	}
	for _, c := range cases {
		if got := len(digList(c.body, c.path)); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// The shortener answers are nested differently per service, and all of them have
// to resolve to the same field.
func TestDigStringAtFollowsEveryShortenerShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"reurl wraps in data.result", `{"data":{"result":{"short_url":"https://a/1"}}}`, "https://a/1"},
		{"data.short_url", `{"data":{"short_url":"https://b/2"}}`, "https://b/2"},
		{"result is a bare string", `{"data":{"result":"https://c/3"}}`, "https://c/3"},
		{"one element list", `{"data":{"result":[{"short_url":"https://d/4"}]}}`, "https://d/4"},
		{"top level", `{"short_url":"https://e/5"}`, "https://e/5"},
		{"absent", `{"data":{}}`, ""},
		{"not json", `<html>`, ""},
	}
	for _, c := range cases {
		paths := [][]string{
			{"data", "result", "short_url"},
			{"data", "short_url"},
			{"data", "result"},
			{"short_url"},
		}
		var got string
		for _, p := range paths {
			if got = digStringAt([]byte(c.body), p); got != "" {
				break
			}
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClampCountBoundsTheUserRequest(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"5", 5},
		{" 10 ", 10},
		{"0", 0},
		{"", 0},
		{"abc", 0},
		{"-4", 0},
		{"9999", searchMaxResults},
	}
	for _, c := range cases {
		if got := clampCount(c.in); got != c.want {
			t.Errorf("clampCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// Pinterest hands its images back as a list of objects rather than a string.
func TestSearcherMediaURLHandlesNestedImageLists(t *testing.T) {
	s, _ := searcherByID("pin_search")
	item := map[string]interface{}{
		"images_url": []interface{}{
			map[string]interface{}{"url": "https://i.pinimg.com/originals/a.jpg"},
		},
		"grid_title": "a pin",
	}
	if got := s.mediaURL(item); got != "https://i.pinimg.com/originals/a.jpg" {
		t.Errorf("mediaURL = %q", got)
	}
	if got := s.label(item); got != "a pin" {
		t.Errorf("label = %q", got)
	}
}

func TestSearcherMediaURLPrefersTheVideoField(t *testing.T) {
	s, _ := searcherByID("imgur_search")
	item := map[string]interface{}{
		"link":     "https://i.imgur.com/a.png",
		"link_mp4": "https://i.imgur.com/a.mp4",
	}
	if got := s.mediaURL(item); got != "https://i.imgur.com/a.mp4" {
		t.Errorf("mediaURL = %q, want the mp4", got)
	}
}

// The sticker endpoint nests the URL under size and format, so the registry uses
// the dedicated picker for it.
func TestStickerSearcherUsesTheNestedPicker(t *testing.T) {
	s, _ := searcherByID("sticker_search")
	if s.resolve == nil {
		t.Fatal("the sticker source must use the nested picker")
	}
	item := map[string]interface{}{
		"file": map[string]interface{}{
			"320": map[string]interface{}{
				"webp": map[string]interface{}{"url": "https://t/sticker.webp"},
			},
		},
	}
	if got := s.mediaURL(item); got != "https://t/sticker.webp" {
		t.Errorf("mediaURL = %q", got)
	}
}

// A news feed without a source field, which is what the BBC returns, must still
// render.
func TestNewsRendersWithoutEveryFieldPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"articles":[
			{"title":"Headline one","url":"https://n/1","description":"d1","published_at":"2026-01-01"},
			{"title":"Headline two","url":"https://n/2"}
		]}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	s, _ := searcherByID("news_bbc")
	h.runSearcher(s, -1001, -1001, "", 0, "en")

	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n"
	}
	for _, want := range []string{"BBC", "Headline one", "Headline two", "https://n/2"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestSportsRendersFixtureStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"games":[
			{"name":"U.A.E. v India","status":"India need 271 runs","details":"330/8"}
		]}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	s, _ := searcherByID("sports_cricket")
	h.runSearcher(s, -1001, -1001, "", 0, "en")

	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n"
	}
	for _, want := range []string{"Cricket", "U.A.E. v India", "India need 271 runs", "330/8"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

// A response that is not an object must not panic the source.
func TestSearcherSurvivesANonObjectResponse(t *testing.T) {
	for _, body := range []string{"null", "[]", `"oops"`, "42"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		api := &stubAPI{responders: map[string][]string{
			"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
		}}
		h := igHandler(t, api, srv.URL)
		s, _ := searcherByID("news_bbc")
		h.runSearcher(s, -1001, -1001, "", 0, "en")
		srv.Close()

		if len(api.callsFor("sendMessage")) == 0 {
			t.Errorf("body %q: the user was told nothing", body)
		}
	}
}

func TestSearchKindNameCoversEveryKind(t *testing.T) {
	for _, k := range []searchKind{kindMedia, kindNews, kindSports, kindShortURL} {
		if searchKindName(k) == "unknown" {
			t.Errorf("kind %d has no name", k)
		}
	}
}

// A digest with no channel id must not post, and must not be startable.
func TestDigestStaysOffWithoutAChannel(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")
	h.cfg.Tools.Digest.Enabled = true
	h.cfg.Tools.Digest.ChannelID = 0

	h.StartDigest() // must not start a loop or panic
	h.RunDigestOnce()

	if n := len(api.callsFor("sendMessage")); n != 0 {
		t.Errorf("a digest with no channel posted %d messages", n)
	}
}

func TestDigestRefusesANonNewsSource(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	h.cfg.Tools.Digest.Enabled = true
	h.cfg.Tools.Digest.ChannelID = -1001
	// A media search is not a digest source; starting it must not post anything.
	h.cfg.Tools.Digest.Source = "bing_images"
	h.StartDigest()
	h.RunDigestOnce()

	if n := len(api.callsFor("sendMessage")); n != 0 {
		t.Errorf("a non-news source posted %d messages", n)
	}
}

func TestDigestRefusesAnUnknownSource(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	h.cfg.Tools.Digest.Enabled = true
	h.cfg.Tools.Digest.ChannelID = -1001
	h.cfg.Tools.Digest.Source = "not-a-source"
	h.StartDigest()
	h.RunDigestOnce()

	if n := len(api.callsFor("sendMessage")); n != 0 {
		t.Errorf("an unknown source posted %d messages", n)
	}
}

// A real run posts a headline and then the stories, to the configured channel.
func TestDigestPostsToTheConfiguredChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"articles":[
			{"title":"Story one","url":"https://n/1","description":"first"},
			{"title":"Story two","url":"https://n/2"},
			{"title":"","url":"https://n/3"},
			{"title":"No link here"}
		]}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.cfg.Tools.Digest.Enabled = true
	h.cfg.Tools.Digest.ChannelID = -2001
	h.cfg.Tools.Digest.Source = "news_bbc"

	h.RunDigestOnce()

	sent := api.callsFor("sendMessage")
	// headline + two usable stories
	if len(sent) != 3 {
		t.Fatalf("posted %d messages, want 3", len(sent))
	}
	for _, m := range sent {
		if got := m.params.Get("chat_id"); got != "-2001" {
			t.Errorf("posted to chat %s, want the configured channel", got)
		}
	}
	if !strings.Contains(sent[0].params.Get("text"), "BBC") {
		t.Errorf("the headline should name the source: %q", sent[0].params.Get("text"))
	}
	var body string
	for _, m := range sent {
		body += m.params.Get("text") + "\n"
	}
	for _, want := range []string{"Story one", "https://n/1", "Story two", "https://n/2"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	// A story with no link, and one with no title, are both unusable.
	if strings.Contains(body, "n/3") || strings.Contains(body, "No link here") {
		t.Errorf("an unusable story was posted:\n%s", body)
	}
}

// A failing feed must not post anything, least of all a broken headline.
func TestDigestFailurePostsNothing(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"success":false,"error":"upstream"}`, `{"success":true,"data":{"articles":[]}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		api := &stubAPI{responders: map[string][]string{
			"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
		}}
		h := igHandler(t, api, srv.URL)
		h.cfg.Tools.Digest.Enabled = true
		h.cfg.Tools.Digest.ChannelID = -2002
		h.cfg.Tools.Digest.Source = "news_bbc"
		h.RunDigestOnce()
		srv.Close()

		if n := len(api.callsFor("sendMessage")); n != 0 {
			t.Errorf("body %q posted %d messages", body, n)
		}
	}
}

func TestDigestStatusReflectsTheConfiguration(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	h.cfg.Tools.Digest.Enabled = false
	if got := h.digestStatusShort(); !strings.Contains(got, "off") {
		t.Errorf("disabled status = %q", got)
	}
	h.cfg.Tools.Digest.Enabled = true
	if got := h.digestStatusShort(); !strings.Contains(got, "channel") {
		t.Errorf("missing channel id status = %q", got)
	}
	h.cfg.Tools.Digest.ChannelID = -2003
	h.cfg.Tools.Digest.EveryHours = 6
	got := h.digestStatusShort()
	for _, want := range []string{"-2003", "6"} {
		if !strings.Contains(got, want) {
			t.Errorf("status %q is missing %q", got, want)
		}
	}
}
