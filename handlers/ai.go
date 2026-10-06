package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"telegram-bot/keyboards"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Telegram refuses messages longer than this; the cap is deliberately far lower
// because a chat reply is a short message, not an article.
const aiMaxReplyRunes = 1200

// aiReplyMarker is the token the model inserts when it starts repeating itself
// on open-ended prompts.
const aiReplyMarker = "acknowledged,"

// aiClient is separate from mediaClient: a chat answer should fail fast rather
// than hold a 180 second media timeout, and it keeps the two budgets apart.
var aiClient = &http.Client{Timeout: 60 * time.Second}

// aiEnvelope is the reply envelope from the agent endpoint.
type aiEnvelope struct {
	Status int `json:"status"`
	Data   struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Role    string `json:"role"`
			} `json:"message"`
		} `json:"choices"`
	} `json:"data"`
}

// aiPrompt is what the bot puts in the user message.
//
// The persona lives in the agent endpoint's system prompt, not here: instructions
// smuggled into the user turn read as conversation and get argued with. Only the
// language is repeated, because that has to travel with the text for an endpoint
// that has not been taught about the `lang` parameter.
func aiPrompt(lang, text string) string {
	if lang == "" || lang == "en" {
		return text
	}
	return fmt.Sprintf("Reply in %s.\n\n%s", localization.LanguageName(lang), text)
}

// safeErr renders an error for a log line or a chat message without leaking the
// request URL or the key. A *url.Error carries the URL it failed to fetch, and
// the key used to be part of it; /ai status also shows errors to whoever is in
// the chat, which in a group means everyone.
func safeErr(err error, key string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) {
		// The wrapped error names the cause; the outer one names the URL.
		msg = ue.Err.Error()
	}
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "***")
	}
	return msg
}

// Conversation memory.
//
// The endpoint takes a single text parameter, so context cannot be sent as a
// separate message list; it has to be rendered into the user turn. Without this
// the agent has no idea what was said before and re-introduces itself on every
// message.
const (
	// aiMemoryTurns is how many past exchanges are kept per chat.
	aiMemoryTurns = 6
	// aiMemoryRunes caps the rendered transcript, because the endpoint rejects
	// anything past 4000 characters in one text parameter.
	aiMemoryRunes = 1200
)

// aiTurn is one remembered exchange. It lives in the ai_history bucket rather
// than in sess.Data, because every command that starts a flow replaces that map
// with an empty one: storing memory there meant opening /qr threw the
// conversation away without the user asking for it.
type aiTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// aiHistory reads the remembered turns for a chat.
func (h *Handler) aiHistory(chatID int64) []aiTurn {
	raw := h.store.AIHistory(chatID)
	if len(raw) == 0 {
		return nil
	}
	var out []aiTurn
	if err := json.Unmarshal(raw, &out); err != nil {
		log.Printf("ai history parse: %v", err)
		return nil
	}
	kept := out[:0]
	for _, t := range out {
		if strings.TrimSpace(t.Text) != "" {
			kept = append(kept, t)
		}
	}
	return kept
}

// rememberAiTurn appends an exchange and trims the history back to the cap.
func (h *Handler) rememberAiTurn(chatID int64, user, reply string) {
	history := append(h.aiHistory(chatID),
		aiTurn{Role: "user", Text: user},
		aiTurn{Role: "agent", Text: reply})
	if len(history) > aiMemoryTurns {
		history = history[len(history)-aiMemoryTurns:]
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		log.Printf("ai history encode: %v", err)
		return
	}
	h.store.SetAIHistory(chatID, encoded)
}

// forgetAI clears a chat's memory, which is the point at which a user should be
// able to say the conversation is not wanted any more.
func (h *Handler) forgetAI(chatID int64) {
	h.store.ForgetAIHistory(chatID)
}

