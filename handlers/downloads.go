package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"telegram-bot/keyboards"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// dlMode selects how a downloader turns a link into media.
type dlMode int

const (
	// dlSingle resolves every media URL the endpoint returns and delivers them:
	// one video, an album of images, or a lone photo.
	dlSingle dlMode = iota
	// dlFormats asks the user to pick from a fixed format list first, then calls
	// the endpoint once with the chosen value in the query string.
	dlFormats
	// dlList resolves the endpoint into selectable options and shows a picker
	// when there is more than one.
	dlList
)

// dlLabel is one entry of a fixed format list.
type dlLabel struct {
	value    string // sent back through the callback and used in the query
	key      string // localization key, e.g. "ytFormatMp3"
	fallback string // used when the key is missing, so new sites need no translations
}

// dlOption is a resolved, ready-to-download media URL.
type dlOption struct {
	label    string
	mediaURL string
	items    []mediaItem // more than one means an image carousel
	caption  string      // metadata text, used per mediaCaption
	asAudio  bool        // force audio delivery regardless of Content-Type
}

// dlStoredOption is the persisted form of a resolved option. Sessions round trip
// through JSON and mediaItem keeps unexported fields, so a stored option has to
// be flattened rather than serialised as-is.
type dlStoredOption struct {
	Label    string   `json:"label"`
	MediaURL string   `json:"url"`
	URLs     []string `json:"urls,omitempty"`
	Caption  string   `json:"caption,omitempty"`
	Audio    bool     `json:"audio,omitempty"`
}

func storeOptions(opts []dlOption) []dlStoredOption {
	out := make([]dlStoredOption, 0, len(opts))
	for _, o := range opts {
		st := dlStoredOption{Label: o.label, MediaURL: o.mediaURL, Caption: o.caption, Audio: o.asAudio}
		for _, it := range o.items {
			st.URLs = append(st.URLs, it.url)
		}
		out = append(out, st)
	}
	return out
}

// loadOptions reads options back whether they are still in memory or have been
// through a session round trip.
func loadOptions(raw interface{}) []dlOption {
	switch v := raw.(type) {
	case nil:
		return nil
	case []dlOption:
		return v
	case []dlStoredOption:
		return unflattenOptions(v)
	}
	// Anything else came back out of bbolt as generic JSON values.
	data, err := json.Marshal(raw)
	if err != nil {
		log.Printf("options marshal error: %v", err)
		return nil
	}
	var stored []dlStoredOption
	if err := json.Unmarshal(data, &stored); err != nil {
		log.Printf("options unmarshal error: %v", err)
		return nil
	}
	return unflattenOptions(stored)
}

func unflattenOptions(stored []dlStoredOption) []dlOption {
	out := make([]dlOption, 0, len(stored))
	for _, st := range stored {
		o := dlOption{label: st.Label, mediaURL: st.MediaURL, caption: st.Caption, asAudio: st.Audio}
		for _, u := range st.URLs {
			o.items = append(o.items, mediaItem{url: u})
		}
		out = append(out, o)
	}
	return out
}

// downloader is one entry of the download registry.
type downloader struct {
	id       string   // session key, callback prefix and config.json command key
	name     string   // display name; a proper noun, so never localized
	cmds     []string // command aliases, first one doubles as the menu key
	hosts    []string // host suffixes that identify a link for this site
	endpoint string   // API path appended to EffectiveApiBaseURL()
	mode     dlMode
	key      string            // query parameter carrying the chosen format
	labels   []dlLabel         // dlFormats only
	params   map[string]string // extra static query parameters
	// options overrides how a response becomes selectable media. Needed only
	// when the API nests its list somewhere unusual, like Facebook's
	// allQualities or TikTok's play map.
	options func(d downloader, v interface{}, lang string) []dlOption
	// caption builds the text shown above a picker. Facebook and Twitter both
	// return metadata worth showing; without it their screens lose that block.
	caption func(d downloader, v interface{}, lang string) string
	// mediaCaption also attaches that text to the delivered file. Twitter does,
	// Facebook does not.
	mediaCaption bool
	// imageAsDoc sends images as documents instead of photos, matching how
	// Facebook downloads have always been delivered.
	imageAsDoc bool
	// pick overrides what a picker selection does, for downloaders that keep
	// their own payload in the session instead of a resolved option list.
	pick func(h *Handler, chatID, uid int64, d downloader, sel string, lang string)
	// info replaces the whole flow with a custom screen. TikTok shows a stats
	// card before its picker, which no generic message can express.
	info func(h *Handler, chatID, uid int64, d downloader, mediaURL, lang string)
	// manualOnly keeps a site out of auto-detection. GitHub earns it: a pasted
	// github.com link is usually an issue, file or release, not a repo to zip.
	manualOnly bool
	// fileExt names the payload when neither the Content-Type nor the URL says
	// what it is. A GitHub repository comes back as octet-stream from a URL
	// ending in "zipball", so only the registry knows it is a zip.
	fileExt string
	// variants are extra query parameters that each add another asset to the
	// same post, rather than a different post to choose between. Pinterest needs
	// this: its endpoint returns one file per call and decides which one on its
	// own, so asking once with "type=video" and once without yields the video
	// and the poster of the same pin. dlSingle only, since a variant cannot be
	// merged into a picker.
	variants []string
	// match narrows a host to the URL shapes the endpoint actually accepts.
	// GitHub's only takes owner/repo, so everything else is reported as
	// unsupported instead of being sent to the API to fail there.
	match func(u *url.URL) bool
}

