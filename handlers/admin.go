package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (h *Handler) langOf(chatID int64) string {
	return h.store.GetOrCreate(chatID).Language
}

func (h *Handler) isGroup(chat *tgbotapi.Chat) bool {
	return chat.Type == "group" || chat.Type == "supergroup"
}

func (h *Handler) isChannel(chat *tgbotapi.Chat) bool {
	return chat.Type == "channel"
}

func (h *Handler) isChat(chat *tgbotapi.Chat) bool {
	return h.isGroup(chat) || h.isChannel(chat)
}

func (h *Handler) requireAdmin(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) bool {
	if !h.isGroup(chat) {
		h.sendMsg(chat.ID, localization.Get("noGroupAccess", lang), emptyKB)
		return false
	}
	if h.cfg.IsAdmin(int64(user.ID)) || h.isChatAdmin(chat.ID, int64(user.ID)) {
		return true
	}
	h.sendMsg(chat.ID, localization.Get("notAdmin", lang), emptyKB)
	return false
}

func (h *Handler) requireGroupAdmin(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) bool {
	if !h.isGroup(chat) {
		h.sendMsg(chat.ID, localization.Get("noGroupAccess", lang), emptyKB)
		return false
	}
	if h.cfg.IsAdmin(int64(user.ID)) || h.isChatAdmin(chat.ID, int64(user.ID)) {
		return true
	}
	h.sendMsg(chat.ID, localization.Get("notAdmin", lang), emptyKB)
	return false
}

// HandleChannelPost records that the bot is present in a channel and counts the
// post. Nothing else happens here: a channel has no interactive conversation, so
// the message and command handlers, which both read update.Message, never see it.
func (h *Handler) HandleChannelPost(update tgbotapi.Update) {
	post := update.ChannelPost
	if post == nil || !h.isChannel(post.Chat) {
		return
	}
	g := h.store.GetGroup(post.Chat.ID)
	g.Title = post.Chat.Title
	g.IsChannel = true
	g.MsgCount++
	g.LastActive = time.Now().Format(time.RFC3339)
	h.store.SetGroup(g)
}

func (h *Handler) requireChannel(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) bool {
	if !h.isChannel(chat) {
		h.sendMsg(chat.ID, localization.Get("noChannelAccess", lang), emptyKB)
		return false
	}
	if h.cfg.IsAdmin(int64(user.ID)) || h.isChannelAdmin(chat.ID, int64(user.ID)) {
		return true
	}
	h.sendMsg(chat.ID, localization.Get("notAdmin", lang), emptyKB)
	return false
}

func (h *Handler) isChatAdmin(chatID, userID int64) bool {
	m, err := h.bot.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{ChatID: chatID, UserID: userID},
	})
	if err != nil {
		return false
	}
	return m.IsAdministrator() || m.IsCreator()
}

func (h *Handler) isChannelAdmin(chatID, userID int64) bool {
	return h.isChatAdmin(chatID, userID)
}

func (h *Handler) botIsAdmin(chatID int64) bool {
	m, err := h.bot.GetChatMember(tgbotapi.GetChatMemberConfig{ChatConfigWithUser: tgbotapi.ChatConfigWithUser{ChatID: chatID, UserID: h.selfID}})
	if err != nil {
		return false
	}
	return m.IsAdministrator() || m.IsCreator()
}

func (h *Handler) cmdBan(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	h.store.SetState(chat.ID, "awaiting_ban_user")
	h.sendMsg(chat.ID, localization.Get("banPrompt", lang), emptyKB)
}

func (h *Handler) doBan(chatID int64, targetID int64, lang string) {
	if !h.botIsAdmin(chatID) {
		h.sendMsg(chatID, localization.Get("banError", lang), emptyKB)
		return
	}
	if _, err := h.bot.Request(tgbotapi.BanChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: targetID},
		UntilDate:        0,
		RevokeMessages:   true,
	}); err != nil {
		log.Printf("doBan error: %v", err)
		h.sendMsg(chatID, localization.Get("banError", lang), emptyKB)
		return
	}
	h.sendMsg(chatID, fmt.Sprintf("🚫 %s\n%s", fmt.Sprintf("@%d", targetID), localization.Get("banSuccess", lang)), emptyKB)
}

