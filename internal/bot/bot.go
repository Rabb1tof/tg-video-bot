package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/config"
	"github.com/tg-video-bot/bot/internal/db"
	"github.com/tg-video-bot/bot/internal/worker"
)

// Handler processes all Telegram updates.
type Handler struct {
	api       telegramAPI
	botName   string
	cfg       *config.Config
	cache     botCache
	db        botDB
	pool      botPool
	log       *slog.Logger
	startedAt time.Time
}

func NewHandler(api *tgbotapi.BotAPI, cfg *config.Config, c *cache.Cache, pgDB *db.DB, pool *worker.Pool, log *slog.Logger) *Handler {
	return &Handler{api: api, botName: api.Self.UserName, cfg: cfg, cache: c, db: pgDB, pool: pool, log: log, startedAt: time.Now()}
}

// Dispatch routes a Telegram update to the correct handler.
func (h *Handler) Dispatch(update tgbotapi.Update) {
	if u := extractUser(update); u != nil {
		userCopy := *u
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := h.db.UpsertUser(ctx, userCopy.ID, userCopy.FirstName, userCopy.UserName); err != nil {
				h.log.Error("upsert user failed", "err", err, "user_id", userCopy.ID)
			}
		}()
	}
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
/history — история скачанных видео
/clear — очистить историю`, msg.From.FirstName, h.botName, h.botName)
	case "help":
		text = `<b>Справка:</b>

📱 <b>В личке:</b> просто отправь мне ссылку на видео, и я его скачаю

🔍 <b>Инлайн-режим:</b> напиши @VidqueBot и ссылку в любом чате

<b>Команды:</b>
/start — приветствие
/help — эта справка
/history — история скачанных видео
/clear — очистить историю`
	case "stats":
		h.handleAdminStats(msg)
		return
	case "addadmin":
		h.handleAddAdmin(msg)
		return
	case "removeadmin":
		h.handleRemoveAdmin(msg)
		return
	case "helpadmin":
		h.handleAdminHelp(msg)
		return
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
		cached, err := h.cache.GetCachedVideo(ctx, normalizedText)
		if err != nil {
			h.log.Error("cache get error", "err", err)
		}

		if cached != nil {
			go func() {
				// Продлеваем TTL если видео в топ-3 истории пользователя
				h.cache.RefreshFileIDTTL(ctx, normalizedText, msg.From.ID)
				// Добавляем в историю этого пользователя (если ещё нет)
				h.addToHistoryIfNew(ctx, msg.From.ID, normalizedText, cached)
			}()

			// Send cached video immediately
			videoMsg := tgbotapi.NewVideo(msg.Chat.ID, tgbotapi.FileID(cached.FileID))
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
		h.botName)
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

// addToHistoryIfNew добавляет видео в историю пользователя при cache hit,
// только если этого fileID ещё нет в его истории.
func (h *Handler) addToHistoryIfNew(ctx context.Context, userID int64, url string, cv *cache.CachedVideo) {
	history, err := h.cache.GetVideoHistory(ctx, userID)
	if err != nil {
		h.log.Error("get video history failed", "err", err, "user_id", userID)
		return
	}
	for _, v := range history {
		if v.FileID == cv.FileID {
			return // уже есть
		}
	}
	title := cv.Title
	if title == "" {
		title = url
	}
	if err := h.cache.AddVideoHistory(ctx, userID, cache.VideoHistory{
		FileID:  cv.FileID,
		Title:   title,
		AddedAt: time.Now(),
	}); err != nil {
		h.log.Error("add video history failed", "err", err, "user_id", userID)
		return
	}
	if err := h.cache.IncrInlineVersion(ctx, userID); err != nil {
		h.log.Error("incr inline version failed", "err", err, "user_id", userID)
	}
}

func (h *Handler) reply(msg *tgbotapi.Message, text string) {
	reply := tgbotapi.NewMessage(msg.Chat.ID, text)
	reply.ParseMode = "HTML"
	reply.DisableWebPagePreview = true
	if _, err := h.api.Send(reply); err != nil {
		h.log.Error("send reply failed", "err", err)
	}
}

// isAdmin returns true if userID is a bot administrator.
// Checks config-defined IDs first (no network), then Redis-stored admins.
func (h *Handler) isAdmin(userID int64) bool {
	for _, id := range h.cfg.AdminIDs {
		if id == userID {
			return true
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.db.IsAdmin(ctx, userID)
}

func (h *Handler) handleAdminStats(msg *tgbotapi.Message) {
	if !h.isAdmin(msg.From.ID) {
		h.reply(msg, "🚫 Доступно только администраторам.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gs, err := h.db.GetStats(ctx)
	if err != nil {
		h.log.Error("get global stats failed", "err", err)
		h.reply(msg, "❌ Не удалось получить статистику")
		return
	}
	if cv, err := h.cache.CountCached(ctx); err == nil {
		gs.CachedVideos = cv
	}
	top, err := h.db.GetTopUsers(ctx, 5)
	if err != nil {
		h.log.Error("get top users failed", "err", err)
	}
	var topSection string
	if len(top) > 0 {
		topSection = "\n\n🏆 <b>Топ загрузчиков:</b>\n"
		for i, u := range top {
			name := u.FirstName
			if u.Username != "" {
				name += " (@" + u.Username + ")"
			}
			topSection += fmt.Sprintf("%d. %s — <b>%d</b> %s\n",
				i+1, name, u.DownloadCount, downloadWord(u.DownloadCount))
		}
	}
	text := fmt.Sprintf(`📊 <b>Статистика бота</b>