var downloaders = []downloader{
	{
		id: "yt", name: "YouTube", cmds: []string{"yt", "download"},
		hosts:    []string{"youtube.com", "youtu.be", "m.youtube.com"},
		endpoint: "/loaderto/download", mode: dlFormats, key: "format",
		labels: []dlLabel{
			{"mp3", "ytFormatMp3", "MP3"},
			{"360", "ytFormat360", "360p"},
			{"720", "ytFormat720", "720p"},
			{"1080", "ytFormat1080", "1080p"},
		},
	},
	{
		id: "ig", name: "Instagram", cmds: []string{"ig", "instagram"},
		hosts:    []string{"instagram.com", "instagr.am"},
		endpoint: "/instagram/download", mode: dlSingle,
	},
	{
		id: "tt", name: "TikTok", cmds: []string{"tt", "tiktok"},
		hosts:    []string{"tiktok.com"},
		endpoint: "/tiktok/download", mode: dlList,
		labels: []dlLabel{
			{"wm", "ttFormatWM", "🎬 With watermark"},
			{"nowm", "ttFormatNoWM", "🎬 No watermark"},
			{"nowm_hd", "ttFormatHD", "🎬 No watermark HD"},
			{"music", "ttFormatMusic", "🎵 Music only"},
		},
		pick: func(h *Handler, chatID, uid int64, d downloader, sel, lang string) {
			sess := h.store.GetOrCreate(uid)
			ttData, _ := sess.Data["tt_api_data"].(map[string]interface{})
			h.store.ClearSessionData(uid)
			go func() {
				defer h.recoverPanic()
				h.downloadTikTok(chatID, uid, ttData, sel, lang)
			}()
		},
		info: func(h *Handler, chatID, uid int64, d downloader, mediaURL, lang string) {
			h.sendMsg(chatID, dlText(d, "Working", lang), keyboards.Back(lang))
			go func() {
				defer h.recoverPanic()
				msg, ok := h.tiktokInfo(uid, mediaURL)
				if !ok {
					h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
					return
				}
				h.sendMsg(chatID, msg, keyboards.DownloadFormatPicker(d.id, dlLabelTexts(d, lang), dlLabelValues(d), lang))
			}()
		},
	},
	{
		id: "fb", name: "Facebook", cmds: []string{"fb", "facebook"},
		hosts:    []string{"facebook.com", "fb.watch", "fb.com"},
		endpoint: "/fbdown/download", mode: dlList,
		options:    fbOptions,
		caption:    fbCaption,
		imageAsDoc: true,
	},
	{
		id: "pin", name: "Pinterest", cmds: []string{"pin", "pinterest"},
		hosts:    []string{"pinterest.com", "pin.it"},
		endpoint: "/download/pinterest", mode: dlSingle,
		variants: []string{"type=video", "format=mp4"},
	},
	{
		id: "sc", name: "Snapchat", cmds: []string{"sc", "snapchat"},
		hosts:    []string{"snapchat.com", "t.snapchat.com"},
		endpoint: "/download/snapchat", mode: dlList,
	},
	{
		id: "tw", name: "Twitter", cmds: []string{"tw", "twitter"},
		hosts:    []string{"twitter.com", "x.com", "t.co"},
		endpoint: "/twitter/download", mode: dlList,
		caption:      twCaption,
		mediaCaption: true,
	},

	{
		id: "threads", name: "Threads", cmds: []string{"threads", "th"},
		hosts:    []string{"threads.net", "threads.com"},
		endpoint: "/thrdown/download", mode: dlSingle,
	},
	{
		id: "gh", name: "GitHub", cmds: []string{"gh", "github"},
		hosts:    []string{"github.com"},
		endpoint: "/gitclone/download", mode: dlSingle,
		manualOnly: true, match: ghRepoPath, fileExt: ".zip",
	},
	{
		id: "odysee", name: "Odysee", cmds: []string{"odysee", "ody"},
		hosts:    []string{"odysee.com"},
		endpoint: "/download/odysee", mode: dlList,
	},
	{
		id: "vidsplay", name: "Vidsplay", cmds: []string{"vidsplay", "vps"},
		hosts:    []string{"vidsplay.com"},
		endpoint: "/download/vidsplay", mode: dlSingle,
	},
	{
		id: "istock", name: "iStock", cmds: []string{"istock", "ist"},
		hosts:    []string{"istockphoto.com"},
		endpoint: "/download/istock", mode: dlSingle,
	},
	{
		id: "alamy", name: "Alamy", cmds: []string{"alamy"},
		hosts:    []string{"alamy.com"},
		endpoint: "/download/alamy", mode: dlSingle,
	},
	{
		id: "capcut", name: "CapCut", cmds: []string{"capcut", "cap"},
		hosts:    []string{"capcut.com"},
		endpoint: "/capdown/download", mode: dlSingle,
	},
	{
		id: "imdb", name: "IMDb", cmds: []string{"imdb"},
		hosts:    []string{"imdb.com"},
		endpoint: "/download/imdb", mode: dlSingle,
	},
}

func dlByID(id string) (downloader, bool) {
	for _, d := range downloaders {
		if d.id == id {
			return d, true
		}
	}
	return downloader{}, false
}

func dlByCmd(cmd string) (downloader, bool) {
	for _, d := range downloaders {
		for _, c := range d.cmds {
			if c == cmd {
				return d, true
			}
		}
	}
	return downloader{}, false
}

