package worker

import (
	"context"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/downloader"
)

// videoDownloader is the subset of *downloader.Downloader used by Pool.
type videoDownloader interface {
	Download(ctx context.Context, url string) (*downloader.Result, error)
	Cleanup(tmpDir string)
}

// telegramSender is the subset of *tgbotapi.BotAPI used by Pool.
type telegramSender interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
}

// poolCache is the subset of *cache.Cache used by Pool.
type poolCache interface {
	SetCachedVideo(ctx context.Context, url, fileID, title string) error
	AddVideoHistory(ctx context.Context, userID int64, video cache.VideoHistory) error
	IncrInlineVersion(ctx context.Context, userID int64) error
}

// poolDB is the subset of *db.DB (via RecordDownload) used by Pool.
type poolDB interface {
	RecordDownload(ctx context.Context, userID int64, url, title string, success bool) error
}
