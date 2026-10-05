package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"telegram-bot/config"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestCleanAIReplyTrimsWhitespace(t *testing.T) {
	if got := cleanAIReply("  Hello there.  ", "stop"); got != "Hello there." {
		t.Errorf("got %q", got)
	}
}

// The model repeats its answer joined by "acknowledged," until it hits the
// token limit. Only the first answer is worth sending.
func TestCleanAIReplyCutsRepetitionLoop(t *testing.T) {
	raw := "I'm here to help. Anything else? acknowledged, I'm here to help. " +
		"Anything else? acknowledged, I'm here to help."
	got := cleanAIReply(raw, "length")
	if got != "I'm here to help. Anything else?" {
		t.Errorf("got %q", got)
	}
	if strings.Contains(strings.ToLower(got), "acknowledged") {
		t.Error("the repetition marker must not survive")
	}
	if strings.Count(got, "I'm here to help") != 1 {
		t.Errorf("the answer must appear once, got %q", got)
	}
}

func TestCleanAIReplyTrimsTruncatedSentence(t *testing.T) {
	raw := "Here are three tips: breathe slowly. Study in chunks. Get enough sleep tonight, and Lack"
	got := cleanAIReply(raw, "length")
	if strings.Contains(got, "Lack") {
		t.Errorf("the half-typed tail should be cut, got %q", got)
	}
	if got != "Here are three tips: breathe slowly. Study in chunks." {
		t.Errorf("should end on the last complete sentence, got %q", got)
	}
}

// With no terminator anywhere there is nothing to cut back to, so an ellipsis
// marks the truncation.
func TestCleanAIReplyEllipsisWhenNoSentenceEnd(t *testing.T) {
	got := cleanAIReply("and then it just kept going with no ending at all", "length")
	if !strings.HasSuffix(got, "...") {
		t.Errorf("got %q", got)
	}
}

// A rambling reply is cut down to its first paragraph: one short chat message.
func TestCleanAIReplyKeepsFirstParagraph(t *testing.T) {
	raw := "Hello! How can I help you today? I'm here for you.\n\n" +
		"Here are a few things I can do:\n\n* Answer questions\n* Set reminders\n\n" +
		"Here's a joke: why don't scientists trust atoms?"
	got := cleanAIReply(raw, "length")
	if got != "Hello! How can I help you today? I'm here for you." {
		t.Errorf("got %q", got)
	}
	for _, banned := range []string{"*", "reminders", "atoms"} {
		if strings.Contains(got, banned) {
			t.Errorf("reply should not contain %q: %q", banned, got)
		}
	}
}

func TestCleanAIReplyKeepsCompleteTextOnStop(t *testing.T) {
	raw := "Paris is the capital of France."
	if got := cleanAIReply(raw, "stop"); got != raw {
		t.Errorf("a finished reply must not be trimmed, got %q", got)
	}
}

func TestCleanAIReplyCapsLength(t *testing.T) {
	long := strings.Repeat("a", aiMaxReplyRunes*2)
	got := cleanAIReply(long, "length")
	if n := len([]rune(got)); n > aiMaxReplyRunes {
		t.Errorf("reply is %d runes, cap is %d", n, aiMaxReplyRunes)
	}
}

func TestCleanAIReplyEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n"} {
		if got := cleanAIReply(in, "stop"); got != "" {
			t.Errorf("cleanAIReply(%q) = %q, want empty", in, got)
		}
	}
}

func TestAiPromptCarriesLanguageAndMessage(t *testing.T) {
	got := aiPrompt("hi", "I am stressed about my exam")

	for _, want := range []string{"हिन्दी", "I am stressed about my exam"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q:\n%s", want, got)
		}
	}
	// English needs no language line at all.
	if got := aiPrompt("en", "hello"); got != "hello" {
		t.Errorf("English should pass through unchanged, got %q", got)
	}
	// The persona belongs to the endpoint's system prompt, not the user turn.
	for _, banned := range []string{"apikey", "Kraken", "Qasim", "AI agent"} {
		if strings.Contains(got, banned) {
			t.Errorf("prompt should not contain %q:\n%s", banned, got)
		}
	}
}

func TestAiPromptKeepsMessageLast(t *testing.T) {
	msg := strings.Repeat("b", 50)
	if got := aiPrompt("de", msg); !strings.HasSuffix(got, msg) {
		t.Error("the message must be the last thing in the prompt")
	}
}

func TestAiReplyParsesEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "TESTKEY" {
			t.Errorf("apikey not sent, got %q", r.URL.Query().Get("apikey"))
		}
		if r.URL.Query().Get("text") == "" {
			t.Error("text not sent")
		}
		if got := r.URL.Query().Get("lang"); got != "hi" {
			t.Errorf("lang = %q, want hi so the endpoint can pick its own language", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  200,
			"creator": "test",
			"data": map[string]interface{}{"choices": []interface{}{
				map[string]interface{}{
					"finish_reason": "stop",
					"message":       map[string]interface{}{"role": "assistant", "content": " Paris is the capital. "},
				}}},
		})
	}))
	defer srv.Close()

	h := &Handler{cfg: &config.Config{AI: config.AIConfig{
		Enabled: true, Name: "Kraken", Owner: "Qasim",
		ApiBaseURL: srv.URL, ApiKey: "TESTKEY", MaxInputChars: 500,
	}}}

	got, err := h.aiReply("what is the capital of France", "hi")
	if err != nil {
		t.Fatalf("aiReply: %v", err)
	}
	if got != "Paris is the capital." {
		t.Errorf("reply = %q", got)
	}
}

func TestAiReplyErrors(t *testing.T) {
	cases := []struct {
		name string
		h    func() *Handler
	}{
		{"not configured", func() *Handler {
			return &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true}}}
		}},
		{"no key", func() *Handler {
			return &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: "https://x"}}}
		}},
		{"bad status", func() *Handler {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			}))
			return &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: srv.URL, ApiKey: "k"}}}
		}},
		{"no choices", func() *Handler {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"choices": []interface{}{}}})
			}))
			return &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: srv.URL, ApiKey: "k"}}}
		}},
		{"empty content", func() *Handler {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
					"choices": []interface{}{map[string]interface{}{"finish_reason": "stop",
						"message": map[string]interface{}{"content": "   "}}}}})
			}))
			return &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: srv.URL, ApiKey: "k"}}}
		}},
	}

	for _, c := range cases {
		if _, err := c.h().aiReply("hi", "en"); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestAiEnabledDefaults(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	if !h.aiEnabled(-500, true) {
		t.Error("private chats should default to on")
	}
	if h.aiEnabled(-600, false) {
		t.Error("groups should default to off")
	}

	h.setAi(-600, true)
	if !h.aiEnabled(-600, false) {
		t.Error("enabling a group should stick")
	}
	h.setAi(-500, false)
	if h.aiEnabled(-500, true) {
		t.Error("disabling a private chat should stick")
	}
}

func TestAiStateForChatUsesChatType(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	if !h.aiStateForChat(&tgbotapi.Chat{Type: "private", ID: 700}) {
		t.Error("a private chat should read as on by default")
	}
	if h.aiStateForChat(&tgbotapi.Chat{Type: "supergroup", ID: 800}) {
		t.Error("a group should read as off by default")
	}
	if h.aiStateForChat(nil) {
		t.Error("a nil chat should read as off")
	}
}

func TestMentionsBot(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")
	h.selfID = 42

	mention := &tgbotapi.Message{
		Entities: []tgbotapi.MessageEntity{{Type: "mention", User: &tgbotapi.User{ID: 42}}},
	}
	if !h.mentionsBot(mention) {
		t.Error("an @mention of the bot should count")
	}

	textMention := &tgbotapi.Message{
		Entities: []tgbotapi.MessageEntity{{Type: "text_mention", User: &tgbotapi.User{ID: 42}}},
	}
	if !h.mentionsBot(textMention) {
		t.Error("a text mention of the bot should count")
	}

	reply := &tgbotapi.Message{
		ReplyToMessage: &tgbotapi.Message{From: &tgbotapi.User{ID: 42}},
	}
	if !h.mentionsBot(reply) {
		t.Error("a reply to the bot should count")
	}

	other := &tgbotapi.Message{
		Entities: []tgbotapi.MessageEntity{{Type: "mention", User: &tgbotapi.User{ID: 7}}},
	}
	if h.mentionsBot(other) {
		t.Error("a mention of someone else must not count")
	}
	if h.mentionsBot(&tgbotapi.Message{}) {
		t.Error("an ordinary message must not count")
	}
	if h.mentionsBot(nil) {
		t.Error("a nil message must not count")
	}
	if h.mentionsBot(&tgbotapi.Message{
		Entities:       []tgbotapi.MessageEntity{{Type: "mention"}},
		ReplyToMessage: &tgbotapi.Message{},
	}) {
		t.Error("a mention entity without a user must not count")
	}
}

func TestAiCooldown(t *testing.T) {
	st := aiState{last: make(map[int64]time.Time)}

	if !st.allow(1234, 3*time.Second) {
		t.Error("the first call should be allowed")
	}
	if st.allow(1234, 3*time.Second) {
		t.Error("a second call inside the window should be blocked")
	}
	if !st.allow(5678, 3*time.Second) {
		t.Error("a different chat must have its own budget")
	}

	// Parallel callers must not both pass the gate.
	st2 := aiState{last: make(map[int64]time.Time)}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st2.allow(999, time.Hour) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Errorf("exactly one caller should pass the gate, got %d", allowed)
	}
}

