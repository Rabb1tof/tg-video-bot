package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"

	"github.com/tg-video-bot/bot/internal/bot"
	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/config"
	"github.com/tg-video-bot/bot/internal/downloader"
	"github.com/tg-video-bot/bot/internal/worker"
)

func main() {
	_ = godotenv.Load() // load .env if present (dev convenience)

	cfg := config.Load()

	// ── Logger ─────────────────────────────────────────────────────────────
	logLevel := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	logger.Info("tg-video-bot starting", "version", "1.2.0")

	// ── Redis ──────────────────────────────────────────────────────────────
	redisCache := cache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, cfg.RedisPoolSize, cfg.CacheTTL, cfg.HotCacheTTL)
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := redisCache.Ping(ctx); err != nil {
			logger.Error("redis ping failed", "addr", cfg.RedisAddr, "err", err)
			os.Exit(1)
		}
	}
	logger.Info("redis connected", "addr", cfg.RedisAddr)

	// ── yt-dlp ────────────────────────────────────────────────────────────
	if !downloader.IsAvailable() {
		logger.Error("yt-dlp not found in PATH — install it first")
		os.Exit(1)
	}
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		logger.Info("yt-dlp ready", "version", downloader.Version(ctx))
		cancel()
	}
	dl := downloader.New("/tmp/tg-video-bot", cfg.MaxFileSizeMB, cfg.CookiesFile, cfg.VKUsername, cfg.VKPassword)

	// ── Telegram Bot API (local server) ────────────────────────────────────
	// The local Bot API server removes the 50 MB upload limit (up to 2 GB).
	api, err := tgbotapi.NewBotAPIWithClient(
		cfg.TelegramToken,
		cfg.TelegramLocalAPI+"/bot%s/%s",
		&http.Client{},
	)
	if err != nil {
		logger.Error("telegram bot init failed", "err", err)
		os.Exit(1)
	}
	api.Debug = cfg.LogLevel == "debug"
	logger.Info("telegram bot ready", "username", api.Self.UserName)

	// ── Graceful shutdown ──────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("shutdown signal received", "signal", sig)
		cancel()
	}()

	// ── Worker pool ────────────────────────────────────────────────────────
	pool := worker.NewPool(cfg.WorkerCount, redisCache, dl, api,
		cfg.StorageChannelID, api.Self.UserName, logger)
	pool.Start(ctx)

	// ── Register handlers & start polling ──────────────────────────────────
	handler := bot.NewHandler(api, cfg, redisCache, pool, logger)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := api.GetUpdatesChan(u)

	logger.Info("polling for updates",
		"bot", "@"+api.Self.UserName,
		"workers", cfg.WorkerCount,
		"local_api", cfg.TelegramLocalAPI,
	)

	for {
		select {
		case <-ctx.Done():
			api.StopReceivingUpdates()
			logger.Info("stopped, bye!")
			return
		case update, ok := <-updates:
			if !ok {
				return
			}
			go handler.Dispatch(update) // non-blocking dispatch
		}
	}
}
