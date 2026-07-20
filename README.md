# Telegram Multi-Downloader Bot

A feature-rich Telegram bot with multi-platform media downloading, search engines, text/image effects, multi-language support, and configurable inline menus. Built with Go, uses BoltDB for persistence.

## Features

### Media Downloaders
- **YouTube** — mp3/360p/720p/1080p
- **Instagram** — posts/reels
- **TikTok** — with/without watermark, HD, music audio
- **Facebook** — dynamic quality selection
- **Pinterest** — videos
- **Snapchat** — stories/spotlight
- **Twitter/X** — media downloads

### Search Engines
- **Pinterest Search** — search and download images from Pinterest
- **Sticker Search** — search and download stickers from a sticker API
- **Imgur Search** — search and download images from Imgur
- **YouTube Search** — search videos and get quick access links
- **Bing Search** — web search and image search

### Text & Image Effects
- **TextPro** — create styled text effects (Neon Light, Avengers Logo, Pornhub Style, Harry Potter)
- **Photooxy** — create photo effects (Battle 4, TikTok)
- **Ephoto360** — create ephoto effects (Wolf Galaxy, Free Fire Banner, Apex)

### General
- **Auto URL Detection** — paste any supported link, bot auto-starts the download
- **Multi-Language** — 13 languages with per-user persistence
- **Inline Keyboard Menus** — contextual navigation with back buttons
- **Poll Creator** — two-step flow: question then options (2–10)
- **Feedback System** — free-text feedback stored with timestamps
- **User Tracking** — first seen, last seen, name/username tracking
- **Admin Panel** — `/admin` shows total users and feedback count
- **Inline Mode** — `@botusername help` and `@botusername about`
- **Concurrent Downloads** — semaphore-limited (max 2), 100MB cap
- **Configurable API** — base URL and API key via `config.json` or `.env`

## Getting Started

### 1. Create a Telegram Bot