func (h *Handler) cmdUnban(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if h.promptTarget(chat, msg, lang, "unbanPrompt") {
		return
	}
	target := h.targetFrom(msg)
	if target == 0 {
		return
	}
	if _, err := h.bot.Request(tgbotapi.UnbanChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chat.ID, UserID: target},
		OnlyIfBanned:     true,
	}); err != nil {
		log.Printf("cmdUnban error: %v", err)
		h.sendMsg(chat.ID, localization.Get("unbanError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("unbanSuccess", lang), emptyKB)
}

func (h *Handler) cmdKick(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if h.promptTarget(chat, msg, lang, "kickPrompt") {
		return
	}
	target := h.targetFrom(msg)
	if target == 0 {
		return
	}
	if _, err := h.bot.Request(tgbotapi.UnbanChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chat.ID, UserID: target},
		OnlyIfBanned:     false,
	}); err != nil {
		log.Printf("cmdKick error: %v", err)
		h.sendMsg(chat.ID, localization.Get("kickError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("kickSuccess", lang), emptyKB)
}

func (h *Handler) cmdMute(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if h.promptTarget(chat, msg, lang, "mutePrompt") {
		return
	}
	target := h.targetFrom(msg)
	if target == 0 {
		return
	}
	if err := h.setMute(chat.ID, target, true); err != nil {
		log.Printf("cmdMute error: %v", err)
		h.sendMsg(chat.ID, localization.Get("muteError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("muteSuccess", lang), emptyKB)
}

func (h *Handler) setMute(chatID, userID int64, mute bool) error {
	until := int64(0)
	perms := tgbotapi.ChatPermissions{
		CanSendMessages:       !mute,
		CanSendMediaMessages:  !mute,
		CanSendOtherMessages:  !mute,
		CanSendPolls:          !mute,
		CanAddWebPagePreviews: !mute,
		CanChangeInfo:         !mute,
		CanInviteUsers:        !mute,
	}
	if mute {
		until = time.Now().Add(365 * 24 * time.Hour).Unix()
	}
	_, err := h.bot.Request(tgbotapi.RestrictChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chatID, UserID: userID},
		Permissions:      &perms,
		UntilDate:        until,
	})
	return err
}

func (h *Handler) cmdPromote(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if h.promptTarget(chat, msg, lang, "addadminPrompt") {
		return
	}
	target := h.targetFrom(msg)
	if target == 0 {
		return
	}
	if _, err := h.bot.Request(tgbotapi.PromoteChatMemberConfig{
		ChatMemberConfig:   tgbotapi.ChatMemberConfig{ChatID: chat.ID, UserID: target},
		CanDeleteMessages:  true,
		CanManageChat:      true,
		CanRestrictMembers: true,
		CanInviteUsers:     true,
		CanChangeInfo:      true,
		CanPinMessages:     true,
	}); err != nil {
		log.Printf("cmdPromote error: %v", err)
		h.sendMsg(chat.ID, localization.Get("addadminError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("addadminSuccess", lang), emptyKB)
}

func (h *Handler) cmdDemote(chat *tgbotapi.Chat, user *tgbotapi.User, msg *tgbotapi.Message, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if h.promptTarget(chat, msg, lang, "removeadminPrompt") {
		return
	}
	target := h.targetFrom(msg)
	if target == 0 {
		return
	}
	if _, err := h.bot.Request(tgbotapi.PromoteChatMemberConfig{
		ChatMemberConfig: tgbotapi.ChatMemberConfig{ChatID: chat.ID, UserID: target},
	}); err != nil {
		log.Printf("cmdDemote error: %v", err)
		h.sendMsg(chat.ID, localization.Get("removeadminError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("removeadminSuccess", lang), emptyKB)
}

func (h *Handler) cmdDel(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	h.store.SetState(chat.ID, "awaiting_del")
	h.sendMsg(chat.ID, localization.Get("delPrompt", lang), emptyKB)
}

func (h *Handler) cmdInvite(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	inv := tgbotapi.CreateChatInviteLinkConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chat.ID}}
	resp, err := h.bot.Request(inv)
	if err != nil {
		log.Printf("cmdInvite error: %v", err)
		h.sendMsg(chat.ID, localization.Get("inviteError", lang), emptyKB)
		return
	}
	link := ""
	if resp != nil && len(resp.Result) > 0 {
		var created struct {
			InviteLink string `json:"invite_link"`
		}
		if err := json.Unmarshal(resp.Result, &created); err != nil {
			log.Printf("cmdInvite decode error: %v", err)
		}
		link = created.InviteLink
	}
	if link == "" {
		h.sendMsg(chat.ID, localization.Get("inviteError", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, inviteMessage(lang, link), emptyKB)
}

func (h *Handler) cmdWelcome(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	g := h.store.GetGroup(chat.ID)
	g.Title = chat.Title
	h.store.SetState(chat.ID, "awaiting_welcome_msg")
	h.store.SetGroup(g)
	h.sendMsg(chat.ID, localization.Get("welcomePrompt", lang), emptyKB)
}

func (h *Handler) cmdGroups(user *tgbotapi.User, lang string) {
	if !h.cfg.IsAdmin(int64(user.ID)) {
		h.sendMsg(user.ID, localization.Get("noPermission", lang), emptyKB)
		return
	}
	groups := h.store.ListGroups()
	if len(groups) == 0 {
		h.sendMsg(user.ID, localization.Get("groupsTitle", lang)+"\n\n0", emptyKB)
		return
	}
	var b strings.Builder
	b.WriteString(localization.Get("groupsTitle", lang))
	b.WriteString("\n\n")
	for _, g := range groups {
		title := g.Title
		if title == "" {
			title = fmt.Sprintf("%d", g.ChatID)
		}
		b.WriteString(fmt.Sprintf("• %s (%d)\n", title, g.ChatID))
	}
	h.sendMsg(user.ID, b.String(), emptyKB)
}

func (h *Handler) cmdChannels(user *tgbotapi.User, lang string) {
	if !h.cfg.IsAdmin(int64(user.ID)) {
		h.sendMsg(user.ID, localization.Get("noPermission", lang), emptyKB)
		return
	}
	channels := h.store.ListChannels()
	if len(channels) == 0 {
		h.sendMsg(user.ID, localization.Get("channelsNoChannels", lang), emptyKB)
		return
	}
	var b strings.Builder
	b.WriteString(localization.Get("channelsTitle", lang))
	b.WriteString("\n\n")
	for _, c := range channels {
		title := c.Title
		if title == "" {
			title = fmt.Sprintf("%d", c.ChatID)
		}
		b.WriteString(fmt.Sprintf("• %s (%d)\n", title, c.ChatID))
		b.WriteString(localization.Get("chstatsMsgsShort", lang, c.MsgCount))
		if c.LastActive != "" {
			b.WriteString(localization.Get("chstatsSeen", lang, c.LastActive[:min(19, len(c.LastActive))]))
		}
		b.WriteString("\n")
	}
	h.sendMsg(user.ID, b.String(), emptyKB)
}

func (h *Handler) cmdGInfo(chat *tgbotapi.Chat, user *tgbotapi.User, lang string, part string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	if part == "name" {
		h.store.SetState(chat.ID, "awaiting_ginfo_name")
		h.sendMsg(chat.ID, localization.Get("ginfoNamePrompt", lang), emptyKB)
		return
	}
	h.store.SetState(chat.ID, "awaiting_ginfo_desc")
	h.sendMsg(chat.ID, localization.Get("ginfoDescPrompt", lang), emptyKB)
}

func (h *Handler) cmdGSettings(chat *tgbotapi.Chat, user *tgbotapi.User, lang string, toggle string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	g := h.store.GetGroup(chat.ID)
	g.Title = chat.Title
	switch toggle {
	case "welcome":
		g.WelcomeOn = !g.WelcomeOn
		h.store.SetGroup(g)
		if g.WelcomeOn {
			h.sendMsg(chat.ID, localization.Get("welcomeEnabled", lang), emptyKB)
		} else {
			h.sendMsg(chat.ID, localization.Get("welcomeDisabled", lang), emptyKB)
		}
	case "lockdown":
		g.Lockdown = !g.Lockdown
		h.store.SetGroup(g)
		h.sendMsg(chat.ID, localization.Get("gsettingsToggle", lang, fmt.Sprintf("lockdown=%v", g.Lockdown)), emptyKB)
	case "antilinks":
		g.AntiLinks = !g.AntiLinks
		h.store.SetGroup(g)
		h.sendMsg(chat.ID, localization.Get("gsettingsToggle", lang, fmt.Sprintf("antiLinks=%v", g.AntiLinks)), emptyKB)
	case "anticaps":
		g.AntiCaps = !g.AntiCaps
		h.store.SetGroup(g)
		h.sendMsg(chat.ID, localization.Get("gsettingsToggle", lang, fmt.Sprintf("antiCaps=%v", g.AntiCaps)), emptyKB)
	default:
		msg := fmt.Sprintf("%s\n\nwelcome=%v\nlockdown=%v\nantiLinks=%v\nantiCaps=%v",
			localization.Get("gsettingsTitle", lang), g.WelcomeOn, g.Lockdown, g.AntiLinks, g.AntiCaps)
		h.sendMsg(chat.ID, msg, emptyKB)
	}
}

func (h *Handler) cmdModerate(chat *tgbotapi.Chat, user *tgbotapi.User, lang string, toggle string) {
	h.cmdGSettings(chat, user, lang, toggle)
}

func (h *Handler) cmdGStats(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireGroupAdmin(chat, user, lang) {
		return
	}
	g := h.store.GetGroup(chat.ID)
	memberCount := 0
	if c, err := h.bot.GetChatMembersCount(tgbotapi.ChatMemberCountConfig{ChatConfig: tgbotapi.ChatConfig{ChatID: chat.ID}}); err == nil {
		memberCount = c
	}
	msg := fmt.Sprintf("%s\n%s\n%s\n%s",
		localization.Get("gstatsTitle", lang, g.MsgCount, "-"),
		localization.Get("gstatsMembers", lang, memberCount),
		localization.Get("gstatsMsgs", lang, g.MsgCount),
		localization.Get("gstatsTop", lang, "-"))
	h.sendMsg(chat.ID, msg, emptyKB)
}

func (h *Handler) cmdChSettings(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireChannel(chat, user, lang) {
		return
	}
	g := h.store.GetGroup(chat.ID)
	g.Title = chat.Title
	g.IsChannel = true
	h.store.SetGroup(g)

	var b strings.Builder
	b.WriteString(localization.Get("chsettingsTitle", lang))
	b.WriteString("\n\n")
	b.WriteString(localization.Get("chstatsTitle", lang))
	b.WriteString("\n")
	b.WriteString(localization.Get("gstatsMsgs", lang, g.MsgCount))
	if g.LastActive != "" {
		b.WriteString("\n")
		b.WriteString(localization.Get("chstatsLastSeen", lang, g.LastActive[:min(19, len(g.LastActive))]))
	}
	b.WriteString("\n\n")
	b.WriteString(localization.Get("chsettingsHint", lang))
	h.sendMsg(chat.ID, b.String(), emptyKB)
}

func (h *Handler) cmdChStats(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.requireChannel(chat, user, lang) {
		return
	}
	g := h.store.GetGroup(chat.ID)
	msg := fmt.Sprintf("%s\n%s", localization.Get("chstatsTitle", lang), localization.Get("gstatsMsgs", lang, g.MsgCount))
	h.sendMsg(chat.ID, msg, emptyKB)
}

func (h *Handler) cmdStream(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.isChat(chat) {
		h.sendMsg(chat.ID, localization.Get("noGroupAccess", lang), emptyKB)
		return
	}
	if !h.requireGroupAdmin(chat, user, lang) && !h.requireChannel(chat, user, lang) {
		return
	}
	h.store.SetState(chat.ID, "awaiting_stream_url")
	h.sendMsg(chat.ID, localization.Get("streamPrompt", lang), emptyKB)
}

func (h *Handler) cmdPost(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	if !h.isChat(chat) {
		h.sendMsg(chat.ID, localization.Get("noGroupAccess", lang), emptyKB)
		return
	}
	if h.cfg.IsAdmin(int64(user.ID)) || h.isChatAdmin(chat.ID, int64(user.ID)) {
		h.store.SetState(chat.ID, "awaiting_post")
		h.sendMsg(chat.ID, localization.Get("postPrompt", lang), emptyKB)
		return
	}
	h.sendMsg(chat.ID, localization.Get("notAdmin", lang), emptyKB)
}

// targetFrom resolves the user to act on: reply-to message, or /cmd@username.
func (h *Handler) targetFrom(msg *tgbotapi.Message) int64 {
	if msg == nil {
		return 0
	}
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		return int64(msg.ReplyToMessage.From.ID)
	}
	fields := strings.Fields(msg.Text)
	if len(fields) < 2 {
		return 0
	}
	ref := strings.TrimPrefix(fields[1], "@")
	chat, err := h.bot.GetChat(tgbotapi.ChatInfoConfig{ChatConfig: tgbotapi.ChatConfig{SuperGroupUsername: ref}})
	if err != nil || !chat.IsPrivate() {
		return 0
	}
	return chat.ID
}

// needsTarget reports whether the admin still has to be told to reply to
// someone. Kept free of side effects so the nil-message path is testable.
func (h *Handler) needsTarget(msg *tgbotapi.Message) bool {
	return h.targetFrom(msg) == 0
}

// promptTarget asks the admin to reply to the target user, using the prompt
// that belongs to this specific action. It sends against chat, never msg:
// on the button path msg is nil.
func (h *Handler) promptTarget(chat *tgbotapi.Chat, msg *tgbotapi.Message, lang, promptKey string) bool {
	if !h.needsTarget(msg) {
		return false
	}
	h.sendMsg(chat.ID, localization.Get(promptKey, lang), emptyKB)
	return true
}

// onNewMember welcomes users when welcome is on.
func (h *Handler) onNewMember(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	g := h.store.GetGroup(chat.ID)
	if !g.WelcomeOn || g.Welcome == "" {
		return
	}
	txt := strings.ReplaceAll(g.Welcome, "{user}", user.FirstName)
	txt = strings.ReplaceAll(txt, "{username}", user.UserName)
	txt = strings.ReplaceAll(txt, "{group}", chat.Title)
	h.sendMsg(chat.ID, txt, emptyKB)
}

// trackMessage increments counters and applies anti-spam rules.
func (h *Handler) trackMessage(chat *tgbotapi.Chat, user *tgbotapi.User, text string, lang string) {
	h.trackMessageID(chat, user, text, lang, 0)
}

// trackMessageID is trackMessage with the id of the message being policed.
// Without it lockdown muted the sender and then asked deleteMsg to remove
// message 0, which the API rejects, so nothing was ever deleted.
func (h *Handler) trackMessageID(chat *tgbotapi.Chat, user *tgbotapi.User, text string, lang string, msgID int) {
	if !h.isChat(chat) || user == nil {
		return
	}
	g := h.store.GetGroup(chat.ID)
	g.Title = chat.Title
	g.MsgCount++
	g.LastActive = time.Now().Format(time.RFC3339)
	h.store.SetGroup(g)

	if g.Lockdown {
		if err := h.setMute(chat.ID, int64(user.ID), true); err == nil {
			h.deleteMsg(chat.ID, msgID)
		}
		return
	}

	upper := upperRatio(text)
	// Counted per user, not per chat. A shared counter let a third person be
	// muted on a first offence because two others had shouted, and let anyone's
	// ordinary message clear a persistent shouter's tally.
	if g.AntiCaps && upper >= antiCapsRatio && letters(text) >= antiCapsMinLetters {
		warns := g.BumpCapsWarn(int64(user.ID))
		h.store.SetGroup(g)
		if warns >= warnLimit {
			if err := h.setMute(chat.ID, int64(user.ID), true); err == nil {
				h.deleteMsg(chat.ID, msgID)
				h.sendMsg(chat.ID, localization.Get("gMuted", lang), emptyKB)
			}
			g.ClearCapsWarn(int64(user.ID))
			h.store.SetGroup(g)
			return
		}
		h.sendMsg(chat.ID, localization.Get("gWarned", lang, warnLimit-warns), emptyKB)
		return
	}

	// One tolerated message resets that sender's counter, so an occasional shout
	// is not carried over to a later one. Nobody else's is touched.
	if g.CapsWarnCount(int64(user.ID)) > 0 {
		g.ClearCapsWarn(int64(user.ID))
		h.store.SetGroup(g)
	}

	if g.AntiLinks && strings.Contains(text, "http") {
		if h.deleteMsg(chat.ID, msgID) {
			h.sendMsg(chat.ID, localization.Get("gsettingsToggle", lang, "antiLinks"), emptyKB)
		}
	}
}

// warnLimit is how many shouted messages are tolerated before a mute.
const warnLimit = 3

// antiCapsRatio and antiCapsMinLetters keep short acronyms ("OK", "NASA") out of
// the way; only sustained shouting counts.
const (
	antiCapsRatio      = 0.7
	antiCapsMinLetters = 8
)

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// upperRatio is the share of letters that are uppercase.
func upperRatio(s string) float64 {
	total, upper := 0, 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		total++
		if unicode.IsUpper(r) {
			upper++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(upper) / float64(total)
}

func (h *Handler) deleteMsg(chatID int64, msgID int) bool {
	if msgID == 0 {
		return false
	}
	_, err := h.bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: msgID})
	return h.handleDelErr(err)
}

// deleteBatch removes up to 100 messages in one call. tgbotapi has no config
// for deleteMessages, so the raw endpoint is used. Telegram silently skips ids
// it cannot remove, so a successful call means the batch was accepted.
func (h *Handler) deleteBatch(chatID int64, ids []int) bool {
	if len(ids) == 0 {
		return false
	}
	if len(ids) > delBatchMax {
		ids = ids[:delBatchMax]
	}
	list, err := json.Marshal(ids)
	if err != nil {
		return false
	}
	params := tgbotapi.Params{
		"chat_id":     strconv.FormatInt(chatID, 10),
		"message_ids": string(list),
	}
	for attempt := 0; attempt < delBatchRetries; attempt++ {
		_, err := h.bot.MakeRequest("deleteMessages", params)
		if err == nil {
			return true
		}
		if wait := retryAfter(err); wait > 0 {
			log.Printf("deleteMessages flood wait %ds (batch %d)", wait, len(ids))
			time.Sleep(time.Duration(wait) * time.Second)
			continue
		}
		log.Printf("deleteMessages error: %v", err)
		return false
	}
	return false
}

// deleteMsgs deletes a run of recent message ids and returns how many were
// accepted by Telegram. Ids are deduped, capped at 100 and sent in paced
// batches: one deleteMessage per message trips the ~30 msg/s group flood limit
// almost immediately.
func (h *Handler) deleteMsgs(chatID int64, ids []int) int {
	uniq := dedupeIDs(ids)
	if len(uniq) == 0 {
		return 0
	}
	if len(uniq) > delBatchMax {
		uniq = uniq[:delBatchMax]
	}

	deleted := 0
	for start := 0; start < len(uniq); start += delBatchSize {
		end := start + delBatchSize
		if end > len(uniq) {
			end = len(uniq)
		}
		batch := uniq[start:end]

		if h.deleteBatch(chatID, batch) {
			deleted += len(batch)
			if end < len(uniq) {
				time.Sleep(delBatchPause)
			}
			continue
		}

		for _, id := range batch {
			if h.deleteMsg(chatID, id) {
				deleted++
			}
			time.Sleep(delSinglePause)
		}
	}
	if deleted < len(uniq) {
		log.Printf("purge: %d/%d ids removed (rest older than 48h, service messages or not the bot's)", deleted, len(uniq))
	}
	return deleted
}

// dedupeIDs keeps the original order and drops non-positive and repeat ids.
func dedupeIDs(ids []int) []int {
	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// recentIDs builds the run of message ids ending at last, oldest last.
func recentIDs(last, count int) []int {
	if count > delBatchMax {
		count = delBatchMax
	}
	if count < 1 {
		count = 1
	}
	ids := make([]int, 0, count)
	for i := 0; i < count; i++ {
		ids = append(ids, last-i)
	}
	return ids
}

// retryAfter pulls the flood-wait hint out of a Telegram API error, in seconds.
func retryAfter(err error) int {
	var apiErr *tgbotapi.Error
	if errors.As(err, &apiErr) {
		return apiErr.ResponseParameters.RetryAfter
	}
	return 0
}

// handleDelErr logs a failed delete and reports whether it was a flood wait.
func (h *Handler) handleDelErr(err error) bool {
	if err == nil {
		return true
	}
	if wait := retryAfter(err); wait > 0 {
		time.Sleep(time.Duration(wait) * time.Second)
		return false
	}
	if !strings.Contains(err.Error(), "message to delete not found") {
		log.Printf("delete error: %v", err)
	}
	return false
}

func (h *Handler) FireReminders() {
	h.fireReminders()
}

func (h *Handler) HandleNewMembers(update tgbotapi.Update) {
	if update.Message == nil || update.Message.NewChatMembers == nil {
		return
	}
	chat := update.Message.Chat
	if !h.isChat(chat) {
		return
	}
	lang := h.langOf(chat.ID)
	for _, m := range update.Message.NewChatMembers {
		h.onNewMember(chat, &m, lang)
	}
}

func (h *Handler) fireReminders() {
	for _, r := range h.store.DueReminders(time.Now().Unix()) {
		txt := fmt.Sprintf("⏰ %s", r.Text)
		if _, err := h.bot.Send(tgbotapi.NewMessage(r.ChatID, txt)); err != nil {
			log.Printf("fireReminders send error: %v", err)
		}
		h.store.DeleteReminder(r.ID)
	}
}

func (h *Handler) cmdRemind(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	rem := h.store.ListReminders(int64(user.ID))
	if len(rem) == 0 {
		h.sendMsg(chat.ID, localization.Get("remindNoReminders", lang), emptyKB)
		return
	}
	var b strings.Builder
	b.WriteString(localization.Get("remindList", lang, ""))
	for i, r := range rem {
		if i >= 10 {
			break
		}
		b.WriteString(fmt.Sprintf("• %s — %s\n", r.Text, time.Unix(r.DueAt, 0).Format("2006-01-02 15:04")))
	}
	h.sendMsg(chat.ID, b.String(), emptyKB)
}

// parseRemind accepts "10m call mom", "10 m call mom" and "2 hours sleep".
func (h *Handler) parseRemind(input string) (time.Duration, string, bool) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return 0, "", false
	}

	// the amount and unit may be glued together: "10m"
	amount, unit := splitAmountUnit(fields[0])
	rest := fields[1:]
	if amount == "" {
		if len(fields) < 2 {
			return 0, "", false
		}
		amount, unit, rest = fields[0], fields[1], fields[2:]
	}
	if len(rest) == 0 {
		return 0, "", false
	}

	var mult time.Duration
	switch strings.ToLower(unit) {
	case "s", "sec", "secs", "second", "seconds":
		mult = time.Second
	case "m", "min", "mins", "minute", "minutes":
		mult = time.Minute
	case "h", "hr", "hrs", "hour", "hours":
		mult = time.Hour
	case "d", "day", "days":
		mult = 24 * time.Hour
	default:
		return 0, "", false
	}

	d := parseNum(amount, mult)
	if d <= 0 {
		return 0, "", false
	}
	return d, strings.Join(rest, " "), true
}

// splitAmountUnit peels a trailing unit off a token: "10m" -> "10", "m".
func splitAmountUnit(tok string) (amount, unit string) {
	i := len(tok)
	for i > 0 {
		c := tok[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	if i == 0 || i == len(tok) {
		return "", ""
	}
	return tok[:i], tok[i:]
}

func parseNum(s string, unit time.Duration) time.Duration {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return time.Duration(f * float64(unit))
}

func (h *Handler) cmdHistory(chat *tgbotapi.Chat, user *tgbotapi.User, lang string) {
	qs := h.store.RecentQueries(int64(user.ID), 15)
	if len(qs) == 0 {
		h.sendMsg(chat.ID, localization.Get("historyEmpty", lang), emptyKB)
		return
	}
	var b strings.Builder
	b.WriteString(localization.Get("historyTitle", lang))
	b.WriteString("\n\n")
	for i, q := range qs {
		b.WriteString(fmt.Sprintf("%d. %s — %s\n", i+1, q.Command, q.Input))
	}
	h.sendMsg(chat.ID, b.String(), emptyKB)
}

func (h *Handler) logQuery(uid int64, cmd, input string) {
	h.store.LogQuery(uid, cmd, input)
}

// weatherResp mirrors /info/weather. Exported shapes so the API contract is
// testable against captured payloads instead of a hand-copied struct.
type weatherResp struct {
	Data struct {
		Result struct {
			CurrentCondition []struct {
				TempC         string `json:"temp_C"`
				FeelsLikeC    string `json:"FeelsLikeC"`
				Humidity      string `json:"humidity"`
				WindspeedKmph string `json:"windspeedKmph"`
				WeatherDesc   []struct {
					Value string `json:"value"`
				} `json:"weatherDesc"`
			} `json:"current_condition"`
		} `json:"result"`
	} `json:"data"`
}

type translateResp struct {
	Data struct {
		Translation      string `json:"translation"`
		DetectedLanguage string `json:"detectedLanguage"`
	} `json:"data"`
}

type convertResp struct {
	Data struct {
		Output    float64 `json:"output"`
		Formatted string  `json:"formatted"`
	} `json:"data"`
}

type memeResp struct {
	Data struct {
		MemeURL string `json:"meme_url"`
	} `json:"data"`
}

// fetchQR returns a PNG QR code for text.
func (h *Handler) fetchQR(chatID, uid int64, text, lang string) {
	apiURL := fmt.Sprintf("%s/qr/pro?text=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.QueryEscape(text), h.cfg.EffectiveApiKey())
	h.sendMsg(chatID, localization.Get("qrSending", lang), emptyKB)
	body, _, err := fetchMedia(apiURL)
	if err != nil {
		log.Printf("fetchQR error: %v", err)
		h.sendMsg(chatID, localization.Get("qrError", lang), emptyKB)
		return
	}
	msg := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "qr.png", Bytes: body})
	msg.Caption = h.p(localization.Get("qrSuccess", lang))
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("fetchQR send error: %v", err)
		h.sendMsg(chatID, localization.Get("qrError", lang), emptyKB)
	}
	h.logQuery(uid, "qr", text)
}

func (h *Handler) fetchWeather(chatID, uid int64, city, lang string) {
	apiURL := fmt.Sprintf("%s/info/weather?city=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.QueryEscape(city), h.cfg.EffectiveApiKey())
	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("fetchWeather error: %v", err)
		h.sendMsg(chatID, localization.Get("weatherError", lang), emptyKB)
		return
	}
	var parsed weatherResp
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Data.Result.CurrentCondition) == 0 {
		log.Printf("fetchWeather parse error: %v", err)
		h.sendMsg(chatID, localization.Get("weatherError", lang), emptyKB)
		return
	}
	c := parsed.Data.Result.CurrentCondition[0]
	desc := ""
	if len(c.WeatherDesc) > 0 {
		desc = c.WeatherDesc[0].Value
	}
	msg := localization.Get("weatherResult", lang, escapeMarkdown(city), escapeMarkdown(desc),
		c.TempC+"C", c.WindspeedKmph+" km/h", c.Humidity+"%")
	h.sendMsg(chatID, msg, emptyKB)
	h.logQuery(uid, "weather", city)
}

func (h *Handler) fetchTranslate(chatID, uid int64, text, target, lang string) {
	apiURL := fmt.Sprintf("%s/translate/google?text=%s&to=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.QueryEscape(text), url.QueryEscape(target), h.cfg.EffectiveApiKey())
	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("fetchTranslate error: %v", err)
		h.sendMsg(chatID, localization.Get("translateError", lang), emptyKB)
		return
	}
	var parsed translateResp
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data.Translation == "" {
		log.Printf("fetchTranslate parse error: %v", err)
		h.sendMsg(chatID, localization.Get("translateError", lang), emptyKB)
		return
	}
	// Both values come from the translation API, so both can carry Markdown
	// characters. Google renders a space as "_" in Urdu, which left the message
	// with an unpaired underscore and made Telegram reject the whole send.
	msg := localization.Get("translateResult", lang,
		escapeMarkdown(parsed.Data.DetectedLanguage),
		escapeMarkdown(parsed.Data.Translation))
	h.sendMsg(chatID, msg, emptyKB)
	h.logQuery(uid, "translate", text)
}

func (h *Handler) fetchConvert(chatID, uid int64, amount, from, to, lang string) {
	apiURL := fmt.Sprintf("%s/converter/unit?from=%s&to=%s&value=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.QueryEscape(from), url.QueryEscape(to), url.QueryEscape(amount), h.cfg.EffectiveApiKey())
	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("fetchConvert error: %v", err)
		h.sendMsg(chatID, localization.Get("convertError", lang), emptyKB)
		return
	}
	var parsed convertResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("fetchConvert parse error: %v", err)
		h.sendMsg(chatID, localization.Get("convertError", lang), emptyKB)
		return
	}
	h.sendMsg(chatID, localization.Get("convertResult", lang, amount, from, strconv.FormatFloat(parsed.Data.Output, 'f', -1, 64), to), emptyKB)
	h.logQuery(uid, "convert", amount+" "+from+" "+to)
}

// redditPost is one result. The endpoint nests its list under different keys
// depending on the caller, so the list is picked at runtime rather than declared.
type redditPost struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	Link      string  `json:"link"`
	Permalink string  `json:"permalink"`
	Score     float64 `json:"score"`
	Comments  float64 `json:"num_comments"`
	Author    string  `json:"author"`
	Sub       string  `json:"subreddit"`
	Thumb     string  `json:"thumbnail"`
	Img       string  `json:"url_image"`
}

func (p redditPost) link() string {
	switch {
	case strings.HasPrefix(p.URL, "http"):
		return p.URL
	case strings.HasPrefix(p.Link, "http"):
		return p.Link
	case p.Permalink != "":
		return "https://www.reddit.com" + p.Permalink
	}
	return ""
}

// fetchReddit renders search results as posts.
//
// It used to download the response and send it as reddit.json, which handed the
// user a file to read instead of the thing they asked for. The endpoint's own
// error is now reported instead, because a failing upstream used to arrive as an
// unopenable attachment rather than a message.
func (h *Handler) fetchReddit(chatID, uid int64, sub, lang string) {
	apiURL := fmt.Sprintf("%s/reddit/search?query=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.QueryEscape(sub), h.cfg.EffectiveApiKey())
	// apiGet, not fetchMedia: fetchMedia follows any URL it finds inside a JSON
	// body, so it would have chased the first post link and returned a web page.
	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("reddit: request failed: %v", err)
		h.sendMsg(chatID, localization.Get("redditError", lang), emptyKB)
		return
	}

	var env struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	var root map[string]interface{}
	if err := json.Unmarshal(body, &env); err != nil {
		log.Printf("reddit: decode failed: %v", err)
		h.sendMsg(chatID, localization.Get("redditError", lang), emptyKB)
		return
	}
	if err := json.Unmarshal(body, &root); err != nil {
		log.Printf("reddit: decode body failed: %v", err)
	}
	if !env.Success {
		log.Printf("reddit: upstream error: %s", env.Error)
		msg := localization.Get("redditError", lang)
		if env.Error != "" {
			msg = localization.Get("redditFailed", lang, env.Error)
		}
		h.sendMsg(chatID, msg, emptyKB)
		return
	}

	posts := redditPostsFrom(root)
	if len(posts) == 0 {
		h.sendMsg(chatID, localization.Get("redditNoPosts", lang, sub), emptyKB)
		return
	}

	if len(posts) > redditMaxPosts {
		posts = posts[:redditMaxPosts]
	}
	sent := 0
	for _, p := range posts {
		var b strings.Builder
		b.WriteString(h.p(localization.Get("redditPost", lang)))
		if p.Title != "" {
			b.WriteString("\n" + h.p(localization.Get("redditTitle", lang, p.Title)))
		}
		meta := localization.Get("redditMeta", lang, redditInt(int(p.Score)), redditInt(int(p.Comments)))
		if p.Author != "" {
			meta = localization.Get("redditMetaAuthor", lang, p.Author, meta)
		}
		b.WriteString("\n" + h.p(meta))
		if link := p.link(); link != "" {
			b.WriteString("\n" + h.p(link))
		}

		// A URL button, not a data button. callback_data is capped at 64 bytes
		// and a reddit permalink runs past that, so the whole sendMessage was
		// rejected with BUTTON_DATA_INVALID; a short one produced a callback no
		// handler serves, so the button did nothing either way.
		var kb tgbotapi.InlineKeyboardMarkup
		if link := p.link(); link != "" {
			kb = tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonURL(localization.Get("redditOpen", lang), link),
				),
			)
		}
		msg := tgbotapi.NewMessage(chatID, b.String())
		msg.ReplyMarkup = kb
		if _, err := h.bot.Send(msg); err != nil {
			log.Printf("reddit send: %v", err)
			continue
		}
		sent++
	}

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("redditError", lang), emptyKB)
		return
	}
	h.sendMsg(chatID, localization.Get("redditSent", lang, sub, sent), emptyKB)
	h.logQuery(uid, "reddit", sub)
}

