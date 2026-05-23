package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramToken    string
	TelegramLocalAPI string
	StorageChannelID int64

	RedisAddr     string
	RedisPassword string
	RedisDB       int
	RedisPoolSize int

	WorkerCount   int
	MaxFileSizeMB int

	CacheTTL         time.Duration
	HotCacheTTL      time.Duration // TTL для видео в топ-3 истории пользователей
	RateLimitPerHour int
	LogLevel         string

	// Авторизация для платформ, требующих логина
	CookiesFile string // путь к cookies.txt в Netscape-формате (для VK, TikTok и др.)
	VKUsername  string // логин ВКонтакте (альтернатива cookies)
	VKPassword  string // пароль ВКонтакте

	// Администраторы бота (могут использовать /stats, /addadmin и т.д.)
	AdminIDs []int64 // из ADMIN_IDS в .env, дополнительно хранятся в PostgreSQL

	// PostgreSQL
	DatabaseURL string // DATABASE_URL (postgres://user:pass@host:5432/db?sslmode=disable)
}

func Load() *Config {
	return &Config{
		TelegramToken:    mustEnv("TELEGRAM_TOKEN"),
		TelegramLocalAPI: getEnv("TELEGRAM_LOCAL_API", "http://telegram-bot-api:8081"),
		StorageChannelID: mustInt64("STORAGE_CHANNEL_ID"),
		RedisAddr:        getEnv("REDIS_ADDR", "redis:6379"),
		RedisPassword:    getEnv("REDIS_PASSWORD", ""),
		RedisDB:          getInt("REDIS_DB", 0),
		RedisPoolSize:    getInt("REDIS_POOL_SIZE", 20),
		WorkerCount:      getInt("WORKER_COUNT", 5),
		MaxFileSizeMB:    getInt("MAX_FILE_SIZE_MB", 500),
		CacheTTL:         time.Duration(getInt("CACHE_TTL_HOURS", 24)) * time.Hour,
		HotCacheTTL:      time.Duration(getInt("HOT_CACHE_TTL_HOURS", 168)) * time.Hour, // 168h = 7 дней для топ-3
		RateLimitPerHour: getInt("RATE_LIMIT_PER_HOUR", 5),
		LogLevel:         getEnv("LOG_LEVEL", "info"),
		CookiesFile:      getEnv("COOKIES_FILE", ""),
		VKUsername:       getEnv("VK_USERNAME", ""),
		VKPassword:       getEnv("VK_PASSWORD", ""),
		AdminIDs:         parseInt64List(getEnv("ADMIN_IDS", "")),
		DatabaseURL:      mustEnv("DATABASE_URL"),
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %q is not set", key))
	}
	return v
}

func mustInt64(key string) int64 {
	s := mustEnv(key)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("env var %q must be int64: %v", key, err))
	}
	return v
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func parseInt64List(s string) []int64 {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