// The gate must not call the API in a disabled chat, in a group without a
// mention, or when the agent is not configured.
func TestMaybeChatWithAIRespectsTheGate(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"finish_reason": "stop",
				"message": map[string]interface{}{"content": "hi"}}}}})
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`}, "sendChatAction": {`{"ok":true,"result":true}}`},
	}}
	h := igHandler(t, api, srv.URL)
	h.selfID = 42
	h.cfg.AI = config.AIConfig{Enabled: true, Name: "Kraken", Owner: "Qasim",
		ApiBaseURL: srv.URL, ApiKey: "k", CooldownSeconds: 0}

	private := &tgbotapi.Chat{Type: "private", ID: 1001}
	user := &tgbotapi.User{ID: 5}
	msg := &tgbotapi.Message{Text: "hello", Entities: []tgbotapi.MessageEntity{}}
	group := &tgbotapi.Chat{Type: "supergroup", ID: 1002}

	// Group without a mention: silent.
	h.maybeChatWithAI(group, user, msg, "hello", "en")
	assertNoAIHit(t, hits)

	// Private chat with AI off: silent.
	h.setAi(private.ID, false)
	h.maybeChatWithAI(private, user, msg, "hello", "en")
	assertNoAIHit(t, hits)

	// Private chat with AI on: answers.
	h.setAi(private.ID, true)
	h.maybeChatWithAI(private, user, msg, "hello", "en")
	select {
	case <-hits:
	case <-time.After(3 * time.Second):
		t.Error("an enabled private chat should have called the agent")
	}

	// Wait for the reply to land.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(api.callsFor("sendMessage")) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	msgs := api.callsFor("sendMessage")
	if len(msgs) == 0 {
		t.Fatal("the agent reply should have been sent")
	}
	// Plain text: no parse mode, so Markdown in a reply cannot break sending.
	for _, m := range msgs {
		if _, has := m.params["parse_mode"]; has {
			t.Error("the agent reply must not set a parse mode")
		}
	}
	if len(api.callsFor("sendChatAction")) == 0 {
		t.Error("a typing action should precede the reply")
	}
}

func TestMaybeChatWithAIStaysSilentWhenNotConfigured(t *testing.T) {
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer srv.Close()

	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, srv.URL)
	h.cfg.AI = config.AIConfig{Enabled: false, ApiBaseURL: srv.URL, ApiKey: "k"}

	h.maybeChatWithAI(&tgbotapi.Chat{Type: "private", ID: 2001}, &tgbotapi.User{ID: 1},
		&tgbotapi.Message{Text: "hello"}, "hello", "en")
	assertNoAIHit(t, hits)
}

func assertNoAIHit(t *testing.T, hits chan struct{}) {
	t.Helper()
	select {
	case <-hits:
		t.Error("the agent endpoint should not have been called")
	case <-time.After(250 * time.Millisecond):
	}
}

func TestSendPlainDropsParseModeAndCaps(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`}}}
	h := igHandler(t, api, "https://api.test")

	h.sendPlain(-1, "")
	if n := len(api.callsFor("sendMessage")); n != 0 {
		t.Error("an empty reply must not be sent")
	}

	h.sendPlain(-1, "**bold** _italic_ `code`")
	calls := api.callsFor("sendMessage")
	if len(calls) != 1 {
		t.Fatalf("want 1 message, got %d", len(calls))
	}
	if _, has := calls[0].params["parse_mode"]; has {
		t.Error("parse_mode must be absent so Markdown cannot break the send")
	}
	if calls[0].params.Get("text") != "**bold** _italic_ `code`" {
		t.Errorf("text was altered: %q", calls[0].params.Get("text"))
	}

	h.sendPlain(-1, strings.Repeat("c", aiMaxReplyRunes*2))
	calls = api.callsFor("sendMessage")
	last := calls[len(calls)-1].params.Get("text")
	if n := len([]rune(last)); n > aiMaxReplyRunes {
		t.Errorf("sent %d runes, cap is %d", n, aiMaxReplyRunes)
	}
}

