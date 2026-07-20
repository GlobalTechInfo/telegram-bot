# AGENTS.md

## Project Overview

Telegram bot written in Go using `go-telegram-bot-api/v5` and `bbolt` (embedded key-value DB). Downloads media from YouTube, Instagram, TikTok, Facebook, Pinterest, Snapchat, and Twitter via a configurable third-party API. Includes search engines (Pinterest, Stickers, Imgur, YouTube, Bing), text/image effect generators (TextPro, Photooxy, Ephoto360), and multi-language support (13 languages). Config-driven UI.

## Code Conventions

- **No comments** in source code unless the logic is genuinely non-obvious
- **No emoji** in code unless the user explicitly asks for them
- **Short variable names** in local scope (sess, cfg, cb, uid, etc.)
- **Error handling**: log + user-facing message, don't silently swallow
- **Imports**: stdlib first, then project packages, then third-party (blank line between groups)

## Project Structure

```
main.go                    Entry point, env loading, Telegram client init, event loop
config/config.go           Config structs, Load(), IsAdmin(), Prefix(), EffectiveApiBaseURL(), EffectiveApiKey()
handlers/handlers.go       All bot logic: commands, callbacks, messages, download/search/textpro functions
keyboards/keyboards.go     Inline keyboard markup builders
localization/localization.go  i18n string maps, Get(), SupportedLanguages(), LanguageName()
session/session.go         BoltDB store: GetOrCreate, SetState, TrackUser, AddFeedback, etc.
config.json                Bot configuration (commands, UI, features, localization, apiBaseUrl, apiKey)
.env                       BOT_TOKEN, ADMIN_IDS, API_BASE_URL, API_KEY, DB_PATH (gitignored)
Dockerfile                 Multi-stage debian build
docker-compose.yml         Single service with persistent volume
README.md                  Deployment guide, commands, architecture
AGENTS.md                  This file — architecture patterns for AI agents
```

## Architecture Patterns

### Handler Flow

Every update runs in its own goroutine (`main.go:158-174`). Panics are recovered per-goroutine.

1. **Command** (`/start`, `/help`, `/yt`, etc.) → `HandleCommand` — sets session state, sends prompt
2. **Callback** (button press) → `HandleCallback` — switches on `cb.Data`
3. **Message** (text) → `HandleMessage` — switches on `sess.State`
4. **Auto-detect** (idle state) → `HandleMessage` default case — checks URL patterns, starts download flow

### State Machine

States used in `sess.State`:

| State | Trigger | Next |
|-------|---------|------|
| `idle` | default | Any await state via command/auto-detect |
| `awaiting_yt_url` | `/yt` or auto-detect | URL validation → format picker → `yt_fmt:` callback |
| `awaiting_ig_url` | `/ig` or auto-detect | URL validation → download goroutine |
| `awaiting_tt_url` | `/tt` or auto-detect | URL validation → fetch info → format picker → `tt_fmt:` callback |
| `awaiting_fb_url` | `/fb` or auto-detect | URL validation → fetch info → quality picker → `fb_fmt:` callback |
| `awaiting_pin_url` | `/pin` or auto-detect | URL validation → download goroutine |
| `awaiting_sc_url` | `/sc` or auto-detect | URL validation → fetch info → format picker → `sc_fmt:` callback |
| `awaiting_tw_url` | `/tw` or auto-detect | URL validation → fetch info → format picker → `tw_fmt:` callback |
| `awaiting_feedback` | `/feedback` | Save feedback → idle |
| `awaiting_poll_question` | `/poll` | Save question → `awaiting_poll_options` |
| `awaiting_poll_options` | from question | Create poll → idle |
| `awaiting_bing_query` | `/bing` | Save query → count picker or fetchBingSearch |
| `awaiting_pin_search_query` | Search menu → Pinterest | Save query → count picker → fetchPinSearch |
| `awaiting_sticker_search_query` | Search menu → Stickers | Save query → count picker → fetchStickerSearch |
| `awaiting_imgur_search_query` | Search menu → Imgur | Save query → count picker → fetchImgurSearch |
| `awaiting_yt_search_query` | Search menu → YouTube | Save query → fetchYtSearch |
| `awaiting_textpro_text1` | Text Maker → TextPro effect | Save text1 → if 2-text: await text2, else: fetchTextPro |
| `awaiting_textpro_text2` | from text1 (2-text effect) | Save text2 → fetchTextPro |
| `awaiting_photooxy_text1` | Text Maker → Photooxy effect | Save text1 → if 2-text: await text2, else: fetchPhotooxy |
| `awaiting_photooxy_text2` | from text1 (2-text effect) | Save text2 → fetchPhotooxy |
| `awaiting_ephoto_text1` | Text Maker → Ephoto effect | Save text1 → if 2-text: await text2, else: fetchEphoto |
| `awaiting_ephoto_text2` | from text1 (2-text effect) | Save text2 → fetchEphoto |

