package handlers

import (
	"encoding/base64"
	"log"
	"strings"

	"telegram-bot/keyboards"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Deep links.
//
// /start ignored its payload, so a shared link could not open a downloader, a
// search or a specific chat feature. The payload is a short token that names an
// action; anything unrecognised falls through to the normal welcome, so an old
// link still works.

// deepLinkToken builds the t.me payload for a destination.
func deepLinkToken(action string, args ...string) string {
	return action + "|" + strings.Join(args, "|")
}

// deepLinkURL renders the shareable link for a destination.
//
// The payload is base64url-encoded before it goes in the query. Telegram only
// allows A-Z, a-z, 0-9, _ and - in a start parameter, so the "|" separators
// (and any URL inside the token) would be dropped and the link would open the
// welcome screen instead. It also caps the parameter at 64 characters, which
// base64url does not fix on its own: a dl|<site>|<url> token with a long URL
// still overflows, and an over-long parameter is ignored just as silently.
//
// An empty result means "this destination cannot be expressed as a link", and
// the caller should show the menu instead of a link that does nothing.
func (h *Handler) deepLinkURL(action string, args ...string) string {
	user := h.cfg.Bot.Username
	if user == "" {
		return ""
	}
	tok := base64.RawURLEncoding.EncodeToString([]byte(deepLinkToken(action, args...)))
	if len(tok) > deepLinkMaxChars {
		return ""
	}
	return "https://t.me/" + user + "?start=" + tok
}

// deepLinkMaxChars is Telegram's limit on a start parameter.
const deepLinkMaxChars = 64

// parseDeepLink reads the payload back out of a /start message.
//
// The raw payload is "action|arg|arg"; when it is not in that shape it is tried as
// base64url, which is how a query is packed when the link is built by hand.
func parseDeepLink(payload string) (action string, args []string) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return "", nil
	}

	if decoded, err := base64.RawURLEncoding.DecodeString(payload); err == nil {
		if s := string(decoded); strings.Contains(s, "|") {
			payload = s
		}
	}

	parts := strings.Split(payload, "|")
	action = strings.ToLower(strings.TrimSpace(parts[0]))
	for _, p := range parts[1:] {
		if p = strings.TrimSpace(p); p != "" {
			args = append(args, p)
		}
	}
	return action, args
}

// handleDeepLink runs the action a /start payload names.
//
// It reports whether it handled the link, because an unknown payload must fall
// through to the ordinary welcome rather than confusing the person who clicked it.
func (h *Handler) handleDeepLink(chatID, uid int64, payload, lang string) bool {
	action, args := parseDeepLink(payload)
	if action == "" {
		return false
	}

	switch action {
	case "dl", "download":
		// dl|<site>|<url> downloads straight into this chat.
		if len(args) < 2 {
			h.sendMsg(chatID, localization.Get("deeplinkBadLink", lang), keyboards.MainMenu(h.cfg, lang))
			return true
		}
		d, ok := dlByCmd(args[0])
		if !ok {
			h.sendMsg(chatID, localization.Get("deeplinkUnknownSite", lang, args[0]), keyboards.MainMenu(h.cfg, lang))
			return true
		}
		h.handleDownloadURL(d, chatID, uid, args[1], lang)
		return true

	case "search":
		if len(args) < 1 || args[0] == "" {
			h.sendMsg(chatID, localization.Get("searchMenu", lang), keyboards.SearchMenu(lang))
			return true
		}
		s, ok := searcherByAlias(strings.ToLower(args[0]))
		if !ok {
			h.sendMsg(chatID, localization.Get("deeplinkUnknownSource", lang, args[0]), keyboards.MainMenu(h.cfg, lang))
			return true
		}
		go func() {
			defer h.recoverPanic()
			h.runSearcher(s, chatID, uid, strings.Join(args[1:], " "), 5, lang)
		}()
		return true

	case "chat":
		// chat|<state> jumps straight to a prompt, which is what a help article
		// or a signature line should link to.
		if len(args) < 1 {
			return false
		}
		if menu, ok := menuCommands[args[0]]; ok {
			menu(h, chatID, lang)
			return true
		}
		// A searcher id jumps to that search's own prompt, reusing the state its
		// own menu button would set, so the next message is the query.
		if state, ok := searcherStates[args[0]]; ok {
			h.store.SetState(uid, state)
			h.store.SetSessionData(uid, make(map[string]interface{}))
			h.sendMsg(chatID, localization.Get("searchQuery", lang, searcherByName(args[0])), keyboards.Back(lang))
			return true
		}
		h.sendMsg(chatID, localization.Get("deeplinkUnknownAction", lang, args[0]), keyboards.MainMenu(h.cfg, lang))
		return true
	}

	log.Printf("deeplink: unhandled action %q", action)
	return false
}

// deepLinkPayload reads the start= argument out of the command text.
//
// The library exposes StartingParam on User, but a /start payload arrives on the
// message rather than the user, so the text is read here: "/start <payload>",
// which is also the form a link copied out of a group chat carries.
func deepLinkPayload(msg *tgbotapi.Message) string {
	if msg == nil || msg.Text == "" {
		return ""
	}
	if !msg.IsCommand() {
		return ""
	}
	cmd := msg.Command()
	rest := strings.TrimPrefix(msg.Text, "/"+cmd)
	if at := strings.Index(rest, "@"); at >= 0 {
		rest = rest[at+1:]
		if sp := strings.Index(rest, " "); sp >= 0 {
			rest = rest[sp:]
		}
	}
	return strings.TrimSpace(rest)
}

// searcherStates maps a registry source to the state its menu button sets, so a
// deep link can jump into the same flow.
var searcherStates = map[string]string{
	"bing_images":    "awaiting_bing_query",
	"pin_search":     "awaiting_pin_search_query",
	"sticker_search": "awaiting_sticker_search_query",
	"imgur_search":   "awaiting_imgur_search_query",
	"yt_search":      "awaiting_yt_search_query",
	"news_google":    "awaiting_news_query",
}

// searcherByName gives a display name for a registry id, falling back to the id
// itself so an unknown one is still legible in an error.
func searcherByName(id string) string {
	if s, ok := searcherByID(id); ok {
		return s.name
	}
	return id
}
