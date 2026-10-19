package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Config holds all configuration parameters for the bot
type Config struct {
	BotToken               string
	AdminIDs               []int64
	DBPath                 string
	DownloadDir            string
	MaxConcurrentDownloads int
	TelegramUploadLimitMB  int64
	FreeDailyQuota         int
	RateLimitCooldownSec   int
	YtDlpPath              string
	FFmpegPath             string
	LogLevel               string
	adminMap               map[int64]struct{}
	mu                     sync.RWMutex
}

// LoadConfig loads configuration from environment variables and optional .env file
func LoadConfig() (*Config, error) {
	// Try loading .env file if it exists
	loadDotEnv(".env")

	botToken := getEnv("BOT_TOKEN", "")
	adminIDsStr := getEnv("ADMIN_IDS", "")
	dbPath := getEnv("DB_PATH", "data/bot.db")
	downloadDir := getEnv("DOWNLOAD_DIR", "downloads")
	maxConcurrent := getEnvInt("MAX_CONCURRENT_DOWNLOADS", 5)
	uploadLimitMB := int64(getEnvInt("TELEGRAM_UPLOAD_LIMIT_MB", 50))
	freeDailyQuota := getEnvInt("FREE_DAILY_QUOTA", 5)
	rateLimitSec := getEnvInt("RATE_LIMIT_COOLDOWN_SEC", 3)
	ytDlpPath := getEnv("YTDLP_PATH", "yt-dlp")
	ffmpegPath := getEnv("FFMPEG_PATH", "ffmpeg")
	logLevel := getEnv("LOG_LEVEL", "INFO")

	adminIDs := parseAdminIDs(adminIDsStr)
	adminMap := make(map[int64]struct{}, len(adminIDs))
	for _, id := range adminIDs {
		adminMap[id] = struct{}{}
	}

	cfg := &Config{
		BotToken:               botToken,
		AdminIDs:               adminIDs,
		DBPath:                 dbPath,
		DownloadDir:            downloadDir,
		MaxConcurrentDownloads: maxConcurrent,
		TelegramUploadLimitMB:  uploadLimitMB,
		FreeDailyQuota:         freeDailyQuota,
		RateLimitCooldownSec:   rateLimitSec,
		YtDlpPath:              ytDlpPath,
		FFmpegPath:             ffmpegPath,
		LogLevel:               logLevel,
		adminMap:               adminMap,
	}

	return cfg, nil
}

// IsAdmin checks if a given Telegram user ID is an admin
func (c *Config) IsAdmin(userID int64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.adminMap[userID]
	return ok
}

func parseAdminIDs(s string) []int64 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func loadDotEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			v = strings.Trim(v, `"'`)
			if _, exists := os.LookupEnv(k); !exists {
				os.Setenv(k, v)
			}
		}
	}
}