// redditPostsFrom finds the post list wherever the endpoint put it.
func redditPostsFrom(root map[string]interface{}) []redditPost {
	var out []redditPost
	for _, key := range []string{"data", "posts", "results", "children", "items"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		// A wrapper object holds the list one level down: Reddit's own API puts
		// it at data.children.
		if wrapper, isObj := raw.(map[string]interface{}); isObj {
			for _, inner := range []string{"children", "posts", "data", "items", "results"} {
				if list, isList := wrapper[inner].([]interface{}); isList {
					raw = list
					ok = true
					break
				}
			}
		}
		list, isList := raw.([]interface{})
		if !ok || !isList {
			continue
		}
		for _, item := range list {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			// Reddit's own API nests the post under "data".
			if inner, ok := obj["data"].(map[string]interface{}); ok {
				obj = inner
			}
			b, err := json.Marshal(obj)
			if err != nil {
				continue
			}
			var p redditPost
			if err := json.Unmarshal(b, &p); err != nil {
				continue
			}
			if p.Title == "" && p.link() == "" {
				continue
			}
			out = append(out, p)
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

// redditMaxPosts keeps one query from filling a chat.
const redditMaxPosts = 5

func redditInt(n int) string {
	return strconv.Itoa(n)
}

func (h *Handler) fetchMeme(chatID, uid int64, template, text1, text2, lang string) {
	apiURL := fmt.Sprintf("%s/meme/%s?text1=%s&text2=%s&apiKey=%s",
		h.cfg.EffectiveApiBaseURL(), url.PathEscape(template),
		url.QueryEscape(text1), url.QueryEscape(text2), h.cfg.EffectiveApiKey())
	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("fetchMeme error: %v", err)
		h.sendMsg(chatID, localization.Get("memeError", lang), emptyKB)
		return
	}
	var parsed memeResp
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data.MemeURL == "" {
		log.Printf("fetchMeme parse error: %v", err)
		h.sendMsg(chatID, localization.Get("memeError", lang), emptyKB)
		return
	}
	img, _, err := fetchMedia(parsed.Data.MemeURL)
	if err != nil {
		log.Printf("fetchMeme image error: %v", err)
		h.sendMsg(chatID, localization.Get("memeError", lang), emptyKB)
		return
	}
	msg := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "meme.jpg", Bytes: img})
	msg.Caption = h.p(localization.Get("memeSuccess", lang))
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("fetchMeme send error: %v", err)
	}
	h.logQuery(uid, "meme", template)
}