### Adding a New Downloader

Follow this checklist (example: "twitter"):

1. **`handlers/handlers.go`**:
   - Add `isValidTwitterURL(raw string) bool` — parse URL, check host
   - Add command case in `HandleCommand`: `case "tw", "twitter":`
   - Add callback case in `HandleCallback`: `case data == "tw":`
   - Add state case in `HandleMessage`: `case "awaiting_tw_url":`
   - Add URL check in default case (auto-detect): `else if isValidTwitterURL(text) {`
   - Add download function: `func (h *Handler) downloadTwitter(...)`
   - Callback data convention: `tw_fmt:TYPE` for format selection (if applicable)
2. **`localization/localization.go`**:
   - Add keys to English map: `twPrompt`, `twInvalid`, `twDownloading`, `twUploading`, `twSuccess`, `twError`
   - Non-English maps will fall back to English automatically
3. **`keyboards/keyboards.go`**:
   - Add format picker function if multiple formats exist
4. **`config.json`**:
   - Add `"tw": { "enabled": true, "description": "..." }` to commands
   - Add button to `ui.mainMenu.buttons`
5. **Build and test**

### Adding a New Search Feature

Follow the Pinterest search pattern:

1. **`handlers/handlers.go`**:
   - Add callback: `case data == "search_xxx":` → set state, clear session data, show prompt
   - Add count callback: `case strings.HasPrefix(data, "xxx_search:"):` → read query, launch goroutine
   - Add state: `case "awaiting_xxx_search_query":` → save query, show count picker
   - Add function: `func (h *Handler) fetchXxxSearch(...)` — call API, iterate results, send media
2. **`localization/localization.go`**: Add prompt, count, sending, success, error keys
3. **`keyboards/keyboards.go`**: Add button to `SearchMenu()`, add count picker function
4. **`config.json`**: Add command entry if needed

### Adding a New Text Effect Service (TextPro/Photooxy/Ephoto360 pattern)

1. **`handlers/handlers.go`**:
   - Add callback: `case data == "xxx":` → show effect menu
   - Add effect callback: `case strings.HasPrefix(data, "xxx:"):` → parse effect, set state
   - Add text1/text2 states: `awaiting_xxx_text1`, `awaiting_xxx_text2`
   - Add function: `func (h *Handler) fetchXxx(...)` — call API, download image, send as photo
2. **`keyboards/keyboards.go`**:
   - Add button to `TextMakerMenu()`
   - Add `XxxMenu()` with effect buttons (callback pattern: `xxx:EFFECT:TEXT_COUNT`)
3. **`localization/localization.go`**: Add menu, sending, success, error keys
4. Reuse `textProPrompt` / `textProPrompt2` for text input prompts

### Download Function Template

```go
func (h *Handler) downloadXxx(chatID int64, mediaURL, lang string) {
    h.acquireDL()
    defer h.releaseDL()

    apiURL := fmt.Sprintf("%s/xxx/download?apiKey=%s&url=%s",
        h.cfg.EffectiveApiBaseURL(), h.cfg.EffectiveApiKey(), url.QueryEscape(mediaURL))

    body, _, err := fetchMedia(apiURL)
    // parse response, resolve media URL, download media, send, cleanup
}
```