// dlHit is the result of matching a link against the registry.
type dlHit struct {
	d       downloader
	ok      bool // a downloader claims this link
	offPath bool // the site's host matched but this URL shape is unsupported
}

// dlByHost matches a pasted link against the registry. Suffix matching keeps
// www and regional subdomains working while refusing lookalike hosts such as
// notyoutube.com.
func dlByHost(raw string) dlHit {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return dlHit{}
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range downloaders {
		for _, h := range d.hosts {
			if host != h && !strings.HasSuffix(host, "."+h) {
				continue
			}
			if d.match != nil && !d.match(u) {
				return dlHit{d: d, offPath: true}
			}
			return dlHit{d: d, ok: true}
		}
	}
	return dlHit{}
}

// dlHasHost reports whether the link belongs to any registered downloader, which
// decides whether auto-detection should offer it instead of the shortener.
func dlHasHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, d := range downloaders {
		for _, h := range d.hosts {
			if host == h || strings.HasSuffix(host, "."+h) {
				return true
			}
		}
	}
	return false
}

// dlNameKeys are the suffixes whose shared wording names the site.
var dlNameKeys = map[string]bool{"Prompt": true, "Invalid": true, "Working": true, "Unsupported": true}

// dlSuffix maps an engine concept onto the key existing downloaders already use,
// so no per-site translation is lost when a site has no key for the generic
// concept.
var dlSuffix = map[string]string{
	"Working":      "Downloading",
	"ChooseFormat": "Format",
}

// dlText picks a per-site string when one exists and falls back to the shared
// downloader wording otherwise, so every existing translation keeps winning and
// a new site needs no localization work at all.
func dlText(d downloader, suffix, lang string, args ...interface{}) string {
	if site, ok := dlSuffix[suffix]; ok {
		key := d.id + strings.ToUpper(site[:1]) + site[1:]
		if localization.Has(key, lang) {
			return localization.Get(key, lang, args...)
		}
	}
	site := d.id + strings.ToUpper(suffix[:1]) + suffix[1:]
	if localization.Has(site, lang) {
		return localization.Get(site, lang, args...)
	}
	if suffix == "Menu" {
		// A menu button is labelled with the site, not with a generic word.
		return d.name
	}
	if dlNameKeys[suffix] {
		args = append([]interface{}{d.name}, args...)
	}
	return localization.Get("dl"+suffix, lang, args...)
}

func (l dlLabel) text(lang string) string {
	if localization.Has(l.key, lang) {
		return localization.Get(l.key, lang)
	}
	return l.fallback
}

func dlState(id string) string { return "awaiting_" + id + "_url" }

// dlIsPick reports whether callback data selects a downloader format or item.
func dlIsPick(data string) bool {
	i := strings.Index(data, "_fmt:")
	if i < 0 {
		return false
	}
	_, ok := dlByID(data[:i])
	return ok
}

// dlSplitPick separates a <id>_fmt:<value> callback into its downloader and the
// chosen format value.
func dlSplitPick(data string) (downloader, string) {
	i := strings.Index(data, "_fmt:")
	d, _ := dlByID(data[:i])
	return d, data[i+len("_fmt:"):]
}

// dlPendingState reports whether the session is waiting for a downloader link.
func dlPendingState(state string) (downloader, bool) {
	if !strings.HasPrefix(state, "awaiting_") || !strings.HasSuffix(state, "_url") {
		return downloader{}, false
	}
	return dlByID(strings.TrimSuffix(strings.TrimPrefix(state, "awaiting_"), "_url"))
}

// dlAutoHit resolves a pasted link for auto-detection only, keeping manualOnly
// sites out of it.
//
// dlByHost deliberately matches every site, because it also validates the link a
// user pasted in answer to /gh. Filtering manualOnly inside it therefore broke
// that path as well, so the exclusion lives here, at the one caller that means
// "start a download without being asked".
func dlAutoHit(raw string) dlHit {
	hit := dlByHost(raw)
	if hit.d.manualOnly {
		return dlHit{}
	}
	return hit
}

// dlAutoHost reports whether a pasted link belongs to an auto-detected site.
func dlAutoHost(raw string) bool {
	if !dlHasHost(raw) {
		return false
	}
	return dlAutoHit(raw).d.id != ""
}

// dlHostOK checks that a pasted link really belongs to the site whose prompt the
// user answered, so pasting a TikTok link at /yt reports an invalid link instead
// of downloading the wrong thing.
func dlHostOK(d downloader, raw string) bool {
	hit := dlByHost(raw)
	return hit.ok && hit.d.id == d.id
}

// handleDownloadURL is the shared entry point for every "user pasted a link"
// path: from the command prompt and from auto-detection alike.
func (h *Handler) handleDownloadURL(d downloader, chatID, uid int64, mediaURL, lang string) {
	sess := h.store.GetOrCreate(chatID)
	sess.Data[d.id+"_url"] = mediaURL
	h.store.SetSessionData(chatID, sess.Data)
	h.store.SetState(chatID, "idle")

	if d.info != nil {
		d.info(h, chatID, uid, d, mediaURL, lang)
		return
	}

	switch d.mode {
	case dlFormats:
		h.sendMsg(chatID, dlText(d, "ChooseFormat", lang),
			keyboards.DownloadFormatPicker(d.id, dlLabelTexts(d, lang), dlLabelValues(d), lang))
	default:
		h.sendMsg(chatID, dlText(d, "Working", lang), keyboards.Back(lang))
		go func() {
			// This runs outside the per-update goroutine's recover, so a panic
			// here would take the process down rather than one update.
			defer h.recoverPanic()
			h.resolveDownload(d, chatID, uid, mediaURL, "", lang)
		}()
	}
}