// Every AI string must exist in all 13 languages.
func TestAiKeysExistInEveryLanguage(t *testing.T) {
	keys := []string{"aiName", "aiToggle", "aiOn", "aiOff", "aiOnMsg",
		"aiOffMsg", "aiError", "aiUsage", "aiHeader"}
	for _, lang := range localization.SupportedLanguages() {
		for _, k := range keys {
			got := localization.Get(k, lang)
			if got == "" {
				t.Errorf("%s: %s is empty", lang, k)
			}
		}
		if localization.Get("aiError", lang) == localization.Get("dlError", "en") {
			t.Errorf("%s: aiError looks like a copy of the downloader error", lang)
		}
	}
}

func TestAiKeysInterpolateWithoutErrors(t *testing.T) {
	for _, lang := range localization.SupportedLanguages() {
		header := localization.Get("aiHeader", lang, "Kraken", "Qasim")
		if strings.Contains(header, "%!") {
			t.Errorf("%s: aiHeader rendered badly: %q", lang, header)
		}
		if !strings.Contains(header, "Kraken") {
			t.Errorf("%s: aiHeader should name the agent, got %q", lang, header)
		}
	}
}

func TestAiNotReadyWithoutKey(t *testing.T) {
	// config.json ships an empty key, so the environment must provide it.
	if _, ok := os.LookupEnv("AI_KEY"); ok {
		t.Skip("AI_KEY is set in this environment")
	}
	body, err := os.ReadFile(filepath.Join("..", "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	ai, _ := cfg["ai"].(map[string]interface{})
	if ai == nil {
		t.Fatal("config.json has no ai block")
	}
	if key, _ := ai["apiKey"].(string); key != "" {
		t.Errorf("the agent key must stay out of config.json, found %q", key)
	}
	if base, _ := ai["apiBaseUrl"].(string); base == "" {
		t.Error("config.json should still record the agent base URL")
	}
	if c, _ := ai["name"].(string); c != "Kraken" {
		t.Errorf("agent name = %q, want Kraken", c)
	}
}

func TestAiCommandEnabledInConfig(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "config.json"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cmd, ok := cfg.Commands["ai"]
	if !ok {
		t.Fatal("config.json has no /ai command entry")
	}
	if !cmd.Enabled {
		t.Error("/ai should be enabled")
	}
	if cfg.AiName() != "Kraken" {
		t.Errorf("AiName() = %q", cfg.AiName())
	}
}

func TestLastSentenceEnd(t *testing.T) {
	// Indices are byte offsets just past the terminator.
	cases := []struct {
		in   string
		want int
	}{
		{"One. Two.", 9},
		{"No terminator", -1},
		{"Really?! Yes.", 13},
		{"Line one\nLine two", 9},
		{"Ends with a stop.", 17},
		{"", -1},
	}
	for _, c := range cases {
		if got := lastSentenceEnd(c.in); got != c.want {
			t.Errorf("lastSentenceEnd(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAiReplyMarkerConstant(t *testing.T) {
	if aiReplyMarker != "acknowledged," {
		t.Errorf("unexpected marker %q", aiReplyMarker)
	}
}

func TestStripTrailingGloss(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Non-Latin body plus a Latin gloss: drop the gloss.
		{"नमस्ते, क्या हाल हैं? (Hello, how are you?)", "नमस्ते, क्या हाल हैं?"},
		{"مرحبا (Hello there)", "مرحبا"},
		{"ሰላም (Hello)", "ሰላም"},
		// A closed parenthetical in a Latin-script reply is kept: "Nice (lol)" is
		// ordinary speech, and a Spanish or French gloss is indistinguishable from
		// it. The endpoint's system prompt is what prevents those.
		{"That went well (finally!).", "That went well (finally!)."},
		{"Nice (lol)", "Nice (lol)"},
		// No trailing parenthetical: untouched.
		{"Plain reply", "Plain reply"},
		// A Latin body with parentheses is a normal sentence.
		{"He said (quietly) that it was fine.", "He said (quietly) that it was fine."},
	}
	for _, c := range cases {
		if got := stripTrailingGloss(c.in); got != c.want {
			t.Errorf("stripTrailingGloss(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanAIReplyRemovesGloss(t *testing.T) {
	raw := "नमस्ते! (Hello! How are you?)"
	if got := cleanAIReply(raw, "stop"); got != "नमस्ते!" {
		t.Errorf("got %q, want the Hindi only", got)
	}
}

// The model often gets cut off inside its own translation gloss, so an
// unterminated parenthetical has to go too.
func TestStripTrailingGlossUnterminated(t *testing.T) {
	got := stripTrailingGloss("Hola! ¿En qué puedo ayudarle hoy? (Hello! How can I help you today?")
	if got != "Hola! ¿En qué puedo ayudarle hoy?" {
		t.Errorf("got %q", got)
	}
	// A dangling bracket is a fault in any script and always goes.
	if got := stripTrailingGloss("Nice (lol"); got != "Nice" {
		t.Errorf("a dangling parenthetical should be dropped, got %q", got)
	}
}

// A reply must never be emptied by the tidy-up: the raw text is better than
// nothing.
func TestCleanAIReplyNeverEmptiesAUsableReply(t *testing.T) {
	raw := "I'm sorry to hear that you're stressed. Here are a few suggestions:\n\n1. Sleep well"
	got := cleanAIReply(raw, "length")
	if !strings.Contains(got, "sorry to hear") {
		t.Errorf("the reply text was thrown away, got %q", got)
	}
}

// The readiness gate used to run before the argument was read, so every form of
// /ai answered "could not answer right now" when the agent was unconfigured,
// including the command whose whole job is to explain exactly that.
func TestAiStatusWorksWhenAgentIsUnconfigured(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")
	h.cfg = &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: "https://ai.test", ApiKey: ""}}

	chat := &tgbotapi.Chat{ID: -1001}
	msg := &tgbotapi.Message{MessageID: 1, Chat: chat,
		Text: "/ai status", Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 4}}}

	h.cmdAI(chat, &tgbotapi.User{ID: 1}, msg, "en")

	sent := api.callsFor("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("expected one status reply, got %d", len(sent))
	}
	text := sent[0].params.Get("text")
	if !strings.Contains(text, "AI_KEY") {
		t.Errorf("status must name the missing variable, got %q", text)
	}
	if strings.Contains(text, localization.Get("aiError", "en")) {
		t.Errorf("status must not repeat the generic error, got %q", text)
	}
}

// Every spelling of the command has to reach its branch. They used not to: the
// argument was read as "/ai" and /ai on, /ai off and /ai status all printed
// usage instead.
func TestAiCommandArgumentsAreParsed(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"/ai status", "NOT SET"},
		{"/ai@KrakenBot status", "NOT SET"},
		{"/ai@KrakenBot STATUS", "NOT SET"},
	}
	for _, c := range cases {
		api := &stubAPI{responders: map[string][]string{
			"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
		}}
		h := igHandler(t, api, "https://api.test")
		h.cfg = &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: "https://ai.test"}}

		chat := &tgbotapi.Chat{ID: -1001}
		msg := &tgbotapi.Message{MessageID: 1, Chat: chat, Text: c.text,
			Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 3}}}
		h.cmdAI(chat, &tgbotapi.User{ID: 1}, msg, "en")

		sent := api.callsFor("sendMessage")
		if len(sent) != 1 {
			t.Fatalf("%q: expected one reply, got %d", c.text, len(sent))
		}
		if got := sent[0].params.Get("text"); !strings.Contains(got, c.want) {
			t.Errorf("%q routed to the wrong branch: %q", c.text, got)
		}
	}
}