// renderAIHistory formats the remembered turns as a short transcript, newest
// last, and drops the oldest if it does not fit.
func renderAIHistory(history []aiTurn) string {
	if len(history) == 0 {
		return ""
	}
	build := func(turns []aiTurn) string {
		var b strings.Builder
		for _, t := range turns {
			who := "user"
			if t.Role == "agent" {
				who = "agent"
			}
			text := strings.ReplaceAll(strings.TrimSpace(t.Text), "\n", " ")
			if r := []rune(text); len(r) > 240 {
				text = string(r[:240]) + "..."
			}
			b.WriteString(who + ": " + text + "\n")
		}
		return b.String()
	}

	full := build(history)
	if r := []rune(full); len(r) <= aiMemoryRunes {
		return full
	}
	// Drop the oldest turns until the remainder fits.
	for i := 1; i < len(history); i++ {
		trimmed := build(history[i:])
		if r := []rune(trimmed); len(r) <= aiMemoryRunes {
			return trimmed
		}
	}
	return ""
}

// aiPromptWithHistory puts the remembered turns in front of the new message.
// The newest content has to stay last, because that is what the model answers.
//
// It deliberately does not add the language line. aiReply applies that on the way
// out, and doing it in both places doubled it on the first message of a chat
// while leaving the history prompt with the history framing only.
func aiPromptWithHistory(text string, history []aiTurn, budget int) string {
	past := renderAIHistory(history)
	if past == "" {
		return text
	}

	const (
		head = "Earlier in this chat:\n"
		tail = "\nNew message: "
	)
	// budget is the same limit aiReply truncates to, not aiTotalRunes. Those
	// differ (2000 vs 3400), so a combined frame could pass here and then be
	// cut by aiReply — and aiReply keeps the head, which is the history. The
	// user's actual question was the part that got thrown away.
	room := budget - len([]rune(head)) - len([]rune(tail)) - len([]rune(text))
	if room < 0 {
		return text
	}

	// Newest turn last, so when the frame does not fit the oldest goes first.
	for i := len(history); i > 0; i-- {
		candidate := renderAIHistory(history[len(history)-i:])
		if len([]rune(head+candidate+tail+text)) > budget {
			break
		}
		past = candidate
	}
	if past == "" {
		return text
	}
	return head + past + tail + text
}

// aiInputLimit is the effective per-request budget, matching aiReply.
func (h *Handler) aiInputLimit() int {
	if n := h.cfg.AI.MaxInputChars; n > 0 {
		return n
	}
	return aiDefaultMaxChars
}

// aiDefaultMaxChars is the fallback request budget.
const aiDefaultMaxChars = 2000

// chatWithMemory runs one exchange and remembers it.
func (h *Handler) chatWithMemory(chatID int64, text, lang string) (string, error) {
	reply, err := h.aiReply(aiPromptWithHistory(text, h.aiHistory(chatID), h.aiInputLimit()), lang)
	if err != nil {
		return "", err
	}
	h.rememberAiTurn(chatID, text, reply)
	return reply, nil
}

// aiReply asks the agent and returns its cleaned answer.
func (h *Handler) aiReply(text, lang string) (string, error) {
	base := h.cfg.EffectiveAiBaseURL()
	key := h.cfg.EffectiveAiKey()
	if base == "" || key == "" {
		return "", fmt.Errorf("AI agent is not configured")
	}
	// The same limit aiPromptWithHistory budgets against, so a framed history
	// can never arrive here already too long and get its question cut.
	maxChars := h.aiInputLimit()
	if r := []rune(text); len(r) > maxChars {
		text = string(r[:maxChars])
	}

	// The endpoint reads its arguments from the query string and is deployed that
	// way, so the request stays a GET. The key is therefore in the URL: safeErr
	// below is what keeps it out of the logs and out of any chat.
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	endpoint := base + sep + "apikey=" + url.QueryEscape(key) +
		"&lang=" + url.QueryEscape(lang) +
		"&text=" + url.QueryEscape(aiPrompt(lang, text))

	resp, err := aiClient.Get(endpoint)
	if err != nil {
		return "", errors.New(safeErr(err, key))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent endpoint returned status %d", resp.StatusCode)
	}

	raw, err := readBody(resp, maxAPISize)
	if err != nil {
		return "", err
	}

	var env aiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode agent reply: %w", err)
	}
	if len(env.Data.Choices) == 0 {
		return "", fmt.Errorf("agent reply carried no choices")
	}

	choice := env.Data.Choices[0]
	cleaned := cleanAIReply(choice.Message.Content, choice.FinishReason)
	if cleaned == "" {
		return "", fmt.Errorf("agent reply was empty")
	}
	return cleaned, nil
}

