package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"

	"telegram-bot/keyboards"
	"telegram-bot/localization"
)

// A registry for everything the bot looks up rather than downloads.
//
// The searches, the news feeds, the sports feeds and the link shorteners were
// twenty-two near-identical functions: build a URL, GET it, read the body, check
// success, walk to a list, loop over it. Adding a source meant copying one of
// them. Each source is now a line of data here, and inline mode can reuse the
// same engine instead of needing its own.

type searchKind int

const (
	kindMedia searchKind = iota // images and videos, fetched and sent
	kindNews
	kindSports
	kindShortURL
)

// searcher is one data source.
type searcher struct {
	id       string
	name     string
	endpoint string
	// param carries the user's query. Empty means the endpoint takes none.
	param string
	// listPath is the route to the result list inside the response.
	listPath []string
	// urlKeys are the item fields that may hold a media URL, in preference order.
	urlKeys []string
	// titleKeys are the item fields that may hold a label.
	titleKeys []string
	count     int
	kind      searchKind
	// noQuery marks a source that is asked without the user's input.
	noQuery bool
	// resolve overrides urlKeys for a source whose media URL sits somewhere the
	// generic walk cannot reach. The sticker endpoint nests it under
	// size/format and used a dedicated helper for that.
	resolve func(item map[string]interface{}) string
}

func (s searcher) label(item map[string]interface{}) string {
	for _, k := range s.titleKeys {
		if v := digString(item, k); v != "" {
			return v
		}
	}
	return ""
}

// mediaURL returns the first usable media URL in an item.
func (s searcher) mediaURL(item map[string]interface{}) string {
	if s.resolve != nil {
		return s.resolve(item)
	}
	for _, k := range s.urlKeys {
		switch v := item[k].(type) {
		case string:
			if strings.HasPrefix(v, "http") {
				return v
			}
		case []interface{}:
			// Pinterest hands back images_url as a list of objects.
			for _, el := range v {
				m, ok := el.(map[string]interface{})
				if !ok {
					continue
				}
				if u := digString(m, "url", "img"); strings.HasPrefix(u, "http") {
					return u
				}
			}
		case map[string]interface{}:
			if u := digString(v, "url", "img"); strings.HasPrefix(u, "http") {
				return u
			}
		}
	}
	return ""
}

// searchers is every lookup source in one table.
var searchers = []searcher{
	{
		id: "bing_images", name: "Bing Images", endpoint: "/bing/image", param: "query",
		listPath: []string{"data", "results"},
		urlKeys:  []string{"direct", "url"}, titleKeys: []string{"title", "description"},
		count: 10, kind: kindMedia,
	},
	{
		id: "pin_search", name: "Pinterest", endpoint: "/pinterest/search", param: "query",
		listPath: []string{"data", "list"},
		urlKeys:  []string{"images_url", "pin"}, titleKeys: []string{"grid_title", "id"},
		count: 10, kind: kindMedia,
	},
	{
		id: "sticker_search", name: "Stickers", endpoint: "/stickers/search", param: "query",
		listPath: []string{"data", "result", "data", "data"},
		urlKeys:  []string{"file"}, titleKeys: []string{"title", "slug"},
		count: 10, kind: kindMedia, resolve: stickerFileURL,
	},
	{
		id: "imgur_search", name: "Imgur", endpoint: "/imgur/search", param: "query",
		listPath: []string{"data", "results"},
		urlKeys:  []string{"link_mp4", "link_gif", "link"}, titleKeys: []string{"title", "id"},
		count: 10, kind: kindMedia,
	},
	{
		id: "yt_search", name: "YouTube", endpoint: "/yts/searchVideos", param: "query",
		listPath: []string{"data", "videos"},
		urlKeys:  []string{"url", "thumbnail"}, titleKeys: []string{"title", "description"},
		count: 10, kind: kindMedia,
	},
	{
		id: "news_google", name: "Google News", endpoint: "/news/google", param: "query",
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "news_bbc", name: "BBC", endpoint: "/news/bbc", noQuery: true,
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "news_cnn", name: "CNN", endpoint: "/news/cnn", noQuery: true,
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "news_aljazeera", name: "Al Jazeera", endpoint: "/news/aljazeera", noQuery: true,
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "news_cgtn", name: "CGTN", endpoint: "/news/cgtnWorld", noQuery: true,
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "news_trt", name: "TRT", endpoint: "/news/trtWorld", noQuery: true,
		listPath: []string{"data", "articles"},
		urlKeys:  []string{"url"}, titleKeys: []string{"title"},
		count: 10, kind: kindNews,
	},
	{
		id: "sports_cricket", name: "Cricket", endpoint: "/sports/cricket", noQuery: true,
		listPath: []string{"data", "games"},
		urlKeys:  []string{}, titleKeys: []string{"name"},
		count: 15, kind: kindSports,
	},
	{
		id: "sports_nfl", name: "NFL", endpoint: "/sports/nfl", noQuery: true,
		listPath: []string{"data", "games"},
		urlKeys:  []string{}, titleKeys: []string{"name"},
		count: 15, kind: kindSports,
	},
	{
		id: "sports_nba", name: "NBA", endpoint: "/sports/nba", noQuery: true,
		listPath: []string{"data", "games"},
		urlKeys:  []string{}, titleKeys: []string{"name"},
		count: 15, kind: kindSports,
	},
	{
		id: "sports_cricbuzz", name: "Cricbuzz", endpoint: "/sports/cricbuzz", noQuery: true,
		listPath: []string{"data", "games"},
		urlKeys:  []string{}, titleKeys: []string{"name"},
		count: 15, kind: kindSports,
	},
	// The shorteners return one link, not a list.
	{id: "short_reurl", name: "reurl", endpoint: "/shortener/reurl", kind: kindShortURL},
	{id: "short_tinycc", name: "tinycc", endpoint: "/shortener/tinycc", kind: kindShortURL},
	{id: "short_itsssl", name: "itsssl", endpoint: "/shortener/itsssl", kind: kindShortURL},
	{id: "short_cuqin", name: "cuqin", endpoint: "/shortener/cuqin", kind: kindShortURL},
	{id: "short_vurl", name: "vurl", endpoint: "/shortener/vurl", kind: kindShortURL},
	{id: "short_tiny", name: "tiny", endpoint: "/shortener/tiny", kind: kindShortURL},
}

