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
handlers/admin.go          Group/channel admin actions, bulk delete, shared delete helpers
keyboards/keyboards.go     Inline keyboard markup builders
localization/localization.go  i18n string maps, Get(), SupportedLanguages(), LanguageName()
session/session.go         BoltDB store: GetOrCreate, SetState, TrackUser, AddFeedback, etc.
config.json                Bot configuration (commands, UI, features, localization, apiBaseUrl, apiKey)
.env                       BOT_TOKEN, ADMIN_IDS, API_BASE_URL, API_KEY, DB_PATH, MEM_LIMIT_MB (gitignored)
Dockerfile                 Multi-stage debian build
docker-compose.yml         Single service with persistent volume
README.md                  Deployment guide, commands, architecture
AGENTS.md                  This file — architecture patterns for AI agents
```

## Architecture Patterns

### Handler Flow

Every update runs in its own goroutine, capped by the `maxUpdateWorkers` semaphore (`main.go`). Panics are recovered per-goroutine.

1. **Command** (`/start`, `/help`, `/yt`, etc.) → `HandleCommand` — sets session state, sends prompt
2. **Callback** (button press) → `HandleCallback` — switches on `cb.Data`
3. **Message** (text) → `HandleMessage` — switches on `sess.State`
4. **Auto-detect** (idle state) → `HandleMessage` default case — checks URL patterns, starts download flow
5. **Channel post** → `HandleChannelPost`
6. **Inline query** → `HandleInline`

`main.go` must subscribe to every one of these. `AllowedUpdates` was never set,
and Telegram's default list excludes `channel_post` and `inline_query` — the bot
could not receive channel posts or inline queries at all. The list is explicit
now.

**Inline mode cannot be enabled from the Bot API.** There is no `setInlineMode`
method; it is `/setinline` in @BotFather, which asks for a placeholder string.
`EnableInline` only reads `supports_inline_queries` from getMe and warns with
that instruction. It used to send a `setInlineMode` request that could only ever
fail, while the README told operators BotFather was unnecessary.

A link button may never carry a URL in `callback_data`: that field is capped at
64 bytes, a reddit permalink or pronunciation URL exceeds it, and Telegram
rejects the whole `sendMessage` with `BUTTON_DATA_INVALID`. Use
`NewInlineKeyboardButtonURL`. Same rule in the opposite direction: Telegram only
accepts `A-Za-z0-9_-` in a start parameter, so `deepLinkURL` base64url-encodes
the token and refuses anything over 64 characters.

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
| `awaiting_reurl_url` | Short URL → Reurl | Save URL → fetchReurl |
| `awaiting_image_effect` | Image Effect → blur/brightness/etc | Photo upload → POST to `/sharp/EFFECT` |
| `awaiting_artistic_effect` | Artistic Effect → pencilSketch/etc | Photo upload → POST to `/sharp/EFFECT` |
| `awaiting_news_query` | News → Google News | Save query → fetchGoogleNews |
| `awaiting_define_word` | `/define` | Save word → fetchDefine |
| `awaiting_translate_text` | `/translate` | Save text → target picker or fetchTranslate |
| `awaiting_translate_lang` | reply-to-`/translate` | Text already known → picker then fetchTranslate |
| `awaiting_media` | `/media` | Photo upload → show ops |
| `awaiting_media_trim` | Trim button | Read `12` or `12-40` → run the trim |
| `awaiting_reddit_sub` | `/reddit` | Save subreddit → fetchReddit |
| `awaiting_channel_post` | channel post | N/A — auto-detect, never a state |

`awaiting_translate_text` and `awaiting_translate_lang` share one case on
purpose: the translate body used to sit under a case labelled
`awaiting_define_word`, so `awaiting_translate_text` had no case at all and typed
text fell through to the default branch (which handed it to Kraken).

### Adding a New Downloader

Adding a site is a registry entry. Do **not** add handler cases, state cases or
auto-detect branches: `HandleCommand`, `HandleMessage`, `HandleCallback` and
`DownloadMenu` all read `downloaders` in `handlers/downloads.go`.

1. Probe the endpoint (`curl "<base><endpoint>?apiKey=...&url=..."`) and decide the mode:
   - `dlSingle` — the endpoint describes one post: one file, a video plus its preview, or an image carousel. Everything it returns is delivered (video, album of 10, or a single photo). Works with `data.download_url`, `data[]`, `data.result[]` and nested lists with no extra code.
   - `dlList` — the endpoint returns alternatives to choose from; the user gets a picker, and one option means it just downloads.
   - `dlFormats` — a fixed format list is shown first and the choice is passed as `key=<value>`.
2. Append an entry to `downloaders`:
   ```go
   {
       id: "sc", name: "SoundCloud", cmds: []string{"sc", "soundcloud"},
       hosts:    []string{"soundcloud.com"},
       endpoint: "/download/soundcloud", mode: dlSingle,
   },
   ```
   - `cmds[0]` must equal `id`, so it matches the `config.json` command key
   - `hosts` are bare lowercase suffixes; matching is exact-or-subdomain, so `soundcloud.com` accepts `www.` and `m.` but not `notsoundcloud.com`
   - aliases must not collide with a built-in command (there is a test for this)
3. `config.json`: add `"sc": { "enabled": true, "description": "..." }` under `commands`
4. **No localization work.** `dlText` prefers `<id><Suffix>` (e.g. `scPrompt`, `scError`) and falls back to the shared `dlPrompt`/`dlError`/… strings, which exist in all 13 languages. Only add per-site keys if you want site-specific wording.
5. Add a test. `TestRegistryInvariants`, `TestRegistryCommandsDoNotShadowBuiltins` and
   `TestEveryDownloaderHasPromptInEveryLanguage` cover a new entry automatically; add a
   per-site flow test when the response shape is unusual.

Hooks, only when the default path cannot express the API:

| Hook | Use it when |
|------|-------------|
| `options` | the media list is somewhere unusual (Facebook's `allQualities`, TikTok's `play` map) |
| `caption` | the API returns metadata worth showing above the picker |
| `mediaCaption` | that metadata should also be attached to the delivered file (Twitter) |
| `imageAsDoc` | images must go out as documents, not photos (Facebook) |
| `pick` | the payload is kept in the session rather than resolved up front (TikTok) |
| `info` | the site shows a custom screen before any picker (TikTok's stats card) |

### Adding a New Search Feature

Follow the Pinterest search pattern:

1. **`handlers/handlers.go`**:
   - Add callback: `case data == "search_xxx":` → set state, clear session data, show prompt
   - Add count callback: `case strings.HasPrefix(data, "xxx_search:"):` → read query, launch goroutine
   - Add state: `case "awaiting_xxx_search_query":` → save query, show count picker
   - Add function: `func (h *Handler) fetchXxxSearch(...)` — call API, iterate results, send media
2. **`localization/localization.go`**: Add prompt, count, sending, success, error keys to **all 13 language maps**
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
3. **`localization/localization.go`**: Add menu, sending, success, error keys to **all 13 language maps**
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

## Download Registry

`handlers/downloads.go` holds one `downloader` struct per site. Everything about a
site is data, so wiring a new one never means touching a handler:

| Field | Purpose |
|-------|---------|
| `id` | session key, callback prefix (`<id>_fmt:`) and `config.json` command key |
| `name` | display name; a proper noun, so it is never localized |
| `cmds` | command aliases; `cmds[0]` must equal `id` |
| `hosts` | host suffixes; matching is exact or subdomain |
| `endpoint` | API path appended to `EffectiveApiBaseURL()` |
| `mode` | `dlSingle` (one post), `dlList` (pick one) or `dlFormats` (fixed list) |
| `key`, `labels` | `dlFormats` query parameter and its buttons, each label carrying a key plus a plain fallback so a new site needs no translations |
| `manualOnly` | keep the site out of auto-detection (GitHub: a pasted repo link is usually not a request to zip a repo) |
| `match` | restrict a host to the URL shapes the endpoint accepts; anything else is reported with `dlUnsupported` instead of failing inside the API (GitHub takes only `owner/repo`) |
| `fileExt` | payload extension for when neither Content-Type nor the URL says what the file is (GitHub's zipball) |

Flow: `handleDownloadURL` → `resolveDownload` → `apiJSON` → `dlOptions` → either
`deliver` (one file) or `sendCarousel` (album of 10) → `sendResult`.
`dlText(d, suffix, lang)` picks `<id><Suffix>` when it exists and falls back to the
shared `dl<Suffix>` string, so existing translations always win.

Rules the engine enforces, all of which were bugs once:

- **`dlQuery` must send `apiKey`.** It reads `EffectiveApiKey()`; the endpoint
  rejects every call without it. `TestEveryDownloaderSendsApiKey` guards this,
  because the stub API used to ignore the key and let the omission through.
- **`primaryMedia`** narrows one entry to what is actually delivered: a video wins
  over its preview image, while a real image carousel keeps every image. Without
  it, a `{video, image}` entry (Snapchat, Vidsplay) became a two-item album that
  skipped the video and shipped the thumbnail.
- **`dlExtractURL`** pulls the link out of the message, so a URL inside a sentence
  or typed without a scheme still auto-detects, and trailing text never reaches the
  API. A scheme-less token only counts when it is the whole message.
- **Endpoint paths come from the registry only.** `TestNoHardcodedApiInGoSource`
  fails if an API host or key is written into any non-test Go file.

Registered sites: `yt`, `ig`, `tt`, `fb`, `pin`, `sc`, `tw`, `threads`, `gh`,
`vidsplay`, `odysee`, `istock`, `alamy`, `capcut`, `imdb` (15).

Response shapes, as consumed by the engine:

| Site | Endpoint | Payload |
|------|----------|---------|
| `threads` | `/thrdown/download` | `data.title` caption, `data.video` media |
| `odysee` | `/download/odysee` | `data.result[]` of `{video, image}` |
| `gh` | `/gitclone/download` | `data.url` zipball |
| `vidsplay` | `/download/vidsplay` | `data.result[]` of `{video, image}` |
| `istock` | `/download/istock` | `data.result`, a single object |
| `alamy` | `/download/alamy` | `data.result`, a list |
| `capcut` | `/capdown/download` | `template-detail` links only |
| `imdb` | `/download/imdb` | `data.result[]` of `{image, video_hd, video_sd}` |

## Searcher Registry

`handlers/search.go` is the same idea applied to search: 21 entries covering
media searches, news, sports and shorteners. `runSearcher` renders by `kind`
(`kindMedia`, `kindNews`, `kindSports`, `kindShortURL`), so a new source is one
registry line and no new function. It replaced 22 near-identical functions and took
`handlers.go` from 4241 to 2789 lines.

Fields: `id`, `name`, `endpoint`, `param`, `listPath`, `urlKeys`, `titleKeys`,
`kind`, `count`, `noQuery` (feed takes no query string), `resolve` (custom media
extractor, e.g. stickers), `noResults`.

## Download Infrastructure

- **`mediaClient`**: One shared `*http.Client` for every outbound request (dial 10s, TLS 10s, response headers 20s, total 180s, bounded idle conns). Never use bare `http.Get` — the stdlib default has no timeout, so one stalled CDN pins a goroutine and a `dlSem` slot forever.
- **`fetchMedia(apiURL)`**: Downloads from URL, returns `([]byte, contentType, error)`. If the response is JSON it resolves a media URL via `extractURL()` and follows it, bounded by `fetchMediaDepth`'s redirect cap of 4.
- **`readBody(resp, limit)`**: Size-capped reader. Rejects on `Content-Length > limit` up front and pre-allocates from the header, instead of `io.ReadAll`'s repeated doubling. Use `maxDownloadSize` (100MB) for media and `maxAPISize` (8MB) for JSON API responses.
- **`extractURL(v interface{})`**: Returns the *first* URL found, searching arrays from end to start. Only correct for single-file endpoints — for anything that can list several files use `collectURLs`.
- **`collectURLs(v) []mediaItem`**: Returns *every* URL in document order, deduplicated, with the sibling `title` label attached. Nested list containers (`data`, `items`, `medias`, …) are walked before a wrapper's own scalar fields so a tracker `url` can't shadow the real media list.
- **`isVideoItem(mediaItem)`**: True for an entry the API labels video/reel or whose URL ends in `.mp4`/`.mov`/`.webm`/`.m4v`. Thumbnail/cover labels always win, so a reel's preview image is never sent as the video.
- **`maxConcurrentDownloads = 2`**: Semaphore via `dlSem`. Acquire it around one transfer at a time, never around a whole loop of transfers, or a multi-item post blocks every other download for minutes.
- **Memory cleanup**: Set `body = nil` after sending. Do **not** call `runtime.GC()` — GC pacing is handled by `tuneMemory` in `main.go`.
- **Options in the session**: picker options are stored as `[]dlStoredOption`, never `[]dlOption`, because `mediaItem` keeps unexported fields and would silently serialise to nothing. Read them back with `loadOptions`.

## AI Agent (Kraken)

`handlers/ai.go` answers anything that is not a command, a forward, a link or a
pending prompt. Config lives under `ai` in config.json: `enabled`, `name`,
`owner`, `apiBaseUrl`, `apiKey`, `maxInputChars`, `cooldownSeconds`.
`EffectiveAiBaseURL()` and `EffectiveAiKey()` prefer `AI_BASE_URL` / `AI_KEY`, so
a deployment can keep the key in `.env` instead. The operator chose to commit the
key in config.json, so it is **not** blank — do not "fix" that back to empty.

The endpoint is `GET <base>/?apikey=KEY&lang=LANG&text=...` and the answer lives
at `data.choices[0].message.content`, not at the top level. It takes a single
`text` parameter, so the persona and the "behave like a person" instructions live
in the Worker's system prompt. The language travels **twice on purpose**: as
`lang`, which the Worker turns into `Write only in <language>`, and as a line
inside `text` (`aiPrompt`), so a Worker that ignored `lang` would still get it.

**`aiPrompt` is the only place the language line is applied.** It used to also be
applied by `aiPromptWithHistory`, which doubled it on the first message of a chat
and left the history framing with none. `TestLanguageInstructionAppearsOnce`
guards that.

Rules the cleanup enforces, each of which the model actually does:

- **`cleanAIReply`** cuts the `acknowledged,` repetition loop the model falls
  into on open-ended prompts, then cuts `finish_reason: "length"` output back to
  the last complete sentence.
- **`stripTrailingGloss`** removes the "(and in English, ...)" tail the model
  appends to non-Latin replies however firmly the prompt forbids it. A Latin-script
  parenthetical in a Latin-script reply is left alone.
- **`sendPlain`** sends with no ParseMode. The model emits Markdown freely, which
  would make the bot's Markdown parser reject the whole message.
- **A typing chat action** stands in for a "thinking..." notice, and replies carry
  no bot prefix and no keyboard so the illusion holds.
- **The gate**: private chats always, groups only when the message mentions the
  bot or replies to it; `aiEnabled` defaults on in private and off in groups; a
  3s per-chat cooldown lives in memory (`aiCooldown`), never in bbolt.
- Everything Kraken sees goes to a third-party endpoint, so the gate deliberately
  sits last in the fallback chain.

Kraken **remembers the chat**: `aiPromptWithHistory` replays earlier turns so
"what did I just ask?" works. The newest turn stays last, and the whole frame is
dropped rather than truncated when it would exceed `aiTotalRunes`.

## Markdown Escaping of Third-Party Text

`sendMsg` sets `ParseMode: "Markdown"`, and `h.p()` only prepends the bot
prefix — it does **not** escape. Any value that came from an external API has to
go through `escapeMarkdown` before it reaches a message, or the send is lost.

This is not hypothetical: Google writes a space as `_` in Urdu, so `/translate`
produced `آپ_کیسے_ہیں`, an unpaired underscore, and Telegram rejected the whole
message. `translate`, news headlines, `define`, `weather` and sports results
were all affected. `TestTranslateEscapesMarkdown` guards it.

`h.p()` must **not** be called inside a string that will be handed to `sendMsg`
or `sendPlain`, which prefix the whole message themselves. The sports renderer
did, so every fixture line carried its own copy of the prefix.

## Stickers: There Is No Bot Pack

`tools.sticker` has no pack name or title, on purpose. `createNewStickerSet` and
`addStickerToSet` both take `user_id` for the **owner** of the set, and Telegram
answers `USER_IS_BOT` when that is a bot: a sticker set is owned by a person, and
a bot may only edit a set a person already created. There is no way for a bot to
own a pack.

So `sendSticker` posts the sticker to the chat with `sendSticker`, which is what
a user wants from a sticker search anyway — they long-press it and add it to
their own collection. Do not reintroduce pack creation.

`toStickerPNG` produces 512x512 PNG because Telegram insists on exactly one side
being 512 and neither over. `resizeImage` refuses to enlarge, which is right for a
download and wrong here, so stickers go through `scaleTo`.

## Video Editing Needs ffmpeg

`resolveFFmpeg` tries the configured path, then `ffmpeg` on `PATH`, then the two
usual absolute locations, and returns `""` when there is none. `MediaOpsMenu` and
`mediaOpKind` both consult `ffmpegAvailable()`, so the video buttons are **hidden**
rather than shown and then failing — including trim, which is ffmpeg-backed like
the rest. The `/media` menu button itself is gated on `tools.media.enabled`, not on
ffmpeg, because the six photo edits are pure Go.

The Dockerfile installs Debian's ffmpeg. Its `libopus` support is what the voice
note needs; a hand-rolled static build usually lacks it and fails at runtime, not
at build time.

`debug.SetMemoryLimit` does **not** cover ffmpeg, since it is a subprocess. The GIF
conversion is the one operation that holds every frame at once, so it is capped at
`gifMaxSeconds`/`gifFps` and every ffmpeg call passes `-threads 2`.

## Installers

`install.sh` and `install.ps1` must stay in step with the feature set, because a
native install is the other way to run this besides Docker. Two things bit:

- **ffmpeg is installed optionally, not required.** Video buttons need it, the
  photo buttons do not, so a failure is a warning rather than a fatal error.
- **`.env.example` must not carry an uncommented placeholder key.** An env value
  overrides `config.json`, so a commented-out-in-spirit `AI_KEY=your-ai-key` wins
  over the real key and every agent request then 401s, leaving Kraken silent.
  Placeholders stay commented.

The Linux installer offers a systemd unit, and when it is enabled it must not
also `exec ./telegram-bot`: two processes on one long-poll means Telegram drops
updates at random.

## Memory Tuning

`main.go:tuneMemory` runs before anything else allocates:

- **`debug.SetMemoryLimit`** from `MEM_LIMIT_MB` (default 400). A soft limit makes the GC work harder as the heap approaches it instead of letting the OOM killer step in.
- **`debug.SetGCPercent`** from `GC_PERCENT` (default 50). The default of 100 lets a 200MB live heap become 400MB.
- **`maxUpdateWorkers = 32`** semaphore around the per-update goroutine, with a 3s wait before dropping an update. Without it a burst of updates spawns unbounded goroutines that can each hold a media buffer.
- **`store.Cleanup()`** also runs every 6h, not just at startup, so sessions/users/feedbacks stay bounded and the DB mmap stays small.
- **`GetOrCreate`** reads through `db.View` and only writes when the session is missing. It used to open a write transaction (fsync) per read, several times per update.

## Bulk Delete (`/del`, `tools_del`)

Both entry points land in `cmdDel` → state `awaiting_del`.

- **`deleteMsgs(chatID, ids)`**: dedupes, caps at 100, then sends batches of 20 with `delBatchPause` between them. Telegram throttles deletes to ~30/s in groups, so one request per message fails almost immediately.
- **`deleteBatch(chatID, ids)`**: Calls `deleteMessages` through `bot.MakeRequest("deleteMessages", params)` with `message_ids` as a JSON array. tgbotapi v5.5.1 has no config type for it and `Chattable` has unexported methods, so it cannot be wrapped. Retries up to 3 times, honouring `Error.ResponseParameters.RetryAfter`.
- **`retryAfter(err)`**: Extracts the flood-wait hint from a `*tgbotapi.Error`.
- Telegram skips ids it can't remove (>48h old in supergroups, service messages, other users' messages) and never reports how many went, so `deleteMsgs` counts what it submitted and logs the shortfall.


## Helper Functions

- **`truncate(s, max)`**: Truncates string to `max` runes with `...` suffix
- **`escapeMarkdown(s)`**: Escapes `_`, `*`, `` ` ``, `[` for Telegram Markdown mode

