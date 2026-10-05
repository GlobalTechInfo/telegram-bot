package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"telegram-bot/config"
	"telegram-bot/keyboards"
	"telegram-bot/localization"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxDownloadSize = 100 << 20 // 100 MB max per download
const maxAPISize = 8 << 20        // 8 MB max for JSON API responses
const maxConcurrentDownloads = 2

// Instagram carousel delivery: albums of 10 (Telegram's cap), at most 50 items
// per post, and never more than 40MB of images held for one album.
const igMaxItems = 50
const igAlbumSize = 10
const igAlbumMaxBytes = 40 << 20
const igAlbumPause = 700 * time.Millisecond

// Telegram deletes at most 100 messages per call and throttles bulk deletes to
// roughly 30 per second in groups, so batches stay small and paced.
const delBatchMax = 100
const delBatchSize = 20
const delBatchRetries = 3
const delBatchPause = 1200 * time.Millisecond
const delSinglePause = 90 * time.Millisecond

type Handler struct {
	bot    *tgbotapi.BotAPI
	cfg    *config.Config
	store  *session.Store
	dlSem  chan struct{}
	token  string
	selfID int64
}

func New(bot *tgbotapi.BotAPI, cfg *config.Config, store *session.Store, token string) *Handler {
	selfID := int64(0)
	if u, err := bot.GetMe(); err == nil {
		selfID = u.ID
	}
	return &Handler{
		bot:    bot,
		cfg:    cfg,
		store:  store,
		dlSem:  make(chan struct{}, maxConcurrentDownloads),
		selfID: selfID,
		token:  token,
	}
}

var emptyKB tgbotapi.InlineKeyboardMarkup

// One client for every outbound request. The stdlib default has no timeout at
// all, so a single stalled CDN connection used to pin a goroutine and a
// download slot forever.
var mediaClient = &http.Client{
	Timeout: 180 * time.Second,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
	},
}

// readBody reads at most limit bytes, failing early when Content-Length already
// exceeds it. Pre-allocating from the header avoids the repeated doubling that
// io.ReadAll does on the way to a 100MB buffer.
func readBody(resp *http.Response, limit int) ([]byte, error) {
	if resp.ContentLength > int64(limit) {
		return nil, fmt.Errorf("response too large (%d bytes, limit %d)", resp.ContentLength, limit)
	}
	var buf bytes.Buffer
	if resp.ContentLength > 0 {
		buf.Grow(int(resp.ContentLength))
	}
	n, err := io.CopyN(&buf, resp.Body, int64(limit)+1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if n > int64(limit) {
		return nil, fmt.Errorf("response too large (>%d bytes)", limit)
	}
	return buf.Bytes(), nil
}

func (h *Handler) apiGet(apiURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mediaClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return readBody(resp, maxDownloadSize)
}

func (h *Handler) downloadFile(f tgbotapi.File) ([]byte, error) {
	resp, err := mediaClient.Get(f.Link(h.token))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return readBody(resp, maxDownloadSize)
}

func (h *Handler) recoverPanic() {
	if r := recover(); r != nil {
		log.Printf("🔥 Panic recovered: %v", r)
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
	if len(markup.InlineKeyboard) > 0 {
		msg.ReplyMarkup = markup
	}
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("sendMsg error: %v", err)
	}
}

func (h *Handler) editMsg(chatID int64, msgID int, text string, markup tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageText(chatID, msgID, h.p(text))
	msg.ParseMode = "Markdown"
	if len(markup.InlineKeyboard) > 0 {
		msg.ReplyMarkup = &markup
	}
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
	if len(markup.InlineKeyboard) > 0 {
		msg.ReplyMarkup = markup
	}
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
		h.sendMsg(chat.ID, localization.Get("settings", lang),
			keyboards.Settings(lang, h.notifState(uid), h.aiStateForChat(chat), h.cfg.AiName()))
	case "profile":
		msg := localization.Get("profile", lang, user.ID, user.FirstName+" "+user.LastName, lang, h.formatTime(sess.JoinedAt))
		h.sendMsg(chat.ID, msg, keyboards.Back(lang))
	case "feedback":
		h.store.SetState(uid, "awaiting_feedback")
		h.sendMsg(chat.ID, localization.Get("feedbackPrompt", lang), keyboards.Back(lang))
	case "about":
		msg := localization.Get("about", lang, h.cfg.Bot.Name, h.cfg.Bot.Version, h.cfg.Bot.Description,
			h.cfg.Owner.Name, h.cfg.Owner.Username, h.now())
		h.sendMsg(chat.ID, msg, keyboards.Back(lang))
	case "poll":
		h.store.SetState(uid, "awaiting_poll_question")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("pollQuestion", lang), keyboards.Back(lang))
	case "bing":
		h.sendMsg(chat.ID, localization.Get("bingPrompt", lang), keyboards.BingModePicker(lang))
	case "search":
		h.sendMsg(chat.ID, localization.Get("searchMenu", lang), keyboards.SearchMenu(lang))
	case "qr":
		h.store.SetState(uid, "awaiting_qr_text")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("qrPrompt", lang), keyboards.Back(lang))
	case "weather":
		h.store.SetState(uid, "awaiting_weather_city")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("weatherPrompt", lang), keyboards.Back(lang))
	case "translate":
		h.store.SetState(uid, "awaiting_translate_text")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("translatePrompt", lang), keyboards.TranslateLangPicker(lang))
	case "convert":
		h.store.SetState(uid, "awaiting_convert")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("convertPrompt", lang), keyboards.Back(lang))
	case "meme":
		h.sendMsg(chat.ID, localization.Get("memeSelectTemplate", lang), keyboards.MemeMenu(lang))
	case "reddit":
		h.store.SetState(uid, "awaiting_reddit_sub")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("redditPrompt", lang), keyboards.Back(lang))
	case "remind":
		if len(strings.Fields(update.Message.Text)) < 2 {
			h.cmdRemind(chat, user, lang)
			return
		}
		h.store.SetState(uid, "awaiting_remind")
		h.store.SetSessionData(uid, make(map[string]interface{}))
		h.sendMsg(chat.ID, localization.Get("remindPrompt", lang), keyboards.Back(lang))
	case "history":
		h.cmdHistory(chat, user, lang)
	case "ban", "kick", "mute", "unban", "promote", "admin_add", "demote", "admin_remove":
		switch cmd {
		case "ban":
			h.cmdBan(chat, user, lang)
		case "unban":
			h.cmdUnban(chat, user, update.Message, lang)
		case "kick":
			h.cmdKick(chat, user, update.Message, lang)
		case "mute":
			h.cmdMute(chat, user, update.Message, lang)
		case "promote", "admin_add":
			h.cmdPromote(chat, user, update.Message, lang)
		case "demote", "admin_remove":
			h.cmdDemote(chat, user, update.Message, lang)
		}
	case "ai":
		h.cmdAI(chat, user, update.Message, lang)
	case "del", "purge":
		h.cmdDel(chat, user, lang)
	case "invite", "link":
		h.cmdInvite(chat, user, lang)
	case "welcome":
		h.cmdWelcome(chat, user, lang)
	case "groups":
		h.cmdGroups(user, lang)
	case "channels":
		h.cmdChannels(user, lang)
	case "ginfo":
		h.cmdGInfo(chat, user, lang, "")
	case "gsetname":
		h.cmdGInfo(chat, user, lang, "name")
	case "gsetdesc":
		h.cmdGInfo(chat, user, lang, "desc")
	case "gsettings":
		h.cmdGSettings(chat, user, lang, "")
	case "lockdown":
		h.cmdGSettings(chat, user, lang, "lockdown")
	case "antilinks":
		h.cmdGSettings(chat, user, lang, "antilinks")
	case "anticaps":
		h.cmdGSettings(chat, user, lang, "anticaps")
	case "moderate":
		h.cmdModerate(chat, user, lang, "")
	case "gstats":
		h.cmdGStats(chat, user, lang)
	case "chsettings":
		h.cmdChSettings(chat, user, lang)
	case "chstats":
		h.cmdChStats(chat, user, lang)
	case "stream":
		h.cmdStream(chat, user, lang)
	case "post":
		h.cmdPost(chat, user, lang)
	case "admin":
		if !h.cfg.IsAdmin(int64(user.ID)) {
			h.sendMsg(chat.ID, localization.Get("noPermission", lang), keyboards.Back(lang))
			return
		}
		totalUsers, totalFeedback := h.store.GetStats()
		msg := localization.Get("adminPanel", lang, totalUsers, totalFeedback)
		kb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(localization.Get("viewFeedback", lang), "admin_feedback"),
			),
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
			),
		)
		h.sendMsg(chat.ID, msg, kb)
	default:
		if d, ok := dlByCmd(cmd); ok {
			h.store.SetState(uid, dlState(d.id))
			h.store.SetSessionData(uid, make(map[string]interface{}))
			h.sendMsg(chat.ID, dlText(d, "Prompt", lang), keyboards.Back(lang))
			return
		}
		h.sendMsg(chat.ID, localization.Get("unknownCommand", lang), keyboards.Back(lang))
	}
}

