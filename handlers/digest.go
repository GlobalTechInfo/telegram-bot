package handlers

import (
	"log"
	"strconv"
	"sync"
	"time"

	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Scheduled digests.
//
// The news feeds already existed and could only be read by asking for them in a
// chat. A digest posts the same feeds to a channel on a timer, which is what a
// channel is usually wanted for. It reuses the searcher registry, so a new feed is
// one line in search.go rather than another function.
//
// It is off unless tools.digest.channelId is set: posting to the wrong channel is
// not something to discover at runtime.

// digestIntervalFloor stops a typo in the config from posting every minute.
const digestIntervalFloor = 15 * time.Minute

var digestMu sync.Mutex

// StartDigest runs the digest loop until the process ends.
func (h *Handler) StartDigest() {
	channel := h.cfg.Tools.Digest.ChannelID
	if !h.cfg.Tools.Digest.Enabled || channel == 0 {
		return
	}

	every := time.Duration(h.cfg.Tools.Digest.EveryHours) * time.Hour
	if every < digestIntervalFloor {
		every = digestIntervalFloor
	}
	source, ok := searcherByID(h.cfg.Tools.Digest.Source)
	if !ok {
		log.Printf("⚠️ Digest source %q is not a registered searcher; digest is off",
			h.cfg.Tools.Digest.Source)
		return
	}
	if source.kind != kindNews {
		log.Printf("⚠️ Digest source %q is not a news feed; digest is off", source.id)
		return
	}

	log.Printf("🗞 Digest to %d every %s from %s", channel, every, source.name)

	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for range ticker.C {
			h.RunDigestOnce()
		}
	}()
}

// RunDigestOnce posts one digest. It is separate from the loop so it can be
// triggered on demand and tested.
func (h *Handler) RunDigestOnce() {
	digestMu.Lock()
	defer digestMu.Unlock()

	channel := h.cfg.Tools.Digest.ChannelID
	if channel == 0 {
		return
	}
	source, ok := searcherByID(h.cfg.Tools.Digest.Source)
	if !ok {
		return
	}
	// The same check StartDigest makes. Running once directly would otherwise post
	// whatever a misconfigured source returned, which for a media feed means
	// dumping images into the channel.
	if source.kind != kindNews {
		return
	}

	v, err := h.apiJSON(h.searchQuery(source, ""))
	if err != nil {
		log.Printf("digest: request failed: %v", err)
		return
	}
	if failed, isObj := apiFailed(v); failed {
		if isObj {
			log.Printf("digest: success=false: %s", apiReason(v))
		} else {
			log.Printf("digest: response was %T", v)
		}
		return
	}

	items := digList(v, source.listPath)
	if len(items) == 0 {
		log.Printf("digest: no stories from %s", source.id)
		return
	}

	headline := localization.Get("digestHeader", "en", source.name, time.Now().Format("2 Jan 2006"))
	if _, err := h.bot.Send(tgbotapi.NewMessage(channel, headline)); err != nil {
		log.Printf("digest: headline failed: %v", err)
		return
	}

	sent := 0
	for _, raw := range items {
		if sent >= digestMaxStories {
			break
		}
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		title := source.label(item)
		link := digString(item, "url", "link")
		if title == "" || link == "" {
			continue
		}
		line := "<b>" + escapeHTML(title) + "</b>"
		if desc := digString(item, "description"); desc != "" {
			if r := []rune(desc); len(r) > 200 {
				desc = string(r[:200]) + "…"
			}
			line += "\n" + escapeHTML(desc)
		}
		line += "\n" + escapeHTML(link)

		msg := tgbotapi.NewMessage(channel, line)
		msg.ParseMode = "HTML"
		msg.DisableWebPagePreview = true
		if _, err := h.bot.Send(msg); err != nil {
			log.Printf("digest: story failed: %v", err)
			continue
		}
		sent++
	}
	log.Printf("🗞 digest posted %d stories to %d", sent, channel)
}

// digestMaxStories keeps one run from flooding a channel.
const digestMaxStories = 5

// digestStatusShort is used in the admin panel.
func (h *Handler) digestStatusShort() string {
	d := h.cfg.Tools.Digest
	if !d.Enabled {
		return localization.Get("digestOff", "en")
	}
	if d.ChannelID == 0 {
		return localization.Get("digestNoChannel", "en")
	}
	every := d.EveryHours
	if every <= 0 {
		every = 6
	}
	return localization.Get("digestOn", "en", strconv.FormatInt(d.ChannelID, 10), strconv.Itoa(every))
}
