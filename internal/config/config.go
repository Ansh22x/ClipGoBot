package config

import (
	"bufio"
	"fmt"
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
	TelegramAPIBase        string
	PotProviderURL         string
	FreeDailyQuota         int
	RateLimitCooldownSec   int
	YtDlpPath              string
	FFmpegPath             string
	LogLevel               string
	ProxyURL               string
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
	maxConcurrent := getEnvInt("MAX_CONCURRENT_DOWNLOADS", 4)
	tgAPIBase := getEnv("TG_API_BASE", getEnv("TELEGRAM_API_BASE", "http://tg-bot-api:8081"))
	potURL := getEnv("POT_PROVIDER_URL", getEnv("BGUTIL_URL", "http://bgutil:4416"))

	// Default to 1900MB for local Bot API, or 50MB for official cloud API
	defaultUploadMB := 1900
	if tgAPIBase == "https://api.telegram.org" || strings.Contains(tgAPIBase, "api.telegram.org") {
		defaultUploadMB = 50
	}
	uploadLimitMB := int64(getEnvInt("TELEGRAM_UPLOAD_LIMIT_MB", defaultUploadMB))

	freeDailyQuota := getEnvInt("FREE_DAILY_QUOTA", 5)
	rateLimitSec := getEnvInt("RATE_LIMIT_COOLDOWN_SEC", 3)
	ytDlpPath := getEnv("YTDLP_PATH", "yt-dlp")
	ffmpegPath := getEnv("FFMPEG_PATH", "ffmpeg")
	logLevel := getEnv("LOG_LEVEL", "INFO")
	proxyURL := getEnv("YTDLP_PROXY", getEnv("PROXY", getEnv("HTTP_PROXY", "")))

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
		TelegramAPIBase:        tgAPIBase,
		PotProviderURL:         potURL,
		FreeDailyQuota:         freeDailyQuota,
		RateLimitCooldownSec:   rateLimitSec,
		YtDlpPath:              ytDlpPath,
		FFmpegPath:             ffmpegPath,
		LogLevel:               logLevel,
		ProxyURL:               proxyURL,
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

// GetProxyURL thread-safely returns the current proxy URL
func (c *Config) GetProxyURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ProxyURL
}

// SetProxyURL thread-safely updates the proxy URL and persists to .env
func (c *Config) SetProxyURL(proxy string) {
	c.mu.Lock()
	c.ProxyURL = strings.TrimSpace(proxy)
	c.mu.Unlock()

	updateEnvVar(".env", "YTDLP_PROXY", c.ProxyURL)
}

func updateEnvVar(envPath, key, val string) {
	lines := []string{}
	found := false

	if file, err := os.Open(envPath); err == nil {
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, key+"=") {
				if val != "" {
					lines = append(lines, fmt.Sprintf("%s=%s", key, val))
				}
				found = true
			} else {
				lines = append(lines, line)
			}
		}
		file.Close()
	}

	if !found && val != "" {
		lines = append(lines, fmt.Sprintf("%s=%s", key, val))
	}

	_ = os.WriteFile(envPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
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