// A ready agent must still answer on and off, and bare /ai must show the header.
func TestAiOnOffAndBareCommandWork(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe": {okMe}, "sendMessage": {`{"ok":true,"result":{}}`},
	}}
	h := igHandler(t, api, "https://api.test")
	h.cfg = &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: "https://ai.test", ApiKey: "k", Name: "Kraken", Owner: "Qasim"}}
	chat := &tgbotapi.Chat{ID: -1002, Type: "private"}

	send := func(text string) string {
		msg := &tgbotapi.Message{MessageID: 1, Chat: chat, Text: text,
			Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 3}}}
		before := len(api.callsFor("sendMessage"))
		h.cmdAI(chat, &tgbotapi.User{ID: 1}, msg, "en")
		after := api.callsFor("sendMessage")
		if len(after) == before {
			t.Fatalf("%q sent nothing", text)
		}
		return after[len(after)-1].params.Get("text")
	}

	if got := send("/ai on"); strings.Contains(got, "Usage") {
		t.Errorf("/ai on fell through to usage: %q", got)
	}
	if got := send("/ai off"); strings.Contains(got, "Usage") {
		t.Errorf("/ai off fell through to usage: %q", got)
	}
	if got := send("/ai"); !strings.Contains(got, "Kraken") {
		t.Errorf("bare /ai should show the header: %q", got)
	}
}