func searcherByID(id string) (searcher, bool) {
	for _, s := range searchers {
		if s.id == id {
			return s, true
		}
	}
	return searcher{}, false
}

// digList walks the path to the result list, returning an empty slice when the
// response does not have it.
func digList(v interface{}, path []string) []interface{} {
	cur := v
	for _, p := range path {
		obj, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	list, _ := cur.([]interface{})
	return list
}

// digString reads the first present string field from a set of candidate names.
func digString(item map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := item[k].(string); ok {
			return v
		}
	}
	return ""
}

// searchQuery builds the request URL for a source.
func (h *Handler) searchQuery(s searcher, query string) string {
	q := url.Values{}
	q.Set("apiKey", h.cfg.EffectiveApiKey())
	if s.param != "" && query != "" {
		q.Set(s.param, query)
	}
	return h.cfg.EffectiveApiBaseURL() + s.endpoint + "?" + q.Encode()
}

// runSearcher performs one lookup and renders the result.
//
// count is the user's chosen result count for the media sources; the feeds ignore
// it and use their own configured count.
func (h *Handler) runSearcher(s searcher, chatID, uid int64, query string, count int, lang string) {
	defer h.recoverPanic()

	if s.kind == kindShortURL {
		h.runShortener(s, chatID, uid, query, lang)
		return
	}

	v, err := h.apiJSON(h.searchQuery(s, query))
	if err != nil {
		log.Printf("%s: request failed: %v", s.id, err)
		h.sendMsg(chatID, localization.Get("dlError", lang), keyboards.Back(lang))
		return
	}
	if failed, isObj := apiFailed(v); failed {
		if isObj {
			log.Printf("%s: success=false: %s", s.id, apiReason(v))
		} else {
			log.Printf("%s: response was %T, not an object", s.id, v)
		}
		h.sendMsg(chatID, localization.Get("dlError", lang), keyboards.Back(lang))
		return
	}

	items := digList(v, s.listPath)
	if len(items) == 0 {
		log.Printf("%s: no results (path %v)", s.id, s.listPath)
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}

	limit := s.count
	if s.kind == kindMedia && count > 0 && count < limit {
		limit = count
	}
	if len(items) < limit {
		limit = len(items)
	}

	switch s.kind {
	case kindMedia:
		if s.id == "sticker_search" {
			h.sendStickerResults(chatID, items[:limit], lang)
			return
		}
		h.sendSearchMedia(s, chatID, items[:limit], lang)
	case kindNews:
		h.sendSearchNews(s, chatID, items[:limit], lang)
	case kindSports:
		h.sendSearchSports(s, chatID, items[:limit], lang)
	}

	if query != "" {
		h.logQuery(uid, s.id, query)
	}
}