// forwardMedia re-downloads media from a forwarded message and sends it back.
func (h *Handler) forwardMedia(chatID, uid int64, from *tgbotapi.Message, lang string) {
	h.acquireDL()
	defer h.releaseDL()

	var mediaURL string
	if from.Photo != nil && len(from.Photo) > 0 {
		mediaURL = from.Photo[len(from.Photo)-1].FileID
	} else if from.Video != nil {
		mediaURL = from.Video.FileID
	} else if from.Document != nil {
		mediaURL = from.Document.FileID
	} else if from.Sticker != nil {
		mediaURL = from.Sticker.FileID
	}
	if mediaURL == "" {
		h.sendMsg(chatID, localization.Get("forwardNoMedia", lang), emptyKB)
		return
	}

	h.sendMsg(chatID, localization.Get("forwardProcessing", lang), emptyKB)

	f, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: mediaURL})
	if err != nil {
		log.Printf("forwardMedia getfile error: %v", err)
		h.sendMsg(chatID, localization.Get("forwardError", lang), emptyKB)
		return
	}
	data, err := h.downloadFile(f)
	if err != nil {
		log.Printf("forwardMedia download error: %v", err)
		h.sendMsg(chatID, localization.Get("forwardError", lang), emptyKB)
		return
	}

	if from.Video != nil {
		msg := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "file", Bytes: data})
		if _, err := h.bot.Send(msg); err != nil {
			h.sendMsg(chatID, localization.Get("forwardError", lang), emptyKB)
		}
		return
	}
	msg := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "file", Bytes: data})
	if _, err := h.bot.Send(msg); err != nil {
		h.sendMsg(chatID, localization.Get("forwardError", lang), emptyKB)
	}
}

