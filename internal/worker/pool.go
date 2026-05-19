package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/downloader"
)

// Job represents a single video download request.
type Job struct {
	URL      string
	UserID   int64
	ResultID string // For matching with chosen inline result
}

// Pool manages a fixed set of download workers.
type Pool struct {
	size             int
	jobs             chan Job
	inProgress       sync.Map
	inlineMessageIDs sync.Map // resultID -> inlineMessageID
	cache            *cache.Cache
	dl               *downloader.Downloader
	api              *tgbotapi.BotAPI
	channelID        int64
	botUsername      string
	log              *slog.Logger
}

func NewPool(
	size int,
	c *cache.Cache,
	dl *downloader.Downloader,
	api *tgbotapi.BotAPI,
	channelID int64,
	botUsername string,
	log *slog.Logger,
) *Pool {
	return &Pool{
		size: size, jobs: make(chan Job, size*4),
		cache: c, dl: dl, api: api,
		channelID: channelID, botUsername: botUsername,
		log: log,
	}
}

func (p *Pool) Start(ctx context.Context) {
	p.log.Info("starting worker pool", "workers", p.size)
	for i := range p.size {
		go p.worker(ctx, i+1)
	}
}

func (p *Pool) Enqueue(job Job) bool {
	if _, loaded := p.inProgress.LoadOrStore(job.URL, struct{}{}); loaded {
		return false
	}
	select {
	case p.jobs <- job:
		p.log.Info("job enqueued", "url", job.URL, "user_id", job.UserID)
		return true
	default:
		p.inProgress.Delete(job.URL)
		p.log.Warn("job queue full", "url", job.URL)
		return false
	}
}

func (p *Pool) IsInProgress(url string) bool {
	_, ok := p.inProgress.Load(url)
	return ok
}

func (p *Pool) QueueLen() int { return len(p.jobs) }

func (p *Pool) worker(ctx context.Context, id int) {
	p.log.Info("worker started", "worker_id", id)
	defer p.log.Info("worker stopped", "worker_id", id)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.jobs:
			p.process(ctx, job, id)
		}
	}
}

func (p *Pool) process(ctx context.Context, job Job, workerID int) {
	log := p.log.With("url", job.URL, "user_id", job.UserID, "worker_id", workerID)
	defer p.inProgress.Delete(job.URL)

	start := time.Now()
	log.Info("downloading")

	result, err := p.dl.Download(ctx, job.URL)
	if err != nil {
		log.Error("download failed", "err", err)
		var unavail *downloader.UnavailableError
		if errors.As(err, &unavail) {
			p.notify(job.UserID, "❌ "+unavail.Error())
		} else {
			p.notify(job.UserID, fmt.Sprintf("❌ Не удалось скачать видео:\n%s", err.Error()))
		}
		return
	}
	defer p.dl.Cleanup(result.TmpDir)

	log.Info("uploading to channel", "file", result.FilePath)

	videoUpload := tgbotapi.NewVideo(p.channelID, tgbotapi.FilePath(result.FilePath))
	// Limit caption length to 1024 characters (Telegram limit)
	caption := result.Title
	if len(caption) > 100 {
		caption = caption[:100] + "..."
	}
	videoUpload.Caption = caption
	videoUpload.DisableNotification = true

	msg, err := p.api.Send(videoUpload)
	if err != nil {
		log.Error("telegram upload failed", "err", err)
		p.notify(job.UserID, "❌ Не удалось загрузить видео в Telegram. Попробуй позже.")
		return
	}

	fileID := msg.Video.FileID
	log.Info("uploaded", "file_id", fileID, "elapsed", time.Since(start))

	if err := p.cache.SetCachedVideo(ctx, job.URL, fileID, result.Title); err != nil {
		log.Error("cache write failed", "err", err)
	}

	// Add to user's video history
	videoHistory := cache.VideoHistory{
		FileID:  fileID,
		Title:   result.Title,
		AddedAt: time.Now(),
	}
	if err := p.cache.AddVideoHistory(ctx, job.UserID, videoHistory); err != nil {
		log.Error("add video history failed", "err", err)
	} else {
		log.Info("video added to history", "user_id", job.UserID, "title", result.Title)
		// Инвалидируем inline-кэш: следующий запрос @bot вернёт свежую историю
		if err := p.cache.IncrInlineVersion(ctx, job.UserID); err != nil {
			log.Error("incr inline version failed", "err", err)
		}
	}

	// Send video to user's PM after successful download
	videoMsg := tgbotapi.NewVideo(job.UserID, tgbotapi.FileID(fileID))
	if result.Title != "" {
		videoMsg.Caption = truncateCaption(result.Title)
	}
	if _, err := p.api.Send(videoMsg); err != nil {
		log.Error("send video to PM failed", "err", err)
		p.notify(job.UserID, "❌ Не удалось отправить видео в личку. Попробуй снова.")
	} else {
		log.Info("video sent to PM", "user_id", job.UserID)
	}

	log.Info("job completed", "elapsed", time.Since(start))
}

func truncateCaption(s string) string {
	const maxCaptionLength = 1000
	if len(s) <= maxCaptionLength {
		return s
	}
	return s[:maxCaptionLength-3] + "..."
}

func (p *Pool) notify(userID int64, text string) {
	msg := tgbotapi.NewMessage(userID, text)
	if _, err := p.api.Send(msg); err != nil {
		p.log.Warn("PM notification failed", "user_id", userID, "err", err)
	}
}

// SetInlineMessageID stores the inline message ID for a result ID.
// Returns true if the result ID is found in in-progress jobs.
func (p *Pool) SetInlineMessageID(resultID, inlineMessageID string) bool {
	// Check if this result ID is in any in-progress job
	found := false
	p.inProgress.Range(func(key, value interface{}) bool {
		job, ok := value.(Job)
		if ok && job.ResultID == resultID {
			found = true
			p.inlineMessageIDs.Store(resultID, inlineMessageID)
			return false // stop iteration
		}
		return true
	})
	return found
}

// GetInlineMessageID retrieves the inline message ID for a result ID.
func (p *Pool) GetInlineMessageID(resultID string) (string, bool) {
	if val, ok := p.inlineMessageIDs.Load(resultID); ok {
		inlineMessageID, ok := val.(string)
		return inlineMessageID, ok
	}
	return "", false
}
