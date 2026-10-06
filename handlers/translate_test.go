package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// The translate body was filed under a case labelled awaiting_define_word, so the
// state /translate actually sets had no case and the typed text reached the
// default branch instead.
func TestTranslateTextStateIsHandled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"translation":"hola","detectedLanguage":"en"}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)

	sess := h.store.GetOrCreate(-1001)
	sess.Data["tr_target"] = "es"
	h.store.SetSessionData(-1001, sess.Data)
	h.store.SetState(-1001, "awaiting_translate_text")

	chat := &tgbotapi.Chat{ID: -1001}
	msg := &tgbotapi.Message{MessageID: 1, Chat: chat, Text: "hello", From: &tgbotapi.User{ID: 7}}
	h.HandleMessage(tgbotapi.Update{Message: msg})
	body := waitForText(t, api)

	if !strings.Contains(body, "hola") {
		t.Errorf("the text was not translated:\n%s", body)
	}
	if state := h.store.GetOrCreate(-1001).State; state != "idle" {
		t.Errorf("state after translating = %q, want idle", state)
	}
}

// Replying to a message and typing /translate should translate the reply, which
// is the case that actually gets used.
func TestTranslateUsesTheRepliedMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true,"data":{"translation":"bonjour","detectedLanguage":"en"}}`))
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, srv.URL)

	chat := &tgbotapi.Chat{ID: -1002}
	msg := &tgbotapi.Message{
		MessageID: 2, Chat: chat, Text: "/translate", From: &tgbotapi.User{ID: 7},
		Entities:       []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 10}},
		ReplyToMessage: &tgbotapi.Message{MessageID: 1, Chat: chat, Text: "good morning"},
	}
	h.HandleCommand(tgbotapi.Update{Message: msg})

	pending := h.store.GetOrCreate(-1002).Data["tr_text"]
	if pending != "good morning" {
		t.Errorf("the replied text was not captured, got %v", pending)
	}
	if state := h.store.GetOrCreate(-1002).State; state != "awaiting_translate_lang" {
		t.Errorf("state = %q, want awaiting_translate_lang so a language can be picked", state)
	}
}

// A caption is text too, and captions are common on photos and videos.
func TestReplyTextOfReadsTextAndCaption(t *testing.T) {
	cases := []struct {
		name string
		msg  *tgbotapi.Message
		want string
	}{
		{"no reply", &tgbotapi.Message{Text: "x"}, ""},
		{"nil message", nil, ""},
		{"plain text", &tgbotapi.Message{ReplyToMessage: &tgbotapi.Message{Text: " hi "}}, "hi"},
		{"caption only", &tgbotapi.Message{ReplyToMessage: &tgbotapi.Message{Caption: "cap"}}, "cap"},
		{"text wins over caption", &tgbotapi.Message{ReplyToMessage: &tgbotapi.Message{Text: "t", Caption: "c"}}, "t"},
		{"neither", &tgbotapi.Message{ReplyToMessage: &tgbotapi.Message{}}, ""},
	}
	for _, c := range cases {
		if got := replyTextOf(c.msg); got != c.want {
			t.Errorf("%s: replyTextOf = %q, want %q", c.name, got, c.want)
		}
	}
}

// waitForText collects the sent messages once the fetch goroutine has finished.
func waitForText(t *testing.T, api *stubAPI) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		sent := api.callsFor("sendMessage")
		if len(sent) > 0 {
			var b strings.Builder
			for _, m := range sent {
				b.WriteString(m.params.Get("text") + "\n")
			}
			return b.String()
		}
		if time.Now().After(deadline) {
			return ""
		}
		time.Sleep(10 * time.Millisecond)
	}
}