## Localization

- `Get(key, lang, args...)` — falls back to English if key missing in requested language (defensive only)
- **All 13 languages must have every new key defined.** Do NOT rely on English fallback.
- When adding new features, add translations to all 13 language maps (`en`, `es`, `fr`, `de`, `hi`, `ur`, `sw`, `ha`, `yo`, `zu`, `am`, `af`, `ig`).
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

## How `collectURLs` Works

`collectURLs` walks parsed JSON and returns every URL it finds, in document order:

1. A string starting with `http://`/`https://` is an item; its `title` comes from the object it was found in
2. For a map, list-shaped containers (`data`, `items`, `medias`, `results`, `posts`, `images`, `videos`) are walked **first**, then the known URL keys (`url`, `download_url`, `video_url`, `media_url`, `link`, `file`, `downloadLink`, `downloadUrl`), then any remaining field
3. Arrays recurse element by element, preserving order
4. Duplicate URLs are dropped

`extractURL` is the older single-hit variant and still used for single-file endpoints (it searches arrays from the end, which is why reels used to work by accident).

## Instagram Delivery

`downloadInstagram` branches on what the API returns:

- **Non-JSON response** → treated as one media file (`sendSingleMedia` picks photo/video/document from Content-Type)
- **JSON with a video entry** → that one video is sent; thumbnail/cover entries are skipped (`isVideoItem`)
- **JSON with images only** → every image is delivered as Telegram albums of 10 (`sendAlbum` → `SendMediaGroup`), capped at `igMaxItems` (50). One album is downloaded, sent and released before the next, and `igAlbumMaxBytes` stops an album growing past 40MB. A rejected album falls back to individual `sendPhoto` calls.
- `InputMediaPhoto` needs `Type: "photo"` set explicitly — tgbotapi does not fill it in, and Telegram rejects the whole album without it.

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
| Short URL (Reurl) | `/api/shortener/reurl?apiKey=KEY&url=URL` | Utility |
| Google News | `/api/news/google?apiKey=KEY&query=QUERY` | News |
| TextPro Effects | `/textpro/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
| Photooxy Effects | `/photooxy/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
| Ephoto360 Effects | `/ephoto/EFFECT?text=TEXT` or `text1=T1&text2=T2` | Text Effect |
| Image Effects | `POST /sharp/EFFECT?apiKey=KEY` with `file` multipart | Image Effect (blur, brightness, contrast, etc.) |
| Artistic Effects | `POST /sharp/EFFECT?apiKey=KEY` with `file` multipart | Artistic Effect (pencilSketch, etc.) |
