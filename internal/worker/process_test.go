package worker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/downloader"
)

// ── mock implementations ──────────────────────────────────────────────────────

type mockDownloader struct {
	mu      sync.Mutex
	result  *downloader.Result
	err     error
	cleaned []string
}

func (m *mockDownloader) Download(_ context.Context, _ string) (*downloader.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.result, m.err
}
func (m *mockDownloader) Cleanup(tmpDir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleaned = append(m.cleaned, tmpDir)
}

type mockTelegramSender struct {
	mu      sync.Mutex
	sent    []tgbotapi.Chattable
	sendErr error
	// Return a message with Video on second call (upload to channel)
	callCount int
	fileID    string
}

func (m *mockTelegramSender) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, c)
	m.callCount++
	if m.sendErr != nil {
		return tgbotapi.Message{}, m.sendErr
	}
	// First Send = upload to channel → return message with Video
	if m.callCount == 1 {
		return tgbotapi.Message{Video: &tgbotapi.Video{FileID: m.fileID}}, nil
	}
	return tgbotapi.Message{}, nil
}
func (m *mockTelegramSender) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type mockPoolCache struct {
	mu            sync.Mutex
	setCachedErr  error
	addHistoryErr error
	incrVerErr    error
	history       []cache.VideoHistory
}

func (m *mockPoolCache) SetCachedVideo(_ context.Context, _, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setCachedErr
}
func (m *mockPoolCache) AddVideoHistory(_ context.Context, _ int64, v cache.VideoHistory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, v)
	return m.addHistoryErr
}
func (m *mockPoolCache) IncrInlineVersion(_ context.Context, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.incrVerErr
}

type mockPoolDB struct {
	mu            sync.Mutex
	recordCalls   []bool
	recordErr     error
}

func (m *mockPoolDB) RecordDownload(_ context.Context, _ int64, _, _ string, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordCalls = append(m.recordCalls, success)
	return m.recordErr
}

// ── test pool constructor ─────────────────────────────────────────────────────

