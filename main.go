package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"telegram-bot/config"
	"telegram-bot/handlers"
	"telegram-bot/session"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func loadEnv(path string) map[string]string {
	env := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return env
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"'")
		env[key] = val
	}
	return env
}

func getEnv(env map[string]string, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if v, ok := env[key]; ok {
		return v
	}
	return ""
}

func main() {
	env := loadEnv(".env")

	cfg, err := config.Load("config.json")
	if err != nil {
		log.Fatalf("❌ Failed to load config: %v", err)
	}

	token := getEnv(env, "BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN is required. Set it in .env file or as environment variable.")
	}
	if len(token) > 8 {
		log.Printf("🔑 Token loaded: %s...%s", token[:4], token[len(token)-4:])
	}

	if adminIDs := getEnv(env, "ADMIN_IDS"); adminIDs != "" {
		os.Setenv("ADMIN_IDS", adminIDs)
	}
	cfg.LoadAdminFromEnv()

	if cfg.AdminIDs == nil {
		cfg.AdminIDs = []int64{}
	}

	if apiBase := getEnv(env, "API_BASE_URL"); apiBase != "" {
		os.Setenv("API_BASE_URL", apiBase)
	}
	if apiKey := getEnv(env, "API_KEY"); apiKey != "" {
		os.Setenv("API_KEY", apiKey)
	}

	dbPath := getEnv(env, "DB_PATH")
	if dbPath == "" {
		exe, _ := os.Executable()
		dbPath = filepath.Dir(exe) + "/bot.db"
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Failed to create bot: %v", err)
	}

	bot.Debug = false

	store := session.NewStore(dbPath)
	defer store.Close()

	handler := handlers.New(bot, cfg, store)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	log.Printf("🤖 %s is running!", cfg.Bot.Username)
	log.Printf("📋 Version: %s", cfg.Bot.Version)
	log.Printf("🗄️  Database: %s", dbPath)
	log.Printf("👥 Admin IDs: %v", cfg.AdminIDs)
	log.Printf("🌐 Default language: %s", cfg.Localization.DefaultLanguage)
	log.Printf("📦 Commands loaded: %d", len(cfg.Commands))
	fmt.Println("────────────────────────────────────────")

	greeting := fmt.Sprintf("🚀 *%s is online!*\n\nVersion: %s\nTime: %s\n\nUse /start to begin.",
		cfg.Bot.Name, cfg.Bot.Version, time.Now().Format("2006-01-02 15:04:05 MST"))

	recipients := []int64{}
	if cfg.Owner.ID != 0 {
		recipients = append(recipients, cfg.Owner.ID)
	}
	for _, id := range cfg.AdminIDs {
		found := false
		for _, r := range recipients {
			if r == id {
				found = true
				break
			}
		}
		if !found {
			recipients = append(recipients, id)
		}
	}

	for _, chatID := range recipients {
		msg := tgbotapi.NewMessage(chatID, greeting)
		msg.ParseMode = "Markdown"
		if _, err := bot.Send(msg); err != nil {
			log.Printf("ℹ️ Greeting to %d skipped (user must start the bot first)", chatID)
		} else {
			log.Printf("✅ Greeting sent to %d", chatID)
		}
	}

	if len(recipients) == 0 {
		log.Println("ℹ️ No owner ID or admin IDs configured — no startup greeting sent.")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("🛑 Shutting down...")
		bot.StopReceivingUpdates()
		store.Close()
		os.Exit(0)
	}()

	for update := range updates {
		go func(u tgbotapi.Update) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🔥 Panic recovered: %v", r)
				}
			}()
			if u.Message != nil && u.Message.IsCommand() {
				handler.HandleCommand(u)
			} else if u.CallbackQuery != nil {
				handler.HandleCallback(u)
			} else if u.InlineQuery != nil {
				handler.HandleInline(u)
			} else if u.Message != nil {
				handler.HandleMessage(u)
			}
		}(update)
	}
}
