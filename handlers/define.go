package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"telegram-bot/keyboards"
	"telegram-bot/localization"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// dictionaryEntry is one response from the dictionary endpoint. The schema is
// optional at every level, so every field is checked before use.
type dictionaryEntry struct {
	Word      string `json:"word"`
	Phonetic  string `json:"phonetic"`
	Phonetics []struct {
		Text  string `json:"text"`
		Audio string `json:"audio"`
	} `json:"phonetics"`
	Meanings []struct {
		PartOfSpeech string `json:"partOfSpeech"`
		Definitions  []struct {
			Definition string `json:"definition"`
			Example    string `json:"example"`
			Synonyms   []string
		} `json:"definitions"`
		Synonyms []string `json:"synonyms"`
	} `json:"meanings"`
}

// definitionsPerWord caps how much of an entry is rendered, so a heavily defined
// word does not fill a chat.
const definitionsPerWord = 3

func (e dictionaryEntry) phoneticText() string {
	if strings.TrimSpace(e.Phonetic) != "" {
		return strings.TrimSpace(e.Phonetic)
	}
	for _, p := range e.Phonetics {
		if t := strings.TrimSpace(p.Text); t != "" {
			return t
		}
	}
	return ""
}

// audioLink returns the first pronunciation audio the entry offers.
func (e dictionaryEntry) audioLink() string {
	for _, p := range e.Phonetics {
		if strings.HasPrefix(p.Audio, "http") {
			return p.Audio
		}
	}
	return ""
}

// defineEnabled reports whether the dictionary lookup is configured. The endpoint
// is config-driven because it is the one integration that is not part of the
// media API, so an operator can point it elsewhere or switch it off.
func (h *Handler) defineEnabled() bool {
	return h.cfg.Tools.Define.Enabled && strings.TrimSpace(h.cfg.Tools.Define.Endpoint) != ""
}

func (h *Handler) showDefineMenu(chatID int64, lang string) {
	if !h.defineEnabled() {
		h.sendMsg(chatID, localization.Get("unknownCommand", lang), keyboards.Back(lang))
		return
	}
	h.store.SetState(chatID, "awaiting_define_word")
	h.store.SetSessionData(chatID, make(map[string]interface{}))
	h.sendMsg(chatID, localization.Get("definePrompt", lang), keyboards.Back(lang))
}

// handleDefineState consumes the word typed after /define.
func (h *Handler) handleDefineState(chatID int64, text, lang string) {
	h.store.SetState(chatID, "idle")
	word := strings.TrimSpace(text)
	if word == "" {
		h.sendMsg(chatID, localization.Get("definePrompt", lang), keyboards.Back(lang))
		return
	}
	h.fetchDefine(chatID, chatID, word, lang)
}

// fetchDefine looks a word up and renders the definitions.
func (h *Handler) fetchDefine(chatID, uid int64, word, lang string) {
	endpoint := strings.TrimSpace(h.cfg.Tools.Define.Endpoint)
	word = strings.TrimSpace(word)
	if len([]rune(word)) > defineMaxRunes {
		word = string([]rune(word)[:defineMaxRunes])
	}

	apiURL := endpoint + url.PathEscape(word)
	body, status, err := h.apiGetStatus(apiURL)
	if err != nil {
		log.Printf("define: request failed for %q: %v", word, err)
		h.sendMsg(chatID, localization.Get("defineError", lang), keyboards.Back(lang))
		return
	}
	if status == http.StatusNotFound {
		// The endpoint answered and said it has no such word.
		h.sendMsg(chatID, localization.Get("defineNotFound", lang, escapeMarkdown(word)), keyboards.Back(lang))
		return
	}

	entries, ok := parseDefine(body)
	if !ok {
		// A 404 from a dictionary endpoint means the word is not in it, which is
		// a normal answer rather than a failure.
		log.Printf("define: no entry for %q", word)
		h.sendMsg(chatID, localization.Get("defineNotFound", lang, escapeMarkdown(word)), keyboards.Back(lang))
		return
	}
	if len(entries) == 0 {
		h.sendMsg(chatID, localization.Get("defineNotFound", lang, escapeMarkdown(word)), keyboards.Back(lang))
		return
	}

	for _, e := range entries {
		h.sendDefineEntry(chatID, e, lang)
	}
	h.logQuery(uid, "define", word)
}

// defineMaxRunes bounds the lookup so a pasted paragraph cannot become a request
// path.
const defineMaxRunes = 64

