package bot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/worker"
)

func (h *Handler) handleInline(q *tgbotapi.InlineQuery) {
	rawQuery := strings.TrimSpace(q.Query)
	log := h.log.With("query_id", q.ID, "user_id", q.From.ID, "query", rawQuery)

	// Handle inline commands
	if strings.HasPrefix(rawQuery, "/") {
		h.handleInlineCommand(q, rawQuery)
		return
	}

	// If query is empty, show user's video history
	if rawQuery == "" {
		h.showVideoHistoryInline(q)
		return
	}

	// Normalize URL if missing protocol
	normalizedQuery := rawQuery
	if strings.Contains(rawQuery, ".") && !strings.HasPrefix(rawQuery, "http://") && !strings.HasPrefix(rawQuery, "https://") {
		normalizedQuery = "https://" + rawQuery
	}

	// If it's a URL, handle as before
	if !isValidURL(normalizedQuery) {
		h.answerInline(q.ID, []interface{}{invalidURLArticle()}, 10)
		return
	}

	ctx := context.Background()

	// Cache hit → instant response
	cached, err := h.cache.GetCachedVideo(ctx, normalizedQuery)
	if err != nil {
		log.Error("cache get error", "err", err)
	}
	if cached != nil {
		log.Info("cache hit", "file_id", cached.FileID)
		go func() {
			// Продлеваем TTL если видео в топ-3 истории пользователя
			h.cache.RefreshFileIDTTL(ctx, normalizedQuery, q.From.ID)
			// Добавляем в историю этого пользователя (если ещё нет)
			h.addToHistoryIfNew(ctx, q.From.ID, normalizedQuery, cached)
		}()
		result := tgbotapi.NewInlineQueryResultCachedVideo(
			"cached-"+shortHash(normalizedQuery), cached.FileID, "▶️ Видео",
		)
		h.answerInline(q.ID, []interface{}{result}, 300)
		return
	}

	// Rate limit check
	count, err := h.cache.IncrRateLimit(ctx, q.From.ID)
	if err != nil {
		log.Error("rate limit error", "err", err)
	} else if count > int64(h.cfg.RateLimitPerHour) {
		log.Info("user rate limited", "count", count)
		h.answerInline(q.ID, []interface{}{rateLimitArticle(h.cfg.RateLimitPerHour)}, 60)
		return
	}

	// Enqueue download (Pool handles deduplication internally)
	resultID := "loading-" + shortHash(normalizedQuery)
	if !h.pool.IsInProgress(normalizedQuery) {
		if ok := h.pool.Enqueue(worker.Job{URL: normalizedQuery, UserID: q.From.ID, ResultID: resultID}); ok {
			log.Info("download job enqueued")
		} else {
			log.Warn("queue full or duplicate")
		}
	} else {
		log.Info("download already in progress")
	}

	// Return loading stub — short TTL so user can retry quickly
	h.answerInline(q.ID, []interface{}{loadingArticleWithID(resultID)}, 5)
}

func (h *Handler) showVideoHistoryInline(q *tgbotapi.InlineQuery) {
	ctx := context.Background()

	// Получаем версию — меняется после каждой загрузки, что заставляет
	// Telegram запросить свежий список (result_id с новой версией = cache miss)
	ver := h.cache.GetInlineVersion(ctx, q.From.ID)

	history, err := h.cache.GetVideoHistory(ctx, q.From.ID)
	if err != nil {
		h.log.Error("get video history failed", "err", err)
		h.answerInline(q.ID, []interface{}{usageArticle(h.api.Self.UserName)}, 300)
		return
	}

	if len(history) == 0 {
		h.answerInline(q.ID, []interface{}{usageArticle(h.api.Self.UserName)}, 30)
		return
	}

	// Версия встраивается в result_id — при смене версии Telegram не найдёт
	// старые ID в своём кэше и запросит заново
	var results []interface{}
	for i, video := range history {
		if i >= 50 {
			break
		}
		result := tgbotapi.NewInlineQueryResultCachedVideo(
			fmt.Sprintf("h%d-%d", ver, i), video.FileID, video.Title,
		)
		results = append(results, result)
	}

	h.answerInline(q.ID, results, 300)
}

func (h *Handler) handleInlineCommand(q *tgbotapi.InlineQuery, command string) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		h.answerInline(q.ID, []interface{}{usageArticle(h.api.Self.UserName)}, 300)
		return
	}

	cmd := parts[0]
	var result tgbotapi.InlineQueryResultArticle

	switch cmd {
	case "/help", "/start":
		result = helpInlineArticle(h.api.Self.UserName)
	case "/stats":
		result = statsInlineArticle(h)
	case "/youtube":
		result = platformArticle("YouTube", "https://youtube.com/watch?v=...", "youtube")
	case "/tiktok":
		result = platformArticle("TikTok", "https://tiktok.com/@user/video/...", "tiktok")
	case "/instagram":
		result = platformArticle("Instagram", "https://instagram.com/p/...", "instagram")
	default:
		result = unknownCommandArticle(cmd)
	}

	h.answerInline(q.ID, []interface{}{result}, 300)
}

