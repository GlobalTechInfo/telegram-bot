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

start_bot() {
  if [ -z "$TOKEN" ]; then
    echo ""
    echo "  ⚠  Set BOT_TOKEN in $BOT_DIR/.env and run manually."
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
create_shortcut
start_bot