func (h *Handler) sendDefineEntry(chatID int64, e dictionaryEntry, lang string) {
	heading := e.Word
	if heading == "" {
		heading = "?"
	}
	if ph := e.phoneticText(); ph != "" {
		heading += " " + ph
	}

	var body strings.Builder
	shown := 0
	for _, m := range e.Meanings {
		if shown >= definitionsPerWord {
			break
		}
		for _, d := range m.Definitions {
			if shown >= definitionsPerWord {
				break
			}
			if strings.TrimSpace(d.Definition) == "" {
				continue
			}
			label := m.PartOfSpeech
			if label == "" {
				label = "•"
			}
			body.WriteString("\n" + h.p(localization.Get("definePartOfSpeech", lang, label)))
			body.WriteString(escapeMarkdown(d.Definition))
			if ex := strings.TrimSpace(d.Example); ex != "" {
				body.WriteString("\n" + escapeMarkdown(localization.Get("defineExample", lang, escapeMarkdown(ex))))
			}
			if syn := defineSynonyms(m.Synonyms); syn != "" {
				body.WriteString("\n" + escapeMarkdown(localization.Get("defineSynonyms", lang, escapeMarkdown(syn))))
			}
			shown++
		}
	}

	if shown == 0 {
		return
	}

	msg := tgbotapi.NewMessage(chatID, h.p(localization.Get("defineDef", lang, escapeMarkdown(heading), body.String())))
	if audio := e.audioLink(); audio != "" {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				// URL button: a pronunciation URL far exceeds the 64-byte
				// callback_data limit, which cost the whole definition card.
				tgbotapi.NewInlineKeyboardButtonURL(localization.Get("definePronounce", lang), audio),
			),
		)
	}
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("define send: %v", err)
	}
}

// defineSynonyms picks a short comma list, dropping the entry's own word and
// anything too long to be a synonym.
func defineSynonyms(in []string) string {
	var kept []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 24 || len(kept) >= 5 {
			continue
		}
		kept = append(kept, s)
	}
	return strings.Join(kept, ", ")
}

// parseDefine reads the endpoint's response. A dictionary returns either a list of
// entries or an error object, and the second shape must not be read as an empty
// list.
func parseDefine(body []byte) ([]dictionaryEntry, bool) {
	var entries []dictionaryEntry
	if err := json.Unmarshal(body, &entries); err == nil && entries != nil {
		return entries, true
	}
	var failure struct {
		Title string `json:"title"`
		Msg   string `json:"message"`
	}
	if err := json.Unmarshal(body, &failure); err == nil && (failure.Title != "" || failure.Msg != "") {
		return nil, false
	}
	// Unrecognised body: treat as no result rather than rendering a blank card.
	return nil, false
}

// startTranslate remembers the text to translate and asks for a target language.
func (h *Handler) startTranslate(chatID, uid int64, lang, text string) {
	sess := h.store.GetOrCreate(uid)
	sess.Data["tr_text"] = text
	h.store.SetSessionData(uid, sess.Data)
	h.store.SetState(uid, "awaiting_translate_lang")

	// When the text is already there the only missing piece is the language, so
	// the picker is shown and nothing else has to be typed.
	h.sendMsg(chatID, localization.Get("translatePickLang", lang), keyboards.TranslateLangPicker(lang))
}

// handleTranslateState runs whichever half of /translate the user is at: choosing
// a language, or supplying the text.
func (h *Handler) handleTranslateState(chat *tgbotapi.Chat, msg *tgbotapi.Message, uid int64, lang string) {
	sess := h.store.GetOrCreate(uid)

	switch sess.State {
	case "awaiting_translate_lang":
		h.store.SetState(uid, "idle")
		text, _ := sess.Data["tr_text"].(string)
		// The target comes from the picker, not from lang. lang is the chat's
		// interface language, so typing "urdu" instead of tapping the button used
		// to translate into whatever the chat was set to — usually English.
		target := h.translateTargetOf(sess)
		if text == "" || target == "" {
			h.store.SetState(uid, "awaiting_translate_text")
			h.store.SetSessionData(uid, make(map[string]interface{}))
			h.sendMsg(chat.ID, localization.Get("translatePrompt", lang), keyboards.TranslateLangPicker(lang))
			return
		}
		go h.fetchTranslate(chat.ID, uid, text, target, lang)

	case "awaiting_translate_text":
		h.store.SetState(uid, "idle")
		text := strings.TrimSpace(msg.Text)
		if reply := replyTextOf(msg); reply != "" {
			// A reply while the prompt is up means translate that instead.
			text = reply
		}
		if text == "" {
			h.store.SetState(uid, "awaiting_translate_text")
			h.sendMsg(chat.ID, localization.Get("translatePrompt", lang), keyboards.TranslateLangPicker(lang))
			return
		}
		go h.fetchTranslate(chat.ID, uid, text, h.translateTargetOf(sess), lang)
	}
}

// translateTargetOf returns the language chosen from the picker, defaulting to the
// chat's own language pair when none was chosen.
func (h *Handler) translateTargetOf(sess *session.SessionData) string {
	if t, _ := sess.Data["tr_target"].(string); t != "" {
		return t
	}
	return "en"
}

// replyTextOf pulls the text out of whatever the user replied to.
func replyTextOf(msg *tgbotapi.Message) string {
	if msg == nil || msg.ReplyToMessage == nil {
		return ""
	}
	r := msg.ReplyToMessage
	switch {
	case r.Text != "":
		return strings.TrimSpace(r.Text)
	case r.Caption != "":
		return strings.TrimSpace(r.Caption)
	}
	return ""
}
