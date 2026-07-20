# AGENTS.md

## Project Overview

Telegram bot written in Go using `go-telegram-bot-api/v5` and `bbolt` (embedded key-value DB). Downloads media from YouTube, Instagram, TikTok, Facebook, and Pinterest via a third-party API. 13-language i18n. Config-driven UI.

## Code Conventions

- **No comments** in source code unless the logic is genuinely non-obvious
- **No emoji** in code unless the user explicitly asks for them
- **Short variable names** in local scope (sess, cfg, cb, uid, etc.)
- **Error handling**: log + user-facing message, don't silently swallow
- **Imports**: stdlib first, then project packages, then third-party (blank line between groups)

## Project Structure

```
main.go                    Entry point, env loading, Telegram client init, event loop
config/config.go           Config structs, Load(), IsAdmin(), Prefix()
handlers/handlers.go       All bot logic: commands, callbacks, messages, download functions
keyboards/keyboards.go     Inline keyboard markup builders
localization/localization.go  i18n string maps, Get(), SupportedLanguages(), LanguageName()
session/session.go         BoltDB store: GetOrCreate, SetState, TrackUser, AddFeedback, etc.
config.json                Bot configuration (commands, UI, features, localization)
.env                       BOT_TOKEN, ADMIN_IDS, DB_PATH (gitignored)
Dockerfile                 Multi-stage debian build
docker-compose.yml         Single service with persistent volume
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
| `awaiting_feedback` | `/feedback` | Save feedback → idle |
| `awaiting_poll_question` | `/poll` | Save question → `awaiting_poll_options` |
| `awaiting_poll_options` | from question | Create poll → idle |

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
   - Add keys to English map: `twPrompt`, `twInvalid`, `twDownloading`, `twUploading`, `twSuccess`, `twError` (add format keys if needed)
   - Non-English maps will fall back to English automatically
3. **`keyboards/keyboards.go`**:
   - Add format picker function if multiple formats exist
4. **`config.json`**:
   - Add `"tw": { "enabled": true, "description": "..." }` to commands
   - Add button to `ui.mainMenu.buttons`
5. **Build and test**

### Download Function Template

```go
func (h *Handler) downloadXxx(chatID int64, mediaURL, lang string) {
    h.acquireDL()
    defer h.releaseDL()

    // 1. Call external API
    apiURL := fmt.Sprintf("https://api.qasimdev.dpdns.org/api/xxx/download?apiKey=qasim-dev&url=%s",
        url.QueryEscape(mediaURL))

    // 2. Download + parse response
    // 3. Resolve media URL
    // 4. Download media via fetchMedia()
    // 5. Send to Telegram (video/audio/photo/document)
    // 6. Cleanup: body = nil; runtime.GC()
}
```

### Session Data Conventions

- Store transient data in `sess.Data["key"]` (e.g., `yt_url`, `tt_api_data`, `fb_qualities`, `question`, `notifications_on`)
- Use `h.store.GetOrCreate(uid)` to read, `h.store.SetSessionData(uid, sess.Data)` to write
- Always re-fetch session after state changes: `sess = h.store.GetOrCreate(uid)`

## Download Infrastructure

- **`fetchMedia(apiURL)`**: Downloads from URL, returns `([]byte, contentType, error)`. If response is JSON, recursively resolves a media URL from it via `extractURL()`. Capped at 100MB via `io.LimitReader`.
- **`extractURL(v interface{})`**: Recursively searches for URLs in JSON. Checks known keys first (`url`, `download_url`, `video_url`, etc.), then iterates all remaining keys. Searches arrays from end to start.
- **`maxConcurrentDownloads = 2`**: Semaphore via `dlSem` channel. Always acquire before starting a download.
- **Memory cleanup**: After sending media, set `body = nil` and call `runtime.GC()`.

## Localization

- `Get(key, lang, args...)` — falls back to English if key missing in requested language
- Only English has all keys defined. Non-English maps can be partial.
- Language code must be added to `SupportedLanguages()` in the map in `LanguageName()`.
- Use `fmt.Sprintf` style `%s`, `%d` placeholders in strings; pass args to `Get()`.

## Configuration

- `config.json` is re-read on every startup only (no hot-reload)
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

This means `extractURL` will find URLs nested at any depth in any JSON structure.

## API Endpoints

All use `apiKey=qasim-dev`:

| Platform | Endpoint |
|----------|----------|
| YouTube  | `/api/loaderto/download?format=FORMAT&url=URL` |
| Instagram | `/api/instagram/download?url=URL` |
| TikTok   | `/api/tiktok/download?url=URL` |
| Facebook | `/api/fbdown/download?url=URL` |
| Pinterest | `/api/download/pinterest?url=URL` |

Base: `https://api.qasimdev.dpdns.org`
