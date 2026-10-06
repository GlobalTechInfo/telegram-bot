package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDefineAcceptsBothShapes(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantOK  bool
		wantLen int
	}{
		{"entry list", `[{"word":"hello","meanings":[{"partOfSpeech":"noun","definitions":[{"definition":"A greeting."}]}]}]`, true, 1},
		{"two entries", `[{"word":"a"},{"word":"b"}]`, true, 2},
		{"error object", `{"title":"No Definitions Found","message":"nothing"}`, false, 0},
		{"html error page", `<html>502</html>`, false, 0},
		{"empty array", `[]`, true, 0},
		{"null", `null`, false, 0},
	}
	for _, c := range cases {
		entries, ok := parseDefine([]byte(c.body))
		if ok != c.wantOK || len(entries) != c.wantLen {
			t.Errorf("%s: got (%d, ok=%v), want (%d, ok=%v)", c.name, len(entries), ok, c.wantLen, c.wantOK)
		}
	}
}

// The endpoint nests phonetics under two keys and synonyms at two levels; both
// have to be found or the card renders bare.
func TestDefineEntryReadsOptionalFields(t *testing.T) {
	body := `[{"word":"hello","phonetic":"həˈləʊ","phonetics":[{"text":"","audio":"https://a/x.mp3"}],
	 "meanings":[{"partOfSpeech":"noun","synonyms":["hi","greeting","a very long synonym that should be dropped"],
	 "definitions":[{"definition":"A greeting.","example":"Hello there!"}]}]}]`
	entries, ok := parseDefine([]byte(body))
	if !ok || len(entries) != 1 {
		t.Fatalf("parseDefine = %v ok=%v", entries, ok)
	}
	e := entries[0]
	if e.phoneticText() != "həˈləʊ" {
		t.Errorf("phoneticText = %q", e.phoneticText())
	}
	if e.audioLink() != "https://a/x.mp3" {
		t.Errorf("audioLink = %q", e.audioLink())
	}
	syn := defineSynonyms(e.Meanings[0].Synonyms)
	if strings.Contains(syn, "very long synonym") {
		t.Errorf("an over-long synonym survived: %q", syn)
	}
	if !strings.Contains(syn, "hi") {
		t.Errorf("synonyms lost: %q", syn)
	}
}

// An unrecognised body must render the not-found line, never an empty card.
func TestDefineRendersTheEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/hello") {
			w.Write([]byte(`[{"word":"hello","phonetic":"həˈləʊ","meanings":[{"partOfSpeech":"noun",
				"definitions":[{"definition":"A greeting.","example":"Hello there!","synonyms":["hi"]}]}]}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"title":"No Definitions Found","message":"nothing there"}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.cfg.Tools.Define.Enabled = true
	h.cfg.Tools.Define.Endpoint = srv.URL + "/"

	h.fetchDefine(-1001, 1, "hello", "en")
	h.fetchDefine(-1001, 1, "zzzqqx", "en")

	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n---\n"
	}
	for _, want := range []string{"hello", "A greeting.", "Hello there!"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in rendered card:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "zzzqqx") {
		t.Errorf("a word the endpoint does not know should be named back:\n%s", body)
	}
}

func TestDefineStaysOffWhenNotConfigured(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	if h.defineEnabled() {
		t.Error("define must be off when the endpoint is empty")
	}
	h.cfg.Tools.Define.Endpoint = "https://x.test/"
	if h.defineEnabled() {
		t.Error("an endpoint alone must not switch it on while disabled")
	}
	h.cfg.Tools.Define.Enabled = true
	if !h.defineEnabled() {
		t.Error("define must be on once enabled with an endpoint")
	}
}
