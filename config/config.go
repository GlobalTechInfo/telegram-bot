package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Button struct {
	Label  string `json:"label"`
	Icon   string `json:"icon"`
	Action string `json:"action"`
}

type MainMenu struct {
	Cols    int      `json:"cols"`
	Buttons []Button `json:"buttons"`
}

type Theme struct {
	PrimaryEmoji string `json:"primaryEmoji"`
	Separator    string `json:"separator"`
}

type ConfirmButtons struct {
	YesLabel  string `json:"yesLabel"`
	NoLabel   string `json:"noLabel"`
	YesAction string `json:"yesAction"`
	NoAction  string `json:"noAction"`
}

type CommandConfig struct {
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
}

type UI struct {
	Prefix         string         `json:"prefix"`
	MainMenu       MainMenu       `json:"mainMenu"`
	ConfirmButtons ConfirmButtons `json:"confirmButtons"`
	Theme          Theme          `json:"theme"`
}

func (c *Config) Prefix() string {
	if c.UI.Prefix == "" {
		return c.Bot.Name
	}
	return c.UI.Prefix
}

type Localization struct {
	DefaultLanguage    string   `json:"defaultLanguage"`
	SupportedLanguages []string `json:"supportedLanguages"`
}

type Features struct {
	InlineMode     bool `json:"inlineMode"`
	UserTracking   bool `json:"userTracking"`
	FeedbackSystem bool `json:"feedbackSystem"`
	Polls          bool `json:"polls"`
}

type BotConfig struct {
	Name            string `json:"name"`
	Username        string `json:"username"`
	Description     string `json:"description"`
	Version         string `json:"version"`
	Photo           string `json:"photo"`
	StartupMessage  string `json:"startupMessage"`
}

type Owner struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	ID       int64  `json:"id"`
}

type Config struct {
	Bot          BotConfig                  `json:"bot"`
	Owner        Owner                      `json:"owner"`
	Timezone     string                     `json:"timezone"`
	Commands     map[string]CommandConfig   `json:"commands"`
	AdminIDs     []int64                    `json:"adminIds"`
	Features     Features                   `json:"features"`
	Localization Localization               `json:"localization"`
	UI           UI                         `json:"ui"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Timezone == "" {
		cfg.Timezone = "UTC"
	}

	return &cfg, nil
}

func (c *Config) IsAdmin(userID int64) bool {
	for _, id := range c.AdminIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (c *Config) IsCommandEnabled(name string) bool {
	cmd, ok := c.Commands[name]
	if !ok {
		return true
	}
	return cmd.Enabled
}

func (c *Config) LoadAdminFromEnv() {
	envIDs := os.Getenv("ADMIN_IDS")
	if envIDs == "" {
		return
	}
	parts := strings.Split(envIDs, ",")
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err == nil {
			c.AdminIDs = append(c.AdminIDs, id)
		}
	}
}