func newProcessTestPool(dl videoDownloader, api telegramSender, c poolCache, d poolDB) *Pool {
	return &Pool{
		size:      2,
		jobs:      make(chan Job, 8),
		cache:     c,
		db:        d,
		dl:        dl,
		api:       api,
		channelID: -100123,
		botUsername: "testbot",
		log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// ── process tests ─────────────────────────────────────────────────────────────

func TestProcess_DownloadError(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{err: errors.New("network error")}
	api := &mockTelegramSender{}
	db := &mockPoolDB{}
	p := newProcessTestPool(dl, api, &mockPoolCache{}, db)

	job := Job{URL: "https://example.com/fail", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	// notify called for user
	if api.sentCount() < 1 {
		t.Error("expected PM notification on download error")
	}
	// failure recorded in DB
	if len(db.recordCalls) == 0 {
		t.Error("expected RecordDownload call on failure")
	}
	if db.recordCalls[0] != false {
		t.Error("expected success=false recorded")
	}
	// inProgress cleaned up
	if _, ok := p.inProgress.Load(job.URL); ok {
		t.Error("inProgress should be deleted after process")
	}
}

func TestProcess_DownloadUnavailableError(t *testing.T) {
	t.Parallel()
	unavailErr := &downloader.UnavailableError{}
	_ = unavailErr // just checks it's the right type via errors.As
	dl := &mockDownloader{err: &downloader.UnavailableError{}}
	api := &mockTelegramSender{}
	db := &mockPoolDB{}
	p := newProcessTestPool(dl, api, &mockPoolCache{}, db)

	job := Job{URL: "https://private.example.com", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	if api.sentCount() < 1 {
		t.Error("expected PM notification for unavailable video")
	}
}

func TestProcess_TelegramUploadError(t *testing.T) {
	t.Parallel()
	dl := &mockDownloader{result: &downloader.Result{
		FilePath: "/tmp/video.mp4",
		Title:    "Test Video",
		TmpDir:   t.TempDir(),
	}}
	api := &mockTelegramSender{sendErr: errors.New("telegram error")}
	db := &mockPoolDB{}
	p := newProcessTestPool(dl, api, &mockPoolCache{}, db)

	job := Job{URL: "https://example.com/upload-fail", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	if len(db.recordCalls) == 0 {
		t.Error("expected RecordDownload on upload failure")
	}
	if db.recordCalls[0] != false {
		t.Error("expected success=false on upload failure")
	}
}

func TestProcess_Success(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dl := &mockDownloader{result: &downloader.Result{
		FilePath: tmpDir + "/video.mp4",
		Title:    "My Awesome Video",
		TmpDir:   tmpDir,
	}}
	api := &mockTelegramSender{fileID: "uploaded_fid"}
	c := &mockPoolCache{}
	db := &mockPoolDB{}
	p := newProcessTestPool(dl, api, c, db)

	job := Job{URL: "https://youtube.com/watch?v=ok", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	// Upload + PM send
	if api.sentCount() < 2 {
		t.Errorf("expected >=2 sends (channel + PM), got %d", api.sentCount())
	}
	// Cache written
	if len(c.history) == 0 {
		t.Error("expected video added to history")
	}
	// DB recorded success
	if len(db.recordCalls) == 0 {
		t.Error("expected RecordDownload call")
	}
	if db.recordCalls[0] != true {
		t.Error("expected success=true recorded")
	}
	// Cleanup called
	if len(dl.cleaned) == 0 {
		t.Error("expected Cleanup to be called")
	}
}

func TestProcess_LongTitle(t *testing.T) {
	t.Parallel()
	longTitle := string(make([]byte, 150))
	for i := range longTitle {
		longTitle = longTitle[:i] + "a" + longTitle[i+1:]
	}
	tmpDir := t.TempDir()
	dl := &mockDownloader{result: &downloader.Result{
		FilePath: tmpDir + "/video.mp4",
		Title:    longTitle,
		TmpDir:   tmpDir,
	}}
	api := &mockTelegramSender{fileID: "fid"}
	p := newProcessTestPool(dl, api, &mockPoolCache{}, &mockPoolDB{})

	job := Job{URL: "https://example.com/long-title", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	// Should not panic with long title
	if api.sentCount() < 1 {
		t.Error("expected sends")
	}
}

func TestProcess_CacheWriteError(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dl := &mockDownloader{result: &downloader.Result{
		FilePath: tmpDir + "/video.mp4",
		Title:    "Video",
		TmpDir:   tmpDir,
	}}
	api := &mockTelegramSender{fileID: "fid"}
	c := &mockPoolCache{setCachedErr: errors.New("cache error")}
	db := &mockPoolDB{}
	p := newProcessTestPool(dl, api, c, db)

	job := Job{URL: "https://example.com/cache-err", UserID: 42}
	p.inProgress.Store(job.URL, job)
	p.process(context.Background(), job, 1)

	// Should continue despite cache error
	if len(db.recordCalls) == 0 {
		t.Error("expected RecordDownload even on cache error")
	}
}

// ── Start / worker goroutine integration ─────────────────────────────────────

func TestPool_StartAndProcess(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	dl := &mockDownloader{result: &downloader.Result{
		FilePath: tmpDir + "/video.mp4",
		Title:    "Test",
		TmpDir:   tmpDir,
	}}
	api := &mockTelegramSender{fileID: "fid_start"}
	db := &mockPoolDB{}
	c := &mockPoolCache{}
	p := newProcessTestPool(dl, api, c, db)
	p.size = 1
	p.jobs = make(chan Job, 4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	p.Start(ctx)

	job := Job{URL: "https://example.com/start-test", UserID: 99}
	ok := p.Enqueue(job)
	if !ok {
		t.Fatal("Enqueue should succeed")
	}

	// Wait for processing
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(db.recordCalls) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(db.recordCalls) == 0 {
		t.Error("job was not processed within timeout")
	}
}

// ── notify ────────────────────────────────────────────────────────────────────

func TestNotify_SendsMessage(t *testing.T) {
	t.Parallel()
	api := &mockTelegramSender{}
	p := &Pool{
		api: api,
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	p.notify(42, "test notification")
	if api.sentCount() != 1 {
		t.Errorf("notify: expected 1 send, got %d", api.sentCount())
	}
}

func TestNotify_SendError(t *testing.T) {
	t.Parallel()
	api := &mockTelegramSender{sendErr: errors.New("telegram down")}
	p := &Pool{
		api: api,
		log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	// Should not panic on send error
	p.notify(42, "test")
}