// sendSearchMedia fetches and sends each result. A result that cannot be fetched
// is skipped rather than ending the run.
func (h *Handler) sendSearchMedia(s searcher, chatID int64, items []interface{}, lang string) {
	sent := 0
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		u := s.mediaURL(item)
		if u == "" {
			continue
		}

		h.acquireDL()
		body, ct, err := fetchMedia(u)
		h.releaseDL()
		if err != nil {
			log.Printf("%s: media %s: %v", s.id, u, err)
			continue
		}

		title := s.label(item)
		caption := ""
		if title != "" {
			caption = h.p(title)
		}
		it := mediaItem{url: u, title: title}
		if h.sendMediaFile(chatID, s.id, body, ct, it, "", caption, false, false, lang) {
			sent++
			body = nil
		}
	}

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}
	h.sendMsg(chatID, localization.Get("searchSent", lang, sent), keyboards.MainMenu(h.cfg, lang))
}

// sendSearchNews renders a numbered list of linked headlines. Only the fields the
// endpoint actually returns are shown: the BBC feed has no source, the Google one
// does.
func (h *Handler) sendSearchNews(s searcher, chatID int64, items []interface{}, lang string) {
	var b strings.Builder
	b.WriteString(localization.Get("newsResultNamed", lang, s.name, len(items)))
	for i, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		title := s.label(item)
		link := digString(item, "url", "link")
		if title == "" && link == "" {
			continue
		}
		// Headlines come from a third-party feed and routinely contain "_", "*"
		// and "[", any of which breaks the Markdown parse and loses the message.
		b.WriteString("\n" + strconv.Itoa(i+1) + ". ")
		if link != "" {
			b.WriteString("[" + escapeMarkdown(title) + "](" + link + ")")
		} else {
			b.WriteString(escapeMarkdown(title))
		}
		if src := digString(item, "source"); src != "" {
			b.WriteString("\n   " + escapeMarkdown(src))
		}
		if desc := digString(item, "description"); desc != "" {
			if r := []rune(desc); len(r) > 160 {
				desc = string(r[:160]) + "…"
			}
			b.WriteString("\n   " + escapeMarkdown(desc))
		}
		if when := digString(item, "published_at", "published"); when != "" {
			b.WriteString("\n   " + escapeMarkdown(when))
		}
		b.WriteString("\n")
	}
	h.sendMsg(chatID, b.String(), keyboards.MainMenu(h.cfg, lang))
}

// sendSearchSports renders each fixture with its live status and score.
func (h *Handler) sendSearchSports(s searcher, chatID int64, items []interface{}, lang string) {
	var b strings.Builder
	b.WriteString(localization.Get("sportsResultNamed", lang, s.name))
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name := s.label(item)
		status := digString(item, "status")
		details := digString(item, "details")
		if name == "" && status == "" {
			continue
		}
		// h.p() prepends the bot name and a blank line, and sendMsg applies it
		// to the whole message anyway — so calling it per line repeated the
		// prefix once per fixture. Escaped for the same reason as the news
		// headlines: this is a third-party feed, and sendMsg sends as Markdown.
		b.WriteString("\n• " + escapeMarkdown(name))
		if status != "" {
			b.WriteString("\n  " + escapeMarkdown(status))
		}
		if details != "" {
			b.WriteString("\n  " + escapeMarkdown(details))
		}
		b.WriteString("\n")
	}
	h.sendMsg(chatID, b.String(), keyboards.MainMenu(h.cfg, lang))
}

