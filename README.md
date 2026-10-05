# Telegram Multipurpose Bot

A feature-rich multipurpose Telegram bot with media downloading, search engines, text/image effects, URL shortener, news, sports scores, group and channel administration, reminders, and 13 languages. Built with Go, uses BoltDB for persistence.

## Interface

The main menu stays at **11 buttons**; everything else lives one level down.

| Main menu | Submenu | Contains |
|---|---|---|
| ❓ Help | — | Command list |
| 👤 Profile | — | Your user info |
| ⚙️ Settings | — | Language, notifications |
| 💬 Feedback | — | Free-text feedback |
| 📊 Poll | — | Two-step poll creator |
| ⬇️ Download | → | YouTube, Instagram, TikTok, Facebook, Pinterest, Snapchat, Twitter |
| 🔍 Search | → | Pinterest, Sticker, Imgur, YouTube, Bing web, Bing images |
| 🎨 Create | → | Text Maker (TextPro/Photooxy/Ephoto), Meme, Image Effects, Artistic |
| 🧰 Tools | → | QR, Weather, Translate, Convert, Short URL, Remind, History, Reddit |
| 📢 More | → | News, Sports, Poll, **About** |
| 🛡️ Manage | → | Groups, Stats, Group Settings, Welcome, Ban, Kick, Mute, Invite, Purge |

Group commands need the bot to be an **administrator** in that chat. The Manage
menu is hidden in channels, where only channel admin actions apply.

## Features

### 🦑 Kraken, the AI agent

Any message that is not a command, a link or a pending prompt is answered by
**Kraken**, Qasim's personal AI agent. It replies in whichever of the 13
languages the chat is set to, and is tuned to answer like a person rather than a
help desk.

- **Private chats**: answers every message, on by default.
- **Groups**: quiet unless you reply to it or @mention it, off by default.
- `/ai on`, `/ai off`, `/ai status` — or the Kraken row in **Settings**, per chat.
- Replies carry no bot prefix and no buttons, so it reads like a person typing.

```bash
AI_BASE_URL=https://mistral.stacktoy.workers.dev
AI_KEY=your-key-here
```

The key is read from the environment only; `config.json` ships with an empty
`ai.apiKey` so no secret is committed. Without `AI_KEY` the agent stays silent
and everything else keeps working.

Run `/ai status` in Telegram to confirm the endpoint is reachable; it reports
which setting is missing when it is not.

### 📥 Media Downloaders
| Platform | Formats |
|----------|---------|
| **YouTube** | mp3, 360p, 720p, 1080p |
| **Instagram** | Posts (single image or full carousel, delivered as albums of 10, up to 50 items), reels |
| **TikTok** | No watermark, HD, music audio |
| **Facebook** | Dynamic quality selection |
| **Pinterest** | Videos |
| **Snapchat** | Stories, spotlight |
| **Twitter/X** | Media downloads |
| **Threads** | Posts and carousels |
| **GitHub** | Public repository as a zip (`/gh`, link must be `owner/repo`) |
| **Vidsplay** | Stock video clips |
| **Odysee** | Video posts |
| **iStock / Alamy** | Stock photos |
| **CapCut** | Template clips |
| **IMDb** | Trailers |

A pasted link from any of these sites starts the download on its own. GitHub is the
exception: use `/gh` or the menu, since a pasted `github.com` link is usually a file
or issue rather than a request to download a repository.

### 🛡️ Group & Channel Administration
Reply to a message, or pass `@username`, to act on a member.

- **Ban / Unban** — ban with message deletion, revoke on unban
- **Kick** — remove without a permanent ban
- **Mute** — restrict permissions (silences the member)
- **Promote / Demote** — grant or remove admin rights
- **Purge** — delete one message (reply) or the last 1–100 (send a number)
- **Invite Link** — create a fresh invite link and post it
- **Welcome Message** — greets new members, supports `{user}`, `{username}`, `{group}`
- **Group Settings** — toggle welcome, lockdown, anti-links, anti-caps
- **Group Stats** — message counter and live member count
- **Channel Settings / Stats** — per-channel info
- **Moderation** — lockdown mutes the sender and deletes their message
- **Stream Link** — store a live stream URL on the chat record
- **Post** — relay a message into a group or channel
- **Group List** — every chat the bot has been active in