func dlLabelTexts(d downloader, lang string) []string {
	out := make([]string, 0, len(d.labels))
	for _, l := range d.labels {
		out = append(out, l.text(lang))
	}
	return out
}

func dlLabelValues(d downloader) []string {
	out := make([]string, 0, len(d.labels))
	for _, l := range d.labels {
		out = append(out, l.value)
	}
	return out
}

// dlQuery builds the endpoint URL for a download.
func (h *Handler) dlQuery(d downloader, mediaURL, sel string) string {
	return h.dlQueryWith(d, mediaURL, sel, nil)
}

// dlQueryWith builds one request URL, optionally adding extra static parameters.
// A variant is expressed as a bare "key=value" string because that is the only
// thing the registry has room to describe.
func (h *Handler) dlQueryWith(d downloader, mediaURL, sel string, extra map[string]string) string {
	q := url.Values{}
	q.Set("url", mediaURL)
	q.Set("apiKey", h.cfg.EffectiveApiKey())
	if sel != "" && d.key != "" {
		q.Set(d.key, sel)
	}
	for k, v := range d.params {
		q.Set(k, v)
	}
	for k, v := range extra {
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(d.endpoint, "?") {
		sep = "&"
	}
	return h.cfg.EffectiveApiBaseURL() + d.endpoint + sep + q.Encode()
}

// parseVariant turns "type=video" into a parameter map.
func parseVariant(v string) map[string]string {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return nil
	}
	return map[string]string{strings.TrimSpace(k): strings.TrimSpace(val)}
}

// apiJSON performs the API call and returns the parsed body.
func (h *Handler) apiJSON(apiURL string) (interface{}, error) {
	resp, err := mediaClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := readBody(resp, maxAPISize)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}

	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return v, nil
}

