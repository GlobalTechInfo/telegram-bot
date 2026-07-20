package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"telegram-bot/config"
	"telegram-bot/keyboards"
	"telegram-bot/localization"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxDownloadSize = 100 << 20 // 100 MB max per download
const maxConcurrentDownloads = 2

type Handler struct {
	bot       *tgbotapi.BotAPI
	cfg       *config.Config
	store     *session.Store
	dlSem     chan struct{}
}

func New(bot *tgbotapi.BotAPI, cfg *config.Config, store *session.Store) *Handler {
	return &Handler{
		bot:   bot,
		cfg:   cfg,
		store: store,
		dlSem: make(chan struct{}, maxConcurrentDownloads),
	}
}

func (h *Handler) acquireDL() {
	h.dlSem <- struct{}{}
}

func (h *Handler) releaseDL() {
	<-h.dlSem
}

func (h *Handler) p(text string) string {
	return h.cfg.Prefix() + "\n\n" + text
}

func (h *Handler) now() string {
	loc, err := time.LoadLocation(h.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02 15:04:05 MST")
}

func (h *Handler) formatTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	loc, err := time.LoadLocation(h.cfg.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func (h *Handler) sendMsg(chatID int64, text string, markup tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewMessage(chatID, h.p(text))
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = markup
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("sendMsg error: %v", err)
	}
}

func (h *Handler) editMsg(chatID int64, msgID int, text string, markup tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageText(chatID, msgID, h.p(text))
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = &markup
	if _, err := h.bot.Send(msg); err != nil {
		if strings.Contains(err.Error(), "there is no text") {
			h.sendMsg(chatID, text, markup)
		} else {
			log.Printf("editMsg error: %v", err)
		}
	}
}

func (h *Handler) answerCb(cbID string, text string) {
	cb := tgbotapi.NewCallback(cbID, text)
	if _, err := h.bot.Request(cb); err != nil {
		log.Printf("answerCb error: %v", err)
	}
}

func (h *Handler) sendPhoto(chatID int64, photo string, caption string, markup tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewPhoto(chatID, tgbotapi.FileID(photo))
	msg.Caption = h.p(caption)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = markup
	if _, err := h.bot.Send(msg); err != nil {
		h.sendMsg(chatID, caption, markup)
	}
}

func (h *Handler) notifState(uid int64) bool {
	sess := h.store.GetOrCreate(uid)
	val, _ := sess.Data["notifications_on"].(bool)
	return val
}

func (h *Handler) setNotif(uid int64, on bool) {
	sess := h.store.GetOrCreate(uid)
	sess.Data["notifications_on"] = on
	h.store.SetSessionData(uid, sess.Data)
}

func (h *Handler) HandleCommand(update tgbotapi.Update) {
	if update.Message == nil || !update.Message.IsCommand() {
		return
	}

	chat := update.Message.Chat
	user := update.Message.From
	cmd := update.Message.Command()

	h.store.TrackUser(int64(user.ID), user.FirstName, user.LastName, user.UserName)
	sess := h.store.GetOrCreate(int64(chat.ID))
	lang := sess.Language
	uid := int64(chat.ID)

	switch cmd {
	case "start":
		ownerDisplay := h.cfg.Owner.Name
		if h.cfg.Owner.Username != "" {
			ownerDisplay += " (" + h.cfg.Owner.Username + ")"
		}
		msg := localization.Get("welcome", lang, h.cfg.Bot.Name, ownerDisplay)
		if h.cfg.Bot.StartupMessage != "" {
			msg = h.cfg.Bot.StartupMessage
		}
		markup := keyboards.MainMenu(h.cfg, lang)
		if h.cfg.Bot.Photo != "" {
			h.sendPhoto(chat.ID, h.cfg.Bot.Photo, msg, markup)
		} else {
			h.sendMsg(chat.ID, msg, markup)
		}
	case "help":
		h.sendMsg(chat.ID, localization.Get("help", lang), keyboards.Back(lang))
	case "settings":
		h.sendMsg(chat.ID, localization.Get("settings", lang), keyboards.Settings(lang, h.notifState(uid)))
	case "profile":
		msg := localization.Get("profile", lang, user.ID, user.FirstName+" "+user.LastName, lang, h.formatTime(sess.JoinedAt))
		h.sendMsg(chat.ID, msg, keyboards.Back(lang))
	case "feedback":
		h.store.SetState(uid, "awaiting_feedback")
		h.sendMsg(chat.ID, localization.Get("feedbackPrompt", lang), keyboards.Back(lang))
	case "about":
		ownerDisplay := h.cfg.Owner.Name
		if h.cfg.Owner.Username != "" {
			ownerDisplay += " (" + h.cfg.Owner.Username + ")"
		}
		msg := localization.Get("about", lang, h.cfg.Bot.Name, h.cfg.Bot.Version, h.cfg.Bot.Description,
			ownerDisplay, h.now())
		h.sendMsg(chat.ID, msg, keyboards.Back(lang))
	case "poll":
		h.store.SetState(uid, "awaiting_poll_question")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("pollQuestion", lang), keyboards.Back(lang))
	case "yt", "download":
		h.store.SetState(uid, "awaiting_yt_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("ytPrompt", lang), keyboards.Back(lang))
	case "ig", "instagram":
		h.store.SetState(uid, "awaiting_ig_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("igPrompt", lang), keyboards.Back(lang))
	case "tt", "tiktok":
		h.store.SetState(uid, "awaiting_tt_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("ttPrompt", lang), keyboards.Back(lang))
	case "fb", "facebook":
		h.store.SetState(uid, "awaiting_fb_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("fbPrompt", lang), keyboards.Back(lang))
	case "pin", "pinterest":
		h.store.SetState(uid, "awaiting_pin_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("pinPrompt", lang), keyboards.Back(lang))
	case "sc", "snapchat":
		h.store.SetState(uid, "awaiting_sc_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("scPrompt", lang), keyboards.Back(lang))
	case "tw", "twitter":
		h.store.SetState(uid, "awaiting_tw_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("twPrompt", lang), keyboards.Back(lang))
	case "bing":
		h.sendMsg(chat.ID, localization.Get("bingPrompt", lang), keyboards.BingModePicker(lang))
	case "search":
		h.sendMsg(chat.ID, localization.Get("searchMenu", lang), keyboards.SearchMenu(lang))
	case "admin":
		if !h.cfg.IsAdmin(int64(user.ID)) {
			h.sendMsg(chat.ID, localization.Get("noPermission", lang), keyboards.Back(lang))
			return
		}
		totalUsers, totalFeedback := h.store.GetStats()
		msg := localization.Get("adminPanel", lang, totalUsers, totalFeedback)
		h.sendMsg(chat.ID, msg, keyboards.Back(lang))
	default:
		h.sendMsg(chat.ID, localization.Get("unknownCommand", lang), keyboards.Back(lang))
	}
}

func (h *Handler) HandleCallback(update tgbotapi.Update) {
	if update.CallbackQuery == nil {
		return
	}

	cb := update.CallbackQuery
	chat := cb.Message.Chat
	data := cb.Data
	msgID := cb.Message.MessageID
	uid := int64(chat.ID)

	h.store.TrackUser(int64(cb.From.ID), cb.From.FirstName, cb.From.LastName, cb.From.UserName)
	sess := h.store.GetOrCreate(uid)

	switch {
	case data == "help":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("help", sess.Language), keyboards.Back(sess.Language))

	case data == "profile":
		h.answerCb(cb.ID, "")
		user := cb.From
		msg := localization.Get("profile", sess.Language, user.ID, user.FirstName+" "+user.LastName, sess.Language, h.formatTime(sess.JoinedAt))
		h.editMsg(chat.ID, msgID, msg, keyboards.Back(sess.Language))

	case data == "settings":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, h.notifState(uid)))

	case data == "settings_language":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("selectLanguage", sess.Language),
			keyboards.LanguagePicker(sess.Language))

	case data == "settings_notifications":
		h.answerCb(cb.ID, "")
		on := h.notifState(uid)
		status := localization.Get("notifDisabled", sess.Language)
		if on {
			status = localization.Get("notifEnabled", sess.Language)
		}
		h.editMsg(chat.ID, msgID, localization.Get("notifStatus", sess.Language, status),
			keyboards.SettingsNotifications(sess.Language, on))

	case data == "notif_on":
		h.answerCb(cb.ID, localization.Get("notifOnAlert", sess.Language))
		h.setNotif(uid, true)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, true))

	case data == "notif_off":
		h.answerCb(cb.ID, localization.Get("notifOffAlert", sess.Language))
		h.setNotif(uid, false)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, false))

	case data == "feedback":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_feedback")
		h.editMsg(chat.ID, msgID, localization.Get("feedbackPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "poll":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_poll_question")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("pollQuestion", sess.Language), keyboards.Back(sess.Language))

	case data == "about":
		h.answerCb(cb.ID, "")
		ownerDisplay := h.cfg.Owner.Name
		if h.cfg.Owner.Username != "" {
			ownerDisplay += " (" + h.cfg.Owner.Username + ")"
		}
		msg := localization.Get("about", sess.Language, h.cfg.Bot.Name, h.cfg.Bot.Version, h.cfg.Bot.Description,
			ownerDisplay, h.now())
		h.editMsg(chat.ID, msgID, msg, keyboards.Back(sess.Language))

	case data == "yt":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_yt_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("ytPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "ig":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_ig_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("igPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "tt":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_tt_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("ttPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "fb":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_fb_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("fbPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "pin":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_pin_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("pinPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "sc":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_sc_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("scPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "tw":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_tw_url")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("twPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "bing_search":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_bing_query")
		h.store.SetSessionData(uid, map[string]interface{}{"bing_mode": "search"})
		h.editMsg(chat.ID, msgID, localization.Get("bingQuery", sess.Language), keyboards.Back(sess.Language))

	case data == "bing_images":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_bing_query")
		h.store.SetSessionData(uid, map[string]interface{}{"bing_mode": "images"})
		h.editMsg(chat.ID, msgID, localization.Get("bingQuery", sess.Language), keyboards.Back(sess.Language))

	case strings.HasPrefix(data, "bing_cnt:"):
		h.answerCb(cb.ID, localization.Get("bingSending", sess.Language))
		countStr := strings.TrimPrefix(data, "bing_cnt:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		query, _ := sess.Data["bing_query"].(string)
		go h.fetchBingImages(chat.ID, query, countStr, sess.Language)

	case data == "search":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("searchMenu", sess.Language), keyboards.SearchMenu(sess.Language))

	case data == "textmaker":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("textMakerMenu", sess.Language), keyboards.TextMakerMenu(sess.Language))

	case data == "textpro":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_textpro_text")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("textProPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "search_pin":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_pin_search_query")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("pinSearchPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "search_sticker":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_sticker_search_query")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("stickerPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "search_imgur":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_imgur_search_query")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("imgurPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "search_yt":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "awaiting_yt_search_query")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.editMsg(chat.ID, msgID, localization.Get("ytSearchPrompt", sess.Language), keyboards.Back(sess.Language))

	case strings.HasPrefix(data, "pin_search:"):
		h.answerCb(cb.ID, localization.Get("pinSearchSending", sess.Language))
		countStr := strings.TrimPrefix(data, "pin_search:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		query, _ := sess.Data["pin_search_query"].(string)
		go h.fetchPinSearch(chat.ID, query, countStr, sess.Language)

	case strings.HasPrefix(data, "sticker_search:"):
		h.answerCb(cb.ID, localization.Get("stickerSending", sess.Language))
		countStr := strings.TrimPrefix(data, "sticker_search:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		query, _ := sess.Data["sticker_search_query"].(string)
		go h.fetchStickerSearch(chat.ID, query, countStr, sess.Language)

	case strings.HasPrefix(data, "imgur_search:"):
		h.answerCb(cb.ID, localization.Get("imgurSending", sess.Language))
		countStr := strings.TrimPrefix(data, "imgur_search:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		query, _ := sess.Data["imgur_search_query"].(string)
		go h.fetchImgurSearch(chat.ID, query, countStr, sess.Language)

	case strings.HasPrefix(data, "yt_fmt:"):
		h.answerCb(cb.ID, localization.Get("ytDownloading", sess.Language))
		format := strings.TrimPrefix(data, "yt_fmt:")
		sess = h.store.GetOrCreate(uid)
		ytURL, _ := sess.Data["yt_url"].(string)
		h.store.SetState(uid, "idle")

		go h.downloadAndSend(chat.ID, uid, ytURL, format, sess.Language)

	case strings.HasPrefix(data, "tt_fmt:"):
		h.answerCb(cb.ID, localization.Get("ttDownloading", sess.Language))
		format := strings.TrimPrefix(data, "tt_fmt:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")

		ttData, _ := sess.Data["tt_api_data"].(map[string]interface{})
		go h.downloadTikTok(chat.ID, ttData, format, sess.Language)

	case strings.HasPrefix(data, "fb_fmt:"):
		h.answerCb(cb.ID, localization.Get("fbDownloading", sess.Language))
		idxStr := strings.TrimPrefix(data, "fb_fmt:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		go h.downloadFb(chat.ID, uid, idxStr, sess.Language)

	case strings.HasPrefix(data, "sc_fmt:"):
		h.answerCb(cb.ID, localization.Get("scDownloading", sess.Language))
		idxStr := strings.TrimPrefix(data, "sc_fmt:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		go h.downloadSnap(chat.ID, uid, idxStr, sess.Language)

	case strings.HasPrefix(data, "tw_fmt:"):
		h.answerCb(cb.ID, localization.Get("twDownloading", sess.Language))
		idxStr := strings.TrimPrefix(data, "tw_fmt:")
		sess = h.store.GetOrCreate(uid)
		h.store.SetState(uid, "idle")
		go h.downloadTwitter(chat.ID, uid, idxStr, sess.Language)

	case data == "confirm_yes":
		h.answerCb(cb.ID, localization.Get("confirmedAlert", sess.Language))
		h.store.SetState(uid, "idle")
		h.editMsg(chat.ID, msgID, localization.Get("confirmAction", sess.Language), keyboards.MainMenu(h.cfg, sess.Language))

	case data == "confirm_no":
		h.answerCb(cb.ID, localization.Get("cancelledAlert", sess.Language))
		h.store.SetState(uid, "idle")
		h.editMsg(chat.ID, msgID, localization.Get("cancelAction", sess.Language), keyboards.MainMenu(h.cfg, sess.Language))

	case strings.HasPrefix(data, "set_lang:"):
		h.answerCb(cb.ID, "")
		newLang := strings.TrimPrefix(data, "set_lang:")
		h.store.SetLanguage(uid, newLang)
		h.editMsg(chat.ID, msgID, localization.Get("languageChanged", newLang, localization.LanguageName(newLang)),
			keyboards.Settings(newLang, h.notifState(uid)))

	case data == "back":
		h.answerCb(cb.ID, "")
		h.store.SetState(uid, "idle")
		sess = h.store.GetOrCreate(uid)
		h.editMsg(chat.ID, msgID, localization.Get("mainMenu", sess.Language),
			keyboards.MainMenu(h.cfg, sess.Language))

	case data == "close":
		h.answerCb(cb.ID, localization.Get("closedAlert", sess.Language))
		del := tgbotapi.NewDeleteMessage(chat.ID, msgID)
		if _, err := h.bot.Request(del); err != nil {
			log.Printf("delete error: %v", err)
		}

	default:
		h.answerCb(cb.ID, localization.Get("error", sess.Language))
	}
}

func (h *Handler) HandleMessage(update tgbotapi.Update) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}

	chat := update.Message.Chat
	user := update.Message.From
	text := update.Message.Text
	uid := int64(chat.ID)

	h.store.TrackUser(int64(user.ID), user.FirstName, user.LastName, user.UserName)
	sess := h.store.GetOrCreate(uid)
	lang := sess.Language

	switch sess.State {
	case "awaiting_feedback":
		h.store.SetState(uid, "idle")
		h.store.AddFeedback(int64(user.ID), text)
		h.sendMsg(chat.ID, localization.Get("feedbackReceived", lang), keyboards.MainMenu(h.cfg, lang))

	case "awaiting_poll_question":
		sess.Data["question"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "awaiting_poll_options")
		h.sendMsg(chat.ID, localization.Get("pollOptions", lang), keyboards.Back(lang))

	case "awaiting_poll_options":
		options := strings.Split(text, "\n")
		var clean []string
		for _, o := range options {
			o = strings.TrimSpace(o)
			if o != "" {
				clean = append(clean, o)
			}
		}
		if len(clean) < 2 || len(clean) > 10 {
			h.sendMsg(chat.ID, localization.Get("pollOptions", lang), keyboards.Back(lang))
			return
		}
		h.store.SetState(uid, "idle")
		sess = h.store.GetOrCreate(uid)
		question, _ := sess.Data["question"].(string)

		poll := tgbotapi.NewPoll(chat.ID, question, clean...)
		poll.IsAnonymous = false
		if _, err := h.bot.Send(poll); err != nil {
			log.Printf("poll error: %v", err)
			h.sendMsg(chat.ID, localization.Get("error", lang), keyboards.Back(lang))
			return
		}
		h.sendMsg(chat.ID, localization.Get("pollCreated", lang), keyboards.MainMenu(h.cfg, lang))

	case "awaiting_yt_url":
		if !isValidYouTubeURL(text) {
			h.sendMsg(chat.ID, localization.Get("ytInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["yt_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("ytFormat", lang), keyboards.YtFormatPicker(lang))

	case "awaiting_ig_url":
		if !isValidInstagramURL(text) {
			h.sendMsg(chat.ID, localization.Get("igInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["ig_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("igDownloading", lang), keyboards.Back(lang))
		go h.downloadInstagram(chat.ID, text, lang)

	case "awaiting_tt_url":
		if !isValidTikTokURL(text) {
			h.sendMsg(chat.ID, localization.Get("ttInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["tt_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("ttDownloading", lang), keyboards.Back(lang))
		go h.fetchTikTokInfo(chat.ID, uid, text, lang)

	case "awaiting_fb_url":
		if !isValidFacebookURL(text) {
			h.sendMsg(chat.ID, localization.Get("fbInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["fb_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("fbDownloading", lang), keyboards.Back(lang))
		go h.fetchFbInfo(chat.ID, uid, text, lang)

	case "awaiting_pin_url":
		if !isValidPinterestURL(text) {
			h.sendMsg(chat.ID, localization.Get("pinInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["pin_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("pinDownloading", lang), keyboards.Back(lang))
		go h.downloadPinterest(chat.ID, text, lang)

	case "awaiting_sc_url":
		if !isValidSnapchatURL(text) {
			h.sendMsg(chat.ID, localization.Get("scInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["sc_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("scDownloading", lang), keyboards.Back(lang))
		go h.fetchSnapInfo(chat.ID, uid, text, lang)

	case "awaiting_tw_url":
		if !isValidTwitterURL(text) {
			h.sendMsg(chat.ID, localization.Get("twInvalid", lang), keyboards.Back(lang))
			return
		}
		sess.Data["tw_url"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("twDownloading", lang), keyboards.Back(lang))
		go h.fetchTwitterInfo(chat.ID, uid, text, lang)

	case "awaiting_bing_query":
		if text == "" {
			return
		}
		mode, _ := sess.Data["bing_mode"].(string)
		sess.Data["bing_query"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		if mode == "images" {
			h.sendMsg(chat.ID, localization.Get("bingCount", lang), keyboards.BingCountPicker(lang))
		} else {
			go h.fetchBingSearch(chat.ID, text, lang)
		}

	case "awaiting_pin_search_query":
		if text == "" {
			return
		}
		sess.Data["pin_search_query"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("pinSearchCount", lang), keyboards.PinSearchCountPicker(lang))

	case "awaiting_sticker_search_query":
		if text == "" {
			return
		}
		sess.Data["sticker_search_query"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("stickerCount", lang), keyboards.StickerSearchCountPicker(lang))

	case "awaiting_imgur_search_query":
		if text == "" {
			return
		}
		sess.Data["imgur_search_query"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("imgurCount", lang), keyboards.ImgurSearchCountPicker(lang))

	case "awaiting_yt_search_query":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		go h.fetchYtSearch(chat.ID, text, lang)

	case "awaiting_textpro_text":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("textProSending", lang), keyboards.Back(lang))
		go h.fetchTextPro(chat.ID, text, lang)

	case "awaiting_confirm":
		h.store.SetState(uid, "idle")
		if strings.ToLower(text) == "yes" || text == "y" {
			h.sendMsg(chat.ID, localization.Get("confirmed", lang), keyboards.MainMenu(h.cfg, lang))
		} else {
			h.sendMsg(chat.ID, localization.Get("cancelled", lang), keyboards.MainMenu(h.cfg, lang))
		}

	default:
		if isValidTikTokURL(text) {
			sess.Data["tt_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("ttDownloading", lang), keyboards.Back(lang))
			go h.fetchTikTokInfo(chat.ID, uid, text, lang)
		} else if isValidFacebookURL(text) {
			sess.Data["fb_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("fbDownloading", lang), keyboards.Back(lang))
			go h.fetchFbInfo(chat.ID, uid, text, lang)
		} else if isValidPinterestURL(text) {
			sess.Data["pin_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("pinDownloading", lang), keyboards.Back(lang))
			go h.downloadPinterest(chat.ID, text, lang)
		} else if isValidSnapchatURL(text) {
			sess.Data["sc_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("scDownloading", lang), keyboards.Back(lang))
			go h.fetchSnapInfo(chat.ID, uid, text, lang)
		} else if isValidTwitterURL(text) {
			sess.Data["tw_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("twDownloading", lang), keyboards.Back(lang))
			go h.fetchTwitterInfo(chat.ID, uid, text, lang)
		} else if isValidYouTubeURL(text) {
			sess.Data["yt_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("ytFormat", lang), keyboards.YtFormatPicker(lang))
		} else if isValidInstagramURL(text) {
			sess.Data["ig_url"] = text
			h.store.SetSessionData(uid, sess.Data)
			h.sendMsg(chat.ID, localization.Get("igDownloading", lang), keyboards.Back(lang))
			go h.downloadInstagram(chat.ID, text, lang)
		} else {
			h.sendMsg(chat.ID, localization.Get("unknownCommand", lang), keyboards.MainMenu(h.cfg, lang))
		}
	}
}

func (h *Handler) HandleInline(update tgbotapi.Update) {
	if update.InlineQuery == nil {
		return
	}

	results := make([]interface{}, 0)

	helpResult := tgbotapi.NewInlineQueryResultArticle("1", "Help", "Get help with the bot")
	helpResult.Description = "Shows help message"

	aboutResult := tgbotapi.NewInlineQueryResultArticle("2", "About", fmt.Sprintf("About %s", h.cfg.Bot.Name))
	aboutResult.Description = "Learn about this bot"

	results = append(results, helpResult, aboutResult)

	conf := tgbotapi.InlineConfig{
		InlineQueryID: update.InlineQuery.ID,
		Results:       results,
		CacheTime:     0,
	}
	if _, err := h.bot.Request(conf); err != nil {
		log.Printf("inline error: %v", err)
	}
}

func extractURL(v interface{}) string {
	switch val := v.(type) {
	case string:
		if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") {
			return val
		}
	case map[string]interface{}:
		knownKeys := []string{"url", "download_url", "video_url", "media_url", "link", "file", "downloadLink", "downloadUrl"}
		for _, key := range knownKeys {
			if u := extractURL(val[key]); u != "" {
				return u
			}
		}
		for k, sub := range val {
			isKnown := false
			for _, known := range knownKeys {
				if k == known {
					isKnown = true
					break
				}
			}
			if !isKnown {
				if u := extractURL(sub); u != "" {
					return u
				}
			}
		}
	case []interface{}:
		for i := len(val) - 1; i >= 0; i-- {
			if u := extractURL(val[i]); u != "" {
				return u
			}
		}
	}
	return ""
}

func fetchMedia(apiURL string) ([]byte, string, error) {
	resp, err := http.Get(apiURL)
	if err != nil {
		return nil, "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	limitReader := io.LimitReader(resp.Body, maxDownloadSize+1)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, "", fmt.Errorf("read: %w", err)
	}

	if len(body) == 0 {
		return nil, "", fmt.Errorf("empty body")
	}
	if len(body) > maxDownloadSize {
		return nil, "", fmt.Errorf("response too large (>100MB)")
	}

	ct := resp.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") || body[0] == '{' {
		var result interface{}
		if err := json.Unmarshal(body, &result); err == nil {
			if u := extractURL(result); u != "" {
				log.Printf("Resolved media URL from JSON: %s", u)
				return fetchMedia(u)
			}
		}
		preview := string(body)
		if len(preview) > 800 {
			preview = preview[:800]
		}
		log.Printf("Full API response (%d bytes): %s", len(body), preview)
		return nil, "", fmt.Errorf("no media URL found in API response")
	}

	return body, ct, nil
}

func isValidYouTubeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be") || strings.Contains(host, "m.youtube.com")
}

func (h *Handler) downloadAndSend(chatID int64, uid int64, videoURL, format, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/loaderto/download?apiKey=%s&format=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(format), url.QueryEscape(videoURL))

	log.Printf("YT download: format=%s url=%s", format, videoURL)

	body, _, err := fetchMedia(apiURL)
	if err != nil {
		log.Printf("YT download failed: %v", err)
		h.sendMsg(chatID, localization.Get("ytError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("ytUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if format == "mp3" {
		ext = ".mp3"
	}

	fileName := fmt.Sprintf("youtube_%s_%s%s", format, time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: body}

	if format == "mp3" {
		audio := tgbotapi.NewAudio(chatID, fileBytes)
		if _, err := h.bot.Send(audio); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("ytError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("ytError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	}

	body = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("ytSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func isValidInstagramURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "instagram.com") || strings.Contains(host, "instagr.am")
}

func isValidTikTokURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "tiktok.com")
}

func isValidFacebookURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "facebook.com") || strings.Contains(host, "fb.watch") || strings.Contains(host, "fb.com")
}

func isValidPinterestURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "pinterest.com") || strings.Contains(host, "pin.it")
}

func isValidSnapchatURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "snapchat.com") || strings.Contains(host, "t.snapchat.com")
}

func isValidTwitterURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "twitter.com") || strings.Contains(host, "x.com") || strings.Contains(host, "t.co")
}

func (h *Handler) downloadInstagram(chatID int64, mediaURL, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/instagram/download?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(mediaURL))

	log.Printf("IG download: %s", mediaURL)

	body, ct, err := fetchMedia(apiURL)
	if err != nil {
		log.Printf("IG download failed: %v", err)
		h.sendMsg(chatID, localization.Get("igError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("igUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("instagram_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: body}

	if strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("igError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else if strings.Contains(ct, "image") {
		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("igError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("igError", lang), keyboards.Back(lang))
			body = nil
			return
		}
	}

	body = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("igSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchTikTokInfo(chatID int64, uid int64, videoURL, lang string) {
	apiURL := fmt.Sprintf("%s/tiktok/download?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(videoURL))

	log.Printf("TT info: %s", videoURL)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("TT API error: %v", err)
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("TT read error: %v", err)
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("TT JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("TT API returned success=false")
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("TT API returned no data")
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	title, _ := data["title"].(string)
	duration, _ := data["duration"].(string)
	region, _ := data["region"].(string)

	statsData, _ := data["stats"].(map[string]interface{})
	views := ""
	likes := ""
	comments := ""
	shares := ""
	downloads := ""
	if statsData != nil {
		views, _ = statsData["views"].(string)
		likes, _ = statsData["likes"].(string)
		comments, _ = statsData["comment"].(string)
		shares, _ = statsData["share"].(string)
		downloads, _ = statsData["download"].(string)
	}

	authorData, _ := data["author"].(map[string]interface{})
	author := ""
	if authorData != nil {
		nickname, _ := authorData["nickname"].(string)
		fullname, _ := authorData["fullname"].(string)
		if nickname != "" && nickname != fullname {
			author = fmt.Sprintf("%s (%s)", nickname, fullname)
		} else if fullname != "" {
			author = fullname
		} else {
			author = nickname
		}
	}

	sizes := ""
	if s, ok := data["size_nowm"]; ok {
		if sz, ok := toFloat64(s); ok {
			sizes = fmt.Sprintf("%.1fMB", float64(sz)/1024/1024)
		}
	}

	msg := fmt.Sprintf("📱 *TikTok Video*\n\n")
	if title != "" {
		msg += fmt.Sprintf("*Title:* %s\n", title)
	}
	if duration != "" {
		msg += fmt.Sprintf("*Duration:* %s\n", duration)
	}
	if region != "" {
		msg += fmt.Sprintf("*Region:* %s\n", region)
	}
	if author != "" {
		msg += fmt.Sprintf("*Author:* %s\n", author)
	}
	if sizes != "" {
		msg += fmt.Sprintf("*Size:* %s\n", sizes)
	}
	msg += "\n*Stats:*\n"
	if views != "" {
		msg += fmt.Sprintf("👁 %s Views", views)
	}
	if likes != "" {
		msg += fmt.Sprintf("  ❤️ %s Likes", likes)
	}
	if comments != "" {
		msg += fmt.Sprintf("\n💬 %s Comments", comments)
	}
	if shares != "" {
		msg += fmt.Sprintf("  🔄 %s Shares", shares)
	}
	if downloads != "" {
		msg += fmt.Sprintf("\n📥 %s Downloads", downloads)
	}
	msg += "\n\n*Choose format:*"

	sess := h.store.GetOrCreate(uid)
	sess.Data["tt_api_data"] = data
	h.store.SetSessionData(uid, sess.Data)

	h.sendMsg(chatID, msg, keyboards.TtFormatPicker(lang))
}

func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f := 0.0
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func (h *Handler) downloadTikTok(chatID int64, data map[string]interface{}, format, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	var mediaURL string

	switch format {
	case "wm":
		items, _ := data["data"].([]interface{})
		if len(items) > 0 {
			if item, ok := items[0].(map[string]interface{}); ok {
				mediaURL, _ = item["url"].(string)
			}
		}
	case "nowm":
		items, _ := data["data"].([]interface{})
		if len(items) > 1 {
			if item, ok := items[1].(map[string]interface{}); ok {
				mediaURL, _ = item["url"].(string)
			}
		}
	case "nowm_hd":
		items, _ := data["data"].([]interface{})
		if len(items) > 2 {
			if item, ok := items[2].(map[string]interface{}); ok {
				mediaURL, _ = item["url"].(string)
			}
		}
	case "music":
		musicInfo, _ := data["music_info"].(map[string]interface{})
		if musicInfo != nil {
			mediaURL, _ = musicInfo["url"].(string)
		}
	}

	if mediaURL == "" {
		log.Printf("TT no URL found for format=%s", format)
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	log.Printf("TT download: format=%s url=%s", format, mediaURL)

	body, ct, err := fetchMedia(mediaURL)
	if err != nil {
		log.Printf("TT download failed: %v", err)
		h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("ttUploading", lang), keyboards.Back(lang))

	isMusic := format == "music"
	ext := ".mp4"
	if isMusic {
		ext = ".mp3"
	} else if strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("tiktok_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: body}

	if isMusic {
		audio := tgbotapi.NewAudio(chatID, fileBytes)
		if _, err := h.bot.Send(audio); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else if strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("ttError", lang), keyboards.Back(lang))
			body = nil
			return
		}
	}

	body = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("ttSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchFbInfo(chatID int64, uid int64, videoURL, lang string) {
	apiURL := fmt.Sprintf("%s/fbdown/download?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(videoURL))

	log.Printf("FB info: %s", videoURL)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("FB API error: %v", err)
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("FB read error: %v", err)
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("FB JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("FB API returned success=false")
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("FB API returned no data")
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	title, _ := data["title"].(string)
	duration, _ := data["duration"].(string)
	quality, _ := data["quality"].(string)

	qualities, _ := data["allQualities"].([]interface{})

	msg := fmt.Sprintf("📘 *Facebook Video*\n\n")
	if title != "" {
		msg += fmt.Sprintf("*Title:* %s\n", title)
	}
	if duration != "" {
		msg += fmt.Sprintf("*Duration:* %s\n", duration)
	}
	if quality != "" {
		msg += fmt.Sprintf("*Quality:* %s\n", quality)
	}
	msg += "\n*Choose quality:*"

	sess := h.store.GetOrCreate(uid)
	sess.Data["fb_qualities"] = qualities
	if len(qualities) == 0 {
		sess.Data["fb_direct_url"], _ = data["download"].(string)
	}
	h.store.SetSessionData(uid, sess.Data)

	if len(qualities) > 0 {
		h.sendMsg(chatID, msg, keyboards.FbQualityPicker(qualities, lang))
	} else if directURL, ok := data["download"].(string); ok && directURL != "" {
		log.Printf("FB no quality list, using direct URL: %s", directURL)
		go h.downloadFb(chatID, uid, "-1", lang)
	} else {
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
	}
}

func (h *Handler) downloadFb(chatID int64, uid int64, idxStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	sess := h.store.GetOrCreate(uid)

	var mediaURL string
	qualities, _ := sess.Data["fb_qualities"].([]interface{})

	if idxStr == "-1" {
		mediaURL, _ = sess.Data["fb_direct_url"].(string)
	} else {
		idx := 0
		fmt.Sscanf(idxStr, "%d", &idx)
		if idx >= 0 && idx < len(qualities) {
			if item, ok := qualities[idx].(map[string]interface{}); ok {
				mediaURL, _ = item["url"].(string)
			}
		}
	}

	if mediaURL == "" {
		log.Printf("FB no URL found for idx=%s", idxStr)
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	log.Printf("FB download: idx=%s url=%s", idxStr, mediaURL)

	body, ct, err := fetchMedia(mediaURL)
	if err != nil {
		log.Printf("FB download failed: %v", err)
		h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("fbUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("facebook_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: body}

	if strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
				body = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("fbError", lang), keyboards.Back(lang))
			body = nil
			return
		}
	}

	body = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("fbSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) downloadPinterest(chatID int64, mediaURL, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/download/pinterest?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(mediaURL))

	log.Printf("Pinterest download: %s", mediaURL)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Pinterest API error: %v", err)
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Pinterest read error: %v", err)
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Pinterest JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Pinterest API returned success=false")
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Pinterest API returned no data")
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	downloadURL, _ := data["download_url"].(string)
	if downloadURL == "" {
		log.Printf("Pinterest no download_url in response")
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	log.Printf("Pinterest resolved URL: %s", downloadURL)

	dlBody, ct, err := fetchMedia(downloadURL)
	if err != nil {
		log.Printf("Pinterest download failed: %v", err)
		h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("pinUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("pinterest_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: dlBody}

	if strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else if strings.Contains(ct, "image") {
		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("pinError", lang), keyboards.Back(lang))
			dlBody = nil
			return
		}
	}

	dlBody = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("pinSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchSnapInfo(chatID int64, uid int64, mediaURL, lang string) {
	apiURL := fmt.Sprintf("%s/download/snapchat?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(mediaURL))

	log.Printf("Snapchat info: %s", mediaURL)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Snapchat API error: %v", err)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Snapchat read error: %v", err)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Snapchat JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Snapchat API returned success=false")
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Snapchat API returned no data")
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	snaps, _ := data["result"].([]interface{})
	if len(snaps) == 0 {
		log.Printf("Snapchat API returned empty result array")
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	sess := h.store.GetOrCreate(uid)
	sess.Data["sc_snaps"] = snaps
	h.store.SetSessionData(uid, sess.Data)

	if len(snaps) == 1 {
		h.sendMsg(chatID, localization.Get("scDownloading", lang), keyboards.Back(lang))
		go h.downloadSnap(chatID, uid, "0", lang)
	} else {
		h.sendMsg(chatID, localization.Get("scFormat", lang), keyboards.SnapPicker(len(snaps), lang))
	}
}

func (h *Handler) downloadSnap(chatID int64, uid int64, idxStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	sess := h.store.GetOrCreate(uid)
	snaps, _ := sess.Data["sc_snaps"].([]interface{})

	idx := 0
	fmt.Sscanf(idxStr, "%d", &idx)
	if idx < 0 || idx >= len(snaps) {
		log.Printf("Snapchat invalid index %d (len=%d)", idx, len(snaps))
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	item, ok := snaps[idx].(map[string]interface{})
	if !ok {
		log.Printf("Snapchat invalid item at index %d", idx)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	mediaURL, _ := item["video"].(string)
	if mediaURL == "" {
		mediaURL, _ = item["image"].(string)
	}
	if mediaURL == "" {
		log.Printf("Snapchat no video or image URL at index %d", idx)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	log.Printf("Snapchat download idx=%d url=%s", idx, mediaURL)

	h.sendMsg(chatID, localization.Get("scDownloading", lang), keyboards.Back(lang))

	dlBody, ct, err := fetchMedia(mediaURL)
	if err != nil {
		log.Printf("Snapchat download failed: %v", err)
		h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("scUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("snapchat_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: dlBody}

	if strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else if strings.Contains(ct, "image") {
		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("scError", lang), keyboards.Back(lang))
			dlBody = nil
			return
		}
	}

	dlBody = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("scSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func escapeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "[", "\\[")
	return s
}

func buildTwitterCaption(data map[string]interface{}) string {
	authorName, _ := data["authorName"].(string)
	authorUsername, _ := data["authorUsername"].(string)
	text, _ := data["text"].(string)
	date, _ := data["date"].(string)

	likes := 0
	retweets := 0
	replies := 0
	if v, ok := data["likes"].(float64); ok {
		likes = int(v)
	}
	if v, ok := data["retweets"].(float64); ok {
		retweets = int(v)
	}
	if v, ok := data["replies"].(float64); ok {
		replies = int(v)
	}

	author := ""
	if authorName != "" && authorUsername != "" {
		author = fmt.Sprintf("%s (@%s)", authorName, authorUsername)
	} else if authorUsername != "" {
		author = "@" + authorUsername
	} else if authorName != "" {
		author = authorName
	}

	stats := ""
	if likes > 0 || retweets > 0 || replies > 0 {
		parts := []string{}
		if likes > 0 {
			parts = append(parts, fmt.Sprintf("❤️ %d", likes))
		}
		if retweets > 0 {
			parts = append(parts, fmt.Sprintf("🔄 %d", retweets))
		}
		if replies > 0 {
			parts = append(parts, fmt.Sprintf("💬 %d", replies))
		}
		stats = strings.Join(parts, "  ")
	}

	caption := ""
	if author != "" {
		caption += "🐦 " + author + "\n"
	}
	if stats != "" {
		caption += stats + "\n"
	}
	if text != "" {
		truncated := truncate(text, 400)
		caption += "\n" + truncated + "\n"
	}
	if date != "" {
		caption += "\n📅 " + date
	}

	if len([]rune(caption)) > 1000 {
		caption = string([]rune(caption)[:1000]) + "..."
	}

	return caption
}

func (h *Handler) fetchTwitterInfo(chatID int64, uid int64, tweetURL, lang string) {
	apiURL := fmt.Sprintf("%s/twitter/download?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(tweetURL))

	log.Printf("Twitter info: %s", tweetURL)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Twitter API error: %v", err)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Twitter read error: %v", err)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Twitter JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Twitter API returned success=false")
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Twitter API returned no data")
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	media, _ := data["media"].([]interface{})
	if len(media) == 0 {
		log.Printf("Twitter API returned no media")
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	caption := buildTwitterCaption(data)

	sess := h.store.GetOrCreate(uid)
	sess.Data["tw_media"] = media
	sess.Data["tw_caption"] = caption
	h.store.SetSessionData(uid, sess.Data)

	if len(media) == 1 {
		h.sendMsg(chatID, localization.Get("twDownloading", lang), keyboards.Back(lang))
		go h.downloadTwitter(chatID, uid, "0", lang)
	} else {
		h.sendMsg(chatID, caption, keyboards.MediaPicker(len(media), "Media", "tw", lang))
	}
}

func (h *Handler) downloadTwitter(chatID int64, uid int64, idxStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	sess := h.store.GetOrCreate(uid)
	media, _ := sess.Data["tw_media"].([]interface{})
	caption, _ := sess.Data["tw_caption"].(string)

	idx := 0
	fmt.Sscanf(idxStr, "%d", &idx)
	if idx < 0 || idx >= len(media) {
		log.Printf("Twitter invalid index %d (len=%d)", idx, len(media))
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	item, ok := media[idx].(map[string]interface{})
	if !ok {
		log.Printf("Twitter invalid item at index %d", idx)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	mediaURL, _ := item["url"].(string)
	mediaType, _ := item["type"].(string)
	if mediaURL == "" {
		log.Printf("Twitter no URL at index %d", idx)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	log.Printf("Twitter download idx=%d type=%s url=%s", idx, mediaType, mediaURL)

	h.sendMsg(chatID, localization.Get("twDownloading", lang), keyboards.Back(lang))

	dlBody, ct, err := fetchMedia(mediaURL)
	if err != nil {
		log.Printf("Twitter fetch failed: %v", err)
		h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("twUploading", lang), keyboards.Back(lang))

	ext := ".mp4"
	if mediaType == "photo" || strings.Contains(ct, "image") {
		ext = ".jpg"
	}

	fileName := fmt.Sprintf("twitter_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: dlBody}

	if mediaType == "video" || strings.Contains(ct, "video") {
		video := tgbotapi.NewVideo(chatID, fileBytes)
		video.Caption = caption
		video.ParseMode = "Markdown"
		video.SupportsStreaming = true
		if _, err := h.bot.Send(video); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			doc.Caption = caption
			doc.ParseMode = "Markdown"
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else if mediaType == "photo" || strings.Contains(ct, "image") {
		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		photo.Caption = caption
		photo.ParseMode = "Markdown"
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			doc.Caption = caption
			doc.ParseMode = "Markdown"
			if _, err := h.bot.Send(doc); err != nil {
				h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
				dlBody = nil
				return
			}
		}
	} else {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		doc.Caption = caption
		doc.ParseMode = "Markdown"
		if _, err := h.bot.Send(doc); err != nil {
			h.sendMsg(chatID, localization.Get("twError", lang), keyboards.Back(lang))
			dlBody = nil
			return
		}
	}

	dlBody = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("twSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchBingSearch(chatID int64, query, lang string) {
	apiURL := fmt.Sprintf("%s/bing/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Bing search: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Bing search API error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Bing search read error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Bing search JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Bing search API returned success=false")
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Bing search no data")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	results, _ := data["results"].([]interface{})
	if len(results) == 0 {
		log.Printf("Bing search no results")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	msg := fmt.Sprintf("*🔍 Search results for:* %s\n\n", truncate(query, 100))

	var rows [][]tgbotapi.InlineKeyboardButton
	maxResults := 8
	if len(results) < maxResults {
		maxResults = len(results)
	}

	for i := 0; i < maxResults; i++ {
		item, ok := results[i].(map[string]interface{})
		if !ok {
			continue
		}
		title, _ := item["title"].(string)
		snippet, _ := item["snippet"].(string)
		link, _ := item["link"].(string)

		if title == "" {
			title = fmt.Sprintf("Result %d", i+1)
		}
		if snippet == "" {
			snippet = "No description"
		}

		msg += fmt.Sprintf("*%d.* %s\n%s\n\n", i+1, title, truncate(snippet, 150))

		if link != "" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL(fmt.Sprintf("🔗 %d", i+1), link),
			))
		}
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
	))

	h.sendMsg(chatID, msg, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (h *Handler) fetchBingImages(chatID int64, query, countStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/bing/image?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Bing images: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Bing images API error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Bing images read error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Bing images JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Bing images API returned success=false")
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Bing images no data")
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	results, _ := data["results"].([]interface{})
	if len(results) == 0 {
		log.Printf("Bing images no results")
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}

	count := 5
	if countStr == "0" {
		count = len(results)
	} else {
		fmt.Sscanf(countStr, "%d", &count)
	}
	if count < 1 {
		count = 1
	}
	if count > len(results) {
		count = len(results)
	}

	h.sendMsg(chatID, localization.Get("bingSending", lang), keyboards.Back(lang))

	sent := 0
	for i := 0; i < count; i++ {
		item, ok := results[i].(map[string]interface{})
		if !ok {
			continue
		}

		direct, _ := item["direct"].(string)
		if direct == "" {
			continue
		}

		imgBody, ct, err := fetchMedia(direct)
		if err != nil {
			log.Printf("Bing image %d fetch error: %v", i, err)
			continue
		}

		ext := ".jpg"
		if strings.Contains(ct, "png") {
			ext = ".png"
		} else if strings.Contains(ct, "gif") {
			ext = ".gif"
		} else if strings.Contains(ct, "webp") {
			ext = ".webp"
		}

		fileName := fmt.Sprintf("bing_%s_%d%s", time.Now().Format("150405"), i, ext)
		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				log.Printf("Bing image %d send error: %v", i, err)
			}
		}

		imgBody = nil
		sent++
	}

	runtime.GC()

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("bingSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchPinSearch(chatID int64, query, countStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/pinterest/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Pinterest search: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Pinterest search API error: %v", err)
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Pinterest search read error: %v", err)
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Pinterest search JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Pinterest search API returned success=false")
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
		return
	}

	results, _ := result["data"].([]interface{})
	if len(results) == 0 {
		log.Printf("Pinterest search no results")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	count := 5
	if countStr == "0" {
		count = len(results)
	} else {
		fmt.Sscanf(countStr, "%d", &count)
	}
	if count < 1 {
		count = 1
	}
	if count > len(results) {
		count = len(results)
	}

	h.sendMsg(chatID, localization.Get("pinSearchSending", lang), keyboards.Back(lang))

	sent := 0
	for i := 0; i < count; i++ {
		item, ok := results[i].(map[string]interface{})
		if !ok {
			continue
		}

		imgURL, _ := item["images_url"].(string)
		if imgURL == "" {
			continue
		}

		imgBody, ct, err := fetchMedia(imgURL)
		if err != nil {
			log.Printf("Pinterest img %d fetch error: %v", i, err)
			continue
		}

		ext := ".jpg"
		if strings.Contains(ct, "png") {
			ext = ".png"
		} else if strings.Contains(ct, "gif") {
			ext = ".gif"
		} else if strings.Contains(ct, "webp") {
			ext = ".webp"
		}

		fileName := fmt.Sprintf("pinterest_%s_%d%s", time.Now().Format("150405"), i, ext)
		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				log.Printf("Pinterest img %d send error: %v", i, err)
			}
		}

		imgBody = nil
		sent++
	}

	runtime.GC()

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("pinSearchSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchStickerSearch(chatID int64, query, countStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/stickers/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Sticker search: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Sticker search API error: %v", err)
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Sticker search read error: %v", err)
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Sticker search JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Sticker search API returned success=false")
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Sticker search no data")
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	res, _ := data["result"].(map[string]interface{})
	if res == nil {
		log.Printf("Sticker search no result")
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	resData, _ := res["data"].(map[string]interface{})
	if resData == nil {
		log.Printf("Sticker search no result data")
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}

	results, _ := resData["data"].([]interface{})
	if len(results) == 0 {
		log.Printf("Sticker search no results")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	count := 5
	if countStr == "0" {
		count = len(results)
	} else {
		fmt.Sscanf(countStr, "%d", &count)
	}
	if count < 1 {
		count = 1
	}
	if count > len(results) {
		count = len(results)
	}

	h.sendMsg(chatID, localization.Get("stickerSending", lang), keyboards.Back(lang))

	sent := 0
	for i := 0; i < count; i++ {
		item, ok := results[i].(map[string]interface{})
		if !ok {
			continue
		}

		file, _ := item["file"].(map[string]interface{})
		if file == nil {
			continue
		}

		imgURL := pickStickerURL(file)
		if imgURL == "" {
			continue
		}

		imgBody, ct, err := fetchMedia(imgURL)
		if err != nil {
			log.Printf("Sticker %d fetch error: %v", i, err)
			continue
		}

		ext := ".jpg"
		if strings.Contains(ct, "png") {
			ext = ".png"
		} else if strings.Contains(ct, "gif") {
			ext = ".gif"
		} else if strings.Contains(ct, "webp") {
			ext = ".webp"
		}

		fileName := fmt.Sprintf("sticker_%s_%d%s", time.Now().Format("150405"), i, ext)
		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				log.Printf("Sticker %d send error: %v", i, err)
			}
		}

		imgBody = nil
		sent++
	}

	runtime.GC()

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("stickerSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func pickStickerURL(file map[string]interface{}) string {
	sizes := []string{"hd", "md", "400", "320", "240"}
	formats := []string{"png", "webp", "gif"}

	for _, size := range sizes {
		sizeData, ok := file[size].(map[string]interface{})
		if !ok {
			continue
		}
		for _, format := range formats {
			formatData, ok := sizeData[format].(map[string]interface{})
			if !ok {
				continue
			}
			url, _ := formatData["url"].(string)
			if url != "" {
				return url
			}
		}
	}
	return ""
}

func (h *Handler) fetchImgurSearch(chatID int64, query, countStr, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/imgur/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Imgur search: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("Imgur search API error: %v", err)
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Imgur search read error: %v", err)
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Imgur search JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Imgur search API returned success=false")
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Imgur search no data")
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}

	results, _ := data["results"].([]interface{})
	if len(results) == 0 {
		log.Printf("Imgur search no results")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	count := 5
	if countStr == "0" {
		count = len(results)
	} else {
		fmt.Sscanf(countStr, "%d", &count)
	}
	if count < 1 {
		count = 1
	}
	if count > len(results) {
		count = len(results)
	}

	h.sendMsg(chatID, localization.Get("imgurSending", lang), keyboards.Back(lang))

	sent := 0
	for i := 0; i < count; i++ {
		item, ok := results[i].(map[string]interface{})
		if !ok {
			continue
		}

		imgURL, _ := item["link"].(string)
		if imgURL == "" {
			continue
		}

		time.Sleep(300 * time.Millisecond)
		imgBody, ct, err := fetchMedia(imgURL)
		if err != nil {
			gifURL, _ := item["link_gif"].(string)
			if gifURL == "" {
				continue
			}
			time.Sleep(300 * time.Millisecond)
			imgBody, ct, err = fetchMedia(gifURL)
			if err != nil {
				log.Printf("Imgur %d fetch error: %v", i, err)
				continue
			}
		}

		ext := ".jpg"
		if strings.Contains(ct, "png") {
			ext = ".png"
		} else if strings.Contains(ct, "gif") {
			ext = ".gif"
		} else if strings.Contains(ct, "webp") {
			ext = ".webp"
		} else if strings.Contains(ct, "mp4") {
			ext = ".mp4"
		}

		fileName := fmt.Sprintf("imgur_%s_%d%s", time.Now().Format("150405"), i, ext)
		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				log.Printf("Imgur %d send error: %v", i, err)
			}
		}

		imgBody = nil
		sent++
	}

	runtime.GC()

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("imgurSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchYtSearch(chatID int64, query, lang string) {
	apiURL := fmt.Sprintf("%s/yts/searchVideos?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("YouTube search: %s", query)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("YouTube search API error: %v", err)
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("YouTube search read error: %v", err)
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("YouTube search JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("YouTube search API returned success=false")
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("YouTube search no data")
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}

	videos, _ := data["videos"].([]interface{})
	if len(videos) == 0 {
		log.Printf("YouTube search no results")
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	totalResults, _ := data["total_results"].(float64)
	msg := fmt.Sprintf("*🎬 YouTube search:* %s", truncate(query, 100))
	if totalResults > 0 {
		msg += fmt.Sprintf(" _(results: %.0f)_", totalResults)
	}
	msg += "\n\n"

	var rows [][]tgbotapi.InlineKeyboardButton
	maxResults := 10
	if len(videos) < maxResults {
		maxResults = len(videos)
	}

	for i := 0; i < maxResults; i++ {
		item, ok := videos[i].(map[string]interface{})
		if !ok {
			continue
		}

		title, _ := item["title"].(string)
		if title == "" {
			title = fmt.Sprintf("Video %d", i+1)
		}
		title = escapeMarkdown(title)

		videoURL, _ := item["url"].(string)
		desc, _ := item["description"].(string)
		desc = escapeMarkdown(truncate(desc, 120))
		published, _ := item["published"].(string)
		published = escapeMarkdown(published)
		views, _ := item["views"].(float64)
		authorData, _ := item["author"].(map[string]interface{})
		authorName := ""
		if authorData != nil {
			authorName, _ = authorData["name"].(string)
			authorName = escapeMarkdown(authorName)
		}
		durationData, _ := item["duration"].(map[string]interface{})
		duration := ""
		if durationData != nil {
			duration, _ = durationData["timestamp"].(string)
		}

		msg += fmt.Sprintf("*%d.* %s\n", i+1, title)
		details := ""
		if views > 0 {
			details += fmt.Sprintf("👁 %.0f", views)
		}
		if duration != "" {
			if details != "" {
				details += " | "
			}
			details += fmt.Sprintf("⏱ %s", duration)
		}
		if authorName != "" {
			if details != "" {
				details += " | "
			}
			details += fmt.Sprintf("👤 %s", authorName)
		}
		if published != "" {
			if details != "" {
				details += " | "
			}
			details += published
		}
		if details != "" {
			msg += details + "\n"
		}
		if desc != "" {
			msg += truncate(desc, 120) + "\n"
		}
		msg += "\n"

		if videoURL != "" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonURL(fmt.Sprintf("▶️ %d", i+1), videoURL),
			))
		}
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
	))

	h.sendMsg(chatID, msg, tgbotapi.NewInlineKeyboardMarkup(rows...))
}

func (h *Handler) fetchTextPro(chatID int64, text, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/textpro/neonLight?apiKey=%s&text=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(text))

	log.Printf("TextPro: %s", text)

	resp, err := http.Get(apiURL)
	if err != nil {
		log.Printf("TextPro API error: %v", err)
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("TextPro read error: %v", err)
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("TextPro JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("TextPro API returned success=false")
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("TextPro no data")
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	status, _ := data["status"].(bool)
	if !status {
		log.Printf("TextPro data status=false")
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	imageURL, _ := data["image_url"].(string)
	if imageURL == "" {
		log.Printf("TextPro no image_url")
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	imgBody, ct, err := fetchMedia(imageURL)
	if err != nil {
		log.Printf("TextPro image fetch error: %v", err)
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}

	ext := ".jpg"
	if strings.Contains(ct, "png") {
		ext = ".png"
	} else if strings.Contains(ct, "gif") {
		ext = ".gif"
	} else if strings.Contains(ct, "webp") {
		ext = ".webp"
	}

	fileName := fmt.Sprintf("textpro_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

	photo := tgbotapi.NewPhoto(chatID, fileBytes)
	if _, err := h.bot.Send(photo); err != nil {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			log.Printf("TextPro send error: %v", err)
			h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
			imgBody = nil
			runtime.GC()
			return
		}
	}

	imgBody = nil
	runtime.GC()
	h.sendMsg(chatID, localization.Get("textProSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}