Anti-link and anti-caps are enforced on the message path, not via commands, so
they apply to ordinary chat traffic rather than admin actions.

### 🧰 Tools
- **QR Code** — renders text or a URL as a PNG
- **Weather** — current conditions by city (temperature, description, wind, humidity)
- **Translate** — detects the source language, translates to a chosen target
- **Unit Converter** — `5 kg to lb`, `100 usd to eur`
- **Reminders** — `/remind 10m standup`, fires from a background ticker
- **History** — your last 15 queries, per user
- **Reddit** — fetch posts from a subreddit
- **Meme** — generate from templates (Drake, Buzz, Change, Distracted, …)

Reminder durations accept `s`, `m`, `h`, `d` in either `10m text` or `10 m text`
form. Unreadable text is preserved; only the leading amount is parsed.

### 🔍 Search Engines
- **Pinterest Search** — search and download images
- **Sticker Search** — search stickers from API
- **Imgur Search** — search and download images
- **YouTube Search** — search videos with quick access links
- **Bing Search** — web search and image search

### 🎨 Text & Image Effects
- **TextPro** — styled text (Neon Light, Avengers, Pornhub Style, Harry Potter, and more)
- **Photooxy** — photo effects (Battle 4, TikTok, and more)
- **Ephoto360** — ephoto effects (Wolf Galaxy, Free Fire Banner, Apex, and more)
- **Memes** — text-on-image templates
- **Image Effects** — blur, brightness, contrast, invert, grayscale, sharpen
- **Artistic Effects** — pencil sketch, HDR, bokeh, thermal, X-ray, infrared, auto enhance

### 🔗 URL Shortener
- **Reurl**, **Tiny.cc**, **Its.sl**, **Cuq.in**, **Vurl**, **TinyURL**

### 📰 News
- **Google News** (with search query), **BBC**, **CNN**, **Al Jazeera**, **CGTN World**, **TRT World**

### ⚽ Sports
- **Cricket**, **NFL**, **NBA**, **Cricbuzz**

### ⚙️ General
- **Auto URL Detection** — paste any supported link, bot auto-starts the download
- **Multi-Language** — 13 languages (en, es, fr, de, hi, ur, sw, ha, yo, zu, am, af, ig), every string translated in all 13
- **Inline Keyboard Menus** — 11-button main menu, six submenus, back navigation
- **Forwarded Media** — re-download and repost media from a forwarded message
- **Poll Creator** — two-step flow: question then options (2–10)
- **Feedback System** — free-text feedback stored with timestamps
- **User Tracking** — first seen, last seen, name/username tracking
- **Admin Panel** — total users and feedback count, with a feedback viewer
- **Update Audit Log** — every command, callback and message is logged with chat, user and action
- **Inline Mode** — `@botusername help` and `@botusername about`
- **Concurrent Downloads** — semaphore-limited (max 2), 100MB cap
- **Configurable API** — base URL and API key via `config.json` or `.env`
- **Timezone-aware** — configurable timezone for timestamps

## Getting Started

### 1. Create a Telegram Bot