func (h *Handler) answerInline(queryID string, results []interface{}, cacheTime int) {
	cfg := tgbotapi.InlineConfig{
		InlineQueryID: queryID,
		Results:       results,
		CacheTime:     cacheTime,
	}
	if _, err := h.api.Request(cfg); err != nil {
		h.log.Error("answerInlineQuery failed", "query_id", queryID, "err", err)
	}
}

func usageArticle(botUsername string) tgbotapi.InlineQueryResultArticle {
	r := tgbotapi.NewInlineQueryResultArticle("usage", "📥 Скачай видео в ЛС с ботом",
		fmt.Sprintf("Отправь боту ссылку на видео в личку, а потом введи @%s здесь — видео появится в истории.", botUsername))
	r.Description = "История пуста. Скачай видео через ЛС с ботом"
	return r
}

func invalidURLArticle() tgbotapi.InlineQueryResultArticle {
	r := tgbotapi.NewInlineQueryResultArticle("invalid", "❌ Неверная ссылка",
		"Введи полный URL, например: youtube.com/watch?v=...")
	r.Description = "Нужен URL видео"
	return r
}

func rateLimitArticle(limit int) tgbotapi.InlineQueryResultArticle {
	r := tgbotapi.NewInlineQueryResultArticle("ratelimit", "🚦 Лимит исчерпан",
		fmt.Sprintf("Достигнут лимит %d видео/час. Попробуй позже.", limit))
	r.Description = fmt.Sprintf("Максимум %d видео в час", limit)
	return r
}

func isValidURL(s string) bool {
	// Add protocol if missing
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "https://" + s
	}
	u, err := url.ParseRequestURI(s)
	if err != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}

func helpInlineArticle(botUsername string) tgbotapi.InlineQueryResultArticle {
	text := fmt.Sprintf(`<b>Инлайн команды:</b>

/help — эта справка
/stats — статистика бота
/youtube — скачать с YouTube
/tiktok — скачать с TikTok
/instagram — скачать с Instagram

<b>Обычное использование:</b>
@%s https://...`, botUsername)
	r := tgbotapi.NewInlineQueryResultArticle("inline-help", "📖 Справка", text)
	r.InputMessageContent = tgbotapi.InputTextMessageContent{Text: text, ParseMode: "HTML"}
	r.Description = "Список инлайн команд"
	return r
}

func statsInlineArticle(h *Handler) tgbotapi.InlineQueryResultArticle {
	cached := int64(0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := h.cache.CountCached(ctx); err == nil {
		cached = c
	}

	text := fmt.Sprintf(`📦 Видео в кэше: <b>%d</b>
👷 Воркеров: <b>%d</b>
📋 Очередь: <b>%d</b>`, cached, h.cfg.WorkerCount, h.pool.QueueLen())
	r := tgbotapi.NewInlineQueryResultArticle("inline-stats", "📊 Статистика", text)
	r.InputMessageContent = tgbotapi.InputTextMessageContent{Text: text, ParseMode: "HTML"}
	r.Description = "Статистика бота"
	return r
}

func platformArticle(platform, example, id string) tgbotapi.InlineQueryResultArticle {
	text := fmt.Sprintf(`<b>%s</b>

Пример ссылки:
%s

Введи ссылку после @VidqueBot для скачивания.`, platform, example)
	r := tgbotapi.NewInlineQueryResultArticle("platform-"+id, platform+" 🎬", text)
	r.InputMessageContent = tgbotapi.InputTextMessageContent{Text: text, ParseMode: "HTML"}
	r.Description = fmt.Sprintf("Скачать с %s", platform)
	return r
}

func unknownCommandArticle(cmd string) tgbotapi.InlineQueryResultArticle {
	text := fmt.Sprintf(`Команда <b>%s</b> не найдена.

Доступные команды:
/help — справка
/stats — статистика
/youtube — YouTube
/tiktok — TikTok
/instagram — Instagram`, cmd)
	r := tgbotapi.NewInlineQueryResultArticle("unknown-"+cmd, "❌ Неизвестная команда", text)
	r.InputMessageContent = tgbotapi.InputTextMessageContent{Text: text, ParseMode: "HTML"}
	r.Description = "Неизвестная команда"
	return r
}

func loadingArticleWithID(resultID string) tgbotapi.InlineQueryResultArticle {
	r := tgbotapi.NewInlineQueryResultArticle(resultID, "⏳ Скачиваю видео...", "⏳ Скачиваю видео...")
	r.InputMessageContent = tgbotapi.InputTextMessageContent{Text: "⏳ Скачиваю видео..."}
	r.Description = "Видео скачивается"
	return r
}
