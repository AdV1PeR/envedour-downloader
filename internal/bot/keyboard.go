package bot

import (
	"fmt"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// createMainKeyboard creates the main inline keyboard
func createMainKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⚙️ Качество", "menu_quality"),
			tgbotapi.NewInlineKeyboardButtonData("🎵 Аудио/Видео", "menu_media"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Статус", "cmd_status"),
		),
	)
}

// createQualityKeyboard creates keyboard for quality selection
func createQualityKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏆 Лучшее", "quality_best"),
			tgbotapi.NewInlineKeyboardButtonData("1080p", "quality_1080p"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("720p", "quality_720p"),
			tgbotapi.NewInlineKeyboardButtonData("480p", "quality_480p"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("360p", "quality_360p"),
			tgbotapi.NewInlineKeyboardButtonData("🎵 Аудио", "quality_audio"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("◀️ Назад", "menu_main"),
		),
	)
}

// createMediaTypeKeyboard creates keyboard for media type selection
func createMediaTypeKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🎬 Видео", "media_video"),
			tgbotapi.NewInlineKeyboardButtonData("🎵 Аудио (MP3)", "media_audio"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("◀️ Назад", "menu_main"),
		),
	)
}

// createDownloadQualityKeyboard creates keyboard for quality selection when downloading
// Uses a job ID instead of full URL to avoid Telegram's 64-byte callback data limit
func createDownloadQualityKeyboard(jobID string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏆 Лучшее", fmt.Sprintf("dl_q_best:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("1080p", fmt.Sprintf("dl_q_1080p:%s", jobID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("2K", fmt.Sprintf("dl_q_1440p:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("4K", fmt.Sprintf("dl_q_2160p:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("8K", fmt.Sprintf("dl_q_4320p:%s", jobID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("720p", fmt.Sprintf("dl_q_720p:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("480p", fmt.Sprintf("dl_q_480p:%s", jobID)),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("360p", fmt.Sprintf("dl_q_360p:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("🎵 Аудио", fmt.Sprintf("dl_q_audio:%s", jobID)),
		),
	)
}

// createPlaylistDeliveryKeyboard asks how a playlist should be delivered once
// the quality step is done.
func createPlaylistDeliveryKeyboard(jobID string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 По очереди", fmt.Sprintf("pl_seq:%s", jobID)),
			tgbotapi.NewInlineKeyboardButtonData("📦 Архивом", fmt.Sprintf("pl_zip:%s", jobID)),
		),
	)
}
