package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telegram-bot/localization"
)

// /reddit shipped the API response as reddit.json, which left the user reading a
// file. These pin the shapes the endpoint can plausibly return.
func TestRedditPostsFromFindsTheList(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"data array", `{"success":true,"data":[{"title":"a","url":"https://x/1"}]}`, 1},
		{"results array", `{"success":true,"results":[{"title":"b","permalink":"/r/go/1"}]}`, 1},
		{"reddit native nesting", `{"data":{"children":[{"data":{"title":"c","url":"https://x/3","score":9,"num_comments":4}}]}}`, 1},
		{"items array", `{"success":true,"items":[{"title":"d","link":"https://x/4"}]}`, 1},
		{"nothing usable", `{"success":true,"data":[{"nope":1}]}`, 0},
		{"empty list", `{"success":true,"data":[]}`, 0},
		{"no list at all", `{"success":true}`, 0},
	}
	for _, c := range cases {
		var root map[string]interface{}
		if err := json.Unmarshal([]byte(c.body), &root); err != nil {
			t.Fatal(err)
		}
		if got := len(redditPostsFrom(root)); got != c.want {
			t.Errorf("%s: got %d posts, want %d", c.name, got, c.want)
		}
	}
}

func TestRedditPostLinkPrefersAbsolute(t *testing.T) {
	cases := []struct {
		name string
		p    redditPost
		want string
	}{
		{"absolute url", redditPost{URL: "https://a/1"}, "https://a/1"},
		{"link field", redditPost{Link: "https://b/2"}, "https://b/2"},
		{"permalink is relative", redditPost{Permalink: "/r/go/x"}, "https://www.reddit.com/r/go/x"},
		{"nothing usable", redditPost{Title: "t"}, ""},
	}
	for _, c := range cases {
		if got := c.p.link(); got != c.want {
			t.Errorf("%s: link() = %q, want %q", c.name, got, c.want)
		}
	}
}

// A failing upstream has to arrive as a message, not as an attachment the user
// cannot read.
func TestRedditUpstreamFailureIsReportedNotAttached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":false,"error":"Failed to search Reddit"}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.fetchReddit(-1001, 1, "golang", "en")

	if n := len(api.callsFor("sendDocument")); n != 0 {
		t.Errorf("a failed search must not send a document (%d times)", n)
	}
	sent := api.callsFor("sendMessage")
	if len(sent) == 0 {
		t.Fatal("the user was told nothing")
	}
	text := sent[len(sent)-1].params.Get("text")
	if !strings.Contains(text, "Failed to search Reddit") {
		t.Errorf("the endpoint's own reason should reach the user, got %q", text)
	}
}

// A successful search renders posts with titles and links, not JSON.
func TestRedditSuccessRendersPosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":[{"title":"Go 1.25 released","url":"https://example.com/a","score":120,"num_comments":31,"author":"gopher"}]}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.fetchReddit(-1001, 1, "golang", "en")

	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n"
	}
	for _, want := range []string{"Go 1.25 released", "https://example.com/a", "120", "31", "gopher"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered post is missing %q; got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "{") {
		t.Errorf("raw JSON leaked into the chat:\n%s", body)
	}
	_ = localization.Get
}