// downloadMenu lists every enabled downloader, preferring its translated button
// label when one exists.
func (h *Handler) downloadMenu(lang string) tgbotapi.InlineKeyboardMarkup {
	entries := make([]keyboards.DownloadEntry, 0, len(downloaders))
	for _, d := range downloaders {
		label := d.name
		if localization.Has(d.id+"Menu", lang) {
			label = localization.Get(d.id+"Menu", lang)
		}
		entries = append(entries, keyboards.DownloadEntry{ID: d.id, Label: label, Enabled: h.cfg.IsCommandEnabled(d.id)})
	}
	return keyboards.DownloadMenu(entries, lang)
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
			keyboards.Settings(sess.Language, h.notifState(uid), h.aiStateForChat(chat), h.cfg.AiName()))

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

	case data == "settings_ai":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.SettingsAI(sess.Language, h.aiStateForChat(chat), h.cfg.AiName()))

	case data == "ai_on":
		h.answerCb(cb.ID, localization.Get("aiOn", sess.Language))
		h.setAi(chat.ID, true)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, h.notifState(uid), true, h.cfg.AiName()))

	case data == "ai_off":
		h.answerCb(cb.ID, localization.Get("aiOff", sess.Language))
		h.setAi(chat.ID, false)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, h.notifState(uid), false, h.cfg.AiName()))

	case data == "notif_on":
		h.answerCb(cb.ID, localization.Get("notifOnAlert", sess.Language))
		h.setNotif(uid, true)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, true, h.aiStateForChat(chat), h.cfg.AiName()))

	case data == "notif_off":
		h.answerCb(cb.ID, localization.Get("notifOffAlert", sess.Language))
		h.setNotif(uid, false)
		h.editMsg(chat.ID, msgID, localization.Get("settings", sess.Language),
			keyboards.Settings(sess.Language, false, h.aiStateForChat(chat), h.cfg.AiName()))

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
		msg := localization.Get("about", sess.Language, h.cfg.Bot.Name, h.cfg.Bot.Version, h.cfg.Bot.Description,
			h.cfg.Owner.Name, h.cfg.Owner.Username, h.now())
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
		h.editMsg(chat.ID, msgID, localization.Get("textProMenu", sess.Language), keyboards.TextProMenu(sess.Language))

	case strings.HasPrefix(data, "textpro:"):
		h.answerCb(cb.ID, "")
		parts := strings.SplitN(strings.TrimPrefix(data, "textpro:"), ":", 2)
		if len(parts) != 2 {
			h.answerCb(cb.ID, localization.Get("error", sess.Language))
			return
		}
		effect := parts[0]
		textCount := parts[1]
		h.store.SetState(uid, "awaiting_textpro_text1")
		h.store.SetSessionData(uid, map[string]interface{}{"textpro_effect": effect, "textpro_count": textCount})
		h.editMsg(chat.ID, msgID, localization.Get("textProPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "photooxy":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("photooxyMenu", sess.Language), keyboards.PhotooxyMenu(sess.Language))

	case strings.HasPrefix(data, "photooxy:"):
		h.answerCb(cb.ID, "")
		parts := strings.SplitN(strings.TrimPrefix(data, "photooxy:"), ":", 2)
		if len(parts) != 2 {
			h.answerCb(cb.ID, localization.Get("error", sess.Language))
			return
		}
		effect := parts[0]
		textCount := parts[1]
		h.store.SetState(uid, "awaiting_photooxy_text1")
		h.store.SetSessionData(uid, map[string]interface{}{"photooxy_effect": effect, "photooxy_count": textCount})
		h.editMsg(chat.ID, msgID, localization.Get("textProPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "ephoto":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("ephotoMenu", sess.Language), keyboards.EphotoMenu(sess.Language))

	case strings.HasPrefix(data, "ephoto:"):
		h.answerCb(cb.ID, "")
		parts := strings.SplitN(strings.TrimPrefix(data, "ephoto:"), ":", 2)
		if len(parts) != 2 {
			h.answerCb(cb.ID, localization.Get("error", sess.Language))
			return
		}
		effect := parts[0]
		textCount := parts[1]
		h.store.SetState(uid, "awaiting_ephoto_text1")
		h.store.SetSessionData(uid, map[string]interface{}{"ephoto_effect": effect, "ephoto_count": textCount})
		h.editMsg(chat.ID, msgID, localization.Get("textProPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "shorturl":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("shortUrlMenu", sess.Language), keyboards.ShortUrlMenu(sess.Language))

	case strings.HasPrefix(data, "shorturl:"):
		h.answerCb(cb.ID, "")
		service := strings.TrimPrefix(data, "shorturl:")
		switch service {
		case "reurl":
			h.store.SetState(uid, "awaiting_reurl_url")
			h.editMsg(chat.ID, msgID, localization.Get("reurlPrompt", sess.Language), keyboards.Back(sess.Language))
		case "tinycc":
			h.store.SetState(uid, "awaiting_tinycc_url")
			h.editMsg(chat.ID, msgID, localization.Get("tinyccPrompt", sess.Language), keyboards.Back(sess.Language))
		case "itsssl":
			h.store.SetState(uid, "awaiting_itsssl_url")
			h.editMsg(chat.ID, msgID, localization.Get("itssslPrompt", sess.Language), keyboards.Back(sess.Language))
		case "cuqin":
			h.store.SetState(uid, "awaiting_cuqin_url")
			h.editMsg(chat.ID, msgID, localization.Get("cuqinPrompt", sess.Language), keyboards.Back(sess.Language))
		case "vurl":
			h.store.SetState(uid, "awaiting_vurl_url")
			h.editMsg(chat.ID, msgID, localization.Get("vurlPrompt", sess.Language), keyboards.Back(sess.Language))
		case "tiny":
			h.store.SetState(uid, "awaiting_tiny_url")
			h.editMsg(chat.ID, msgID, localization.Get("tinyPrompt", sess.Language), keyboards.Back(sess.Language))
		default:
			h.answerCb(cb.ID, localization.Get("error", sess.Language))
		}

	case data == "news":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("newsMenu", sess.Language), keyboards.NewsMenu(sess.Language))

	case strings.HasPrefix(data, "news:"):
		h.answerCb(cb.ID, "")
		service := strings.TrimPrefix(data, "news:")
		switch service {
		case "google":
			h.store.SetState(uid, "awaiting_news_query")
			h.editMsg(chat.ID, msgID, localization.Get("newsPrompt", sess.Language), keyboards.Back(sess.Language))
		case "bbc":
			h.editMsg(chat.ID, msgID, localization.Get("newsSending", sess.Language), keyboards.Back(sess.Language))
			go h.fetchBbcNews(chat.ID, sess.Language)
		case "cnn":
			h.editMsg(chat.ID, msgID, localization.Get("newsSending", sess.Language), keyboards.Back(sess.Language))
			go h.fetchCnnNews(chat.ID, sess.Language)
		case "aljazeera":
			h.editMsg(chat.ID, msgID, localization.Get("newsSending", sess.Language), keyboards.Back(sess.Language))
			go h.fetchAljazeeraNews(chat.ID, sess.Language)
		case "cgtn":
			h.editMsg(chat.ID, msgID, localization.Get("newsSending", sess.Language), keyboards.Back(sess.Language))
			go h.fetchCgtnNews(chat.ID, sess.Language)
		case "trt":
			h.editMsg(chat.ID, msgID, localization.Get("newsSending", sess.Language), keyboards.Back(sess.Language))
			go h.fetchTrtNews(chat.ID, sess.Language)
		}

	case data == "sports":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("sportsMenu", sess.Language), keyboards.SportsMenu(sess.Language))

	case data == "imageeffect":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("imageEffectMenu", sess.Language), keyboards.ImageEffectMenu(sess.Language))

	case strings.HasPrefix(data, "imageeffect:"):
		h.answerCb(cb.ID, "")
		effect := strings.TrimPrefix(data, "imageeffect:")
		h.store.SetState(uid, "awaiting_image_effect")
		h.store.SetSessionData(uid, map[string]interface{}{"image_effect": effect})
		h.editMsg(chat.ID, msgID, localization.Get("imageEffectPrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "artistic":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("artisticMenu", sess.Language), keyboards.ArtisticEffectMenu(sess.Language))

	case strings.HasPrefix(data, "artistic:"):
		h.answerCb(cb.ID, "")
		effect := strings.TrimPrefix(data, "artistic:")
		h.store.SetState(uid, "awaiting_artistic_effect")
		h.store.SetSessionData(uid, map[string]interface{}{"image_effect": effect})
		h.editMsg(chat.ID, msgID, localization.Get("artisticPrompt", sess.Language), keyboards.Back(sess.Language))

	case strings.HasPrefix(data, "sports:"):
		h.answerCb(cb.ID, "")
		service := strings.TrimPrefix(data, "sports:")
		h.editMsg(chat.ID, msgID, localization.Get("sportsSending", sess.Language), keyboards.Back(sess.Language))
		switch service {
		case "cricket":
			go h.fetchCricket(chat.ID, sess.Language)
		case "nfl":
			go h.fetchNfl(chat.ID, sess.Language)
		case "nba":
			go h.fetchNba(chat.ID, sess.Language)
		case "cricbuzz":
			go h.fetchCricbuzz(chat.ID, sess.Language)
		}

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

	case dlIsPick(data):
		d, sel := dlSplitPick(data)
		h.answerCb(cb.ID, dlText(d, "Working", sess.Language))
		h.handleDownloadPick(d, chat.ID, uid, sel, sess.Language)

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
			keyboards.Settings(newLang, h.notifState(uid), h.aiStateForChat(chat), h.cfg.AiName()))

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

	case data == "tools_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("toolsTitle", sess.Language), keyboards.ToolsMenu(h.cfg, sess.Language))

	case data == "dl_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("downloadTitle", sess.Language), h.downloadMenu(sess.Language))

	case data == "create_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("createTitle", sess.Language), keyboards.CreateMenu(sess.Language))

	case data == "more_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("moreTitle", sess.Language), keyboards.MoreMenu(h.cfg, sess.Language))

	case data == "search_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("searchMenu", sess.Language), keyboards.SearchMenu(sess.Language))

	case data == "groups_menu":
		h.answerCb(cb.ID, "")
		h.editMsg(chat.ID, msgID, localization.Get("groupsTitle", sess.Language), keyboards.GroupMenu(h.cfg, sess.Language))

	case strings.HasPrefix(data, "dl_"):
		h.answerCb(cb.ID, "")
		cmd := strings.TrimPrefix(data, "dl_")
		d, ok := dlByID(cmd)
		if !ok {
			h.answerCb(cb.ID, localization.Get("error", sess.Language))
			return
		}
		sess = h.store.GetOrCreate(uid)
		sess.Data = make(map[string]interface{})
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, dlState(d.id))
		h.editMsg(chat.ID, msgID, dlText(d, "Prompt", sess.Language), keyboards.Back(sess.Language))

	case strings.HasPrefix(data, "tools_"):
		h.answerCb(cb.ID, "")
		action := strings.TrimPrefix(data, "tools_")
		if prompt := h.runToolAction(action, cb.Message.Chat, cb.From, sess.Language); prompt == "" {
			return
		} else if sess.State != "idle" {
			h.editMsg(chat.ID, msgID, localization.Get(prompt, sess.Language), keyboards.Back(sess.Language))
		}

	case data == "meme:":
		h.answerCb(cb.ID, "")
		tmpl := strings.TrimPrefix(data, "meme:")
		sess = h.store.GetOrCreate(uid)
		sess.Data["meme_template"] = tmpl
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "awaiting_meme_top")
		h.editMsg(chat.ID, msgID, localization.Get("memeTopText", sess.Language), keyboards.Back(sess.Language))

	case data == "tr_lang:":
		h.answerCb(cb.ID, "")
		target := strings.TrimPrefix(data, "tr_lang:")
		sess = h.store.GetOrCreate(uid)
		sess.Data["tr_target"] = target
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "awaiting_translate_text")
		h.editMsg(chat.ID, msgID, localization.Get("translatePrompt", sess.Language), keyboards.Back(sess.Language))

	case data == "gsettings":
		h.answerCb(cb.ID, "")
		g := h.store.GetGroup(chat.ID)
		msg := fmt.Sprintf("%s\n\nwelcome=%v\nlockdown=%v\nantiLinks=%v\nantiCaps=%v",
			localization.Get("gsettingsTitle", sess.Language), g.WelcomeOn, g.Lockdown, g.AntiLinks, g.AntiCaps)
		h.editMsg(chat.ID, msgID, msg, emptyKB)

	case data == "admin_feedback":
		h.answerCb(cb.ID, "")
		feedbacks := h.store.GetFeedbacks()
		if len(feedbacks) == 0 {
			h.editMsg(chat.ID, msgID, localization.Get("adminFeedback", sess.Language, "No feedback yet."), keyboards.Back(sess.Language))
			return
		}
		var lines []string
		maxShow := 10
		if len(feedbacks) < maxShow {
			maxShow = len(feedbacks)
		}
		for i := 0; i < maxShow; i++ {
			f := feedbacks[i]
			lines = append(lines, fmt.Sprintf("👤 ID: %d\n💬 %s\n🕐 %s",
				f.UserID, escapeMarkdown(f.Message), f.Timestamp))
		}
		text := strings.Join(lines, "\n\n")
		h.editMsg(chat.ID, msgID, localization.Get("adminFeedback", sess.Language, text), keyboards.Back(sess.Language))

	default:
		h.answerCb(cb.ID, localization.Get("error", sess.Language))
	}
}

