#!/usr/bin/env bash
set -e

REPO="https://github.com/GlobalTechInfo/telegram-bot"
BRANCH="main"
BOT_DIR="$HOME/telegram-bot"

print_step() { echo -e "\e[1;34m==>\e[0m \e[1m$1\e[0m"; }
print_ok()   { echo -e "  \e[1;32m✔\e[0m $1"; }
print_err()  { echo -e "  \e[1;31m✘\e[0m $1"; }

OS="$(uname -s)"
case "$OS" in
  Linux)
    if uname -o 2>/dev/null | grep -qi android; then
      PLATFORM="termux"
    else
      PLATFORM="linux"
    fi
    ;;
  Darwin) PLATFORM="macos" ;;
  *)      print_err "Unsupported OS: $OS"; exit 1 ;;
esac

clear
echo "╔═══════════════════════════════════════════╗"
echo "║   Telegram Multipurpose Bot Installer     ║"
echo "╚═══════════════════════════════════════════╝"
echo "  Platform: $PLATFORM"
echo ""

install_deps() {
  print_step "Installing dependencies..."
  case "$PLATFORM" in
    linux)
      if command -v apt &>/dev/null; then
        sudo apt update && sudo apt install -y git golang-go
      elif command -v pacman &>/dev/null; then
        sudo pacman -S --noconfirm git go
      elif command -v dnf &>/dev/null; then
        sudo dnf install -y git golang
      elif command -v apk &>/dev/null; then
        sudo apk add git go
      else
        print_err "No supported package manager. Install git and Go 1.25+ manually."
        exit 1
      fi
      ;;
    termux) pkg update -y && pkg install -y git golang ;;
    macos)
      if ! command -v brew &>/dev/null; then
        /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
      fi
      brew install git go
      ;;
  esac
  print_ok "Dependencies installed"
}

install_ffmpeg() {
  # Video editing in /media shells out to ffmpeg: trim, extract audio, make a
  # voice note, build a GIF. The photo buttons are pure Go and work without it.
  # Treated as optional rather than required — the bot is fully usable without it.
  print_step "Checking ffmpeg (optional, needed for video editing)..."
  if command -v ffmpeg &>/dev/null; then
    print_ok "ffmpeg already installed: $(ffmpeg -version 2>/dev/null | head -1 | cut -d' ' -f1-3)"
    return
  fi

  case "$PLATFORM" in
    linux)
      if command -v apt &>/dev/null; then
        sudo apt install -y ffmpeg || true
      elif command -v pacman &>/dev/null; then
        sudo pacman -S --noconfirm ffmpeg || true
      elif command -v dnf &>/dev/null; then
        # RPM Fusion carries ffmpeg; without it dnf reports "no match".
        sudo dnf install -y https://mirrors.rpmfusion.org/free/el/rpmfusion-free-release-$(rpm -E %rhel).noarch.rpm || true
        sudo dnf install -y ffmpeg || true
      elif command -v apk &>/dev/null; then
        sudo apk add ffmpeg || true
      elif command -v zypper &>/dev/null; then
        sudo zypper install -y ffmpeg || true
      fi
      ;;
    termux) pkg install -y ffmpeg || true ;;
    macos)
      if command -v brew &>/dev/null; then
        brew install ffmpeg || true
      fi
      ;;
  esac

  if command -v ffmpeg &>/dev/null; then
    print_ok "ffmpeg installed: video editing enabled"
  else
    print_err "ffmpeg not installed. Video buttons stay hidden; everything else works."
    echo "       Install it later with your package manager, then restart the bot."
  fi
}

