package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
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

// applyEnv makes every value in the .env file a real environment variable so
// that config.json lookups preferring the environment agree that .env wins.
// Blank values are skipped: an empty AI_KEY line should not erase a key the
// process environment already provides.
func applyEnv(env map[string]string) {
	for k, v := range env {
		if v != "" {
			os.Setenv(k, v)
		}
	}
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

// tuneMemory keeps the process inside the free-instance RAM budget. The default
// GOGC of 100 lets the heap double past what a 512MB container can hold, and
// media downloads are the only large allocations in the process, so a soft
// limit plus an aggressive GC target is what keeps RSS flat.
func tuneMemory(env map[string]string) {
	limitMB := 400
	if v := getEnv(env, "MEM_LIMIT_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limitMB = n
		}
	}
	gcPercent := 50
	if v := getEnv(env, "GC_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			gcPercent = n
		}
	}
	debug.SetMemoryLimit(int64(limitMB) << 20)
	debug.SetGCPercent(gcPercent)
	log.Printf("🧠 Memory: soft limit %dMB, GC target %d%%", limitMB, gcPercent)
}

const maxUpdateWorkers = 32

func main() {
	env := loadEnv(".env")

	// Every value in .env becomes a real environment variable, so that a
	// config.json lookup which prefers the environment agrees that .env wins.
	// Forwarding three named variables by hand meant AI_KEY, which config.json
	// deliberately leaves blank so no secret is committed, never reached the
	// agent at all and it stayed silent with no clue why.
	applyEnv(env)

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

	cfg.LoadAdminFromEnv()

	if cfg.AdminIDs == nil {
		cfg.AdminIDs = []int64{}
	}

	// A relative path keeps `go run main.go` self-contained: the data lands beside
	// the source instead of depending on a system directory the process may not be
	// allowed to create. DB_PATH overrides it for Docker and for deploys that
	// mount a volume.
	dbPath := getEnv(env, "DB_PATH")
	if dbPath == "" {
		dbPath = "./data/bot.db"
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Failed to create bot: %v", err)
	}

	bot.Debug = false
	bot.Client = &http.Client{Timeout: 120 * time.Second}

	tuneMemory(env)
	logEffectiveConfig(env, cfg)

	handlers.ConfigureMediaTools(cfg.Tools.Media.FFmpegPath)

	store := session.NewStore(dbPath)
	defer store.Close()
	store.Cleanup()

	handler := handlers.New(bot, cfg, store, token)
	// Inline mode is enabled here rather than left to BotFather, because a query
	// that never reaches the bot is indistinguishable from a broken feature.
	handler.EnableInline()
	handler.StartDigest()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	// Without this, Telegram applies a default list that leaves out
	// channel_post and inline_query. The bot then never sees a single message
	// posted in a channel, which is why /channels always came back empty, and
	// inline queries never arrive at all.
	u.AllowedUpdates = []string{
		"message",
		"edited_message",
		"channel_post",
		"edited_channel_post",
		"inline_query",
		"chosen_inline_result",
		"callback_query",
		"my_chat_member",
		"chat_member",
	}

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

	registerCommands(bot, cfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			handler.FireReminders()
		}
	}()

	go serveHealth()

	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			store.Cleanup()
		}
	}()

	go func() {
		<-sigCh
		log.Println("🛑 Shutting down...")
		bot.StopReceivingUpdates()
		store.Close()
		os.Exit(0)
	}()

	workers := make(chan struct{}, maxUpdateWorkers)

	for update := range updates {
		select {
		case workers <- struct{}{}:
		case <-time.After(3 * time.Second):
			log.Printf("⚠️ update dropped, %d handlers still busy (goroutines: %d)", maxUpdateWorkers, runtime.NumGoroutine())
			continue
		}
		go func(u tgbotapi.Update) {
			defer func() { <-workers }()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("🔥 Panic recovered: %v", r)
				}
			}()
			if u.Message != nil && u.Message.IsCommand() {
				logUpdate(u.Message.Chat, u.Message.From, "cmd", u.Message.Command())
				handler.HandleCommand(u)
			} else if u.Message != nil && u.Message.NewChatMembers != nil {
				logUpdate(u.Message.Chat, u.Message.From, "new_members", "")
				handler.HandleNewMembers(u)
			} else if u.CallbackQuery != nil {
				logUpdate(u.CallbackQuery.Message.Chat, u.CallbackQuery.From, "cb", u.CallbackQuery.Data)
				handler.HandleCallback(u)
			} else if u.InlineQuery != nil {
				handler.HandleInline(u)
			} else if u.Message != nil {
				logUpdate(u.Message.Chat, u.Message.From, "msg", "")
				handler.HandleMessage(u)
			} else if u.ChannelPost != nil {
				logUpdate(u.ChannelPost.Chat, nil, "channel_post", "")
				handler.HandleChannelPost(u)
			}
		}(update)
	}
}

