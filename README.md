# Telegram Multipurpose Bot

A feature-rich multipurpose Telegram bot with media downloading, search engines, text/image effects, URL shortener, news, sports scores, artistic effects, multi-language support, and configurable inline menus. Built with Go, uses BoltDB for persistence.

## Features

### 📥 Media Downloaders
| Platform | Formats |
|----------|---------|
| **YouTube** | mp3, 360p, 720p, 1080p |
| **Instagram** | Posts, reels |
| **TikTok** | No watermark, HD, music audio |
| **Facebook** | Dynamic quality selection |
| **Pinterest** | Videos |
| **Snapchat** | Stories, spotlight |
| **Twitter/X** | Media downloads |

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
- **Multi-Language** — 13 languages (en, es, fr, de, hi, ur, sw, ha, yo, zu, am, af, ig)
- **Inline Keyboard Menus** — contextual navigation with back buttons
- **Poll Creator** — two-step flow: question then options (2–10)
- **Feedback System** — free-text feedback stored with timestamps
- **User Tracking** — first seen, last seen, name/username tracking
- **Admin Panel** — total users and feedback count
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
| `/admin` | Admin | Stats panel (users, feedback count) |

## Architecture

```
main.go                  Entry point — loads config/env, connects to Telegram, event loop
├── config/config.go     Configuration loading, IsAdmin(), URL/key helpers
├── handlers/handlers.go Command/callback/message handlers, all download/search/effect logic
├── keyboards/keyboards.go Inline keyboard markup builders
├── localization/localization.go i18n — 13 languages with English fallback
├── session/session.go   BoltDB persistence (users, sessions, feedback, settings)
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

## Adding a New Language

1. Add strings to the language map in `localization/localization.go` (all 13 languages)
2. `Get()` falls back to English for missing keys (defensive only — define all keys)

## Adding a New Feature

1. Add handler logic in `handlers/handlers.go`
2. Add localization strings in `localization/localization.go` (all 13 languages)
3. Add keyboard if needed in `keyboards/keyboards.go`
4. Register command + callback + state + auto-detect in `handlers/handlers.go`
5. Add command + menu button in `config.json`

## License

MIT