// apiReason pulls whatever explanation an endpoint offered out of a response.
// It reads the top level and the data wrapper, because endpoints disagree on
// where they put it, and falls back to the cached flag, which is the difference
// between "not supported" and "we tried this before and it was empty".
func apiReason(v interface{}) string {
	var parts []string
	var walk func(interface{}, int)
	walk = func(node interface{}, depth int) {
		if depth > 2 {
			return
		}
		obj, ok := node.(map[string]interface{})
		if !ok {
			if arr, ok := node.([]interface{}); ok && depth < 2 {
				for _, el := range arr {
					walk(el, depth+1)
				}
			}
			return
		}
		for _, k := range []string{"error", "message", "msg", "reason", "detail"} {
			if str, ok := obj[k].(string); ok {
				if t := strings.TrimSpace(str); t != "" && !contains(parts, t) {
					parts = append(parts, t)
				}
			}
		}
		if c, ok := obj["cached"].(bool); ok && c {
			parts = append(parts, "cached result")
		}
		for _, k := range []string{"data", "result", "error"} {
			if child, ok := obj[k]; ok {
				walk(child, depth+1)
			}
		}
	}
	walk(v, 0)
	if len(parts) == 0 {
		return "no reason given"
	}
	return strings.Join(parts, "; ")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// resolveDownload calls the endpoint and either delivers the media or shows a
// picker. sel is empty on the first pass and the picker's value afterwards.
// apiFailed reads the success flag without panicking on a response that is not a
// JSON object. apiJSON returns whatever the endpoint sent, so null, [] and a bare
// string all arrive with no error, and a single-value type assertion on any of
// them panics. That panic was in a goroutine with no recover, so a malformed
// response from a third party could take the whole process down.
//
// The second return says whether the body was an object at all, which separates
// "the endpoint refused" from "the endpoint sent something else".
func apiFailed(v interface{}) (failed, isObject bool) {
	obj, isObject := v.(map[string]interface{})
	if !isObject {
		return true, false
	}
	flag, ok := obj["success"].(bool)
	if !ok {
		return false, true
	}
	return !flag, true
}

// handleDownloadURL drives a download from a pasted or commanded URL.
// resolveVariants makes one request per registered variant and delivers
// everything they return as one post.
//
// The point is that no single call is trustworthy: the endpoint returns one file
// per call and is not consistent about which, serving a poster even when the pin
// is a video. Several calls are therefore sampled and the distinct assets kept,
// one video and one still at most. A variant that fails is logged and dropped
// rather than failing the post, because a pin with a video but no poster is still
// worth delivering.
func (h *Handler) resolveVariants(d downloader, chatID int64, mediaURL, sel, lang string) {
	calls := []map[string]string{nil}
	for _, v := range d.variants {
		calls = append(calls, parseVariant(v))
	}

	var found []mediaItem
	seen := map[string]bool{}
	caption := ""
	audio := dlAudioFormat(sel)

	for _, extra := range calls {
		v, err := h.apiJSON(h.dlQueryWith(d, mediaURL, sel, extra))
		if err != nil {
			log.Printf("%s variant %v failed: %v", d.id, extra, err)
			continue
		}
		if failed, isObject := apiFailed(v); failed {
			if isObject {
				log.Printf("%s variant %v returned success=false: %s", d.id, extra, apiReason(v))
			} else {
				log.Printf("%s variant %v returned %T instead of an object", d.id, extra, v)
			}
			continue
		}
		if caption == "" {
			caption = dlCaptionOf(d, v, lang)
		}
		for _, o := range h.dlOptions(d, v, lang) {
			for _, it := range o.items {
				if it.url == "" || seen[it.url] {
					continue
				}
				seen[it.url] = true
				found = append(found, it)
			}
			if o.asAudio {
				audio = true
			}
		}
	}

	items := firstOfEachKind(found)
	if len(items) == 0 {
		log.Printf("%s: no variant returned media for %s", d.id, mediaURL)
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}
	log.Printf("%s: %d asset(s) from %d call(s) of %d", d.id, len(items), len(calls), len(found))

	h.deliver(chatID, d, dlOption{items: items, caption: caption, asAudio: audio}, lang)
}

// firstOfEachKind keeps the first video and the first non-video, video first, and
// drops the rest. Sampled calls mostly repeat the same poster, and delivering it
// as an album of two identical images is worse than sending it once.
func firstOfEachKind(items []mediaItem) []mediaItem {
	var video, other *mediaItem
	for i := range items {
		if isVideoItem(items[i]) {
			if video == nil {
				video = &items[i]
			}
			continue
		}
		if other == nil {
			other = &items[i]
		}
	}
	out := make([]mediaItem, 0, 2)
	if video != nil {
		out = append(out, *video)
	}
	if other != nil {
		out = append(out, *other)
	}
	return out
}

func (h *Handler) resolveDownload(d downloader, chatID, uid int64, mediaURL, sel, lang string) {
	log.Printf("%s resolve: sel=%q url=%s", d.id, sel, mediaURL)

	if len(d.variants) > 0 {
		h.resolveVariants(d, chatID, mediaURL, sel, lang)
		return
	}

	v, err := h.apiJSON(h.dlQuery(d, mediaURL, sel))
	if err != nil {
		log.Printf("%s API error: %v", d.id, err)
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}
	if failed, isObject := apiFailed(v); failed {
		if isObject {
			log.Printf("%s API returned success=false: %s", d.id, apiReason(v))
		} else {
			log.Printf("%s API returned %T instead of a JSON object", d.id, v)
		}
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}

	caption := dlCaptionOf(d, v, lang)
	audio := dlAudioFormat(sel)

	opts := h.dlOptions(d, v, lang)
	if len(opts) == 0 {
		// An endpoint that answers success with an empty list is not reporting a
		// failure, so its own explanation is the only clue left. Without it the log
		// reads the same for "unsupported link", "snap expired" and "upstream broke".
		log.Printf("%s API returned no downloadable media: %s", d.id, apiReason(v))
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}
	for i := range opts {
		opts[i].caption = caption
		opts[i].asAudio = audio
	}

	if len(opts) == 1 {
		h.deliver(chatID, d, opts[0], lang)
		return
	}

	sess := h.store.GetOrCreate(chatID)
	sess.Data[d.id+"_options"] = storeOptions(opts)
	h.store.SetSessionData(chatID, sess.Data)

	labels := make([]string, 0, len(opts))
	for _, o := range opts {
		labels = append(labels, o.label)
	}
	prompt := dlText(d, "ChooseFormat", lang)
	if caption != "" {
		prompt = caption
	}
	h.sendMsg(chatID, prompt, keyboards.DownloadPicker(d.id, labels, lang))
}

// dlCaptionOf returns a downloader's metadata block, if it has one.
func dlCaptionOf(d downloader, v interface{}, lang string) string {
	if d.caption == nil {
		return ""
	}
	return d.caption(d, v, lang)
}

// dlAudioFormat forces audio delivery for format values that name one, since
// some CDNs answer mp3 with application/octet-stream.
func dlAudioFormat(sel string) bool {
	s := strings.ToLower(sel)
	return s == "mp3" || s == "music" || s == "audio"
}

// dlOptions turns a response into selectable media.
//
// A dlSingle endpoint describes one post, so every media URL it returns is part
// of that post: an image carousel, or one video when the response labels a video.
// A dlList endpoint describes alternatives the user chooses between.
func (h *Handler) dlOptions(d downloader, v interface{}, lang string) []dlOption {
	if d.options != nil {
		return d.options(d, v, lang)
	}
	if d.mode == dlList {
		return listOptions(d, v)
	}
	return postOptions(v)
}

// postOptions treats the whole response as a single post.
func postOptions(v interface{}) []dlOption {
	items := primaryMedia(collectURLs(v))
	if len(items) == 0 {
		return nil
	}
	return []dlOption{{mediaURL: items[0].url, items: items}}
}

// primaryMedia narrows what one entry delivers. APIs routinely pair a video with
// its preview image; sending both would either produce a photo of a thumbnail or
// an album holding one video, so the video wins. A genuine multi-image post has
// no video and keeps every image.
func primaryMedia(items []mediaItem) []mediaItem {
	if len(items) < 2 {
		return items
	}

	hasVideo := false
	videoInGroup := make(map[int]bool)
	for _, it := range items {
		if isVideoItem(it) {
			hasVideo = true
			videoInGroup[it.group] = true
		}
	}
	if !hasVideo {
		return items
	}

	// Two shapes need dropping a still, and neither applies to a plain photo:
	//   - one entry listing {video, image}, where the image is the video's poster;
	//   - two entries, one labelled "Download Thumbnail" and one the video, which
	//     is how the Instagram endpoint returns a reel.
	// A carousel is a list of separate entries and every one is deliverable:
	// Instagram routinely mixes 25 photos with one video, and collapsing those to
	// the video is exactly the bug this function exists to prevent.
	out := make([]mediaItem, 0, len(items))
	for _, it := range items {
		if isPreviewItem(it) {
			continue
		}
		if videoInGroup[it.group] && !isVideoItem(it) {
			continue
		}
		out = append(out, it)
	}

	// Never let the filtering empty a post.
	if len(out) == 0 {
		return items
	}
	return out
}

// isPreviewItem reports whether an entry is the still that belongs to a video
// rather than a post in its own right.
func isPreviewItem(it mediaItem) bool {
	title := strings.ToLower(it.title)
	return strings.Contains(title, "thumbnail") || strings.Contains(title, "cover") ||
		strings.Contains(title, "preview")
}

// listOptions reads a list-shaped response where each entry is an alternative
// the user can pick, such as Facebook's quality list.
func listOptions(d downloader, v interface{}) []dlOption {
	var out []dlOption
	seen := make(map[string]bool)

	for i, entry := range listItems(v) {
		items := primaryMedia(collectURLs(entry))
		if len(items) == 0 {
			continue
		}
		key := ""
		for _, it := range items {
			key += it.url + "\x00"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, dlOption{label: itemLabel(d, entry, i), mediaURL: items[0].url, items: items})
	}
	return out
}

// listItems finds the list of entries an API offers. Container keys are checked
// before scalar ones so a wrapper's own field cannot mask the list.
func listItems(v interface{}) []interface{} {
	switch val := v.(type) {
	case []interface{}:
		return val
	case map[string]interface{}:
		for _, key := range []string{"data", "result", "results", "items", "medias", "media", "posts", "images", "videos", "qualities"} {
			if list, ok := val[key].([]interface{}); ok && len(list) > 0 {
				return list
			}
		}
		for _, key := range []string{"data", "result", "items", "medias", "media"} {
			if obj, ok := val[key].(map[string]interface{}); ok {
				if list := listItems(obj); len(list) > 0 {
					return list
				}
			}
		}
	}
	return nil
}

// itemLabel prefers the API's own wording for an option and falls back to the
// site's name plus an index.
func itemLabel(d downloader, item interface{}, i int) string {
	if obj, ok := item.(map[string]interface{}); ok {
		for _, key := range []string{"quality", "title", "label", "name", "type", "resolution", "format"} {
			if s, ok := obj[key].(string); ok && s != "" {
				return s
			}
		}
	}
	return fmt.Sprintf("%s %d", d.name, i+1)
}

// deliver sends one resolved option, choosing between a video, an album of
// images and a single file from what it holds.
func (h *Handler) deliver(chatID int64, d downloader, o dlOption, lang string) {
	items := o.items
	if len(items) == 0 {
		items = []mediaItem{{url: o.mediaURL}}
	}

	caption := ""
	if d.mediaCaption {
		caption = o.caption
	}

	h.sendMsg(chatID, dlText(d, "Uploading", lang), keyboards.Back(lang))

	if len(items) > 1 {
		// A post holding a video and a still is not a gallery: sending it as one
		// album either drops the video or loses the poster, so each file is sent
		// on its own with the video first.
		if mixedTypes(items) {
			h.deliverMixed(chatID, d, items, caption, lang)
			return
		}
		h.sendCarousel(chatID, d, items, lang)
		return
	}

	h.acquireDL()
	body, ct, err := fetchMedia(items[0].url)
	h.releaseDL()
	if err != nil {
		log.Printf("%s download failed: %v", d.id, err)
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}

	if h.sendMediaFile(chatID, d.id, body, ct, items[0], d.fileExt, caption, o.asAudio, d.imageAsDoc, lang) {
		h.sendResult(chatID, d, 1, lang)
	}
}

// mixedTypes reports whether a set of items holds both a video and something
// that is not, which is the one combination an album cannot carry.
func mixedTypes(items []mediaItem) bool {
	var video, other bool
	for _, it := range items {
		if isVideoItem(it) {
			video = true
		} else {
			other = true
		}
	}
	return video && other
}

// deliverMixed sends each file separately, video first, and reports one success
// at the end however many arrived. A file that cannot be fetched is skipped
// rather than aborting the rest.
func (h *Handler) deliverMixed(chatID int64, d downloader, items []mediaItem, caption, lang string) {
	ordered := make([]mediaItem, 0, len(items))
	for _, it := range items {
		if isVideoItem(it) {
			ordered = append([]mediaItem{it}, ordered...)
		} else {
			ordered = append(ordered, it)
		}
	}

	sent := 0
	for _, it := range ordered {
		h.acquireDL()
		body, ct, err := fetchMedia(it.url)
		h.releaseDL()
		if err != nil {
			log.Printf("%s asset download failed: %v", d.id, err)
			continue
		}
		if h.sendMediaFile(chatID, d.id, body, ct, it, d.fileExt, caption, false, d.imageAsDoc, lang) {
			sent++
		}
	}

	if sent == 0 {
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}
	h.sendResult(chatID, d, sent, lang)
}

// sendResult reports what was delivered, using the count-aware string when a
// site has one.
func (h *Handler) sendResult(chatID int64, d downloader, sent int, lang string) {
	if localization.Has(d.id+"Sent", lang) {
		h.sendMsg(chatID, dlText(d, "Sent", lang, sent), keyboards.MainMenu(h.cfg, lang))
		return
	}
	h.sendMsg(chatID, dlText(d, "Success", lang), keyboards.MainMenu(h.cfg, lang))
}

// sendMediaFile sends one downloaded body as audio, video, photo or document,
// whichever fits, and falls back to a document when Telegram rejects the first
// choice.
func (h *Handler) sendMediaFile(chatID int64, prefix string, body []byte, ct string, it mediaItem, fileExt, caption string, forceAudio, imageAsDoc bool, lang string) bool {
	if body == nil {
		return false
	}
	name := fmt.Sprintf("%s_%s%s", prefix, time.Now().Format("150405"), mediaExt(ct, it.url, fileExt))
	file := func() tgbotapi.FileBytes { return tgbotapi.FileBytes{Name: name, Bytes: body} }

	switch {
	case forceAudio || looksLikeAudio(ct):
		audio := tgbotapi.NewAudio(chatID, file())
		if _, err := h.bot.Send(audio); err == nil {
			return true
		}
	case looksLikeVideo(ct, it, body):
		video := tgbotapi.NewVideo(chatID, file())
		video.SupportsStreaming = true
		video.Caption = caption
		if _, err := h.bot.Send(video); err == nil {
			return true
		}
	case (strings.Contains(ct, "image") || sniffImage(body)) && !imageAsDoc:
		photo := tgbotapi.NewPhoto(chatID, file())
		photo.Caption = caption
		if _, err := h.bot.Send(photo); err == nil {
			return true
		}
	}

	doc := tgbotapi.NewDocument(chatID, file())
	doc.Caption = caption
	if _, err := h.bot.Send(doc); err != nil {
		log.Printf("%s send error: %v", prefix, err)
		h.sendMsg(chatID, localization.Get("error", lang), keyboards.Back(lang))
		return false
	}
	return true
}

// looksLikeVideo decides whether a body should go out as a playable video.
//
// Content-Type alone is not enough. Several CDNs answer a media request with
// application/octet-stream, which matches no branch and used to send a Pinterest
// video as a document the user had to download and open by hand. The file
// extension is the second opinion and the API's own label the third; the
// container magic is checked first because it is the only one of the four that
// cannot be wrong.
func looksLikeVideo(ct string, it mediaItem, body []byte) bool {
	if sniffVideo(body) {
		return true
	}
	lower := strings.ToLower(ct)
	if strings.Contains(lower, "video") || strings.Contains(lower, "mp4") {
		return true
	}
	if isVideoExt(urlExt(it.url)) {
		return true
	}
	return isVideoItem(it)
}

// sniffVideo identifies a video container by its magic bytes, for the case where
// the server says nothing useful and the URL carries no extension. Only real
// video signatures are listed; images cannot match them. Each signature is
// length-checked on its own, so a short but valid MP4 header is still recognised.
func sniffVideo(b []byte) bool {
	// ISO base media (MP4/MOV/M4V): four bytes of size, then "ftyp".
	if len(b) >= 8 && string(b[4:8]) == "ftyp" {
		return true
	}
	// Matroska/WebM EBML header.
	if len(b) >= 4 && b[0] == 0x1A && b[1] == 0x45 && b[2] == 0xDF && b[3] == 0xA3 {
		return true
	}
	// AVI.
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "AVI "
}

// looksLikeAudio keeps audio/video apart. The old check tested for "mpeg", which
// also matches the legacy video/mpeg and would have sent a video as audio.
func looksLikeAudio(ct string) bool {
	lower := strings.ToLower(ct)
	if strings.Contains(lower, "video") {
		return false
	}
	return strings.Contains(lower, "audio") || strings.Contains(lower, "mpeg")
}

func isVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".webm", ".m4v", ".mkv":
		return true
	}
	return false
}