func (h *Handler) HandleMessage(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	chat := update.Message.Chat
	user := update.Message.From
	uid := int64(chat.ID)

	h.store.TrackUser(int64(user.ID), user.FirstName, user.LastName, user.UserName)
	sess := h.store.GetOrCreate(uid)
	lang := sess.Language

	if sess.State == "awaiting_image_effect" || sess.State == "awaiting_artistic_effect" {
		if update.Message.Photo != nil {
			if sess.State == "awaiting_artistic_effect" {
				h.processArtisticEffect(chat.ID, uid, update.Message.Photo, lang)
			} else {
				h.processImageEffect(chat.ID, uid, update.Message.Photo, lang)
			}
			return
		}
		msgKey := "imageEffectPrompt"
		if sess.State == "awaiting_artistic_effect" {
			msgKey = "artisticPrompt"
		}
		h.sendMsg(chat.ID, localization.Get(msgKey, lang), keyboards.Back(lang))
		return
	}

	if update.Message.Text == "" {
		return
	}

	text := update.Message.Text

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

	case "awaiting_qr_text":
		h.store.SetState(uid, "idle")
		go h.fetchQR(chat.ID, uid, text, lang)

	case "awaiting_weather_city":
		h.store.SetState(uid, "idle")
		go h.fetchWeather(chat.ID, uid, text, lang)

	case "awaiting_translate_text":
		sess = h.store.GetOrCreate(uid)
		target, _ := sess.Data["tr_target"].(string)
		if target == "" {
			target = "en"
		}
		h.store.SetState(uid, "idle")
		go h.fetchTranslate(chat.ID, uid, text, target, lang)

	case "awaiting_convert":
		parts := strings.Fields(text)
		if len(parts) < 4 || !strings.EqualFold(parts[2], "to") {
			h.sendMsg(chat.ID, localization.Get("convertInvalid", lang), keyboards.Back(lang))
			return
		}
		h.store.SetState(uid, "idle")
		go h.fetchConvert(chat.ID, uid, parts[0], parts[1], parts[3], lang)

	case "awaiting_meme_template":
		tmpl := strings.Fields(text)
		if len(tmpl) == 0 {
			h.sendMsg(chat.ID, localization.Get("memeSelectTemplate", lang), keyboards.MemeMenu(lang))
			return
		}
		sess.Data["meme_template"] = tmpl[0]
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "awaiting_meme_top")
		h.sendMsg(chat.ID, localization.Get("memeTopText", lang), keyboards.Back(lang))

	case "awaiting_meme_top":
		sess.Data["meme_top"] = text
		h.store.SetSessionData(uid, sess.Data)
		h.store.SetState(uid, "awaiting_meme_bottom")
		h.sendMsg(chat.ID, localization.Get("memeBottomText", lang), keyboards.Back(lang))

	case "awaiting_meme_bottom":
		sess = h.store.GetOrCreate(uid)
		tmpl, _ := sess.Data["meme_template"].(string)
		top, _ := sess.Data["meme_top"].(string)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("memeGenerating", lang), emptyKB)
		go h.fetchMeme(chat.ID, uid, tmpl, top, text, lang)

	case "awaiting_reddit_sub":
		h.store.SetState(uid, "idle")
		go h.fetchReddit(chat.ID, uid, text, lang)

	case "awaiting_remind":
		dur, body, ok := h.parseRemind(text)
		if !ok {
			h.sendMsg(chat.ID, localization.Get("remindInvalidDuration", lang), keyboards.Back(lang))
			return
		}
		h.store.SetState(uid, "idle")
		h.store.AddReminder(&session.Reminder{
			ChatID:    chat.ID,
			UserID:    int64(user.ID),
			Text:      body,
			DueAt:     time.Now().Add(dur).Unix(),
			CreatedAt: time.Now().Format(time.RFC3339),
		})
		h.sendMsg(chat.ID, localization.Get("remindSaved", lang), keyboards.Back(lang))

	case "awaiting_ban_user":
		h.store.SetState(uid, "idle")
		if target := h.targetFrom(update.Message); target != 0 {
			h.doBan(chat.ID, target, lang)
		} else {
			h.sendMsg(chat.ID, localization.Get("banPrompt", lang), emptyKB)
		}

	case "awaiting_del":
		h.store.SetState(uid, "idle")
		if update.Message.ReplyToMessage != nil {
			n := 0
			if h.deleteMsg(chat.ID, update.Message.ReplyToMessage.MessageID) {
				n = 1
			}
			h.sendMsg(chat.ID, localization.Get("delSuccess", lang, n), emptyKB)
			return
		}

		num, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || num < 1 {
			h.sendMsg(chat.ID, localization.Get("delPrompt", lang), emptyKB)
			return
		}
		ids := recentIDs(update.Message.MessageID, num)
		h.sendMsg(chat.ID, localization.Get("delWorking", lang, len(ids)), emptyKB)
		go func() {
			n := h.deleteMsgs(chat.ID, ids)
			h.sendMsg(chat.ID, localization.Get("delSuccess", lang, n), emptyKB)
		}()

	case "awaiting_welcome_msg":
		g := h.store.GetGroup(chat.ID)
		g.Welcome = text
		g.WelcomeOn = true
		g.Title = chat.Title
		h.store.SetGroup(g)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("welcomeMsgSent", lang), emptyKB)

	case "awaiting_ginfo_name":
		if _, err := h.bot.Request(tgbotapi.SetChatTitleConfig{ChatID: chat.ID, Title: text}); err != nil {
			log.Printf("ginfo name error: %v", err)
			h.sendMsg(chat.ID, localization.Get("error", lang), emptyKB)
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("ginfoNameSet", lang), emptyKB)

	case "awaiting_ginfo_desc":
		if _, err := h.bot.Request(tgbotapi.SetChatDescriptionConfig{ChatID: chat.ID, Description: text}); err != nil {
			log.Printf("ginfo desc error: %v", err)
			h.sendMsg(chat.ID, localization.Get("error", lang), emptyKB)
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("ginfoDescSet", lang), emptyKB)

	case "awaiting_stream_url":
		g := h.store.GetGroup(chat.ID)
		g.StreamURL = text
		g.Title = chat.Title
		h.store.SetGroup(g)
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("streamSaved", lang), emptyKB)

	case "awaiting_post":
		h.store.SetState(uid, "idle")
		if _, err := h.bot.Send(tgbotapi.NewMessage(chat.ID, text)); err != nil {
			log.Printf("post error: %v", err)
			h.sendMsg(chat.ID, localization.Get("postError", lang), emptyKB)
			return
		}
		h.sendMsg(chat.ID, localization.Get("postSuccess", lang), emptyKB)

		if d, ok := dlPendingState(sess.State); ok {
			if !dlHostOK(d, text) {
				h.sendMsg(chat.ID, dlText(d, "Invalid", lang), keyboards.Back(lang))
				return
			}
			h.handleDownloadURL(d, chat.ID, uid, text, lang)
			return
		}

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

	case "awaiting_textpro_text1":
		if text == "" {
			return
		}
		sess.Data["textpro_text1"] = text
		count, _ := sess.Data["textpro_count"].(string)
		if count == "2" {
			h.store.SetSessionData(uid, sess.Data)
			h.store.SetState(uid, "awaiting_textpro_text2")
			h.sendMsg(chat.ID, localization.Get("textProPrompt2", lang), keyboards.Back(lang))
		} else {
			h.store.SetState(uid, "idle")
			h.sendMsg(chat.ID, localization.Get("textProSending", lang), keyboards.Back(lang))
			effect, _ := sess.Data["textpro_effect"].(string)
			go h.fetchTextPro(chat.ID, effect, text, "", lang)
		}

	case "awaiting_textpro_text2":
		if text == "" {
			return
		}
		sess.Data["textpro_text2"] = text
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("textProSending", lang), keyboards.Back(lang))
		sess = h.store.GetOrCreate(uid)
		effect, _ := sess.Data["textpro_effect"].(string)
		text1, _ := sess.Data["textpro_text1"].(string)
		go h.fetchTextPro(chat.ID, effect, text1, text, lang)

	case "awaiting_photooxy_text1":
		if text == "" {
			return
		}
		sess.Data["photooxy_text1"] = text
		count, _ := sess.Data["photooxy_count"].(string)
		if count == "2" {
			h.store.SetSessionData(uid, sess.Data)
			h.store.SetState(uid, "awaiting_photooxy_text2")
			h.sendMsg(chat.ID, localization.Get("textProPrompt2", lang), keyboards.Back(lang))
		} else {
			h.store.SetState(uid, "idle")
			h.sendMsg(chat.ID, localization.Get("photooxySending", lang), keyboards.Back(lang))
			effect, _ := sess.Data["photooxy_effect"].(string)
			go h.fetchPhotooxy(chat.ID, effect, text, "", lang)
		}

	case "awaiting_photooxy_text2":
		if text == "" {
			return
		}
		sess.Data["photooxy_text2"] = text
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("photooxySending", lang), keyboards.Back(lang))
		sess = h.store.GetOrCreate(uid)
		effect, _ := sess.Data["photooxy_effect"].(string)
		text1, _ := sess.Data["photooxy_text1"].(string)
		go h.fetchPhotooxy(chat.ID, effect, text1, text, lang)

	case "awaiting_ephoto_text1":
		if text == "" {
			return
		}
		sess.Data["ephoto_text1"] = text
		count, _ := sess.Data["ephoto_count"].(string)
		if count == "2" {
			h.store.SetSessionData(uid, sess.Data)
			h.store.SetState(uid, "awaiting_ephoto_text2")
			h.sendMsg(chat.ID, localization.Get("textProPrompt2", lang), keyboards.Back(lang))
		} else {
			h.store.SetState(uid, "idle")
			h.sendMsg(chat.ID, localization.Get("ephotoSending", lang), keyboards.Back(lang))
			effect, _ := sess.Data["ephoto_effect"].(string)
			go h.fetchEphoto(chat.ID, effect, text, "", lang)
		}

	case "awaiting_ephoto_text2":
		if text == "" {
			return
		}
		sess.Data["ephoto_text2"] = text
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("ephotoSending", lang), keyboards.Back(lang))
		sess = h.store.GetOrCreate(uid)
		effect, _ := sess.Data["ephoto_effect"].(string)
		text1, _ := sess.Data["ephoto_text1"].(string)
		go h.fetchEphoto(chat.ID, effect, text1, text, lang)

	case "awaiting_reurl_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("reurlSending", lang), keyboards.Back(lang))
		go h.fetchReurl(chat.ID, text, lang)

	case "awaiting_tinycc_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("tinyccSending", lang), keyboards.Back(lang))
		go h.fetchTinycc(chat.ID, text, lang)

	case "awaiting_itsssl_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("itssslSending", lang), keyboards.Back(lang))
		go h.fetchItsssl(chat.ID, text, lang)

	case "awaiting_cuqin_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("cuqinSending", lang), keyboards.Back(lang))
		go h.fetchCuqin(chat.ID, text, lang)

	case "awaiting_vurl_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("vurlSending", lang), keyboards.Back(lang))
		go h.fetchVurl(chat.ID, text, lang)

	case "awaiting_tiny_url":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("tinySending", lang), keyboards.Back(lang))
		go h.fetchTiny(chat.ID, text, lang)

	case "awaiting_news_query":
		if text == "" {
			return
		}
		h.store.SetState(uid, "idle")
		h.sendMsg(chat.ID, localization.Get("newsSending", lang), keyboards.Back(lang))
		go h.fetchGoogleNews(chat.ID, text, lang)

	case "awaiting_confirm":
		h.store.SetState(uid, "idle")
		if strings.ToLower(text) == "yes" || text == "y" {
			h.sendMsg(chat.ID, localization.Get("confirmed", lang), keyboards.MainMenu(h.cfg, lang))
		} else {
			h.sendMsg(chat.ID, localization.Get("cancelled", lang), keyboards.MainMenu(h.cfg, lang))
		}

	default:
		link := dlExtractURL(text)
		if h.isChat(chat) && h.botIsAdmin(chat.ID) {
			h.trackMessage(chat, user, text, lang)
		}
		if update.Message.ForwardFromChat != nil || update.Message.ForwardSenderName != "" {
			h.sendMsg(chat.ID, localization.Get("forwardProcessing", lang), emptyKB)
			go h.forwardMedia(chat.ID, uid, update.Message, lang)
		} else if link != "" && dlHasHost(link) {
			hit := dlByHost(link)
			switch {
			case hit.offPath:
				h.sendMsg(chat.ID, dlText(hit.d, "Unsupported", lang), keyboards.Back(lang))
			case hit.ok:
				h.handleDownloadURL(hit.d, chat.ID, uid, link, lang)
			default:
				h.store.SetState(uid, "awaiting_reurl_url")
				sess.Data["reurl_url"] = link
				h.store.SetSessionData(uid, sess.Data)
				h.sendMsg(chat.ID, localization.Get("reurlSending", lang), keyboards.Back(lang))
				go h.fetchReurl(chat.ID, link, lang)
			}
		} else {
			// Anything else is conversation for the AI agent.
			h.maybeChatWithAI(chat, user, update.Message, text, lang)
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

// mediaKeys are the JSON fields known to hold a direct media URL. Checked
// first so a wrapper object's own bookkeeping links don't win over the media.
var mediaKeys = []string{"url", "download_url", "video_url", "media_url", "link", "file", "downloadLink", "downloadUrl"}

// mediaItem is one downloadable file from an API response.
type mediaItem struct {
	url   string
	title string
	// group identifies the object this file was listed in, so a video can be
	// told apart from the other posts in the same carousel.
	group int
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func extractURL(v interface{}) string {
	switch val := v.(type) {
	case string:
		if isHTTPURL(val) {
			return val
		}
	case map[string]interface{}:
		knownKeys := mediaKeys
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

// collectURLs walks an API response and returns every media URL it finds, in
// document order and deduplicated. extractURL walks arrays backwards and keeps
// only the last hit, which is why a multi-image Instagram post used to deliver
// exactly one (the final) image.
func collectURLs(v interface{}) []mediaItem {
	var out []mediaItem
	seen := make(map[string]bool)
	group := 0
	add := func(item mediaItem) {
		if !isHTTPURL(item.url) || seen[item.url] {
			return
		}
		seen[item.url] = true
		out = append(out, item)
	}
	var walk func(v interface{}, title string)
	walk = func(v interface{}, title string) {
		switch val := v.(type) {
		case string:
			add(mediaItem{url: val, title: title, group: group})
		case []interface{}:
			for _, sub := range val {
				walk(sub, title)
			}
		case map[string]interface{}:
			// Each object gets its own group, and the counter never rewinds, so
			// every element of a carousel is a separate group.
			group++
			// An object that carries its own URL: label it from its own fields.
			local := itemTitle(val)
			if local == "" {
				local = title
			}
			// Nested containers first, so a wrapper's own url field can never
			// shadow the media list hanging off the same object.
			for _, key := range []string{"data", "items", "medias", "media", "results", "posts", "images", "videos"} {
				if sub, ok := val[key]; ok {
					walk(sub, local)
				}
			}
			for _, key := range mediaKeys {
				sub, ok := val[key]
				if !ok {
					continue
				}
				if s, ok := sub.(string); ok {
					if isHTTPURL(s) {
						add(mediaItem{url: s, title: local, group: group})
					}
					continue
				}
				walk(sub, local)
			}
			// Sorted so an entry holding both a video and an image always
			// resolves in the same order; Go randomises map iteration.
			rest := make([]string, 0, len(val))
			for k := range val {
				if !isMediaKey(k) {
					rest = append(rest, k)
				}
			}
			sort.Strings(rest)
			for _, k := range rest {
				sub := val[k]
				if s, ok := sub.(string); ok {
					if isHTTPURL(s) {
						add(mediaItem{url: s, title: local, group: group})
					}
					continue
				}
				walk(sub, local)
			}
		}
	}
	walk(v, "")
	return out
}

func isMediaKey(k string) bool {
	for _, known := range mediaKeys {
		if k == known {
			return true
		}
	}
	switch k {
	case "data", "items", "medias", "results", "posts", "images", "videos", "media":
		return true
	}
	return false
}

// itemTitle pulls the short label an API puts next to the URL ("Download
// Video", "Download Image"), used to tell a video post from an image carousel.
// Long values are ignored: some APIs put the post caption in `title`, and a
// caption must not be mistaken for a media label.
func itemTitle(m map[string]interface{}) string {
	for _, key := range []string{"title", "name", "type", "media_type"} {
		s, ok := m[key].(string)
		if !ok || s == "" || len([]rune(s)) > mediaLabelMax {
			continue
		}
		return s
	}
	return ""
}

// mediaLabelMax bounds a media label. Captions are far longer than any label an
// API uses for a file.
const mediaLabelMax = 40

// isVideoItem reports whether an API entry is the video of a post rather than a
// carousel image or a video thumbnail.
func isVideoItem(it mediaItem) bool {
	title := strings.ToLower(it.title)
	// A thumbnail is never the video, whatever else the label says.
	if strings.Contains(title, "thumbnail") || strings.Contains(title, "cover") ||
		strings.Contains(title, "preview") {
		return false
	}
	if strings.Contains(title, "video") || strings.Contains(title, "reel") {
		return true
	}
	lower := strings.ToLower(it.url)
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	return strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".mov") ||
		strings.HasSuffix(lower, ".webm") || strings.HasSuffix(lower, ".m4v")
}

func fetchMedia(apiURL string) ([]byte, string, error) {
	body, ct, err := fetchMediaDepth(apiURL, 0)
	if err != nil {
		return nil, "", err
	}
	return body, ct, nil
}

// fetchMediaDepth follows JSON wrappers that point at another media URL. The
// depth guard stops a misbehaving endpoint that keeps pointing at itself from
// growing the stack without bound.
func fetchMediaDepth(apiURL string, depth int) ([]byte, string, error) {
	if depth > 4 {
		return nil, "", fmt.Errorf("too many media URL redirects")
	}

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		return nil, "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := readBody(resp, maxDownloadSize)
	if err != nil {
		return nil, "", fmt.Errorf("read: %w", err)
	}

	if len(body) == 0 {
		return nil, "", fmt.Errorf("empty body")
	}

	ct := resp.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") || body[0] == '{' {
		var result interface{}
		if err := json.Unmarshal(body, &result); err == nil {
			if u := extractURL(result); u != "" {
				log.Printf("Resolved media URL from JSON: %s", u)
				return fetchMediaDepth(u, depth+1)
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

// sendAlbum pushes images as one media group, falling back to individual sends
// when Telegram rejects the group.
func (h *Handler) sendAlbum(chatID int64, prefix string, album [][]byte, got int, caption, lang string) int {
	if got == 1 {
		h.sendPhotoBytes(chatID, prefix, album[0], caption, lang)
		album[0] = nil
		return 1
	}

	media := make([]interface{}, 0, got)
	stamp := time.Now().Format("150405")
	for i := 0; i < got; i++ {
		item := tgbotapi.InputMediaPhoto{
			BaseInputMedia: tgbotapi.BaseInputMedia{
				Type:  "photo",
				Media: tgbotapi.FileBytes{Name: fmt.Sprintf("%s_%s_%d.jpg", prefix, stamp, i), Bytes: album[i]},
			},
		}
		if i == 0 {
			item.Caption = caption
		}
		media = append(media, item)
	}

	_, err := h.bot.SendMediaGroup(tgbotapi.MediaGroupConfig{ChatID: chatID, Media: media})
	media = nil
	if err == nil {
		for i := range album {
			album[i] = nil
		}
		return got
	}
	log.Printf("%s album rejected (%v), sending individually", prefix, err)

	sent := 0
	for i := 0; i < got; i++ {
		if album[i] == nil {
			continue
		}
		h.sendPhotoBytes(chatID, prefix, album[i], "", lang)
		album[i] = nil
		sent++
		time.Sleep(igAlbumPause)
	}
	return sent
}

func (h *Handler) sendPhotoBytes(chatID int64, prefix string, body []byte, caption, lang string) {
	name := fmt.Sprintf("%s_%s.jpg", prefix, time.Now().Format("150405"))
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: name, Bytes: body})
	photo.Caption = caption
	if _, err := h.bot.Send(photo); err != nil {
		if _, err := h.bot.Send(tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: name, Bytes: body})); err != nil {
			log.Printf("IG photo send error: %v", err)
		}
	}
}

// mediaExt picks a file extension for a downloaded body. The Content-Type wins
// when it is meaningful, then a downloader's declared type, then the URL's own
// extension: a GitHub zipball arrives as application/octet-stream from a URL with
// no extension, and must not be named .jpg. When nothing says anything, no
// extension is invented.
func mediaExt(ct, rawURL, declared string) string {
	lower := strings.ToLower(ct)
	switch {
	case strings.Contains(lower, "png"):
		return ".png"
	case strings.Contains(lower, "gif"):
		return ".gif"
	case strings.Contains(lower, "webp"):
		return ".webp"
	case strings.Contains(lower, "jpeg"), strings.Contains(lower, "jpg"):
		return ".jpg"
	case strings.Contains(lower, "audio"), strings.Contains(lower, "mpeg"):
		return ".mp3"
	case strings.Contains(lower, "video"), strings.Contains(lower, "mp4"):
		return ".mp4"
	case strings.Contains(lower, "zip"), strings.Contains(lower, "compressed"):
		return ".zip"
	case strings.Contains(lower, "pdf"):
		return ".pdf"
	}
	if declared != "" {
		return declared
	}
	return urlExt(rawURL)
}

// urlExt reads a sane extension off a URL path.
func urlExt(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if len(ext) < 2 || len(ext) > 6 {
		return ""
	}
	for _, c := range ext[1:] {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		default:
			return ""
		}
	}
	return ext
}

// tiktokInfo fetches TikTok metadata, stores the raw payload for the format
// pick and returns the info text to show. Presentation is left to the caller so
// the registry can render it with the site's own labels.
func (h *Handler) tiktokInfo(uid int64, videoURL string) (string, bool) {
	apiURL := fmt.Sprintf("%s/tiktok/download?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(videoURL))

	log.Printf("TT info: %s", videoURL)

	v, err := h.apiJSON(apiURL)
	if err != nil {
		log.Printf("TT API error: %v", err)
		return "", false
	}
	result, _ := v.(map[string]interface{})
	if ok, _ := result["success"].(bool); ok == false {
		log.Printf("TT API returned success=false")
		return "", false
	}
	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("TT API returned no data")
		return "", false
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
		if authorData != nil && nickname != "" && nickname != fullname {
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

	msg := "📱 *TikTok Video*\n\n"
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

	sess := h.store.GetOrCreate(uid)
	sess.Data["tt_api_data"] = data
	h.store.SetSessionData(uid, sess.Data)

	return msg + "\n\n*Choose format:*", true
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

func (h *Handler) downloadTikTok(chatID, uid int64, data map[string]interface{}, format, lang string) {
	defer h.recoverPanic()
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
	h.store.ClearSessionData(uid)
	h.sendMsg(chatID, localization.Get("ttSuccess", lang), keyboards.MainMenu(h.cfg, lang))
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

func (h *Handler) fetchBingSearch(chatID int64, query, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/bing/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Bing search: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Bing search API error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/bing/image?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Bing images: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Bing images API error: %v", err)
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("bingError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("bingSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchPinSearch(chatID int64, query, countStr, lang string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/pinterest/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Pinterest search: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Pinterest search API error: %v", err)
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("pinSearchError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("pinSearchSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchStickerSearch(chatID int64, query, countStr, lang string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/stickers/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Sticker search: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Sticker search API error: %v", err)
		h.sendMsg(chatID, localization.Get("stickerError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/imgur/search?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("Imgur search: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Imgur search API error: %v", err)
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("imgurError", lang), keyboards.Back(lang))
	} else {
		h.sendMsg(chatID, localization.Get("imgurSuccess", lang, sent), keyboards.MainMenu(h.cfg, lang))
	}
}

func (h *Handler) fetchYtSearch(chatID int64, query, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/yts/searchVideos?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	log.Printf("YouTube search: %s", query)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("YouTube search API error: %v", err)
		h.sendMsg(chatID, localization.Get("ytSearchError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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

func (h *Handler) fetchTextPro(chatID int64, effect, text1, text2, lang string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	var apiURL string
	if text2 == "" {
		apiURL = fmt.Sprintf("%s/textpro/%s?apiKey=%s&text=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1))
	} else {
		apiURL = fmt.Sprintf("%s/textpro/%s?apiKey=%s&text1=%s&text2=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1), url.QueryEscape(text2))
	}

	log.Printf("TextPro: effect=%s text1=%s text2=%s", effect, text1, text2)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("TextPro API error: %v", err)
		h.sendMsg(chatID, localization.Get("textProError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
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
			return
		}
	}

	imgBody = nil
	h.sendMsg(chatID, localization.Get("textProSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchPhotooxy(chatID int64, effect, text1, text2, lang string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	var apiURL string
	if text2 == "" {
		apiURL = fmt.Sprintf("%s/photooxy/%s?apiKey=%s&text=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1))
	} else {
		apiURL = fmt.Sprintf("%s/photooxy/%s?apiKey=%s&text1=%s&text2=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1), url.QueryEscape(text2))
	}

	log.Printf("Photooxy: effect=%s text1=%s text2=%s", effect, text1, text2)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Photooxy API error: %v", err)
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Photooxy read error: %v", err)
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Photooxy JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Photooxy API returned success=false")
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Photooxy no data")
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}

	imageURL, _ := data["image_url"].(string)
	if imageURL == "" {
		log.Printf("Photooxy no image_url")
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
		return
	}

	imgBody, ct, err := fetchMedia(imageURL)
	if err != nil {
		log.Printf("Photooxy image fetch error: %v", err)
		h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
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

	fileName := fmt.Sprintf("photooxy_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

	photo := tgbotapi.NewPhoto(chatID, fileBytes)
	if _, err := h.bot.Send(photo); err != nil {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			log.Printf("Photooxy send error: %v", err)
			h.sendMsg(chatID, localization.Get("photooxyError", lang), keyboards.Back(lang))
			imgBody = nil
			return
		}
	}

	imgBody = nil
	h.sendMsg(chatID, localization.Get("photooxySuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchEphoto(chatID int64, effect, text1, text2, lang string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	var apiURL string
	if text2 == "" {
		apiURL = fmt.Sprintf("%s/ephoto/%s?apiKey=%s&text=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1))
	} else {
		apiURL = fmt.Sprintf("%s/ephoto/%s?apiKey=%s&text1=%s&text2=%s",
			h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey(), url.QueryEscape(text1), url.QueryEscape(text2))
	}

	log.Printf("Ephoto: effect=%s text1=%s text2=%s", effect, text1, text2)

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Ephoto API error: %v", err)
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Ephoto read error: %v", err)
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Ephoto JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Ephoto API returned success=false")
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Ephoto no data")
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}

	imageURL, _ := data["image_url"].(string)
	if imageURL == "" {
		log.Printf("Ephoto no image_url")
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
		return
	}

	imgBody, ct, err := fetchMedia(imageURL)
	if err != nil {
		log.Printf("Ephoto image fetch error: %v", err)
		h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
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

	fileName := fmt.Sprintf("ephoto_%s%s", time.Now().Format("150405"), ext)
	fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: imgBody}

	photo := tgbotapi.NewPhoto(chatID, fileBytes)
	if _, err := h.bot.Send(photo); err != nil {
		doc := tgbotapi.NewDocument(chatID, fileBytes)
		if _, err := h.bot.Send(doc); err != nil {
			log.Printf("Ephoto send error: %v", err)
			h.sendMsg(chatID, localization.Get("ephotoError", lang), keyboards.Back(lang))
			imgBody = nil
			return
		}
	}

	imgBody = nil
	h.sendMsg(chatID, localization.Get("ephotoSuccess", lang), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchReurl(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/reurl?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Reurl API error: %v", err)
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Reurl read error: %v", err)
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Reurl JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Reurl API returned success=false")
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Reurl no data")
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	res, _ := data["result"].(map[string]interface{})
	if res == nil {
		log.Printf("Reurl no result")
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := res["short_url"].(string)
	if shortURL == "" {
		log.Printf("Reurl no short_url")
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("reurlSuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchTinycc(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/tinycc?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Tinycc API error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Tinycc read error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Tinycc JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Tinycc API returned success=false")
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Tinycc no data")
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := data["short_url"].(string)
	if shortURL == "" {
		log.Printf("Tinycc no short_url")
		h.sendMsg(chatID, localization.Get("tinyccError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("tinyccSuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchItsssl(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/itsssl?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Itsssl API error: %v", err)
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Itsssl read error: %v", err)
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Itsssl JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Itsssl API returned success=false")
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Itsssl no data")
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := data["short_url"].(string)
	if shortURL == "" {
		log.Printf("Itsssl no short_url")
		h.sendMsg(chatID, localization.Get("itssslError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("itssslSuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchCuqin(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/cuqin?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Cuqin API error: %v", err)
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Cuqin read error: %v", err)
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Cuqin JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Cuqin API returned success=false")
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Cuqin no data")
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := data["short_url"].(string)
	if shortURL == "" {
		log.Printf("Cuqin no short_url")
		h.sendMsg(chatID, localization.Get("cuqinError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("cuqinSuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchVurl(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/vurl?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Vurl API error: %v", err)
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Vurl read error: %v", err)
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Vurl JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Vurl API returned success=false")
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Vurl no data")
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := data["short_url"].(string)
	if shortURL == "" {
		log.Printf("Vurl no short_url")
		h.sendMsg(chatID, localization.Get("vurlError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("vurlSuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchTiny(chatID int64, longURL, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/shortener/tiny?apiKey=%s&url=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(longURL))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Tiny API error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Tiny read error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Tiny JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Tiny API returned success=false")
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Tiny no data")
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}

	shortURL, _ := data["short_url"].(string)
	if shortURL == "" {
		log.Printf("Tiny no short_url")
		h.sendMsg(chatID, localization.Get("tinyError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("tinySuccess", lang, shortURL), keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchGoogleNews(chatID int64, query, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/google?apiKey=%s&query=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(query))

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	articles, _ := data["articles"].([]interface{})
	if len(articles) == 0 {
		log.Printf("News no articles")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(articles) < count {
		count = len(articles)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := articles[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		articleURL, _ := article["url"].(string)
		source, _ := article["source"].(string)
		published, _ := article["published_at"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n   %s — %s\n\n", i+1, escapeMarkdown(title), articleURL, escapeMarkdown(source), escapeMarkdown(published))
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchBbcNews(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/bbc?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("BBC News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("BBC News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("BBC News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("BBC News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("BBC News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	articles, _ := data["articles"].([]interface{})
	if len(articles) == 0 {
		log.Printf("BBC News no articles")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(articles) < count {
		count = len(articles)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := articles[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		desc, _ := article["description"].(string)
		articleURL, _ := article["url"].(string)
		source, _ := article["source"].(string)
		published, _ := article["published_at"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n   %s — %s\n", i+1, escapeMarkdown(title), articleURL, escapeMarkdown(source), escapeMarkdown(published))
		if desc != "" {
			msg += fmt.Sprintf("   _%s_\n", escapeMarkdown(truncate(desc, 100)))
		}
		msg += "\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchCnnNews(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/cnn?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("CNN News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("CNN News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("CNN News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("CNN News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("CNN News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	articles, _ := data["articles"].([]interface{})
	if len(articles) == 0 {
		log.Printf("CNN News no articles")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(articles) < count {
		count = len(articles)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := articles[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		desc, _ := article["description"].(string)
		articleURL, _ := article["url"].(string)
		source, _ := article["source"].(string)
		published, _ := article["published_at"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n   %s — %s\n", i+1, escapeMarkdown(title), articleURL, escapeMarkdown(source), escapeMarkdown(published))
		if desc != "" {
			msg += fmt.Sprintf("   _%s_\n", escapeMarkdown(truncate(desc, 100)))
		}
		msg += "\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchAljazeeraNews(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/aljazeera?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Al Jazeera News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Al Jazeera News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Al Jazeera News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Al Jazeera News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Al Jazeera News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	articles, _ := data["articles"].([]interface{})
	if len(articles) == 0 {
		log.Printf("Al Jazeera News no articles")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(articles) < count {
		count = len(articles)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := articles[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		articleURL, _ := article["url"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n\n", i+1, escapeMarkdown(title), articleURL)
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchCgtnNews(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/cgtnWorld?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("CGTN News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("CGTN News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("CGTN News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("CGTN News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("CGTN News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	headlines, _ := data["headlines"].([]interface{})
	if len(headlines) == 0 {
		log.Printf("CGTN News no headlines")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(headlines) < count {
		count = len(headlines)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := headlines[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		articleURL, _ := article["url"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n\n", i+1, escapeMarkdown(title), articleURL)
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchTrtNews(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/news/trtWorld?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("TRT News API error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("TRT News read error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("TRT News JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("TRT News API returned success=false")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("TRT News no data")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	headlines, _ := data["headlines"].([]interface{})
	if len(headlines) == 0 {
		log.Printf("TRT News no headlines")
		h.sendMsg(chatID, localization.Get("newsError", lang), keyboards.Back(lang))
		return
	}

	count := 10
	if len(headlines) < count {
		count = len(headlines)
	}

	msg := localization.Get("newsResult", lang, count)
	for i := 0; i < count; i++ {
		article, _ := headlines[i].(map[string]interface{})
		if article == nil {
			continue
		}
		title, _ := article["title"].(string)
		articleURL, _ := article["url"].(string)
		msg += fmt.Sprintf("%d. [%s](%s)\n\n", i+1, escapeMarkdown(title), articleURL)
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchCricket(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/sports/cricket?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Cricket API error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Cricket read error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Cricket JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Cricket API returned success=false")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Cricket no data")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	games, _ := data["games"].([]interface{})
	if len(games) == 0 {
		log.Printf("Cricket no games")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	msg := localization.Get("sportsResult", lang)
	for _, g := range games {
		game, _ := g.(map[string]interface{})
		if game == nil {
			continue
		}
		name, _ := game["name"].(string)
		status, _ := game["status"].(string)
		details, _ := game["details"].(string)
		msg += fmt.Sprintf("▫️ *%s*\n   %s", escapeMarkdown(name), escapeMarkdown(status))
		if details != "" {
			msg += fmt.Sprintf("\n   `%s`", details)
		}
		msg += "\n\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchNfl(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/sports/nfl?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("NFL API error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("NFL read error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("NFL JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("NFL API returned success=false")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("NFL no data")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	games, _ := data["games"].([]interface{})
	if len(games) == 0 {
		log.Printf("NFL no games")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	msg := localization.Get("sportsResult", lang)
	for _, g := range games {
		game, _ := g.(map[string]interface{})
		if game == nil {
			continue
		}
		name, _ := game["name"].(string)
		status, _ := game["status"].(string)
		date, _ := game["date"].(string)
		home, _ := game["home_team"].(map[string]interface{})
		away, _ := game["away_team"].(map[string]interface{})

		homeName, _ := home["name"].(string)
		homeScore, _ := home["score"].(string)
		homeRecord, _ := home["record"].(string)
		awayName, _ := away["name"].(string)
		awayScore, _ := away["score"].(string)
		awayRecord, _ := away["record"].(string)

		msg += fmt.Sprintf("▫️ *%s*\n   %s\n", escapeMarkdown(name), escapeMarkdown(status))
		msg += fmt.Sprintf("   🏠 %s %s (%s)\n", escapeMarkdown(homeName), homeScore, escapeMarkdown(homeRecord))
		msg += fmt.Sprintf("   🛩 %s %s (%s)\n", escapeMarkdown(awayName), awayScore, escapeMarkdown(awayRecord))
		if date != "" {
			msg += fmt.Sprintf("   🕐 %s\n", escapeMarkdown(date))
		}
		msg += "\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchNba(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/sports/nba?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("NBA API error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("NBA read error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("NBA JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("NBA API returned success=false")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("NBA no data")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	games, _ := data["games"].([]interface{})
	if len(games) == 0 {
		log.Printf("NBA no games")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	msg := localization.Get("sportsResult", lang)
	for _, g := range games {
		game, _ := g.(map[string]interface{})
		if game == nil {
			continue
		}
		name, _ := game["name"].(string)
		status, _ := game["status"].(string)
		date, _ := game["date"].(string)
		home, _ := game["home_team"].(map[string]interface{})
		away, _ := game["away_team"].(map[string]interface{})

		homeName, _ := home["name"].(string)
		homeScore, _ := home["score"].(string)
		awayName, _ := away["name"].(string)
		awayScore, _ := away["score"].(string)

		msg += fmt.Sprintf("▫️ *%s*\n   %s\n", escapeMarkdown(name), escapeMarkdown(status))
		msg += fmt.Sprintf("   🏠 %s %s\n", escapeMarkdown(homeName), homeScore)
		msg += fmt.Sprintf("   🛩 %s %s\n", escapeMarkdown(awayName), awayScore)
		if date != "" {
			msg += fmt.Sprintf("   🕐 %s\n", escapeMarkdown(date))
		}
		msg += "\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) fetchCricbuzz(chatID int64, lang string) {
	defer h.recoverPanic()
	apiURL := fmt.Sprintf("%s/sports/cricbuzz?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey())

	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		log.Printf("Cricbuzz API error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}
	defer resp.Body.Close()

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		log.Printf("Cricbuzz read error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("Cricbuzz JSON error: %v", err)
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	success, _ := result["success"].(bool)
	if !success {
		log.Printf("Cricbuzz API returned success=false")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		log.Printf("Cricbuzz no data")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	games, _ := data["games"].([]interface{})
	if len(games) == 0 {
		log.Printf("Cricbuzz no games")
		h.sendMsg(chatID, localization.Get("sportsError", lang), keyboards.Back(lang))
		return
	}

	msg := localization.Get("sportsResult", lang)
	for _, g := range games {
		game, _ := g.(map[string]interface{})
		if game == nil {
			continue
		}
		title, _ := game["title"].(string)
		matchInfo, _ := game["match_info"].(string)
		status, _ := game["status"].(string)
		gameURL, _ := game["url"].(string)
		team1, _ := game["team1"].(map[string]interface{})
		team2, _ := game["team2"].(map[string]interface{})

		t1Name, _ := team1["name"].(string)
		t1Score, _ := team1["score"].(string)
		t2Name, _ := team2["name"].(string)
		t2Score, _ := team2["score"].(string)

		msg += fmt.Sprintf("▫️ *%s*\n", escapeMarkdown(title))
		msg += fmt.Sprintf("   🏏 %s — %s\n", escapeMarkdown(t1Name), t1Score)
		msg += fmt.Sprintf("   🏏 %s — %s\n", escapeMarkdown(t2Name), t2Score)
		msg += fmt.Sprintf("   📍 %s\n", escapeMarkdown(matchInfo))
		msg += fmt.Sprintf("   ℹ️ %s\n", escapeMarkdown(status))
		msg += fmt.Sprintf("   [🔗 Cricbuzz](%s)\n", gameURL)
		msg += "\n"
	}

	h.sendMsg(chatID, msg, keyboards.MainMenu(h.cfg, lang))
}

func (h *Handler) processImageEffect(chatID int64, uid int64, photos []tgbotapi.PhotoSize, lang string) {
	sess := h.store.GetOrCreate(uid)
	effect, _ := sess.Data["image_effect"].(string)
	if effect == "" {
		effect = "blur"
	}

	h.store.SetState(uid, "idle")
	h.store.SetSessionData(uid, nil)

	h.sendMsg(chatID, localization.Get("imageEffectProcessing", lang), keyboards.Back(lang))

	largest := photos[len(photos)-1]
	file, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: largest.FileID})
	if err != nil {
		log.Printf("ImageEffect GetFile error: %v", err)
		h.sendMsg(chatID, localization.Get("imageEffectError", lang), keyboards.Back(lang))
		return
	}

	go h.fetchImageEffect(chatID, effect, file, lang, "imageEffectError", "imageEffectSuccess")
}

func (h *Handler) processArtisticEffect(chatID int64, uid int64, photos []tgbotapi.PhotoSize, lang string) {
	sess := h.store.GetOrCreate(uid)
	effect, _ := sess.Data["image_effect"].(string)
	if effect == "" {
		effect = "pencilSketch"
	}

	h.store.SetState(uid, "idle")
	h.store.SetSessionData(uid, nil)

	h.sendMsg(chatID, localization.Get("artisticProcessing", lang), keyboards.Back(lang))

	largest := photos[len(photos)-1]
	file, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: largest.FileID})
	if err != nil {
		log.Printf("ArtisticEffect GetFile error: %v", err)
		h.sendMsg(chatID, localization.Get("artisticError", lang), keyboards.Back(lang))
		return
	}

	go h.fetchImageEffect(chatID, effect, file, lang, "artisticError", "artisticSuccess")
}

func (h *Handler) fetchImageEffect(chatID int64, effect string, file tgbotapi.File, lang, errKey, okKey string) {
	defer h.recoverPanic()
	h.acquireDL()
	defer h.releaseDL()

	apiURL := fmt.Sprintf("%s/sharp/%s?apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), effect, h.cfg.EffectiveApiKey())

	fileURL := file.Link(h.bot.Token)
	resp, err := mediaClient.Get(fileURL)
	if err != nil {
		log.Printf("ImageEffect download error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}
	imgBytes, err := readBody(resp, maxDownloadSize)
	resp.Body.Close()
	if err != nil {
		log.Printf("ImageEffect read error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "image.jpg")
	if err != nil {
		log.Printf("ImageEffect form error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}
	fw.Write(imgBytes)
	imgBytes = nil
	w.Close()

	req, err := http.NewRequest("POST", apiURL, &buf)
	if err != nil {
		log.Printf("ImageEffect req error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp2, err := mediaClient.Do(req)
	if err != nil {
		log.Printf("ImageEffect API error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}
	defer resp2.Body.Close()

	body, err := readBody(resp2, maxDownloadSize)
	if err != nil {
		log.Printf("ImageEffect read error: %v", err)
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}

	ct := resp2.Header.Get("Content-Type")

	if strings.Contains(ct, "image/") {
		ext := ".jpg"
		if strings.Contains(ct, "png") {
			ext = ".png"
		} else if strings.Contains(ct, "gif") {
			ext = ".gif"
		} else if strings.Contains(ct, "webp") {
			ext = ".webp"
		}

		fileName := fmt.Sprintf("%s_%s%s", effect, time.Now().Format("150405"), ext)
		fileBytes := tgbotapi.FileBytes{Name: fileName, Bytes: body}

		photo := tgbotapi.NewPhoto(chatID, fileBytes)
		if _, err := h.bot.Send(photo); err != nil {
			doc := tgbotapi.NewDocument(chatID, fileBytes)
			if _, err := h.bot.Send(doc); err != nil {
				log.Printf("ImageEffect send error: %v", err)
				h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
				body = nil
				return
			}
		}

		body = nil
		h.sendMsg(chatID, localization.Get(okKey, lang), keyboards.MainMenu(h.cfg, lang))
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("ImageEffect bad response: %s", string(body))
		h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
		return
	}

	errMsg, _ := result["error"].(string)
	if errMsg == "" {
		errMsg = "Unknown error"
	}
	log.Printf("ImageEffect API error: %s", errMsg)
	h.sendMsg(chatID, localization.Get(errKey, lang), keyboards.Back(lang))
}
