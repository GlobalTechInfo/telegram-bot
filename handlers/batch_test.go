package handlers

import (
	"strings"
	"testing"
)

// Auto-detection acted on the first link only, so a message with five URLs was
// mostly ignored.
func TestBatchLinksFindsEveryLink(t *testing.T) {
	got := batchLinks(`here you go:
https://www.instagram.com/p/aaa/
https://www.tiktok.com/@u/video/123
and https://youtu.be/xyz
trailing text`)

	want := []string{
		"https://www.instagram.com/p/aaa/",
		"https://www.tiktok.com/@u/video/123",
		"https://youtu.be/xyz",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d links, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBatchLinksDropsDuplicatesAndTrailingPunctuation(t *testing.T) {
	got := batchLinks("https://x.test/a. https://x.test/a, https://x.test/b!")
	if len(got) != 2 {
		t.Fatalf("got %v, want two distinct links", got)
	}
	if got[0] != "https://x.test/a" || got[1] != "https://x.test/b" {
		t.Errorf("punctuation not trimmed: %v", got)
	}
}

func TestBatchLinksIgnoresTextWithoutLinks(t *testing.T) {
	if got := batchLinks("just a normal sentence"); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// A message full of unrelated links is conversation, not a download request.
func TestBatchHasDownloadsNeedsAtLeastOneSupportedSite(t *testing.T) {
	if batchHasDownloads([]string{"https://example.com/a", "https://news.ycombinator.com/"}) {
		t.Error("links from no supported site must not start a batch")
	}
	if !batchHasDownloads([]string{"https://example.com/a", "https://www.instagram.com/p/x/"}) {
		t.Error("one supported link is enough")
	}
	if batchHasDownloads(nil) {
		t.Error("an empty list has no supported links")
	}
}

// The batch must report one summary covering every link, including the ones that
// resolved to nothing.
func TestBatchReportsEveryOutcome(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")

	links := []string{
		"https://www.instagram.com/p/aaa/",   // known site, will fail at the API
		"https://github.com/o/r/releases/v1", // known host, off-path
		"https://example.com/not-a-site/",    // unknown
	}
	h.handleBatch(-1001, -1001, links, "en")

	var body strings.Builder
	for _, m := range api.callsFor("sendMessage") {
		body.WriteString(m.params.Get("text") + "\n")
	}
	got := body.String()

	if !strings.Contains(got, "batch") && !strings.Contains(got, "Down") {
		t.Errorf("no summary was sent:\n%s", got)
	}
	// The counts must add up to the number of links handed in.
	if !strings.Contains(got, "3") {
		t.Errorf("the summary should account for all 3 links:\n%s", got)
	}
}