check_go() {
  if ! command -v go &>/dev/null; then install_deps; fi
  GO_VER=$(go version | grep -oP 'go\d+\.\d+' | tr -d 'go')
  MAJOR=${GO_VER%.*}; MINOR=${GO_VER#*.}
  if [ "$MAJOR" -lt 1 ] || { [ "$MAJOR" -eq 1 ] && [ "$MINOR" -lt 25 ]; }; then
    print_err "Go 1.25+ required (found $GO_VER). Please upgrade."
    exit 1
  fi
  print_ok "Go $GO_VER detected"
}

clone_repo() {
  print_step "Cloning repository..."
  if [ -d "$BOT_DIR" ]; then
    echo "  Directory exists. Pulling latest..."
    cd "$BOT_DIR" && git pull origin "$BRANCH"
  else
    git clone --branch "$BRANCH" "$REPO" "$BOT_DIR"
  fi
  print_ok "Repository ready at $BOT_DIR"
}

setup_env() {
  print_step "Configuring bot..."
  cd "$BOT_DIR"

  cp .env.example .env

  read -p "$(echo -e "  \e[1mEnter BOT_TOKEN\e[0m (from @BotFather): ")" TOKEN
  if [ -n "$TOKEN" ]; then
    if grep -q '^BOT_TOKEN=' .env; then
      sed -i "s|^BOT_TOKEN=.*|BOT_TOKEN=$TOKEN|" .env
    else
      echo "BOT_TOKEN=$TOKEN" >> .env
    fi
    print_ok "BOT_TOKEN saved"
  else
    print_err "BOT_TOKEN is required. Edit .env manually before running."
  fi

  read -p "$(echo -e "  \e[1mEnter ADMIN_IDS\e[0m (comma-separated, optional): ")" ADMINS
  [ -n "$ADMINS" ] && sed -i "s|^ADMIN_IDS=.*|ADMIN_IDS=$ADMINS|" .env && print_ok "ADMIN_IDS saved"

  read -p "$(echo -e "  \e[1mEnter API_BASE_URL\e[0m (press Enter for default): ")" API_URL
  [ -n "$API_URL" ] && sed -i "s|^API_BASE_URL=.*|API_BASE_URL=$API_URL|" .env

  read -p "$(echo -e "  \e[1mEnter API_KEY\e[0m (press Enter for default): ")" API_KEY
  [ -n "$API_KEY" ] && sed -i "s|^API_KEY=.*|API_KEY=$API_KEY|" .env

  echo ""
  print_ok "Configuration saved"
}

build_bot() {
  print_step "Building bot..."
  cd "$BOT_DIR"
  go mod download
  go build -o telegram-bot .
  print_ok "Build complete"
}

create_shortcut() {
  case "$PLATFORM" in
    termux)
      mkdir -p "$PREFIX/bin"
      cat > "$PREFIX/bin/telegram-bot" << EOF
#!/data/data/com.termux/files/usr/bin/bash
cd $BOT_DIR && exec ./telegram-bot "\$@"
EOF
      chmod +x "$PREFIX/bin/telegram-bot"
      print_ok "Shortcut: telegram-bot (in PATH)"
      ;;
    linux|macos)
      if [ -d "/usr/local/bin" ]; then
        sudo ln -sf "$BOT_DIR/telegram-bot" /usr/local/bin/telegram-bot
        print_ok "Shortcut: telegram-bot (in /usr/local/bin)"
      fi
      ;;
  esac
}

SERVICE_INSTALLED=0

install_systemd() {
  # Without a service the bot dies the moment the SSH session ends, which is the
  # usual first surprise on a VPS.
  if [ "$PLATFORM" != "linux" ] || ! command -v systemctl &>/dev/null; then
    return
  fi
  echo ""
  read -p "$(echo -e "  \e[1mInstall as a systemd service?\e[0m (recommended, starts on boot) [Y/n]: ")" SVC
  case "$SVC" in
    [nN]*) print_ok "Skipped. Run ./telegram-bot yourself to start it."; return ;;
  esac

  sudo tee /etc/systemd/system/telegram-bot.service >/dev/null <<EOF
[Unit]
Description=Telegram Multipurpose Bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$USER
WorkingDirectory=$BOT_DIR
ExecStart=$BOT_DIR/telegram-bot
Restart=always
RestartSec=5
Environment=GOMEMLIMIT=400MiB

[Install]
WantedBy=multi-user.target
EOF
  sudo systemctl daemon-reload
  sudo systemctl enable --now telegram-bot
  SERVICE_INSTALLED=1
  print_ok "Service installed and running: systemctl status telegram-bot"
}

start_bot() {
  if [ -z "$TOKEN" ]; then
    echo ""
    echo "  ⚠  Set BOT_TOKEN in $BOT_DIR/.env and run manually."
    return
  fi

  # The service is already running it. Starting a second copy here would put two
  # processes on the same long-poll and Telegram would drop updates at random.
  if [ "$SERVICE_INSTALLED" = "1" ]; then
    echo ""
    print_ok "Bot is running as a service."
    echo "     Follow logs:  journalctl -u telegram-bot -f"
    echo "     Restart:      systemctl restart telegram-bot"
    return
  fi

  echo ""
  print_step "Starting bot..."
  echo "  Press Ctrl+C to stop"
  echo ""

  cd "$BOT_DIR"
  exec ./telegram-bot
}

check_go
clone_repo
setup_env
build_bot
install_ffmpeg
create_shortcut
install_systemd

echo ""
print_step "One thing left in @BotFather"
echo "  Run  /setinline  and give it a placeholder text."
echo "  Inline mode cannot be enabled over the Bot API, so without this"
echo "  @yourbot <query> returns nothing. Everything else works regardless."

start_bot
