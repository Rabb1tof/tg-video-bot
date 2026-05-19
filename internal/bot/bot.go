package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/config"
	"github.com/tg-video-bot/bot/internal/worker"
)

// Handler processes all Telegram updates.
type Handler struct {
	api       *tgbotapi.BotAPI
	cfg       *config.Config
	cache     *cache.Cache
	pool      *worker.Pool
	log       *slog.Logger
	startedAt time.Time
}

func NewHandler(api *tgbotapi.BotAPI, cfg *config.Config, c *cache.Cache, pool *worker.Pool, log *slog.Logger) *Handler {
	return &Handler{api: api, cfg: cfg, cache: c, pool: pool, log: log, startedAt: time.Now()}
}

// Dispatch routes a Telegram update to the correct handler.
func (h *Handler) Dispatch(update tgbotapi.Update) {
	switch {
	case update.InlineQuery != nil:
		h.handleInline(update.InlineQuery)
	case update.ChosenInlineResult != nil:
		h.handleChosenInlineResult(update.ChosenInlineResult)
	case update.Message != nil && update.Message.IsCommand():
		h.handleCommand(update.Message)
	case update.Message != nil:
		h.handleMessage(update.Message)
	}
}

func (h *Handler) handleCommand(msg *tgbotapi.Message) {
	var text string
	switch msg.Command() {
	case "start":
		text = fmt.Sprintf(`👋 <b>Привет, %s!</b>

Я скачиваю видео из YouTube, Instagram, TikTok и <a href="https://github.com/yt-dlp/yt-dlp/blob/master/supportedsites.md">тысяч других сайтов</a>.

<b>Как использовать:</b>
В любом чате напиши <code>@%s https://...</code> и выбери результат. Видео появится от твоего имени с пометкой <i>via @%s</i>.

<b>Первый запрос</b> новой ссылки занимает 30–120 сек — я уведомлю тебя в ЛС. Повторный запрос той же ссылки — мгновенно из кэша.

<b>Команды:</b>
/start — это сообщение
/help — справка
/stats — статистика
/history — история скачанных видео
/clear — очистить истории`, msg.From.FirstName, h.api.Self.UserName, h.api.Self.UserName)
	case "help":
		text = `<b>Справка:</b>

📱 <b>В личке:</b> просто отправь мне ссылку на видео, и я его скачаю

🔍 <b>Инлайн-режим:</b> напиши @VidqueBot и ссылку в любом чате

<b>Команды:</b>
/start — приветствие
/help — эта справка
/stats — статистика бота
/history — история скачанных видео
/clear — очистить историю`
	case "stats":
		cached := int64(0)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if c, err := h.cache.CountCached(ctx); err == nil {
			cached = c
		}
		text = fmt.Sprintf(`📊 Статистика:

📦 Видео в кэше: <b>%d</b>
👷 Воркеров: <b>%d</b>
📋 Очередь: <b>%d</b>`, cached, h.cfg.WorkerCount, h.pool.QueueLen())
	case "history":
		h.handleHistoryCommand(msg)
		return
	case "clear":
		h.handleClearCommand(msg)
		return
	default:
		text = "Неизвестная команда. Напиши /help для справки."
	}
	h.reply(msg, text)
}

func (h *Handler) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)

	// Normalize URL if it looks like a URL without protocol
	normalizedText := text
	if strings.Contains(text, ".") && !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		normalizedText = "https://" + text
	}

	// If it's a URL, enqueue for download
	if strings.HasPrefix(normalizedText, "http://") || strings.HasPrefix(normalizedText, "https://") {
		if !isValidURL(normalizedText) {
			h.reply(msg, "❌ Неверный формат ссылки")
			return
		}

		ctx := context.Background()

		// Check if video is in cache
		fileID, err := h.cache.GetFileID(ctx, normalizedText)
		if err != nil {
			h.log.Error("cache get error", "err", err)
		}

		if fileID != "" {
			// Продлеваем TTL если видео в топ-3 истории пользователя
			go h.cache.RefreshFileIDTTL(ctx, normalizedText, msg.From.ID)

			// Send cached video immediately
			videoMsg := tgbotapi.NewVideo(msg.Chat.ID, tgbotapi.FileID(fileID))
			videoMsg.Caption = "📹 Видео из кэша"
			if _, err := h.api.Send(videoMsg); err != nil {
				h.log.Error("send cached video failed", "err", err)
				h.reply(msg, "❌ Не удалось отправить видео")
			} else {
				h.reply(msg, "✅ Видео из кэша отправлено!")
			}
			return
		}

		// Enqueue for download
		if !h.pool.IsInProgress(normalizedText) {
			if ok := h.pool.Enqueue(worker.Job{URL: normalizedText, UserID: msg.From.ID}); ok {
				h.reply(msg, "⏳ Скачиваю видео... Пришлю в личку когда готово.")
				h.log.Info("download job enqueued from PM", "url", normalizedText, "user_id", msg.From.ID)
			} else {
				h.reply(msg, "❌ Очередь переполнена")
			}
		} else {
			h.reply(msg, "⏳ Это видео уже скачивается...")
		}
		return
	}

	// Default message
	replyText := fmt.Sprintf(
		"👆 Отправь мне ссылку на видео, и я его скачаю.\n\nИли используй инлайн-режим: напиши <code>@%s https://...</code> в любом чате.",
		h.api.Self.UserName)
	h.reply(msg, replyText)
}

func (h *Handler) handleChosenInlineResult(result *tgbotapi.ChosenInlineResult) {
	log := h.log.With("result_id", result.ResultID, "inline_message_id", result.InlineMessageID, "user_id", result.From.ID)

	// Check if this result ID corresponds to a download job
	if h.pool.SetInlineMessageID(result.ResultID, result.InlineMessageID) {
		log.Info("inline message ID stored for editing")
	}
}

func (h *Handler) handleHistoryCommand(msg *tgbotapi.Message) {
	ctx := context.Background()
	history, err := h.cache.GetVideoHistory(ctx, msg.From.ID)
	if err != nil {
		h.log.Error("get video history failed", "err", err)
		h.reply(msg, "❌ Не удалось получить историю")
		return
	}

	if len(history) == 0 {
		h.reply(msg, "📭 История пуста. Отправь мне ссылки на видео!")
		return
	}

	text := fmt.Sprintf("📜 История скачанных видео (%d последних):\n\n", len(history))
	for i, video := range history {
		text += fmt.Sprintf("%d. %s\n", i+1, video.Title)
	}

	h.reply(msg, text)
}

func (h *Handler) handleClearCommand(msg *tgbotapi.Message) {
	ctx := context.Background()
	if err := h.cache.ClearVideoHistory(ctx, msg.From.ID); err != nil {
		h.log.Error("clear video history failed", "err", err)
		h.reply(msg, "❌ Не удалось очистить историю")
		return
	}

	h.reply(msg, "✅ История очищена")
}

func (h *Handler) reply(msg *tgbotapi.Message, text string) {
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	reply.ParseMode = "HTML"
	reply.DisableWebPagePreview = true
	if _, err := h.api.Send(reply); err != nil {
		h.log.Error("send reply failed", "err", err)
	}
}
