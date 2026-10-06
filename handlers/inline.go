package handlers

import (
	"fmt"
	"log"
	"strings"

	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Inline mode.
//
// The previous handler answered every query with two static articles that carried
// no InputMessageContent, which Telegram rejects outright, and the query text was
// never read. The other half of the problem was upstream: AllowedUpdates did not
// include inline_query, so a query never arrived in the first place. Both are
// handled now — EnableInline below sets the bot's inline mode, and the search runs
// through the same registry the chat commands use.

// inlineResultsPerQuery bounds one answer.
const inlineResultsPerQuery = 25

// inlinePlaceholderHint is what /setinline asks for. Telegram requires a
// non-empty placeholder before a bot will accept inline queries.
const inlinePlaceholderHint = "type something..."

// EnableInline reports whether the bot can answer inline queries at all.
//
// It cannot turn the feature on. Inline mode is configured with /setinline in
// @BotFather and there is no Bot API method for it — the spec has no
// setInlineMode at all, so the old MakeRequest call here could only ever fail.
// Sending it was worse than useless: the README told operators they could skip
// BotFather, so nobody enabled it and no query ever arrived.
//
// What this does instead is read the flag getMe already reports and say plainly
// what to do when it is off.
func (h *Handler) EnableInline() {
	if !h.cfg.Tools.Inline.Enabled {
		return
	}
	me, err := h.bot.GetMe()
	if err != nil {
		log.Printf("⚠️ Could not read bot identity to check inline mode: %v", err)
		return
	}
	if !me.SupportsInlineQueries {
		log.Printf("⚠️ Inline mode is OFF. Run /setinline in @BotFather "+
			"(placeholder: %s) — inline queries will not arrive until you do.",
			inlinePlaceholderHint)
		return
	}
	log.Printf("🔗 Inline mode enabled")
}

// HandleInline answers an inline query with real, selectable results.
func (h *Handler) HandleInline(update tgbotapi.Update) {
	if update.InlineQuery == nil {
		return
	}

	q := strings.TrimSpace(update.InlineQuery.Query)
	if q == "" {
		h.answerInline(update.InlineQuery.ID, inlineHelpResults(), "")
		return
	}

	// A leading token picks the source, so "@bot yt <word>" searches YouTube and
	// "@bot news <word>" reads a feed.
	rest, sources := splitInlineSource(q)
	if len(sources) == 0 {
		sources = defaultInlineSources()
	}

	results, err := h.inlineResults(rest, sources)
	if err != nil {
		log.Printf("inline: %v", err)
	}
	if len(results) == 0 {
		h.answerInline(update.InlineQuery.ID, inlineNoResults(), q)
		return
	}
	h.answerInline(update.InlineQuery.ID, results, q)
}

// splitInlineSource reads a leading "yt" or "news" style token off the query.
func splitInlineSource(q string) (string, []searcher) {
	fields := strings.Fields(q)
	if len(fields) < 2 {
		return q, nil
	}
	head := strings.ToLower(fields[0])
	if s, ok := searcherByAlias(head); ok {
		return strings.Join(fields[1:], " "), []searcher{s}
	}
	return q, nil
}

// searcherByAlias maps the short words a user would type to a source.
func searcherByAlias(name string) (searcher, bool) {
	aliases := map[string]string{
		"yt": "yt_search", "youtube": "yt_search", "video": "yt_search",
		"img": "bing_images", "images": "bing_images", "bing": "bing_images",
		"pin": "pin_search", "pinterest": "pin_search",
		"sticker": "sticker_search", "sticker_search": "sticker_search",
		"imgur": "imgur_search",
		"news":  "news_google", "sport": "sports_cricket", "sports": "sports_cricket",
	}
	id, ok := aliases[name]
	if !ok {
		return searcher{}, false
	}
	return searcherByID(id)
}

// defaultInlineSources is what a bare query searches: images first, because that
// is what an inline search is usually for.
func defaultInlineSources() []searcher {
	out := make([]searcher, 0, 2)
	for _, id := range []string{"bing_images", "yt_search"} {
		if s, ok := searcherByID(id); ok {
			out = append(out, s)
		}
	}
	return out
}

// inlineResults queries each source in turn and stops once there are enough.
//
// Media sources answer with the media URL directly, which is what an inline
// result needs: picking it drops the picture into the chat without the bot having
// to download and re-upload it.
func (h *Handler) inlineResults(query string, sources []searcher) ([]interface{}, error) {
	results := make([]interface{}, 0, inlineResultsPerQuery)
	var firstErr error

	for _, s := range sources {
		if len(results) >= inlineResultsPerQuery {
			break
		}
		q := query
		if s.noQuery {
			q = ""
		}

		v, err := h.apiJSON(h.searchQuery(s, q))
		if err != nil {
			log.Printf("inline %s: %v", s.id, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		items := digList(v, s.listPath)
		for _, raw := range items {
			if len(results) >= inlineResultsPerQuery {
				break
			}
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if r := s.inlineResult(item, len(results)); r != nil {
				results = append(results, r)
			}
		}
	}
	return results, firstErr
}

// inlineResult builds one selectable result, or nil when the item has nothing to
// show.
func (s searcher) inlineResult(item map[string]interface{}, seq int) interface{} {
	mediaURL := s.mediaURL(item)
	title := s.label(item)
	if mediaURL == "" && title == "" {
		return nil
	}

	headline := inlineResultTitle(title, s.name)
	desc := inlineResultDescription(item, s)

	// A media result sends the media itself, which is the whole point of inline
	// mode: no download, no re-upload. InputMessageContent is deliberately left
	// unset — Telegram only uses it to *replace* the media with a text message, and
	// omitting it is what makes the photo or video post as-is.
	switch ext := urlExt(mediaURL); {
	case mediaURL != "" && strings.EqualFold(ext, ".gif"):
		return inlineGIF{
			Type:         "gif",
			ID:           fmt.Sprintf("%s-%d", s.id, seq),
			URL:          mediaURL,
			ThumbnailURL: mediaURL,
			Title:        headline,
			Description:  desc,
		}
	case mediaURL != "" && isVideoExt(ext):
		return inlineVideo{
			Type:         "video",
			ID:           fmt.Sprintf("%s-%d", s.id, seq),
			URL:          mediaURL,
			MimeType:     "video/mp4",
			ThumbnailURL: s.thumbnail(item, mediaURL),
			Title:        headline,
			Description:  desc,
		}
	case mediaURL != "" && looksLikeImageURL(mediaURL):
		return inlinePhoto{
			Type:         "photo",
			ID:           fmt.Sprintf("%s-%d", s.id, seq),
			URL:          mediaURL,
			ThumbnailURL: mediaURL,
			Title:        headline,
			Description:  desc,
		}
	}

	// Otherwise it is a link, which is still selectable and still useful.
	return tgbotapi.InlineQueryResultArticle{
		Type:        "article",
		ID:          fmt.Sprintf("%s-%d", s.id, seq),
		Title:       headline,
		Description: desc,
		InputMessageContent: tgbotapi.InputTextMessageContent{
			Text:                  localResultText(title, s.name, mediaURL),
			ParseMode:             "HTML",
			DisableWebPagePreview: true,
		},
	}
}

// inlineResultTitle makes sure there is always something to label a result with.
func inlineResultTitle(title, source string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return source
	}
	// Counted in runes, not bytes. len(t) is bytes, so a 40-character Urdu,
	// Hindi or Japanese title can exceed 90 bytes while holding fewer than 90
	// runes — and slicing runes to 90 then panicked on every inline query.
	if r := []rune(t); len(r) > inlineTitleMaxRunes {
		t = string(r[:inlineTitleMaxRunes]) + "…"
	}
	return t
}

// inlineTitleMaxRunes is the result title budget.
const inlineTitleMaxRunes = 90

func inlineResultDescription(item map[string]interface{}, s searcher) string {
	parts := []string{s.name}
	for _, k := range []string{"description", "author", "views", "duration", "published"} {
		if v := digString(item, k); v != "" {
			if r := []rune(v); len(r) > 120 {
				v = string(r[:120]) + "…"
			}
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func localResultText(title, source, link string) string {
	var b strings.Builder
	b.WriteString("<b>" + escapeHTML(title) + "</b>")
	if source != "" {
		b.WriteString("\n<i>" + escapeHTML(source) + "</i>")
	}
	if link != "" {
		b.WriteString("\n" + escapeHTML(link))
	}
	return b.String()
}

func looksLikeImageURL(u string) bool {
	lower := strings.ToLower(u)
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".bmp", ".gif"} {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	// No extension to go on: assume an image, since that is what a search result
	// most often is. A mislabelled one fails at Telegram rather than being lost.
	return !isVideoExt(urlExt(lower))
}

// inlineHelpResults is what an empty query answers with.
func inlineHelpResults() []interface{} {
	rows := []struct{ id, text, desc string }{
		{"help", localization.Get("inlineHelpTitle", "en"), localization.Get("inlineHelpBody", "en")},
		{"start", localization.Get("inlineStartTitle", "en"), localization.Get("inlineStartBody", "en")},
	}
	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, tgbotapi.InlineQueryResultArticle{
			Type:        "article",
			ID:          r.id,
			Title:       r.text,
			Description: r.desc,
			InputMessageContent: tgbotapi.InputTextMessageContent{
				Text:      r.desc,
				ParseMode: "HTML",
			},
		})
	}
	return out
}

func inlineNoResults() []interface{} {
	msg := localization.Get("inlineNoResults", "en")
	return []interface{}{tgbotapi.InlineQueryResultArticle{
		Type:        "article",
		ID:          "none",
		Title:       localization.Get("inlineNoResultsTitle", "en"),
		Description: msg,
		InputMessageContent: tgbotapi.InputTextMessageContent{
			Text:      msg,
			ParseMode: "HTML",
		},
	}}
}

// answerInline sends the results, with a cache window so a repeated query in the
// same chat does not re-hit the API.
func (h *Handler) answerInline(queryID string, results []interface{}, query string) {
	if len(results) > inlineResultsPerQuery {
		results = results[:inlineResultsPerQuery]
	}
	conf := tgbotapi.InlineConfig{
		InlineQueryID: queryID,
		Results:       results,
		CacheTime:     30,
		IsPersonal:    true,
	}
	if _, err := h.bot.Request(conf); err != nil {
		log.Printf("inline answer: %v", err)
	}
}

// escapeHTML is the minimal escape needed for the inline result text, which is
// sent with HTML parse mode.
func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// The three media result shapes are declared here rather than taken from
// tgbotapi, for two reasons the library cannot express.
//
// First, v5.5.1 serialises the thumbnail as "thumb_url". The Bot API renamed
// that field to "thumbnail_url" and dropped the old spelling entirely — it does
// not appear once in the current spec — so the library's tag makes Telegram
// reject the whole answer with BAD_REQUEST: can't parse inline query result.
//
// Second, thumbnail_url is *required* on photo, gif and video results, and a
// search endpoint that returns only a media URL gives us nothing else to put
// there. The media URL is its own thumbnail: same origin, so it always resolves,
// and it keeps the cost at zero extra HTTP requests.
type inlinePhoto struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	URL          string `json:"photo_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
}

type inlineVideo struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	URL          string `json:"video_url"`
	MimeType     string `json:"mime_type"`
	ThumbnailURL string `json:"thumbnail_url"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
}

type inlineGIF struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	URL          string `json:"gif_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
}

// thumbnail finds a still image for a video result. YouTube returns one
// alongside the URL; when there is none the video URL stands in, which Telegram
// accepts as a thumbnail source.
func (s searcher) thumbnail(item map[string]interface{}, fallback string) string {
	for _, k := range []string{"thumbnail", "thumb", "thumbUrl", "thumbnail_url"} {
		if v := digString(item, k); strings.HasPrefix(v, "http") {
			return v
		}
	}
	return fallback
}