// cleanAIReply turns the raw completion into something worth sending.
//
// The model has two bad habits: repeating an answer joined by "acknowledged,"
// until it hits its token limit, and stopping mid-sentence when it does.
func cleanAIReply(content, finishReason string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}

	// Keep only the first answer when the model starts looping.
	if i := strings.Index(strings.ToLower(s), aiReplyMarker); i > 0 {
		s = strings.TrimSpace(s[:i])
	}

	s = stripTrailingGloss(s)

	// The model is a small instruct model and answers "hello" with a bulleted
	// essay. A chat reply is one short paragraph, so keep the first and stop.
	s = keepFirstParagraph(s)
	s = stripBullets(s)
	s = strings.TrimSpace(s)
	if s == "" {
		// The tidy-up went too far on an unusual reply. Better a rough answer
		// than "could not answer".
		s = strings.TrimSpace(keepFirstParagraph(content))
	}
	if s == "" {
		return ""
	}

	// A truncated completion stops mid-sentence; cut back to the last full one.
	// Done regardless of finish_reason, because rambling output ends mid-thought
	// too. Only text with no terminator at all gets an ellipsis.
	if cut := lastSentenceEnd(s); cut > 0 {
		s = strings.TrimSpace(s[:cut])
	} else if finishReason == "length" && strings.TrimSpace(s) != "" {
		s = strings.TrimSpace(s) + "..."
	}

	if r := []rune(s); len(r) > aiMaxReplyRunes {
		s = string(r[:aiMaxReplyRunes])
		s = strings.TrimSpace(s)
	}
	return s
}

// keepFirstParagraph takes the opening paragraph of a reply.
func keepFirstParagraph(s string) string {
	t := strings.TrimSpace(s)
	if i := strings.Index(t, "\n\n"); i > 0 {
		return strings.TrimSpace(t[:i])
	}
	return t
}

// stripBullets removes list markers and the short "Here are a few things:"
// lead-in the model likes to add, which read badly in a chat bubble.
//
// It must never empty the text: a long first paragraph can legitimately end in a
// colon, and dropping it would throw away the whole reply.
func stripBullets(s string) string {
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "* "), strings.HasPrefix(t, "- "),
			strings.HasPrefix(t, "• "), strings.HasPrefix(t, "1. "):
			continue
		case strings.HasSuffix(t, ":") && len([]rune(t)) <= aiLeadInMax:
			// A short line promising a list that we just removed.
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return s
	}
	return strings.Join(kept, " ")
}

// aiLeadInMax bounds what counts as a "here are some things:" lead-in rather
// than a real sentence.
const aiLeadInMax = 60

// stripTrailingGloss removes the "(and here it is in English)" tail the model
// appends to non-Latin replies no matter how firmly the prompt forbids it. Only
// a Latin-script parenthetical after a non-Latin body is removed, so an English
// reply ending in "(lol)" keeps it.
func stripTrailingGloss(s string) string {
	open := strings.LastIndex(s, " (")
	if open <= 0 {
		return s
	}
	tail := s[open+2:]
	if !strings.Contains(tail, ")") {
		// No closing bracket anywhere after it, so the model ran out of tokens
		// mid-parenthetical. That is a fault in any language, and the dangling
		// text is never something worth sending.
		return strings.TrimSpace(s[:open])
	}
	gloss := strings.TrimSuffix(tail, ")")
	body := s[:open]

	hasLatin := false
	for _, r := range gloss {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			hasLatin = true
			break
		}
	}
	hasNonLatin := false
	for _, r := range body {
		if r > 0x2FF {
			hasNonLatin = true
			break
		}
	}
	if hasLatin && hasNonLatin {
		return strings.TrimSpace(body)
	}
	return s
}