Open [BotFather](https://t.me/botfather) and send:

```
/newbot
```

Follow the prompts to choose a name and username. After creation, BotFather will give you an **HTTP API token**:

```
123456789:ABCdefGHIjklmNOPqrStuVWXyz-1234567890
```

Save this token — you'll need it for `BOT_TOKEN`.

### 2. Configuration

```bash
cp .env.example .env
```

Edit `.env` with your bot token and settings:

```ini
BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
ADMIN_IDS=123456789,987654321
API_BASE_URL=https://api.qasimdev.dpdns.org/api
API_KEY=qasim-dev
DB_PATH=/bot/data/bot.db
```

#### Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `BOT_TOKEN` | **Yes** | Telegram Bot API token from BotFather |
| `ADMIN_IDS` | No | Comma-separated admin IDs (merged with config) |
| `API_BASE_URL` | No | Overrides `apiBaseUrl` in config.json |
| `API_KEY` | No | Overrides `apiKey` in config.json |
| `DB_PATH` | No | BoltDB file path (default: `./bot.db`) |
| `PORT` | No | Health-check port (default: `8080`) |
| `MEM_LIMIT_MB` | No | Go soft memory limit in MB (default: `400`) — lower it on a 512MB instance |
| `GC_PERCENT` | No | `GOGC` target (default: `50`) — trades CPU for a smaller heap |
| `AI_BASE_URL` | No | Overrides `ai.apiBaseUrl` in config.json |
| `AI_KEY` | **Yes** for AI chat | Key for the AI agent endpoint — deliberately blank in config.json |

Commands listed in `config.json` with `enabled: true` are published to Telegram's
slash menu on startup, so they show up in the client without manual setup.

#### config.json

All bot settings live in `config.json`: bot info, owner, timezone, commands, UI buttons, API config, localization, and features. See the file directly for the full schema.

Key fields:

| Field | Description |
|-------|-------------|
| `bot.name` | Bot display name |
| `bot.username` | Bot username (without @) |
| `bot.photo` | Welcome image URL |
| `owner.name` | Owner display name |
| `owner.username` | Owner Telegram handle |
| `owner.id` | Owner Telegram user ID (receives startup greeting) |
| `timezone` | Timezone for timestamps (e.g. `Asia/Karachi`, `UTC`) |
| `commands` | Enable/disable commands and set descriptions |
| `adminIds` | Additional admin user IDs |
| `apiBaseUrl` | Third-party API base URL |
| `apiKey` | Third-party API key |
| `ui.prefix` | Prefix shown on every message |
| `ui.mainMenu.buttons` | Main menu button definitions |
| `localization.defaultLanguage` | Fallback language |
| `localization.supportedLanguages` | Available languages |

### 3. Find Your User ID

Message [@userinfobot](https://t.me/userinfobot) — it will reply with your ID. Add it to `adminIds` in `config.json` or `ADMIN_IDS` in `.env`.

## One-Line Install

Install and run in one command — the script will prompt for your BOT_TOKEN and start the bot automatically:

| Platform | Command |
|----------|---------|
| **Linux / macOS** | `bash <(curl -sL https://raw.githubusercontent.com/GlobalTechInfo/telegram-bot/main/install.sh)` |
| **Windows** (PowerShell as Admin) | `iwr -useb https://raw.githubusercontent.com/GlobalTechInfo/telegram-bot/main/install.ps1 \| iex` |
| **Termux** | `pkg install curl -y && bash <(curl -sL https://raw.githubusercontent.com/GlobalTechInfo/telegram-bot/main/install.sh)` |

The installer will:
1. Install Git and Go if missing
2. Clone the repository
3. **Prompt for BOT_TOKEN** (and optionally ADMIN_IDS, API_URL, API_KEY)
4. Build the binary
5. Create a system-wide shortcut (`telegram-bot`)
6. **Start the bot automatically**

## Deployment

### Local (Direct)

Requirements: Go 1.25+

```bash
git clone https://github.com/yourusername/telegram-bot
cd telegram-bot
cp .env.example .env
# Edit .env with your BOT_TOKEN
go mod download
go build -o telegram-bot .
./telegram-bot
```

### Docker

```bash
git clone https://github.com/yourusername/telegram-bot
cd telegram-bot
cp .env.example .env
# Edit .env with your BOT_TOKEN
docker compose up --build -d
docker compose logs -f
```

### Systemd (Linux)

```bash
go build -o telegram-bot .
sudo mv telegram-bot /opt/telegram-bot/
sudo mkdir -p /opt/telegram-bot/data
sudo cp .env.example /opt/telegram-bot/.env
# Edit /opt/telegram-bot/.env with your BOT_TOKEN
```

Create `/etc/systemd/system/telegram-bot.service`:

```ini
[Unit]
Description=Telegram Multipurpose Bot
After=network.target

[Service]
Type=simple
Restart=always
RestartSec=5
WorkingDirectory=/opt/telegram-bot
ExecStart=/opt/telegram-bot/telegram-bot
EnvironmentFile=/opt/telegram-bot/.env

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable telegram-bot
sudo systemctl start telegram-bot
```

### Cloud Hosts

**Railway** — Push to GitHub → New Project → Deploy from repo → Add `BOT_TOKEN` env var

**Render** — New Web Service → Connect repo → Build: `go build -o telegram-bot` → Start: `./telegram-bot` → Add `BOT_TOKEN`

**Fly.io** — `fly launch` → `fly secrets set BOT_TOKEN=your_token_here` → `fly deploy`

**Northflank / Koyeb** — same as Render. Set the health-check path to `/health`
and the port to `8080`; the bot answers `200 {"status":"ok"}` there. It only
long-polls Telegram, so this endpoint exists purely to satisfy the probe — if it
is removed the host will terminate the service.

### Health check

```
GET /health   → 200 {"status":"ok"}
GET /healthz  → 200 {"status":"ok"}
GET /         → 200 {"status":"ok"}
```

Port comes from `$PORT`, defaulting to `8080`.

## Commands

| Command | Access | Description |
|---------|--------|-------------|
| `/start` | Public | Main menu with welcome message |
| `/help` | Public | List of commands |
| `/settings` | Public | Language changer, notification toggle |
| `/profile` | Public | Your user info |
| `/feedback` | Public | Send feedback |
| `/about` | Public | Bot info with timezone |
| `/poll` | Public | Create a poll |
| `/yt` | Public | Download YouTube videos |
| `/ig` | Public | Download Instagram posts/reels |
| `/tt` | Public | Download TikTok videos |
| `/fb` | Public | Download Facebook videos |
| `/pin` | Public | Download Pinterest videos |
| `/sc` | Public | Download Snapchat stories/spotlight |
| `/tw` | Public | Download Twitter/X posts |
| `/bing` | Public | Search the web or find images |
| `/search` | Public | Open search menu (Pinterest, Stickers, Imgur, YouTube, Bing) |
| `/textmaker` | Public | Open text maker menu (TextPro, Photooxy, Ephoto360) |
| `/shorturl` | Public | Open URL shortener menu |
| `/news` | Public | Open news menu |
| `/sports` | Public | Open sports scores menu |
| `/imageeffect` | Public | Apply effects to images |
| `/artistic` | Public | Artistic effects for images |
| `/qr` | Public | Generate a QR code from text or a URL |
| `/weather` | Public | Current weather for a city |
| `/translate` | Public | Translate text to another language |
| `/convert` | Public | Convert units (`5 kg to lb`) |
| `/meme` | Public | Generate a meme from a template |
| `/reddit` | Public | Fetch posts from a subreddit |
| `/remind` | Public | Set a reminder; bare `/remind` lists yours |
| `/history` | Public | Your recent queries |
| `/ban` | Group admin | Ban the replied-to user |
| `/unban` | Group admin | Unban a user |
| `/kick` | Group admin | Kick the replied-to user |
| `/mute` | Group admin | Mute the replied-to user |
| `/promote` | Group admin | Promote a user to admin |
| `/demote` | Group admin | Remove admin from a user |
| `/ai` | Anyone | Turn Kraken on or off for this chat, or check its state |
| `/del` | Group admin | Delete a message (reply) or the last 1–100 (batched, paced to stay under Telegram's flood limit) |
| `/invite` | Group admin | Create and post an invite link |
| `/welcome` | Group admin | Set the new-member welcome message |
| `/ginfo` | Group admin | Show group info |
| `/gsettings` | Group admin | Toggle welcome, lockdown, anti-links, anti-caps |
| `/lockdown` | Group admin | Mute and delete every message |
| `/gstats` | Group admin | Message counter and member count |
| `/groups` | Bot admin | List every known group |
| `/chstats` | Channel admin | Channel stats |
| `/stream` | Group admin | Store a live stream link on the chat |
| `/post` | Group admin | Post a message into the group or channel |
| `/admin` | Bot admin | Stats panel (users, feedback count) |

## Architecture

```
main.go                  Entry point — loads config/env, connects to Telegram, event loop
├── config/config.go     Configuration loading, IsAdmin(), URL/key helpers
├── handlers/handlers.go Command/callback/message dispatch, download/search/effect logic
├── handlers/admin.go    Group & channel administration, reminders, tool API calls
├── keyboards/keyboards.go Inline keyboard markup builders (main menu + 6 submenus)
├── localization/localization.go i18n — 13 languages, every key in every language
├── session/session.go   BoltDB persistence (users, sessions, feedback, groups, reminders, queries)
├── config.json          Bot configuration (commands, UI, features, localization, API)
├── .env                 Secrets (BOT_TOKEN, ADMIN_IDS, API config, DB_PATH)
└── .env.example         Template for .env
```

- **Language**: Go 1.25+
- **Dependencies**: `go-telegram-bot-api/v5`, `bbolt` (embedded)
- **Storage**: Local BoltDB file — no external database required
- **API**: Base URL + API key configurable via `config.json` or `.env`
- **Concurrency**: Each update in its own goroutine; download semaphore (max 2)
- **Memory**: 100MB per-download cap, explicit GC after large transfers
- **State Machine**: Per-user state tracking for multi-step flows
- **Panic Safety**: All goroutines have deferred panic recovery
- **No shared per-update state**: updates are concurrent, so a request's message is
  always passed as an argument — never stored on `Handler`

### Storage buckets

| Bucket | Key | Contents |
|---|---|---|
| `users` | user ID | name, username, first/last seen |
| `sessions` | chat ID | language, state machine, transient data |
| `feedbacks` | sequence | feedback entries, capped at 500 |
| `groups` | chat ID | per-chat settings, welcome, counters, stream URL |
| `reminders` | sequence | pending reminders, deleted once fired |
| `queries` | sequence | per-user query history, capped at 200 |

## Building and Testing

```bash
make build     # build the binary
make run       # run from source
make test      # run the test suite
make vet       # go vet
make fmt       # gofmt -w .
```

Use `make` rather than bare `go build` / `go run` on memory-constrained hosts.
The `Makefile` pins `GOMEMLIMIT=700MiB`, because the Go compiler segfaults when
swap is exhausted. `go run` does not inherit that limit — use `make run`.

### Tests

```
localization/  key parity across all 13 languages, no self-referential values, English fallback
handlers/      every Get() key resolves; every config command dispatched;
               every state handled; every menu button has a callback handler;
               main menu within its 11-button budget;
               API response parsing against captured payloads;
               live API checks (set LIVE_API=1 to enable)
session/       group config, reminder lifecycle, query log cap and user scoping
```

## Adding a New Language

1. Add strings to the language map in `localization/localization.go` (all 13 languages)
2. `make test` — the key-parity test fails if any language is missing a key

## Adding a New Feature

1. Add handler logic in `handlers/handlers.go` or `handlers/admin.go`
2. Add localization strings in `localization/localization.go` (all 13 languages)
3. Add keyboard if needed in `keyboards/keyboards.go`
4. Register command + callback + state + auto-detect
5. Add command in `config.json`; add the menu button to a submenu, not the main grid
6. `make test` — dispatch, state, menu and key tests will catch omissions

## License

MIT