👥 Пользователей: <b>%d</b>  (сегодня: <b>%d</b>)
📥 Загрузок: <b>%d</b>  (✅ <b>%d</b> / ❌ <b>%d</b>)
📦 Видео в кэше: <b>%d</b>

👷 Воркеров: <b>%d</b>  |  📋 Очередь: <b>%d</b>
⏱ Аптайм: <b>%s</b>%s`,
		gs.UniqueUsers, gs.ActiveToday,
		gs.TotalDownloads, gs.SuccessDownloads, gs.FailDownloads,
		gs.CachedVideos,
		h.cfg.WorkerCount, h.pool.QueueLen(),
		formatUptime(time.Since(h.startedAt)),
		topSection,
	)
	h.reply(msg, text)
}

func (h *Handler) handleAddAdmin(msg *tgbotapi.Message) {
	if !h.isAdmin(msg.From.ID) {
		h.reply(msg, "🚫 Доступно только администраторам.")
		return
	}
	arg := strings.TrimSpace(msg.CommandArguments())
	if arg == "" {
		h.reply(msg, "Укажи user_id: /addadmin 123456789")
		return
	}
	targetID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		h.reply(msg, "❌ Неверный формат user_id (должно быть целое число)")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.db.AddAdmin(ctx, targetID); err != nil {
		h.log.Error("add admin failed", "err", err, "target_id", targetID)
		h.reply(msg, "❌ Не удалось добавить администратора")
		return
	}
	h.log.Info("admin added", "target_id", targetID, "by", msg.From.ID)
	h.reply(msg, fmt.Sprintf("✅ Пользователь <code>%d</code> добавлен как администратор", targetID))
}

func (h *Handler) handleRemoveAdmin(msg *tgbotapi.Message) {
	if !h.isAdmin(msg.From.ID) {
		h.reply(msg, "🚫 Доступно только администраторам.")
		return
	}
	arg := strings.TrimSpace(msg.CommandArguments())
	if arg == "" {
		h.reply(msg, "Укажи user_id: /removeadmin 123456789")
		return
	}
	targetID, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		h.reply(msg, "❌ Неверный формат user_id")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.db.RemoveAdmin(ctx, targetID); err != nil {
		h.log.Error("remove admin failed", "err", err, "target_id", targetID)
		h.reply(msg, "❌ Не удалось убрать администратора")
		return
	}
	h.log.Info("admin removed", "target_id", targetID, "by", msg.From.ID)
	h.reply(msg, fmt.Sprintf("✅ Пользователь <code>%d</code> удалён из администраторов", targetID))
}

func (h *Handler) handleAdminHelp(msg *tgbotapi.Message) {
	if !h.isAdmin(msg.From.ID) {
		h.reply(msg, "🚫 Доступно только администраторам.")
		return
	}
	text := `🔧 <b>Команды администратора</b>

/stats — расширенная статистика бота
/addadmin <code>&lt;user_id&gt;</code> — добавить администратора
/removeadmin <code>&lt;user_id&gt;</code> — убрать администратора
/helpadmin — эта справка

<b>Примечания:</b>
• Базовые админы задаются через <code>ADMIN_IDS</code> в .env (через запятую)
• Добавленные через /addadmin хранятся в Redis
• Узнать user_id пользователя: @userinfobot`
	h.reply(msg, text)
}

// extractUser returns the User from any update type, or nil if unavailable.
func extractUser(u tgbotapi.Update) *tgbotapi.User {
	if u.Message != nil && u.Message.From != nil {
		return u.Message.From
	}
	if u.InlineQuery != nil {
		return u.InlineQuery.From
	}
	if u.ChosenInlineResult != nil {
		return u.ChosenInlineResult.From
	}
	return nil
}

// downloadWord returns the correct Russian word form for a download count.
func downloadWord(n int64) string {
	if n%100 >= 11 && n%100 <= 19 {
		return "загрузок"
	}
	switch n % 10 {
	case 1:
		return "загрузка"
	case 2, 3, 4:
		return "загрузки"
	default:
		return "загрузок"
	}
}

// formatUptime formats a duration as a human-readable Russian uptime string.
func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dд %dч %dм", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dч %dм", hours, minutes)
	}
	return fmt.Sprintf("%dм", minutes)
}