// runShortener resolves a long link to a short one. Every shortener nests its
// answer differently, so the field is looked for in each place it is known to use.
func (h *Handler) runShortener(s searcher, chatID, uid int64, longURL, lang string) {
	q := url.Values{}
	q.Set("apiKey", h.cfg.EffectiveApiKey())
	q.Set("url", longURL)
	apiURL := h.cfg.EffectiveApiBaseURL() + s.endpoint + "?" + q.Encode()

	body, err := h.apiGet(apiURL)
	if err != nil {
		log.Printf("%s: request failed: %v", s.id, err)
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	short := ""
	for _, path := range [][]string{
		{"data", "result", "short_url"},
		{"data", "short_url"},
		{"data", "result"},
		{"short_url"},
	} {
		if v := digStringAt(body, path); v != "" {
			short = v
			break
		}
	}
	if short == "" {
		log.Printf("%s: no short url in the response", s.id)
		h.sendMsg(chatID, localization.Get("reurlError", lang), keyboards.Back(lang))
		return
	}

	h.sendMsg(chatID, localization.Get("reurlSuccess", lang, short), keyboards.MainMenu(h.cfg, lang))
	h.logQuery(uid, s.id, longURL)
}

// digStringAt reads a string at a path in a raw JSON body, following lists where
// one is encountered.
func digStringAt(body []byte, path []string) string {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	cur := v
	for _, p := range path {
		// A list is transparent: step into its first element and keep the same
		// path element, because reurl wraps its answer in a one-element list and
		// the field being read is still on the table.
		for {
			list, isList := cur.([]interface{})
			if !isList {
				break
			}
			if len(list) == 0 {
				return ""
			}
			cur = list[0]
		}
		obj, ok := cur.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = obj[p]
	}
	for {
		list, isList := cur.([]interface{})
		if !isList {
			break
		}
		if len(list) == 0 {
			return ""
		}
		cur = list[0]
	}
	s, _ := cur.(string)
	return s
}

// searchKindName is used in logs and the inline switch.
func searchKindName(k searchKind) string {
	switch k {
	case kindMedia:
		return "media"
	case kindNews:
		return "news"
	case kindSports:
		return "sports"
	case kindShortURL:
		return "shorturl"
	}
	return "unknown"
}

var _ = fmt.Sprintf

// clampCount reads the result count a user picked from a count picker. The value
// arrives as a string, and 0 means "as many as the source returned".
func clampCount(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	if n > searchMaxResults {
		return searchMaxResults
	}
	return n
}

// searchMaxResults bounds one search, so a count picker cannot ask for hundreds.
const searchMaxResults = 30

// stickerFileURL pulls the media URL out of a sticker item. The endpoint nests it
// under size and format, so the item is unwrapped before the picker runs.
func stickerFileURL(item map[string]interface{}) string {
	file, ok := item["file"].(map[string]interface{})
	if !ok {
		return ""
	}
	return pickStickerURL(file)
}

// sendStickerResults uploads each result to the bot's pack and sends it as a
// sticker.
//
// A sticker search used to send plain photos, which is the one thing a sticker
// search must not do: a photo cannot be added to a set and does not animate.
func (h *Handler) sendStickerResults(chatID int64, items []interface{}, lang string) {
	if !h.stickersEnabled() {
		h.sendMsg(chatID, localization.Get("stickerPackOff", lang), keyboards.Back(lang))
		return
	}

	sent := 0
	for _, raw := range items {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		// The endpoint hands back a webp or gif in several sizes. Telegram only
		// accepts a static PNG or a video sticker, so the png branch is the one
		// that can be used.
		rawURL := stickerPNGURL(item)
		if rawURL == "" {
			continue
		}

		h.acquireDL()
		body, _, err := fetchMedia(rawURL)
		h.releaseDL()
		if err != nil {
			log.Printf("sticker: fetch %s: %v", rawURL, err)
			continue
		}

		png, err := stickerPNGFrom(body, ctIsPNG(body))
		if err != nil {
			log.Printf("sticker: convert: %v", err)
			continue
		}

		h.sendSticker(chatID, lang, png)
		sent++
		body, png = nil, nil
	}

	if sent == 0 {
		h.sendMsg(chatID, localization.Get("bingNoResults", lang), keyboards.Back(lang))
		return
	}
	h.sendMsg(chatID, localization.Get("searchSent", lang, sent), keyboards.MainMenu(h.cfg, lang))
}

// stickerPNGURL picks the png variant out of a sticker item.
func stickerPNGURL(item map[string]interface{}) string {
	file, ok := item["file"].(map[string]interface{})
	if !ok {
		return ""
	}
	// Largest first, so the sent sticker is the best one available.
	for _, size := range []string{"hd", "md", "400", "320", "240"} {
		sizes, ok := file[size].(map[string]interface{})
		if !ok {
			continue
		}
		if variant, ok := sizes["png"].(map[string]interface{}); ok {
			if u, _ := variant["url"].(string); strings.HasPrefix(u, "http") {
				return u
			}
		}
	}
	return ""
}

// ctIsPNG sniffs the format so a webp is not treated as something Go can decode.
func ctIsPNG(body []byte) bool {
	return len(body) > 4 && string(body[1:4]) == "PNG"
}

// stickerPNGFrom returns the bytes as a sticker-ready PNG. Anything already PNG is
// only resized when it is the wrong size.
func stickerPNGFrom(body []byte, alreadyPNG bool) ([]byte, error) {
	if alreadyPNG {
		if b, err := decodeImage(body); err == nil {
			if b.Bounds().Dx() == stickerSide && b.Bounds().Dy() == stickerSide {
				return body, nil
			}
		}
	}
	return toStickerPNG(body)
}