// lastSentenceEnd returns the index just past the final sentence terminator, or
// -1 when the text has none.
func lastSentenceEnd(s string) int {
	last := -1
	for i, r := range s {
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			last = i + len(string(r))
		}
	}
	return last
}

// aiState holds per-chat rate limiting. It is process-local on purpose: a
// cooldown does not deserve a database write on every message.
type aiState struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

var aiCooldown = aiState{last: make(map[int64]time.Time)}

func (s *aiState) allow(chatID int64, wait time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.last[chatID]; ok && time.Since(prev) < wait {
		return false
	}
	s.last[chatID] = time.Now()
	return true
}

// aiEnabled reports whether Kraken answers in this chat. Absent an explicit
// choice, on in private and off in groups.
func (h *Handler) aiEnabled(chatID int64, private bool) bool {
	sess := h.store.GetOrCreate(chatID)
	if v, ok := sess.Data["ai_on"].(bool); ok {
		return v
	}
	return private
}

func (h *Handler) setAi(chatID int64, on bool) {
	sess := h.store.GetOrCreate(chatID)
	sess.Data["ai_on"] = on
	h.store.SetSessionData(chatID, sess.Data)
}

// aiStateForChat resolves the effective default from the chat type, so the
// Settings button shows the state that is actually in force.
func (h *Handler) aiStateForChat(chat *tgbotapi.Chat) bool {
	if chat == nil {
		return false
	}
	return h.aiEnabled(chat.ID, chat.IsPrivate())
}

// mentionsBot reports whether a group message is aimed at the bot: an
// @mention of it, or a reply to one of its messages.
func (h *Handler) mentionsBot(msg *tgbotapi.Message) bool {
	if msg == nil {
		return false
	}
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && h.selfID != 0 &&
		msg.ReplyToMessage.From.ID == h.selfID {
		return true
	}
	for _, e := range msg.Entities {
		if e.Type != "mention" && e.Type != "text_mention" {
			continue
		}
		if e.User != nil && h.selfID != 0 && e.User.ID == h.selfID {
			return true
		}
	}
	return false
}

// maybeChatWithAI is the last step of the message fallbacks: everything that is
// not a command, a forward, a link or a pending prompt reaches Kraken.
func (h *Handler) maybeChatWithAI(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, text, lang string) {
	if strings.TrimSpace(text) == "" || user == nil {
		return
	}
	if !h.cfg.AiReady() {
		return
	}

	private := chat.IsPrivate()
	if !private && !h.mentionsBot(msg) {
		return
	}
	if !h.aiEnabled(chat.ID, private) {
		return
	}

	wait := time.Duration(h.cfg.AI.CooldownSeconds) * time.Second
	if wait <= 0 {
		wait = 3 * time.Second
	}
	if !aiCooldown.allow(chat.ID, wait) {
		return
	}

	chatID := chat.ID

	go func() {
		defer h.recoverPanic()
		// A typing bubble instead of a "thinking..." notice keeps the illusion.
		if _, err := h.bot.Request(tgbotapi.NewChatAction(chatID, "typing")); err != nil {
			log.Printf("ai typing action: %v", err)
		}

		reply, err := h.chatWithMemory(chatID, text, lang)
		if err != nil {
			log.Printf("ai error: %s", safeErr(err, h.cfg.EffectiveAiKey()))
			h.sendPlain(chatID, localization.Get("aiError", lang))
			return
		}
		h.sendPlain(chatID, reply)
	}()
}

// sendPlain sends text with no parse mode. The agent emits Markdown freely,
// which would make the bot's Markdown parser reject the whole message.
func (h *Handler) sendPlain(chatID int64, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if r := []rune(text); len(r) > aiMaxReplyRunes {
		text = string(r[:aiMaxReplyRunes])
	}
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("ai send error: %v", err)
	}
}

