package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telegram-bot/keyboards"
)

// Commands are published to the Telegram slash menu and listed in the help text
// straight from config.json, so one that HandleCommand cannot route is advertised
// and then answers "unknown command". Six did for a while: textmaker, shorturl,
// news, sports, imageeffect and artistic only existed as button callbacks.
//
// This walks config.json rather than a hand-written list, so a command added
// there cannot ship unrouted.
func TestEveryConfigCommandIsRoutable(t *testing.T) {
	enabled := configCommands(t)

	b, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	for cmd := range enabled {
		_, isDownloader := dlByCmd(cmd)
		_, hasMenu := menuCommands[cmd]
		// A grouped case such as `case "promote", "admin_add":` still contains
		// `case "promote"`, so this substring is a reliable signal.
		hasCase := strings.Contains(src, `case "`+cmd+`"`)

		if isDownloader || hasMenu || hasCase {
			continue
		}
		t.Errorf("/%s is published in config.json and in the help text, but "+
			"HandleCommand cannot route it: not a downloader, not in menuCommands, "+
			"no case", cmd)
	}
}

// The menus the six new commands reach must produce a keyboard with buttons.
// A command that renders an empty keyboard is still broken, just quieter.
func TestMenuCommandsProduceButtons(t *testing.T) {
	lang := "en"
	buttons := map[string]func() int{
		"textmaker":   func() int { return len(keyboards.TextMakerMenu(lang).InlineKeyboard) },
		"shorturl":    func() int { return len(keyboards.ShortUrlMenu(lang).InlineKeyboard) },
		"news":        func() int { return len(keyboards.NewsMenu(lang).InlineKeyboard) },
		"sports":      func() int { return len(keyboards.SportsMenu(lang).InlineKeyboard) },
		"imageeffect": func() int { return len(keyboards.ImageEffectMenu(lang).InlineKeyboard) },
		"artistic":    func() int { return len(keyboards.ArtisticEffectMenu(lang).InlineKeyboard) },
	}
	for _, cmd := range []string{"textmaker", "shorturl", "news", "sports", "imageeffect", "artistic"} {
		if _, ok := menuCommands[cmd]; !ok {
			t.Errorf("/%s has no menu handler", cmd)
			continue
		}
		if n := buttons[cmd](); n == 0 {
			t.Errorf("/%s renders an empty keyboard", cmd)
		}
	}
}

// configCommands reads the enabled command set out of config.json.
func configCommands(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("..", "config.json")
	if _, err := os.Stat(path); err != nil {
		path = "config.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Commands map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for name, v := range cfg.Commands {
		if v.Enabled {
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("config.json exposed no enabled commands")
	}
	return out
}

// /define and /media were added to config.json and to ToolsMenu. The menu must
// not offer a command the handler cannot route, and the commands must be routed.
func TestNewToolsAreRoutedAndReachable(t *testing.T) {
	for _, cmd := range []string{"define", "media"} {
		if !configCommands(t)[cmd] {
			t.Errorf("/%s should be published in config.json", cmd)
		}
		if _, isDownloader := dlByCmd(cmd); isDownloader {
			t.Errorf("/%s must not shadow a downloader", cmd)
		}
	}
}
