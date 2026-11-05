package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken         string
	RedisAddr        string
	RedisPassword    string // empty = no auth
	WorkerCount      int
	TmpfsPath        string
	MaxFileSize      int64
	LocalAPIURL      string
	DonorChatIDs     []int64
	CookiesFile      string // Deprecated: use platform-specific cookies files
	TikTokCookies    string // Path to TikTok cookies file (Netscape format)
	InstagramCookies string // Path to Instagram cookies file (Netscape format)
	YouTubeCookies   string // Path to YouTube cookies file (Netscape format)
	MinFreeMemMB     int    // Minimum free memory in MB (default: 256)
	AdminChatID      int64  // Telegram chat for service reminders (0 = disabled)

	// Anti-spam and resource guards
	AllowedGroupIDs []int64 // Group chats allowed to use the bot (empty = none)
	// AllowedUserIDs: when set, private chats are limited to these Telegram
	// users. Empty = any user may DM the bot.
	AllowedUserIDs   []int64
	RateLimitPerHour int // Downloads per user per hour (0 = unlimited)
	MaxQueueSize     int // Reject new jobs beyond this queue depth (0 = no cap)
	MinFreeTmpfsMB   int // Stop downloading when tmpfs has less free space
	PlaylistMaxItems int // Cap on YouTube playlist length (0 = no cap)

	// Playlist archives: parts are written to disk (tmpfs is RAM-backed and
	// too small for multi-GB archives) and sent one by one.
	ArchiveDir        string // Directory for playlist archive parts
	ArchivePartSizeMB int    // Max size of one archive part (Telegram caps at 2 GB)
}

func Load() (*Config, error) {
	// Try to load .env file from current directory
	envPath := ".env"

	// Also try to find .env in the same directory as the binary
	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		envPathInExeDir := filepath.Join(exeDir, ".env")
		if _, err := os.Stat(envPathInExeDir); err == nil {
			envPath = envPathInExeDir
		}
	}

	// Try to load .env file (ignore error if file doesn't exist)
	godotenv.Load(envPath)

	cfg := &Config{
		BotToken:      getEnv("BOT_TOKEN", ""),
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""), // empty = no auth (backward compatible)
		// One concurrent yt-dlp+ffmpeg pipeline is the safe ceiling on a
		// 4 GB SBC: downloads land in tmpfs and merging peaks in the
		// hundreds of MB. Raise only for audio-only workloads.
		WorkerCount:      getEnvInt("WORKER_COUNT", 1),
		TmpfsPath:        getEnv("TMPFS_PATH", "/dev/shm/videos"),
		MaxFileSize:      int64(getEnvInt("MAX_FILE_SIZE_MB", 512)) * 1024 * 1024,
		LocalAPIURL:      getEnv("LOCAL_API_URL", "http://127.0.0.1:8089"),
		DonorChatIDs:     parseChatIDs(getEnv("DONOR_CHAT_IDS", "")),
		CookiesFile:      getEnv("COOKIES_FILE", ""),        // Deprecated: for backward compatibility
		TikTokCookies:    getEnv("TIKTOK_COOKIES", ""),      // Path to TikTok cookies file
		InstagramCookies: getEnv("INSTAGRAM_COOKIES", ""),   // Path to Instagram cookies file
		YouTubeCookies:   getEnv("YOUTUBE_COOKIES", ""),     // Path to YouTube cookies file
		MinFreeMemMB:     getEnvInt("MIN_FREE_MEM_MB", 256), // Minimum free memory in MB
		AdminChatID:      getEnvInt64("ADMIN_CHAT_ID", 0),   // Telegram chat for service reminders

		AllowedGroupIDs:  parseChatIDs(getEnv("ALLOWED_GROUP_IDS", "")),
		AllowedUserIDs:   parseChatIDs(getEnv("ALLOWED_USER_IDS", "")),
		RateLimitPerHour: getEnvInt("RATE_LIMIT_JOBS_PER_HOUR", 12),
		MaxQueueSize:     getEnvInt("MAX_QUEUE_SIZE", 20),
		MinFreeTmpfsMB:   getEnvInt("MIN_FREE_TMPFS_MB", 300),
		PlaylistMaxItems: getEnvInt("PLAYLIST_MAX_ITEMS", 50),

		ArchiveDir:        getEnv("ARCHIVE_DIR", "/opt/envedour-bot/archives"),
		ArchivePartSizeMB: getEnvInt("ARCHIVE_PART_SIZE_MB", 1900),
	}

	// Validate required fields
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required\n\n" +
			"Пожалуйста, установите переменную окружения BOT_TOKEN:\n" +
			"  export BOT_TOKEN=\"your_token_here\"\n\n" +
			"Или создайте файл .env со следующим содержимым:\n" +
			"  BOT_TOKEN=your_token_here\n\n" +
			"Получить токен можно у @BotFather в Telegram:\n" +
			"  1. Откройте https://t.me/BotFather\n" +
			"  2. Отправьте команду /newbot\n" +
			"  3. Следуйте инструкциям\n" +
			"  4. Скопируйте полученный токен\n\n" +
			"Подробнее см. QUICKSTART.md или README.md")
	}

	return cfg, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.ParseInt(value, 10, 64); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func parseChatIDs(s string) []int64 {
	if s == "" {
		return nil
	}

	var ids []int64
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