// sniffImage is the mirror of sniffVideo, for the same reason: a CDN that answers
// application/octet-stream would otherwise send a photo as a document. imageAsDoc
// still wins where a site asks for documents, so Facebook is unaffected.
func sniffImage(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch {
	case b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return true // JPEG
	case string(b[:4]) == "\x89PNG":
		return true
	case string(b[:4]) == "GIF8":
		return true
	}
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

// sendCarousel delivers a multi-image post as albums, one album in memory at a
// time so a 50 item post cannot spike a small container.
func (h *Handler) sendCarousel(chatID int64, d downloader, items []mediaItem, lang string) {
	if len(items) > igMaxItems {
		log.Printf("%s: capping %d items at %d", d.id, len(items), igMaxItems)
		items = items[:igMaxItems]
	}
	total := len(items)
	sent := 0

	for start := 0; start < total; start += igAlbumSize {
		end := start + igAlbumSize
		if end > total {
			end = total
		}

		album := make([][]byte, 0, end-start)
		held := 0
		for _, it := range items[start:end] {
			if len(album) > 0 && held > igAlbumMaxBytes {
				break
			}
			h.acquireDL()
			body, ct, err := fetchMedia(it.url)
			h.releaseDL()
			if err != nil {
				log.Printf("%s image fetch error: %v", d.id, err)
				continue
			}
			if strings.Contains(ct, "video") {
				continue
			}
			held += len(body)
			album = append(album, body)
		}

		if len(album) == 0 {
			h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
			break
		}

		caption := dlAlbumCaption(d, lang, start+1, end, total)
		sent += h.sendAlbum(chatID, d.id, album, len(album), caption, lang)
		album = nil

		if end < total {
			time.Sleep(igAlbumPause)
		}
	}

	if sent == 0 {
		return
	}
	h.sendResult(chatID, d, sent, lang)
}

func dlAlbumCaption(d downloader, lang string, from, to, total int) string {
	if localization.Has(d.id+"AlbumCaption", lang) {
		return dlText(d, "AlbumCaption", lang, from, to, total)
	}
	return fmt.Sprintf("%s %d-%d/%d", d.name, from, to, total)
}

// handleDownloadPick resolves a picker selection.
func (h *Handler) handleDownloadPick(d downloader, chatID, uid int64, sel string, lang string) {
	h.store.SetState(chatID, "idle")

	if d.pick != nil {
		d.pick(h, chatID, uid, d, sel, lang)
		return
	}

	sess := h.store.GetOrCreate(chatID)
	if d.mode == dlFormats {
		mediaURL, _ := sess.Data[d.id+"_url"].(string)
		if mediaURL == "" {
			h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
			return
		}
		go func() {
			defer h.recoverPanic()
			h.resolveDownload(d, chatID, uid, mediaURL, sel, lang)
		}()
		return
	}

	opts := loadOptions(sess.Data[d.id+"_options"])
	idx := 0
	fmt.Sscanf(sel, "%d", &idx)
	if idx < 0 || idx >= len(opts) {
		log.Printf("%s invalid pick %q (have %d)", d.id, sel, len(opts))
		h.sendMsg(chatID, dlText(d, "Error", lang), keyboards.Back(lang))
		return
	}
	h.store.ClearSessionData(chatID)
	h.deliver(chatID, d, opts[idx], lang)
}

// fbOptions reads Facebook's allQualities list, falling back to the single
// download field when the API offers no quality list.
func fbOptions(d downloader, v interface{}, lang string) []dlOption {
	data, _ := v.(map[string]interface{})["data"].(map[string]interface{})
	if data == nil {
		return nil
	}

	var out []dlOption
	if qualities, ok := data["allQualities"].([]interface{}); ok {
		for i, q := range qualities {
			items := collectURLs(q)
			if len(items) == 0 {
				continue
			}
			out = append(out, dlOption{label: itemLabel(d, q, i), mediaURL: items[0].url, items: items})
		}
		if len(out) > 0 {
			return out
		}
	}

	if direct, _ := data["download"].(string); direct != "" {
		log.Printf("FB no quality list, using direct URL: %s", direct)
		return []dlOption{{label: "", mediaURL: direct}}
	}
	return nil
}

// fbCaption is the Facebook info block shown above the quality picker.
func fbCaption(d downloader, v interface{}, lang string) string {
	data, _ := v.(map[string]interface{})["data"].(map[string]interface{})
	if data == nil {
		return ""
	}

	title, _ := data["title"].(string)
	duration, _ := data["duration"].(string)
	quality, _ := data["quality"].(string)

	msg := "📘 *Facebook Video*\n\n"
	if title != "" {
		msg += fmt.Sprintf("*Title:* %s\n", title)
	}
	if duration != "" {
		msg += fmt.Sprintf("*Duration:* %s\n", duration)
	}
	if quality != "" {
		msg += fmt.Sprintf("*Quality:* %s\n", quality)
	}
	return msg + "\n*Choose quality:*"
}

// twCaption is the Twitter post text, shown above the media picker and
// attached to whatever is downloaded.
func twCaption(d downloader, v interface{}, lang string) string {
	data, _ := v.(map[string]interface{})["data"].(map[string]interface{})
	if data == nil {
		return ""
	}
	return buildTwitterCaption(data)
}

func mustDL(id string) downloader {
	d, _ := dlByID(id)
	return d
}

// linkRe matches a bare domain or a full http(s) URL, so a link pasted without
// a scheme, or wrapped in a sentence, still routes to a downloader.
var linkRe = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}(?:/[^\s]*)?`)

// dlExtractURL pulls the link out of a message. A scheme-less token only counts
// when it is the entire message, so prose like "check out example.com" is not
// mistaken for something to download.
func dlExtractURL(text string) string {
	m := linkRe.FindString(text)
	if m == "" {
		return ""
	}
	m = strings.TrimRight(m, ".,;:!?)]}'\"")
	if !strings.HasPrefix(strings.ToLower(m), "http") {
		if strings.TrimSpace(text) != m {
			return ""
		}
		// gitclone and friends reject a scheme-less URL outright.
		m = "https://" + m
	}
	return m
}

// ghReserved are GitHub's own routes, which occupy the first path segment of a
// non-repository link such as /issues/12 or /pull/34.
var ghReserved = map[string]bool{
	"blob": true, "tree": true, "raw": true, "releases": true, "tags": true,
	"issues": true, "pull": true, "pulls": true, "wiki": true, "actions": true,
	"settings": true, "stargazers": true, "forks": true, "network": true,
	"commit": true, "commits": true, "notifications": true, "explore": true,
	"sponsors": true, "discussions": true, "codespaces": true, "marketplace": true,
}

// ghRepoPath accepts only github.com/owner/repo, with or without a .git suffix.
// Verified against the endpoint: releases/latest, archive/refs/tags/x, tree and
// blob links are all rejected there, so they are refused here with a useful
// message instead of a generic failure.
func ghRepoPath(u *url.URL) bool {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 {
		return false
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	if owner == "" || repo == "" {
		return false
	}
	return !ghReserved[strings.ToLower(owner)]
}