### Session Data Conventions

- Store transient data in `sess.Data["key"]` (e.g., `yt_url`, `tt_api_data`, `fb_qualities`, `question`, `notifications_on`, `pin_search_query`, `textpro_effect`, `textpro_text1`)
- Use `h.store.GetOrCreate(uid)` to read, `h.store.SetSessionData(uid, sess.Data)` to write
- Always re-fetch session after state changes: `sess = h.store.GetOrCreate(uid)`

## Download Infrastructure

- **`fetchMedia(apiURL)`**: Downloads from URL, returns `([]byte, contentType, error)`. If response is JSON, recursively resolves a media URL from it via `extractURL()`. Capped at 100MB via `io.LimitReader`.
- **`extractURL(v interface{})`**: Recursively searches for URLs in JSON. Checks known keys first (`url`, `download_url`, `video_url`, etc.), then iterates all remaining keys. Searches arrays from end to start.
- **`maxConcurrentDownloads = 2`**: Semaphore via `dlSem` channel. Always acquire before starting a download that fetches media.
- **Memory cleanup**: After sending media, set `body = nil` and call `runtime.GC()`.

## Helper Functions

- **`truncate(s, max)`**: Truncates string to `max` runes with `...` suffix
- **`escapeMarkdown(s)`**: Escapes `_`, `*`, `` ` ``, `[` for Telegram Markdown mode

## Localization

- `Get(key, lang, args...)` — falls back to English if key missing in requested language
- Only English has all keys defined. Non-English maps can be partial.
- Language code must be added to `SupportedLanguages()` and `LanguageName()`.
- Use `fmt.Sprintf` style `%s`, `%d` placeholders; pass args to `Get()`.

## Configuration

- `config.json` is re-read on every startup only (no hot-reload)
- `apiBaseUrl` and `apiKey` in config.json; can be overridden via `API_BASE_URL` / `API_KEY` env vars
- `EffectiveApiBaseURL()` and `EffectiveApiKey()` methods check env first, then config
- `ADMIN_IDS` env var is appended to config's `adminIds` slice
- Commands can be disabled by setting `"enabled": false` in config
- Menu buttons with disabled commands are automatically hidden

## Build & Run

- **Build**: `go build -o telegram-bot .`
- **Run**: `go run main.go`
- **Docker**: `docker compose up --build -d`
- Go version: 1.25+
- Dependencies: `go mod download`

## How `extractURL` Works

The `extractURL` function recursively walks parsed JSON to find the first URL string:

1. If the value is a string starting with `http://` or `https://`, return it
2. If it's a map, first check known URL keys (`url`, `download_url`, `video_url`, `media_url`, `link`, `file`, `downloadLink`, `downloadUrl`), then check ALL other keys recursively
3. If it's an array, iterate from last to first and recurse into each element

## API Endpoints

All use configurable base URL and API key (`EffectiveApiBaseURL()` + `EffectiveApiKey()`):

| Service | Endpoint Pattern | Type |
|---------|-----------------|------|
| YouTube Download | `/loaderto/download?format=FORMAT&url=URL` | Downloader |
| Instagram | `/instagram/download?url=URL` | Downloader |
| TikTok | `/tiktok/download?url=URL` | Downloader |
| Facebook | `/fbdown/download?url=URL` | Downloader |
| Pinterest Download | `/download/pinterest?url=URL` | Downloader |
| Snapchat | `/download/snapchat?url=URL` | Downloader |
| Twitter | `/twitter/download?url=URL` | Downloader |
| Bing Search | `/bing/search?query=QUERY` | Search |
| Bing Images | `/bing/image?query=QUERY` | Search |
| Pinterest Search | `/pinterest/search?query=QUERY` | Search |
| Sticker Search | `/stickers/search?query=QUERY` | Search |
| Imgur Search | `/imgur/search?query=QUERY` | Search |
| YouTube Search | `/yts/searchVideos?query=QUERY` | Search |
| TextPro Effects | `/textpro/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
| Photooxy Effects | `/photooxy/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
| Ephoto360 Effects | `/ephoto/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
