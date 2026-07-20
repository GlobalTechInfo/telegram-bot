# Telegram Multi-Downloader Bot

A feature-rich Telegram bot with multi-platform media downloading, multi-language support, inline keyboards, and configurable menus. Built with Go, uses BoltDB for persistence.

## Features

- **Media Downloaders** — YouTube (mp3/360p/720p/1080p), Instagram, TikTok (with/without watermark, HD, music audio), Facebook (dynamic quality selection), Pinterest
- **Auto URL Detection** — Paste any supported link in idle state, the bot automatically starts the download flow
- **Multi-Language** — 13 languages with per-user persistence
- **Inline Keyboard Menus** — Contextual navigation with back buttons
- **Poll Creator** — Two-step flow: question then options (2–10)
- **Feedback System** — Free-text feedback stored with timestamps
- **User Tracking** — First seen, last seen, name/username tracking
- **Admin Panel** — `/admin` shows total users and feedback count
- **Inline Mode** — `@botusername help` and `@botusername about`
- **Concurrent Downloads** — Semaphore-limited (max 2), 100MB cap per download
- **Memory Efficient** — Explicit GC hints after large transfers, nil slice cleanup
- **Graceful Shutdown** — SIGINT/SIGTERM handling
- **Panic Recovery** — Per-update goroutine recovery with logging

## Commands

| Command | Aliases | Access | Description |
|---------|---------|--------|-------------|
| `/start` | | Public | Main menu with welcome message |
| `/help` | | Public | List of commands |
| `/settings` | | Public | Language changer, notification toggle |
| `/profile` | | Public | Your user info |
| `/feedback` | | Public | Send feedback |
| `/about` | | Public | Bot info |
| `/poll` | | Public | Create a poll |
| `/yt` | `/download` | Public | Download YouTube videos |
| `/ig` | `/instagram` | Public | Download Instagram posts/reels |
| `/tt` | `/tiktok` | Public | Download TikTok videos |
| `/fb` | `/facebook` | Public | Download Facebook videos |
| `/pin` | `/pinterest` | Public | Download Pinterest videos |
| `/sc` | `/snapchat` | Public | Download Snapchat stories/spotlight |
| `/admin` | | Admin only | Stats panel |

## Configuration

### config.json

```json
{
  "bot": {
    "name": "Bot Name",
    "username": "BotUsername",
    "description": "Bot description",
    "version": "1.0.0",
    "photo": "https://...photo.jpg or file_id",
    "startupMessage": "Custom welcome (optional)"
  },
  "owner": {
    "name": "Owner",
    "username": "@owner",
    "id": 123456789
  },
  "timezone": "Asia/Karachi",
  "commands": {
    "start": { "enabled": true, "description": "..." }
  },
  "adminIds": [],
  "features": {
    "inlineMode": true,
    "userTracking": true,
    "feedbackSystem": true,
    "polls": true
  },
  "localization": {
    "defaultLanguage": "en",
    "supportedLanguages": ["en","es","fr","de","hi","ur","sw","ha","yo","zu","am","af","ig"]
  },
  "ui": {
    "prefix": "🤖 My Bot",
    "mainMenu": {
      "cols": 2,
      "buttons": [
        { "label": "Help", "icon": "❓", "action": "help" }
      ]
    },
    "confirmButtons": {
      "yesLabel": "✅ Yes", "noLabel": "❌ No",
      "yesAction": "confirm_yes", "noAction": "confirm_no"
    }
  }
}
```

### Environment Variables (.env)

```
BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
ADMIN_IDS=123456789,987654321
DB_PATH=/bot/data/bot.db
```

| Variable | Required | Description |
|----------|----------|-------------|
| `BOT_TOKEN` | **Yes** | Telegram Bot API token |
| `ADMIN_IDS` | No | Comma-separated admin IDs (merged with config) |
| `DB_PATH` | No | BoltDB file path (default: `./bot.db`) |

## Deployment

### Local

```bash
go mod download
go build -o telegram-bot .
cp config.json telegram-bot
echo "BOT_TOKEN=..." > .env
./telegram-bot
```

### Docker

```bash
docker compose build
docker compose up -d
```

### Docker Compose (docker-compose.yml)

```yaml
services:
  bot:
    build: .
    container_name: telegram-bot
    restart: unless-stopped
    env_file: .env
    volumes:
      - ./bot-data:/bot/data
```

### Systemd (manual)

Create `/etc/systemd/system/telegram-bot.service`:

```ini
[Unit]
Description=Telegram Bot
After=network.target

[Service]
Type=simple
Restart=always
RestartSec=5
WorkingDirectory=/opt/telegram-bot
ExecStart=/opt/telegram-bot/telegram-bot
Environment=BOT_TOKEN=your_token_here

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable telegram-bot
systemctl start telegram-bot
```

## Architecture

```
main.go                 Entry point — loads config, connects to Telegram, event loop
├── config/config.go    Configuration loading and validation
├── handlers/handlers.go Command/callback/message handlers, download logic
├── keyboards/keyboards.go Inline keyboard builders
├── localization/localization.go i18n — 13 languages
└── session/session.go  BoltDB persistence (users, sessions, feedbacks)
```

- **Language**: Go 1.25+
- **Dependencies**: `go-telegram-bot-api/v5`, `bbolt`
- **Storage**: Local BoltDB file — no external database needed
- **API**: Media downloads via `https://api.qasimdev.dpdns.org/api/`
- **Concurrency**: Each update handled in its own goroutine; download semaphore (max 2)
- **Memory**: 100MB per-download cap, explicit GC after large transfers

## Adding a New Language

1. Add strings to all language maps in `localization/localization.go`
2. The `Get()` function falls back to English for missing keys

## Adding a New Downloader

1. Add API endpoint + validation function in `handlers/handlers.go`
2. Add localization strings in `localization/localization.go`
3. Add keyboard if needed in `keyboards/keyboards.go`
4. Register command in `HandleCommand` + callback in `HandleCallback` + state in `HandleMessage` + auto-detect in default case
5. Add command entry + menu button in `config.json`

## License

MIT
