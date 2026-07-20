package keyboards

import (
	"fmt"

	"telegram-bot/config"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func MainMenu(cfg *config.Config, lang string) tgbotapi.InlineKeyboardMarkup {
	menu := cfg.UI.MainMenu
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for _, btn := range menu.Buttons {
		if cmd, ok := cfg.Commands[btn.Action]; ok && !cmd.Enabled {
			continue
		}
		label := btn.Icon + " " + btn.Label
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(label, btn.Action))
		if len(row) >= menu.Cols {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func Settings(lang string, notificationsOn bool) tgbotapi.InlineKeyboardMarkup {
	notifLabel := localization.Get("notifications", lang)
	if notificationsOn {
		notifLabel = "🔔 " + localization.Get("notifications", lang) + " [ON]"
	} else {
		notifLabel = "🔕 " + localization.Get("notifications", lang) + " [OFF]"
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("language", lang), "settings_language"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(notifLabel, "settings_notifications"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func SettingsNotifications(lang string, current bool) tgbotapi.InlineKeyboardMarkup {
	label := "🔕 Disable Notifications"
	action := "notif_off"
	if !current {
		label = "🔔 Enable Notifications"
		action = "notif_on"
	}
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, action),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func LanguagePicker(currentLang string) tgbotapi.InlineKeyboardMarkup {
	langs := localization.SupportedLanguages()
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for _, l := range langs {
		prefix := ""
		if l == currentLang {
			prefix = "✅ "
		}
		label := prefix + localization.LanguageName(l)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(label, "set_lang:"+l))
		if len(row) >= 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", currentLang), "back"),
	))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func Confirm(cfg *config.Config, lang, prompt string) tgbotapi.InlineKeyboardMarkup {
	cb := cfg.UI.ConfirmButtons
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(cb.YesLabel, cb.YesAction),
			tgbotapi.NewInlineKeyboardButtonData(cb.NoLabel, cb.NoAction),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func ConfirmWithBack(cfg *config.Config, lang string) tgbotapi.InlineKeyboardMarkup {
	cb := cfg.UI.ConfirmButtons
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(cb.YesLabel, cb.YesAction),
			tgbotapi.NewInlineKeyboardButtonData(cb.NoLabel, cb.NoAction),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func Back(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func YtFormatPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ytFormatMp3", lang), "yt_fmt:mp3"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ytFormat360", lang), "yt_fmt:360"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ytFormat720", lang), "yt_fmt:720"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ytFormat1080", lang), "yt_fmt:1080"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func TtFormatPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ttFormatWM", lang), "tt_fmt:wm"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ttFormatNoWM", lang), "tt_fmt:nowm"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ttFormatHD", lang), "tt_fmt:nowm_hd"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("ttFormatMusic", lang), "tt_fmt:music"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func FbQualityPicker(qualities []interface{}, lang string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for i, q := range qualities {
		item, ok := q.(map[string]interface{})
		if !ok {
			continue
		}
		label, _ := item["quality"].(string)
		if label == "" {
			label = fmt.Sprintf("Quality %d", i)
		}

		row = append(row, tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("fb_fmt:%d", i)))
		if len(row) >= 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func SnapPicker(count int, lang string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for i := 0; i < count; i++ {
		label := fmt.Sprintf("👻 Snap %d", i+1)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("sc_fmt:%d", i)))
		if len(row) >= 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func MediaPicker(count int, label string, prefix string, lang string) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for i := 0; i < count; i++ {
		btnLabel := fmt.Sprintf("%s %s %d", label, prefix, i+1)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(btnLabel, fmt.Sprintf("%s_fmt:%d", prefix, i)))
		if len(row) >= 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	rows = append(rows, tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
	))

	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func BingModePicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingSearch", lang), "bing_search"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingImages", lang), "bing_images"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func BingCountPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount1", lang), "bing_cnt:1"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount5", lang), "bing_cnt:5"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount10", lang), "bing_cnt:10"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCountAll", lang), "bing_cnt:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func SearchMenu(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("searchPin", lang), "search_pin"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("searchSticker", lang), "search_sticker"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("searchImgur", lang), "search_imgur"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("searchYt", lang), "search_yt"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingSearch", lang), "bing_search"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingImages", lang), "bing_images"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func PinSearchCountPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("1", "pin_search:1"),
			tgbotapi.NewInlineKeyboardButtonData("5", "pin_search:5"),
			tgbotapi.NewInlineKeyboardButtonData("10", "pin_search:10"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCountAll", lang), "pin_search:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func StickerSearchCountPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount1", lang), "sticker_search:1"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount5", lang), "sticker_search:5"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount10", lang), "sticker_search:10"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCountAll", lang), "sticker_search:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func ImgurSearchCountPicker(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount1", lang), "imgur_search:1"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount5", lang), "imgur_search:5"),
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCount10", lang), "imgur_search:10"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("bingCountAll", lang), "imgur_search:0"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func TextMakerMenu(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("textPro", lang), "textpro"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("backToMenu", lang), "back"),
		),
	)
}

func Close(lang string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(localization.Get("close", lang), "close"),
		),
	)
}