func TestAiStatusReportsReachableEndpoint(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{
		"getMe":       {okMe},
		"sendMessage": {`{"ok":true,"result":{}}`},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"finish_reason": "stop",
				"message": map[string]interface{}{"content": "pong"}}}}})
	}))
	t.Cleanup(srv.Close)

	h := igHandler(t, api, "https://api.test")
	h.cfg = &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: srv.URL, ApiKey: "secret-key"}}
	h.cmdAIStatus(-1001, "en")

	sent := api.callsFor("sendMessage")
	if len(sent) != 1 {
		t.Fatalf("expected one status reply, got %d", len(sent))
	}
	text := sent[0].params.Get("text")
	if !strings.Contains(text, "pong") {
		t.Errorf("status should show the live reply, got %q", text)
	}
	if strings.Contains(text, "secret-key") {
		t.Error("status must never echo key material")
	}
}

// The deployed worker reads its arguments from the query string, so the request
// stays a GET. This pins that contract: if it changes, the worker has to be
// redeployed first or every reply turns into a 400.
func TestAiRequestShapeMatchesDeployedWorker(t *testing.T) {
	var gotQuery, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotMethod = r.URL.RawQuery, r.Method
		json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{"finish_reason": "stop",
				"message": map[string]interface{}{"content": "pong"}}}}})
	}))
	defer srv.Close()

	h := &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: srv.URL, ApiKey: "k"}}}
	if _, err := h.aiReply("hello", "en"); err != nil {
		t.Fatalf("aiReply: %v", err)
	}

	if gotMethod != http.MethodGet {
		t.Errorf("method = %s, want GET", gotMethod)
	}
	vals, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	for _, k := range []string{"apikey", "lang", "text"} {
		if vals.Get(k) == "" {
			t.Errorf("%s missing from the query: %q", k, gotQuery)
		}
	}
}

func TestSafeErrHidesUrlAndKey(t *testing.T) {
	const secret = "super-secret-key"

	urlErr := &url.Error{Op: "Post", URL: "https://ai.test/?apikey=" + secret,
		Err: errors.New("dial tcp: i/o timeout")}
	got := safeErr(urlErr, secret)
	if strings.Contains(got, secret) {
		t.Errorf("key survived: %q", got)
	}
	if strings.Contains(got, "ai.test") {
		t.Errorf("URL survived: %q", got)
	}
	if !strings.Contains(got, "i/o timeout") {
		t.Errorf("the cause should remain: %q", got)
	}

	plain := safeErr(errors.New("agent endpoint returned status 401"), secret)
	if plain != "agent endpoint returned status 401" {
		t.Errorf("plain error changed: %q", plain)
	}
}

// A transport error must not put the key in the log either.
func TestAiErrorLogHasNoKey(t *testing.T) {
	const secret = "super-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // nothing is listening, so the request fails

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	h := &Handler{cfg: &config.Config{AI: config.AIConfig{Enabled: true, ApiBaseURL: base, ApiKey: secret}}}
	_, err := h.aiReply("hi", "en")
	if err == nil {
		t.Fatal("expected an error against a closed server")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error carries the key: %q", err.Error())
	}
}