Open [BotFather](https://t.me/botfather) and send:

```
/newbot
```

Follow the prompts to choose a name and username. After creation, BotFather will give you an **HTTP API token** that looks like:

```
123456789:ABCdefGHIjklmNOPqrStuVWXyz-1234567890
```

Save this token — you'll need it for `BOT_TOKEN`.

### 2. Configuration

Copy the example config and set your bot token:

```bash
cp config.json my-config.json   # edit config.json directly
echo "BOT_TOKEN=your_token_here" > .env
```

#### config.json

```json
{
  "bot": {
    "name": "My Bot",
    "username": "MyBot",
    "description": "A feature-rich Telegram bot",
    "version": "1.0.0",
    "photo": "https://i.ibb.co/5Wc1R5D0/1784490428744.png",
    "startupMessage": ""
  },
  "owner": {
    "name": "Owner",
    "username": "@owner",
    "id": 123456789
  },
  "timezone": "Asia/Karachi",
  "commands": {
    "start": { "enabled": true, "description": "Start the bot" },
    "help": { "enabled": true, "description": "Show help" },
    "yt": { "enabled": true, "description": "Download YouTube videos" }
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
  "apiBaseUrl": "https://api.qasimdev.dpdns.org/api",
  "apiKey": "qasim-dev",
  "ui": {
    "prefix": "🤖 My Bot",
    "mainMenu": {
      "cols": 2,
      "buttons": [
        { "label": "Help", "icon": "❓", "action": "help" },
        { "label": "Search", "icon": "🔍", "action": "search" },
        { "label": "Text Maker", "icon": "🎨", "action": "textmaker" }
      ]
    },
    "confirmButtons": {
      "yesLabel": "✅ Yes",
      "noLabel": "❌ No",
      "yesAction": "confirm_yes",
      "noAction": "confirm_no"
    }
  }
}
```

#### Environment Variables (.env)

```
BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11
ADMIN_IDS=123456789,987654321
API_BASE_URL=https://api.qasimdev.dpdns.org/api
API_KEY=qasim-dev
DB_PATH=/bot/data/bot.db
```

| Variable | Required | Description |
|----------|----------|-------------|
| `BOT_TOKEN` | **Yes** | Telegram Bot API token from BotFather |
| `ADMIN_IDS` | No | Comma-separated admin IDs (merged with config) |
| `API_BASE_URL` | No | Overrides `apiBaseUrl` in config.json |
| `API_KEY` | No | Overrides `apiKey` in config.json |
| `DB_PATH` | No | BoltDB file path (default: `./bot.db`) |

### 3. Find Your User ID

To give yourself admin access, you need your Telegram user ID. Message [@userinfobot](https://t.me/userinfobot) and it will reply with your ID. Add it to `adminIds` in `config.json` or `ADMIN_IDS` in `.env`.

## Deployment

### Local (Direct)

Requirements: Go 1.25+

```bash
# Clone and enter directory
git clone https://github.com/yourusername/telegram-bot
cd telegram-bot

# Download dependencies
go mod download

# Build
go build -o telegram-bot .

# Create .env with your bot token
echo "BOT_TOKEN=your_token_here" > .env

# Run
./telegram-bot
```

### Docker

Requirements: Docker and Docker Compose

```bash
# Clone
git clone https://github.com/yourusername/telegram-bot
cd telegram-bot

# Create .env
echo "BOT_TOKEN=your_token_here" > .env

# Build and start
docker compose up --build -d

# View logs
docker compose logs -f

# Stop
docker compose down
```

#### docker-compose.yml

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

### Systemd (Linux)

```bash
# Build the binary
go build -o telegram-bot .
sudo mv telegram-bot /opt/telegram-bot/
sudo mkdir -p /opt/telegram-bot/data

# Create .env
sudo tee /opt/telegram-bot/.env <<EOF
BOT_TOKEN=your_token_here
ADMIN_IDS=123456789
EOF
```

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
EnvironmentFile=/opt/telegram-bot/.env

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable telegram-bot
sudo systemctl start telegram-bot
sudo systemctl status telegram-bot
```

### Cloud Hosts

#### Railway

1. Push your repo to GitHub
2. Go to [Railway](https://railway.app) → New Project → Deploy from GitHub repo
3. Add environment variable `BOT_TOKEN` in the dashboard
4. No Dockerfile needed — Railway auto-detects Go

#### Render

1. Push to GitHub
2. Go to [Render](https://render.com) → New Web Service → Connect your repo
3. Set **Build Command**: `go build -o telegram-bot`
4. Set **Start Command**: `./telegram-bot`
5. Add `BOT_TOKEN` as an environment variable
6. Deploy

#### Heroku (via container)

```bash
heroku create my-telegram-bot
heroku stack:set container
heroku config:set BOT_TOKEN=your_token_here
git push heroku main
```

#### Fly.io

```bash
fly launch
fly secrets set BOT_TOKEN=your_token_here
fly deploy
```

#### Self-hosted VPS

```bash
# SSH into your VPS
ssh user@your-vps-ip

# Install Go and git (Ubuntu/Debian)
sudo apt update && sudo apt install golang-go git -y

# Clone and build
git clone https://github.com/yourusername/telegram-bot
cd telegram-bot
go build -o telegram-bot .

# Run with nohup or tmux
echo "BOT_TOKEN=your_token_here" > .env
nohup ./telegram-bot &

# Or set up as a systemd service (see Systemd section above)
```

## Commands

| Command | Access | Description |
|---------|--------|-------------|
| `/start` | Public | Main menu with welcome message |
| `/help` | Public | List of commands |
| `/settings` | Public | Language changer, notification toggle |
| `/profile` | Public | Your user info |
| `/feedback` | Public | Send feedback |
| `/about` | Public | Bot info |
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
| `/admin` | Admin | Stats panel (users, feedback count) |

## Architecture

```
main.go                 Entry point — loads config/env, connects to Telegram, event loop
├── config/config.go    Configuration loading, IsAdmin(), URL/key helpers
├── handlers/handlers.go Command/callback/message handlers, all download & search logic
├── keyboards/keyboards.go Inline keyboard markup builders
├── localization/localization.go i18n — 13 languages with English fallback
├── session/session.go  BoltDB persistence (users, sessions, feedback, settings)
├── config.json         Bot configuration (commands, UI, features, localization, API)
└── .env                BOT_TOKEN, ADMIN_IDS, API_BASE_URL, API_KEY, DB_PATH
```

- **Language**: Go 1.25+
- **Dependencies**: `go-telegram-bot-api/v5`, `bbolt` (embedded)
- **Storage**: Local BoltDB file — no external database
- **API**: Base URL + API key configurable via `config.json` or `.env`
- **Concurrency**: Each update in its own goroutine; download semaphore (max 2)
- **Memory**: 100MB per-download cap, explicit GC after large transfers

## Adding a New Language

1. Add strings to the language map in `localization/localization.go`
2. `Get()` falls back to English for missing keys

## Adding a New Downloader

1. Add validation + API call in `handlers/handlers.go`
2. Add localization strings in `localization/localization.go`
3. Add keyboard if needed in `keyboards/keyboards.go`
4. Register command + callback + state + auto-detect in `handlers/handlers.go`
5. Add command + menu button in `config.json`

## License

MIT
