package bot

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/db"
	"github.com/tg-video-bot/bot/internal/worker"
)

// telegramAPI is the subset of *tgbotapi.BotAPI that Handler uses.
type telegramAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

// botCache is the subset of *cache.Cache that Handler uses.
type botCache interface {
	GetCachedVideo(ctx context.Context, url string) (*cache.CachedVideo, error)
	RefreshFileIDTTL(ctx context.Context, url string, userID int64) error
	AddVideoHistory(ctx context.Context, userID int64, video cache.VideoHistory) error
	GetVideoHistory(ctx context.Context, userID int64) ([]cache.VideoHistory, error)
	ClearVideoHistory(ctx context.Context, userID int64) error
	IncrInlineVersion(ctx context.Context, userID int64) error
	GetInlineVersion(ctx context.Context, userID int64) int64
	IncrRateLimit(ctx context.Context, userID int64) (int64, error)
	CountCached(ctx context.Context) (int64, error)
}

// botDB is the subset of *db.DB that Handler uses.
type botDB interface {
	UpsertUser(ctx context.Context, userID int64, firstName, username string) error
	IsAdmin(ctx context.Context, userID int64) bool
	GetStats(ctx context.Context) (db.GlobalStats, error)
	GetTopUsers(ctx context.Context, limit int) ([]db.UserStat, error)
	AddAdmin(ctx context.Context, userID int64) error
	RemoveAdmin(ctx context.Context, userID int64) error
}

// botPool is the subset of *worker.Pool that Handler uses.
type botPool interface {
	IsInProgress(url string) bool
	Enqueue(job worker.Job) bool
	SetInlineMessageID(resultID, inlineMessageID string) bool
	QueueLen() int
}
