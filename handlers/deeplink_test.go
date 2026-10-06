package handlers

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// /start ignored its payload, so a shared link could not open anything.
func TestParseDeepLinkReadsActionAndArgs(t *testing.T) {
	cases := []struct {
		payload  string
		wantAct  string
		wantArgs []string
	}{
		{"dl|ig|https://instagram.com/p/x/", "dl", []string{"ig", "https://instagram.com/p/x/"}},
		{"search|yt|lofi", "search", []string{"yt", "lofi"}},
		{"chat|textmaker", "chat", []string{"textmaker"}},
		{"CHAT|news", "chat", []string{"news"}},
		{"dl", "dl", nil},
		{"   ", "", nil},
		{"", "", nil},
	}
	for _, c := range cases {
		act, args := parseDeepLink(c.payload)
		if act != c.wantAct {
			t.Errorf("%q: action = %q, want %q", c.payload, act, c.wantAct)
		}
		if len(args) != len(c.wantArgs) {
			t.Errorf("%q: args = %v, want %v", c.payload, args, c.wantArgs)
			continue
		}
		for i := range c.wantArgs {
			if args[i] != c.wantArgs[i] {
				t.Errorf("%q: arg %d = %q, want %q", c.payload, i, args[i], c.wantArgs[i])
			}
		}
	}
}

// A query packed as base64url is how a hand-built link usually looks, since the
// separator would otherwise need escaping.
func TestParseDeepLinkAcceptsBase64(t *testing.T) {
	raw := "dl|ig|https://instagram.com/p/abc"
	enc := base64.RawURLEncoding.EncodeToString([]byte(raw))
	act, args := parseDeepLink(enc)
	if act != "dl" {
		t.Fatalf("action = %q, want dl", act)
	}
	if len(args) != 2 || args[1] != "https://instagram.com/p/abc" {
		t.Errorf("args = %v", args)
	}
}

// The payload has to be found in the command text, including the "@botname" form a
// link copied out of a group carries.
func TestDeepLinkPayloadReadsCommandText(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"/start dl|ig|https://x/1", "dl|ig|https://x/1"},
		{"/start@MyBot search|yt", "search|yt"},
		{"/start@MyBot  chat|news", "chat|news"},
		{"/start", ""},
		{"/help", ""},
	}
	for _, c := range cases {
		msg := &tgbotapi.Message{
			Text:     c.text,
			Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(strings.Fields(c.text)[0])}},
		}
		if got := deepLinkPayload(msg); got != c.want {
			t.Errorf("%q: payload = %q, want %q", c.text, got, c.want)
		}
	}
	if deepLinkPayload(nil) != "" {
		t.Error("a nil message carries no payload")
	}
	// Plain text is not a command, so there is no payload to read.
	plain := &tgbotapi.Message{Text: "hello"}
	if got := deepLinkPayload(plain); got != "" {
		t.Errorf("plain text = %q, want empty", got)
	}
}

// An unknown action must fall through so the person still gets the welcome,
// rather than being told their link failed.
func TestUnknownDeepLinkFallsThrough(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")

	if h.handleDeepLink(-1001, -1001, "nonsense|x", "en") {
		t.Error("an unknown action must report that it was not handled")
	}
	if h.handleDeepLink(-1001, -1001, "", "en") {
		t.Error("an empty payload must report that it was not handled")
	}
}

func TestDeepLinkRejectsUnknownSiteAndSource(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")

	if !h.handleDeepLink(-1001, -1001, "dl|notasite|https://x/1", "en") {
		t.Error("a named site must be handled, even when unknown")
	}
	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n"
	}
	if !strings.Contains(body, "notasite") {
		t.Errorf("the unknown site should be named: %s", body)
	}
}

func TestDeepLinkTruncatedDownloadLinkIsReported(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")

	if !h.handleDeepLink(-1001, -1001, "dl|ig", "en") {
		t.Error("a truncated download link must still be handled")
	}
	var body string
	for _, m := range api.callsFor("sendMessage") {
		body += m.params.Get("text") + "\n"
	}
	if !strings.Contains(strings.ToLower(body), "incomplete") {
		t.Errorf("a truncated link should say so: %s", body)
	}
}

// Every state a deep link can jump into must be one the message handler actually
// has a case for, or the next message would fall through.
func TestDeepLinkStatesAreHandled(t *testing.T) {
	src, err := readSource("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range searcherStates {
		if !strings.Contains(src, `case "`+state+`"`) {
			t.Errorf("%s jumps into %q, which has no case in HandleMessage", id, state)
		}
	}
	// A menu command is reached through the menuCommands map in the default
	// branch, not through an explicit case.
	for cmd := range menuCommands {
		if !strings.Contains(src, `case "`+cmd+`"`) {
			if _, mapped := menuCommands[cmd]; !mapped {
				t.Errorf("chat|%s is neither a case nor a menu command", cmd)
			}
		}
	}
}

func TestDeepLinkURLShape(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	h.cfg.Bot.Username = "MyBot"

	got := h.deepLinkURL("search", "yt", "lofi")
	if !strings.HasPrefix(got, "https://t.me/MyBot?start=") {
		t.Errorf("deepLinkURL = %q", got)
	}
	// The payload has to survive the round trip.
	payload := strings.Split(strings.Split(got, "start=")[1], "%7C")[0]
	if act, _ := parseDeepLink(deepLinkToken("search", "yt", "lofi")); act != "search" {
		t.Errorf("token round trip failed")
	}
	_ = payload

	h.cfg.Bot.Username = ""
	if got := h.deepLinkURL("search"); got != "" {
		t.Errorf("without a username there is no link, got %q", got)
	}
}

func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
