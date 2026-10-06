package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Inline mode answered every query with two static articles carrying no
// InputMessageContent, which Telegram rejects, and the query text was ignored.
func TestInlineQueryReturnsSelectableResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"results":[
			{"title":"A cat","direct":"https://cdn.test/cat.jpg","description":"a feline"},
			{"title":"Another cat","direct":"https://cdn.test/cat2.jpg"}
		]}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, srv.URL)

	h.HandleInline(tgbotapi.Update{InlineQuery: &tgbotapi.InlineQuery{
		ID: "q1", Query: "cat",
	}})

	answered := api.callsFor("answerInlineQuery")
	if len(answered) != 1 {
		t.Fatalf("the query was never answered: %v", api.calls)
	}
	results := strings.Join(answered[0].params["results"], "")
	if results == "" || results == "null" {
		t.Fatal("no results were returned")
	}
	// A null input_message_content is a malformed field, not a missing optional
	// one, and Telegram rejects the whole answer over it.
	if strings.Contains(results, `"input_message_content":null`) {
		t.Errorf("input_message_content must be omitted, not null:\n%s", results)
	}
	if !strings.Contains(results, "cdn.test/cat.jpg") {
		t.Errorf("the media URL is missing:\n%s", results)
	}
}

func TestInlineEmptyQueryReturnsHelp(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	h.HandleInline(tgbotapi.Update{InlineQuery: &tgbotapi.InlineQuery{ID: "q2", Query: "   "}})

	answered := api.callsFor("answerInlineQuery")
	if len(answered) != 1 {
		t.Fatal("an empty query must still be answered")
	}
	joined := strings.Join(answered[0].params["results"], "")
	if !strings.Contains(joined, "inline") &&
		!strings.Contains(joined, "Help") &&
		!strings.Contains(joined, "help") {
		t.Errorf("expected help results, got %s", joined)
	}
}

// A query the sources cannot answer must come back as a result, never as silence:
// Telegram shows nothing at all for an empty answer.
func TestInlineNoResultsStillAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"results":[]}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, srv.URL)
	h.HandleInline(tgbotapi.Update{InlineQuery: &tgbotapi.InlineQuery{ID: "q3", Query: "zzzz"}})

	answered := api.callsFor("answerInlineQuery")
	if len(answered) != 1 {
		t.Fatal("an empty result set must still produce an answer")
	}
	joined := strings.Join(answered[0].params["results"], "")
	if joined == "" || joined == "[]" {
		t.Errorf("results should not be empty: %q", joined)
	}
}

func TestInlineSourcePrefixSelectsTheSource(t *testing.T) {
	rest, sources := splitInlineSource("yt lofi beats")
	if len(sources) != 1 || sources[0].id != "yt_search" {
		t.Fatalf("yt prefix selected %v", sources)
	}
	if rest != "lofi beats" {
		t.Errorf("rest = %q, want the query without the prefix", rest)
	}

	// A bare query searches the defaults.
	_, none := splitInlineSource("lofi beats")
	if len(none) != 0 {
		t.Errorf("a bare query must not pick a source, got %v", none)
	}
	// A single word is not a source prefix.
	rest2, src2 := splitInlineSource("cat")
	if len(src2) != 0 || rest2 != "cat" {
		t.Errorf("single word = (%q, %v)", rest2, src2)
	}
}

func TestSearcherAliasesResolve(t *testing.T) {
	for _, alias := range []string{"yt", "youtube", "img", "images", "pin", "sticker", "news", "news"} {
		if _, ok := searcherByAlias(alias); !ok {
			t.Errorf("alias %q does not resolve", alias)
		}
	}
	if _, ok := searcherByAlias("definitely-not-a-source"); ok {
		t.Error("an unknown alias must not resolve")
	}
}

func TestDefaultInlineSourcesAreMedia(t *testing.T) {
	srcs := defaultInlineSources()
	if len(srcs) == 0 {
		t.Fatal("a bare inline query needs somewhere to look")
	}
	for _, s := range srcs {
		if s.kind != kindMedia {
			t.Errorf("default source %s is not a media search", s.id)
		}
	}
}

// The result id must be unique per result, or Telegram drops the duplicates.
func TestInlineResultIDsAreDistinct(t *testing.T) {
	s, _ := searcherByID("bing_images")
	seen := map[string]bool{}
	items := []map[string]interface{}{
		{"title": "one", "direct": "https://cdn.test/aaaaaaaa.jpg"},
		{"title": "two", "direct": "https://cdn.test/bbbbbbbb.jpg"},
		{"title": "three", "direct": "https://cdn.test/cccccccc.jpg"},
	}
	for i, item := range items {
		r := s.inlineResult(item, i)
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ID string `json:"id"`
		}
		json.Unmarshal(b, &got)
		if got.ID == "" {
			t.Fatal("a result with no id would be dropped")
		}
		if seen[got.ID] {
			t.Errorf("duplicate result id %q", got.ID)
		}
		seen[got.ID] = true
	}
}

func TestInlineResultSkipsItemsWithNothingToShow(t *testing.T) {
	s, _ := searcherByID("bing_images")
	if r := s.inlineResult(map[string]interface{}{"unrelated": "x"}, 0); r != nil {
		t.Errorf("an empty item must produce no result, got %+v", r)
	}
}

func TestInlineResultTitleNeverEmpty(t *testing.T) {
	if got := inlineResultTitle("", "Bing Images"); got != "Bing Images" {
		t.Errorf("an empty title must fall back to the source, got %q", got)
	}
	long := strings.Repeat("x", 300)
	if got := inlineResultTitle(long, "s"); len([]rune(got)) > 95 {
		t.Errorf("a long title was not trimmed: %d runes", len([]rune(got)))
	}
}

func TestInlineAnswerIsCapped(t *testing.T) {
	results := make([]interface{}, inlineResultsPerQuery+10)
	for i := range results {
		results[i] = "x"
	}
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	h.answerInline("q", results, "x")

	answered := api.callsFor("answerInlineQuery")
	if len(answered) != 1 {
		t.Fatal("the query was not answered")
	}
	var got []interface{}
	json.Unmarshal([]byte(strings.Join(answered[0].params["results"], "")), &got)
	if len(got) != inlineResultsPerQuery {
		t.Errorf("returned %d results, cap is %d", len(got), inlineResultsPerQuery)
	}
}

// Media results must not attach a caption: Telegram rejects a result that carries
// both a caption and input message content.
func TestInlineMediaResultCarriesNoCaption(t *testing.T) {
	s, _ := searcherByID("yt_search")
	r := s.inlineResult(map[string]interface{}{
		"title": "a video", "url": "https://youtu.be/clip.mp4",
	}, 0)
	b, _ := json.Marshal(r)
	var got map[string]interface{}
	json.Unmarshal(b, &got)

	if _, hasCaption := got["caption"]; hasCaption {
		t.Errorf("a media result must not set a caption: %s", b)
	}
	if got["type"] != "video" {
		t.Errorf("type = %v, want video", got["type"])
	}
}

func TestEscapeHTMLIsMinimalButSufficient(t *testing.T) {
	got := escapeHTML(`a < b & c > d`)
	if strings.Contains(got, "<b") || strings.Contains(got, " & ") {
		t.Errorf("unescaped: %q", got)
	}
	if !strings.Contains(got, "&lt;") || !strings.Contains(got, "&amp;") {
		t.Errorf("missing escapes: %q", got)
	}
}
