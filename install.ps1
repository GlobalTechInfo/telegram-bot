# Telegram Multipurpose Bot Installer for Windows
# Run in PowerShell (right-click → Run as Administrator)

$ErrorActionPreference = "Stop"
$Repo = "https://github.com/GlobalTechInfo/telegram-bot"
$Branch = "main"
$BotDir = "$env:USERPROFILE\telegram-bot"

function Write-Step($text) { Write-Host "==> $text" -ForegroundColor Blue }
function Write-OK($text)   { Write-Host "  ✔ $text" -ForegroundColor Green }
function Write-Err($text)  { Write-Host "  ✘ $text" -ForegroundColor Red }

Clear-Host
Write-Host "╔═══════════════════════════════════════════╗" -ForegroundColor Cyan
Write-Host "║   Telegram Multipurpose Bot Installer     ║" -ForegroundColor Cyan
Write-Host "╚═══════════════════════════════════════════╝" -ForegroundColor Cyan
Write-Host ""

# --- Check Git ---
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    Write-Step "Installing Git..."
    winget install --id Git.Git -e --source winget
    $env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine")
    Write-OK "Git installed"
} else { Write-OK "Git detected" }

# --- Check Go ---
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Step "Installing Go..."
    winget install --id GoLang.Go -e --source winget
    $env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine")
    Write-OK "Go installed"
}

# --- Go version gate ---
# winget installs whatever is current, which can be older than the 1.25 this
# project needs. Installing without checking used to fail much later at
# `go build`, with a confusing message.
$goVer = (go version) -replace '.*go(\d+\.\d+).*','$1'
$parts = $goVer.Split('.')
if ([int]$parts[0] -lt 1 -or ([int]$parts[0] -eq 1 -and [int]$parts[1] -lt 25)) {
    Write-Err "Go 1.25+ required (found $goVer). Update from https://go.dev/dl/ and re-run."
    exit 1
}
Write-OK "Go $goVer detected"

# --- ffmpeg (optional) ---
# Backs the video buttons in /media: trim, extract audio, voice note, GIF. The
# photo buttons are pure Go and work without it, so a failure here is not fatal.
Write-Step "Checking ffmpeg (optional, needed for video editing)..."
if (Get-Command ffmpeg -ErrorAction SilentlyContinue) {
    Write-OK "ffmpeg already installed"
} else {
    # Gyan.FFmpeg is the winget package that ships a static build with libopus,
    # which the voice note needs. BtbN.FFmpeg is the fallback.
    winget install --id Gyan.FFmpeg -e --source winget --accept-source-agreements --accept-package-agreements 2>$null
    if (-not (Get-Command ffmpeg -ErrorAction SilentlyContinue)) {
        winget install --id BtbN.FFmpeg -e --source winget --accept-source-agreements --accept-package-agreements 2>$null
    }
    $env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine")
    if (Get-Command ffmpeg -ErrorAction SilentlyContinue) {
        Write-OK "ffmpeg installed: video editing enabled"
    } else {
        Write-Err "ffmpeg not installed. Video buttons stay hidden; everything else works."
        Write-Host "       Install it later from https://ffmpeg.org/download.html and restart." -ForegroundColor Yellow
    }
}

# --- Clone ---
Write-Step "Cloning repository..."
if (Test-Path $BotDir) {
    Set-Location $BotDir
    git pull origin $Branch
} else {
    git clone --branch $Branch $Repo $BotDir
    Set-Location $BotDir
}
Write-OK "Repository ready at $BotDir"

# --- Configure interactively ---
Write-Step "Configuring bot..."
Copy-Item ".env.example" ".env" -Force

$Token = Read-Host "`n  Enter BOT_TOKEN (from @BotFather)"
if ($Token) {
    (Get-Content ".env") -replace "^BOT_TOKEN=.*", "BOT_TOKEN=$Token" | Set-Content ".env"
    Write-OK "BOT_TOKEN saved"
} else {
    Write-Host "  ⚠  BOT_TOKEN is required. Edit .env manually." -ForegroundColor Yellow
}

$Admins = Read-Host "  Enter ADMIN_IDS (comma-separated, optional)"
if ($Admins) {
    (Get-Content ".env") -replace "^ADMIN_IDS=.*", "ADMIN_IDS=$Admins" | Set-Content ".env"
    Write-OK "ADMIN_IDS saved"
}

$ApiUrl = Read-Host "  Enter API_BASE_URL (press Enter for default)"
if ($ApiUrl) {
    (Get-Content ".env") -replace "^API_BASE_URL=.*", "API_BASE_URL=$ApiUrl" | Set-Content ".env"
}

$ApiKey = Read-Host "  Enter API_KEY (press Enter for default)"
if ($ApiKey) {
    (Get-Content ".env") -replace "^API_KEY=.*", "API_KEY=$ApiKey" | Set-Content ".env"
}

Write-OK "Configuration saved"

# --- Build ---
Write-Step "Building bot..."
go mod download
go build -o telegram-bot.exe .
Write-OK "Build complete: $BotDir\telegram-bot.exe"

Write-Host ""
Write-Step "One thing left in @BotFather"
Write-Host "  Run  /setinline  and give it a placeholder text." -ForegroundColor Yellow
Write-Host "  Inline mode cannot be enabled over the Bot API, so without this"
Write-Host "  @yourbot <query> returns nothing. Everything else works regardless."

# --- Start ---
if (-not $Token) {
    Write-Host "`n  ⚠  Set BOT_TOKEN in $BotDir\.env and run manually: cd $BotDir && .\telegram-bot.exe" -ForegroundColor Yellow
    exit
}

Write-Host "`n  Starting bot... Press Ctrl+C to stop`n" -ForegroundColor Green
Set-Location $BotDir
& ".\telegram-bot.exe"