// cmdAI handles /ai on, /ai off and /ai status.
func (h *Handler) cmdAI(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	// Read the word after the command itself. Trimming a "/ai@" prefix never
	// matched "/ai on", so fields[0] was "/ai" and every argument, including
	// "on" and "status", fell through to the usage text.
	arg := ""
	if msg != nil && msg.IsCommand() {
		if fields := strings.Fields(msg.Text); len(fields) > 1 {
			arg = strings.ToLower(fields[1])
		}
	}

	// Status comes before the readiness gate, because being unconfigured is
	// exactly the state it exists to explain. Gating it first answered every
	// question about the agent with the same generic error.
	if arg == "status" {
		h.cmdAIStatus(chat.ID, lang)
		return
	}

	if !h.cfg.AiReady() {
		h.sendMsg(chat.ID, localization.Get("aiError", lang), keyboards.Back(lang))
		return
	}

	switch arg {
	case "on":
		h.setAi(chat.ID, true)
		h.sendMsg(chat.ID, localization.Get("aiOnMsg", lang), emptyKB)
	case "off":
		h.setAi(chat.ID, false)
		h.sendMsg(chat.ID, localization.Get("aiOffMsg", lang), emptyKB)
	case "forget":
		h.forgetAI(chat.ID)
		h.sendPlain(chat.ID, localization.Get("aiForgotten", lang))
	case "status":
		h.cmdAIStatus(chat.ID, lang)
	case "":
		private := chat.IsPrivate()
		on := h.aiEnabled(chat.ID, private)
		key := "aiOn"
		if !on {
			key = "aiOff"
		}
		h.sendMsg(chat.ID, localization.Get("aiHeader", lang, h.cfg.AiName(), h.cfg.AI.Owner)+"\n\n"+
			localization.Get(key, lang)+"\n\n"+localization.Get("aiUsage", lang), emptyKB)
	default:
		h.sendMsg(chat.ID, localization.Get("aiUsage", lang), emptyKB)
	}
}

// cmdAIStatus reports whether the agent can actually be reached.
//
// "Kraken could not answer right now" on its own cannot be acted on: one message
// covers a rejected key, a worker answering with an unexpected shape, and a model
// that returned nothing usable. This pings the endpoint with a fixed probe and
// reports which of those it actually is.
func (h *Handler) cmdAIStatus(chatID int64, lang string) {
	base := h.cfg.EffectiveAiBaseURL()
	key := h.cfg.EffectiveAiKey()

	// Name the missing variable rather than saying "not configured": that string
	// is what the owner sees when the agent is silent, and it has to tell them
	// which line of .env to add.
	var missing []string
	if !h.cfg.AI.Enabled {
		missing = append(missing, "ai.enabled=false in config.json")
	}
	if base == "" {
		missing = append(missing, "AI_BASE_URL")
	}
	if key == "" {
		missing = append(missing, "AI_KEY")
	}
	if len(missing) > 0 {
		h.sendPlain(chatID, localization.Get("aiStatus", lang,
			"NOT SET: "+strings.Join(missing, " and "), "-", "no request was sent"))
		return
	}

	host := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	// Length confirms a key arrived without ever echoing any of it.
	masked := fmt.Sprintf("set (%d chars)", len(key))

	if _, err := h.bot.Request(tgbotapi.NewChatAction(chatID, "typing")); err != nil {
		log.Printf("ai status typing action: %v", err)
	}

	reply, err := h.aiReply("Reply with the single word: pong", lang)
	if err != nil {
		// Sanitised: this text goes to the chat, so a group would otherwise see
		// the request URL and the key.
		h.sendPlain(chatID, localization.Get("aiStatus", lang, host, masked,
			"FAILED: "+safeErr(err, key)))
		return
	}
	h.sendPlain(chatID, localization.Get("aiStatus", lang, host, masked, reply))
}