// runToolAction starts the flow behind a menu button. It returns the prompt key
// to show, or "" when the action already answered the user directly.
func (h *Handler) runToolAction(action string, chat *tgbotapi.Chat, user *tgbotapi.User, lang string) string {
	switch action {
	case "qr":
		h.store.SetState(chat.ID, "awaiting_qr_text")
	case "weather":
		h.store.SetState(chat.ID, "awaiting_weather_city")
	case "translate":
		h.store.SetState(chat.ID, "awaiting_translate_text")
	case "convert":
		h.store.SetState(chat.ID, "awaiting_convert")
	case "meme":
		h.store.SetState(chat.ID, "awaiting_meme_template")
	case "reddit":
		h.store.SetState(chat.ID, "awaiting_reddit_sub")
	case "remind":
		h.store.SetState(chat.ID, "awaiting_remind")
	case "history":
		h.cmdHistory(chat, user, lang)
		return ""
	case "groups":
		h.cmdGroups(user, lang)
		return ""
	case "gstats":
		h.cmdGStats(chat, user, lang)
		return ""
	case "gsettings":
		h.cmdGSettings(chat, user, lang, "")
		return ""
	case "welcome":
		h.cmdWelcome(chat, user, lang)
		return ""
	case "ban":
		h.cmdBan(chat, user, lang)
		return ""
	case "kick":
		h.cmdKick(chat, user, nil, lang)
		return ""
	case "mute":
		h.cmdMute(chat, user, nil, lang)
		return ""
	case "invite":
		h.cmdInvite(chat, user, lang)
		return ""
	case "del":
		h.cmdDel(chat, user, lang)
		return ""
	default:
		return ""
	}
	h.store.SetSessionData(chat.ID, make(map[string]interface{}))
	return promptKeyFor(action)
}

func promptKeyFor(action string) string {
	switch action {
	case "qr":
		return "qrPrompt"
	case "weather":
		return "weatherPrompt"
	case "translate":
		return "translatePrompt"
	case "convert":
		return "convertPrompt"
	case "meme":
		return "memeSelectTemplate"
	case "reddit":
		return "redditPrompt"
	case "remind":
		return "remindPrompt"
	}
	return "error"
}

// inviteMessage renders the invite-link reply. Kept separate so the exact text
// sent to the user is testable.
func inviteMessage(lang, link string) string {
	return localization.Get("inviteLink", lang, link)
}