// registerCommands publishes config.json commands to Telegram so they show up in
// the client's slash menu. tgbotapi v5 has no SetMyCommands wrapper, so the raw
// endpoint is used. A failure here only affects discoverability, so it is logged
// and swallowed rather than taking the bot down.
func registerCommands(bot *tgbotapi.BotAPI, cfg *config.Config) {
	payload := make([]map[string]string, 0, len(cfg.Commands))
	for name, cmd := range cfg.Commands {
		if !cmd.Enabled {
			continue
		}
		if !validCommandName(name) {
			log.Printf("⚠️ Command %q is not a valid Telegram command name, skipping it", name)
			continue
		}
		desc := cmd.Description
		if desc == "" {
			desc = name
		}
		payload = append(payload, map[string]string{
			"command":     name,
			"description": clip(desc, 256),
		})
	}
	if len(payload) == 0 {
		return
	}
	sort.Slice(payload, func(i, j int) bool { return payload[i]["command"] < payload[j]["command"] })

	// Telegram caps the list at 100 entries.
	if len(payload) > 100 {
		log.Printf("⚠️ %d commands configured, publishing the first 100", len(payload))
		payload = payload[:100]
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Printf("⚠️ Could not encode command list: %v", err)
		return
	}

	// Clear first so commands removed from config.json disappear from clients.
	if _, err := bot.MakeRequest("deleteMyCommands", nil); err != nil {
		log.Printf("⚠️ Could not clear command list: %v", err)
	}
	if _, err := bot.MakeRequest("setMyCommands", tgbotapi.Params{"commands": string(encoded)}); err != nil {
		log.Printf("⚠️ Could not publish command list: %v", err)
		return
	}
	log.Printf("✅ Published %d commands to Telegram", len(payload))
}

// validCommandName enforces Telegram's 1-32 character lowercase letter, digit
// and underscore rule.
func validCommandName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

// clip truncates to at most max runes.
func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func logUpdate(chat *tgbotapi.Chat, user *tgbotapi.User, kind, detail string) {
	who := "?"
	var id int64
	if user != nil {
		who = user.UserName
		if who == "" {
			who = user.FirstName
		}
		id = user.ID
	}
	title := chat.Title
	if title == "" {
		title = chat.Type
	}
	if detail != "" {
		log.Printf("→ [%s] %s@%d /%s", title, who, id, detail)
		return
	}
	log.Printf("→ [%s] %s@%d", title, who, id)
}

// logEffectiveConfig reports which endpoint the bot will actually call. An env
// var silently overrides config.json, so a stale placeholder in .env takes the
// whole bot down with an unresolvable host; this makes that visible at boot
// instead of in a per-request error hours later.
func logEffectiveConfig(env map[string]string, cfg *config.Config) {
	fromEnv := func(key, resolved string) string {
		if v := getEnv(env, key); v != "" && v != resolved {
			return " (from " + key + ")"
		}
		return ""
	}

	base := cfg.EffectiveApiBaseURL()
	aiBase := cfg.EffectiveAiBaseURL()

	log.Printf("🌐 Media API: %s%s", base, fromEnv("API_BASE_URL", cfg.ApiBaseURL))
	if aiBase != "" {
		state := "off"
		if cfg.AiReady() {
			state = cfg.AiName()
		}
		log.Printf("🤖 %s API: %s%s [%s]", cfg.AiName(), aiBase, fromEnv("AI_BASE_URL", cfg.AI.ApiBaseURL), state)
	} else {
		log.Printf("🤖 AI agent: not configured")
	}
	if strings.Contains(base, "api.example.com") || strings.Contains(aiBase, "api.example.com") {
		log.Printf("⚠️ An endpoint still points at api.example.com, the placeholder from .env.example — every API call will fail until it is removed")
	}
	// Warn when the agent will actually be silent. The old check asked
	// config.json for a key that is intentionally blank there, so it never fired
	// for the real case of AI_KEY missing from .env.
	if !cfg.AI.Enabled {
		log.Printf("ℹ️ AI agent is disabled in config.json; it will stay silent")
	} else if cfg.EffectiveAiKey() == "" {
		log.Printf("⚠️ AI agent has no key (set AI_KEY in .env); every message will get \"could not answer\"")
	}
}

// serveHealth answers the HTTP health checks that PaaS hosts (Northflank, Koyeb,
// Render, Fly) require. The bot itself only long-polls Telegram, so without a
// listener the host sees a refused connection and terminates the service.
// Port comes from $PORT, which hosts set automatically; default 8080.
func serveHealth() {
	port := getEnv(map[string]string{}, "PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"ok"}`)
	}
	mux.HandleFunc("/health", handler)
	mux.HandleFunc("/healthz", handler)
	mux.HandleFunc("/", handler)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("health server error: %v", err)
	}
}
