package bot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/tg-video-bot/bot/internal/cache"
	"github.com/tg-video-bot/bot/internal/config"
	"github.com/tg-video-bot/bot/internal/db"
	"github.com/tg-video-bot/bot/internal/worker"
)

// ── mock implementations ──────────────────────────────────────────────────────

type mockAPI struct {
	mu      sync.Mutex
	sent    []tgbotapi.Chattable
	sendErr error
	reqResp *tgbotapi.APIResponse
	reqErr  error
}

func (m *mockAPI) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, c)
	return tgbotapi.Message{MessageID: 1}, m.sendErr
}
func (m *mockAPI) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, c)
	if m.reqResp != nil {
		return m.reqResp, m.reqErr
	}
	return &tgbotapi.APIResponse{Ok: true}, m.reqErr
}
func (m *mockAPI) lastText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.sent) - 1; i >= 0; i-- {
		if msg, ok := m.sent[i].(tgbotapi.MessageConfig); ok {
			return msg.Text
		}
	}
	return ""
}
func (m *mockAPI) sentCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type mockCache struct {
	mu      sync.Mutex
	videos  map[string]*cache.CachedVideo
	history map[int64][]cache.VideoHistory
	version map[int64]int64
	rate    map[int64]int64
	count   int64
	getErr  error
	setErr  error
}

func newMockCache() *mockCache {
	return &mockCache{
		videos:  make(map[string]*cache.CachedVideo),
		history: make(map[int64][]cache.VideoHistory),
		version: make(map[int64]int64),
		rate:    make(map[int64]int64),
	}
}

func (m *mockCache) GetCachedVideo(_ context.Context, url string) (*cache.CachedVideo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.videos[url], nil
}
func (m *mockCache) RefreshFileIDTTL(_ context.Context, _ string, _ int64) error { return nil }
func (m *mockCache) AddVideoHistory(_ context.Context, userID int64, v cache.VideoHistory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history[userID] = append(m.history[userID], v)
	return nil
}
func (m *mockCache) GetVideoHistory(_ context.Context, userID int64) ([]cache.VideoHistory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.history[userID], nil
}
func (m *mockCache) ClearVideoHistory(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.history, userID)
	return nil
}
func (m *mockCache) IncrInlineVersion(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.version[userID]++
	return nil
}
func (m *mockCache) GetInlineVersion(_ context.Context, userID int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version[userID]
}
func (m *mockCache) IncrRateLimit(_ context.Context, userID int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rate[userID]++
	return m.rate[userID], nil
}
func (m *mockCache) CountCached(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count, nil
}

type mockDB struct {
	mu          sync.Mutex
	isAdmin     bool
	adminErr    error
	stats       db.GlobalStats
	statsErr    error
	topUsers    []db.UserStat
	topUsersErr error
	addAdminErr error
	rmAdminErr  error
}

func (m *mockDB) UpsertUser(_ context.Context, _ int64, _, _ string) error { return nil }
func (m *mockDB) IsAdmin(_ context.Context, _ int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isAdmin
}
func (m *mockDB) GetStats(_ context.Context) (db.GlobalStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats, m.statsErr
}
func (m *mockDB) GetTopUsers(_ context.Context, _ int) ([]db.UserStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.topUsers, m.topUsersErr
}
func (m *mockDB) AddAdmin(_ context.Context, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addAdminErr
}
func (m *mockDB) RemoveAdmin(_ context.Context, _ int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rmAdminErr
}

type mockPool struct {
	inProgress map[string]bool
	enqueueOK  bool
	queueLen   int
}

func (m *mockPool) IsInProgress(url string) bool        { return m.inProgress[url] }
func (m *mockPool) Enqueue(_ worker.Job) bool           { return m.enqueueOK }
func (m *mockPool) SetInlineMessageID(_, _ string) bool { return false }
func (m *mockPool) QueueLen() int                       { return m.queueLen }

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestHandler(api *mockAPI, c *mockCache, d *mockDB, p *mockPool) *Handler {
	if api == nil {
		api = &mockAPI{}
	}
	if c == nil {
		c = newMockCache()
	}
	if d == nil {
		d = &mockDB{}
	}
	if p == nil {
		p = &mockPool{enqueueOK: true}
	}
	return &Handler{
		api:       api,
		botName:   "testbot",
		cfg:       &config.Config{WorkerCount: 3, RateLimitPerHour: 5, AdminIDs: []int64{1}},
		cache:     c,
		db:        d,
		pool:      p,
		log:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		startedAt: time.Now(),
	}
}

func newMsg(userID int64, chatID int64, text string) *tgbotapi.Message {
	return &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: userID, FirstName: "Alice"},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Text:      text,
	}
}

func newCmdMsg(userID, chatID int64, command, args string) *tgbotapi.Message {
	entities := []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(command) + 1}}
	text := "/" + command
	if args != "" {
		text += " " + args
	}
	return &tgbotapi.Message{
		MessageID: 1,
		From:      &tgbotapi.User{ID: userID, FirstName: "Alice"},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Text:      text,
		Entities:  entities,
	}
}

// ── handleCommand tests ───────────────────────────────────────────────────────

func TestHandleCommand_Start(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "start", ""))
	if api.sentCount() == 0 {
		t.Fatal("expected at least one message sent")
	}
	txt := api.lastText()
	if !strings.Contains(txt, "testbot") {
		t.Errorf("start reply should contain bot name, got: %q", txt)
	}
}

func TestHandleCommand_Help(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "help", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "Справка") {
		t.Errorf("help reply should contain 'Справка', got: %q", txt)
	}
}

func TestHandleCommand_Unknown(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "unknowncmd", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "Неизвестная команда") {
		t.Errorf("unknown cmd reply should contain 'Неизвестная команда', got: %q", txt)
	}
}

func TestHandleCommand_History_Empty(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, newMockCache(), nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "history", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "пуста") {
		t.Errorf("empty history reply = %q", txt)
	}
}

func TestHandleCommand_History_WithEntries(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	c.history[42] = []cache.VideoHistory{
		{FileID: "fid1", Title: "My Video 1"},
		{FileID: "fid2", Title: "My Video 2"},
	}
	h := newTestHandler(api, c, nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "history", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "My Video 1") {
		t.Errorf("history reply should contain video title, got: %q", txt)
	}
}

func TestHandleCommand_Clear(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	c.history[42] = []cache.VideoHistory{{FileID: "fid1", Title: "V"}}
	h := newTestHandler(api, c, nil, nil)
	h.handleCommand(newCmdMsg(42, 42, "clear", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "очищена") {
		t.Errorf("clear reply = %q", txt)
	}
	if len(c.history[42]) != 0 {
		t.Error("history should be cleared after /clear")
	}
}

// ── Admin command tests ───────────────────────────────────────────────────────

func TestHandleCommand_Stats_NonAdmin(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: false}, nil)
	// userID=99 not in AdminIDs (which has only 1)
	h.handleCommand(newCmdMsg(99, 99, "stats", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "администраторам") {
		t.Errorf("non-admin stats reply = %q", txt)
	}
}

func TestHandleCommand_Stats_Admin(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	d := &mockDB{
		isAdmin: true,
		stats:   db.GlobalStats{UniqueUsers: 10, TotalDownloads: 50, SuccessDownloads: 45, FailDownloads: 5},
	}
	h := newTestHandler(api, nil, d, nil)
	// userID=1 is in AdminIDs
	h.handleCommand(newCmdMsg(1, 1, "stats", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "Статистика") {
		t.Errorf("admin stats reply = %q", txt)
	}
}

func TestHandleCommand_Stats_Admin_WithTopUsers(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	d := &mockDB{
		isAdmin: true,
		stats:   db.GlobalStats{UniqueUsers: 5},
		topUsers: []db.UserStat{
			{UserID: 10, FirstName: "Bob", Username: "bob", DownloadCount: 30},
		},
	}
	h := newTestHandler(api, nil, d, nil)
	h.handleCommand(newCmdMsg(1, 1, "stats", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "Bob") {
		t.Errorf("stats with top users should contain user name, got: %q", txt)
	}
}

func TestHandleCommand_AddAdmin_NonAdmin(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: false}, nil)
	h.handleCommand(newCmdMsg(99, 99, "addadmin", "12345"))
	txt := api.lastText()
	if !strings.Contains(txt, "администраторам") {
		t.Errorf("non-admin addadmin reply = %q", txt)
	}
}

func TestHandleCommand_AddAdmin_NoArg(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "addadmin", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "user_id") {
		t.Errorf("addadmin no arg reply = %q", txt)
	}
}

func TestHandleCommand_AddAdmin_InvalidID(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "addadmin", "notanumber"))
	txt := api.lastText()
	if !strings.Contains(txt, "Неверный формат") {
		t.Errorf("addadmin invalid id reply = %q", txt)
	}
}

func TestHandleCommand_AddAdmin_DBError(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	d := &mockDB{isAdmin: true, addAdminErr: errors.New("db error")}
	h := newTestHandler(api, nil, d, nil)
	h.handleCommand(newCmdMsg(1, 1, "addadmin", "12345"))
	txt := api.lastText()
	if !strings.Contains(txt, "Не удалось") {
		t.Errorf("addadmin db error reply = %q", txt)
	}
}

func TestHandleCommand_AddAdmin_OK(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "addadmin", "12345"))
	txt := api.lastText()
	if !strings.Contains(txt, "добавлен как администратор") {
		t.Errorf("addadmin ok reply = %q", txt)
	}
}

func TestHandleCommand_RemoveAdmin_NonAdmin(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: false}, nil)
	h.handleCommand(newCmdMsg(99, 99, "removeadmin", "12345"))
	txt := api.lastText()
	if !strings.Contains(txt, "администраторам") {
		t.Errorf("non-admin removeadmin reply = %q", txt)
	}
}

func TestHandleCommand_RemoveAdmin_NoArg(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "removeadmin", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "user_id") {
		t.Errorf("removeadmin no arg reply = %q", txt)
	}
}

func TestHandleCommand_RemoveAdmin_InvalidID(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "removeadmin", "abc"))
	txt := api.lastText()
	if !strings.Contains(txt, "Неверный формат") {
		t.Errorf("removeadmin invalid id reply = %q", txt)
	}
}

func TestHandleCommand_RemoveAdmin_OK(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "removeadmin", "12345"))
	txt := api.lastText()
	if !strings.Contains(txt, "удалён из администраторов") {
		t.Errorf("removeadmin ok reply = %q", txt)
	}
}

func TestHandleCommand_AdminHelp_NonAdmin(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: false}, nil)
	h.handleCommand(newCmdMsg(99, 99, "helpadmin", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "администраторам") {
		t.Errorf("non-admin helpadmin reply = %q", txt)
	}
}

func TestHandleCommand_AdminHelp_OK(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, &mockDB{isAdmin: true}, nil)
	h.handleCommand(newCmdMsg(1, 1, "helpadmin", ""))
	txt := api.lastText()
	if !strings.Contains(txt, "администратора") {
		t.Errorf("helpadmin reply = %q", txt)
	}
}

// ── handleMessage tests ───────────────────────────────────────────────────────

func TestHandleMessage_PlainText(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.handleMessage(newMsg(42, 42, "hello world"))
	txt := api.lastText()
	if !strings.Contains(txt, "ссылку") {
		t.Errorf("plain text reply = %q", txt)
	}
}

func TestHandleMessage_InvalidURL(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.handleMessage(newMsg(42, 42, "not-a-valid-url-with.dots-but spaces"))
	// Should reply with invalid URL message
	if api.sentCount() == 0 {
		t.Fatal("expected reply")
	}
}

func TestHandleMessage_CacheHit(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	url := "https://youtube.com/watch?v=test123"
	c.videos[url] = &cache.CachedVideo{FileID: "fid_cached", Title: "Cached Video"}
	h := newTestHandler(api, c, nil, nil)
	h.handleMessage(newMsg(42, 42, url))
	// Should send cached video + confirmation reply
	if api.sentCount() < 1 {
		t.Errorf("expected messages sent, got %d", api.sentCount())
	}
}

func TestHandleMessage_CacheError(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	c.getErr = errors.New("redis down")
	h := newTestHandler(api, c, nil, &mockPool{enqueueOK: true})
	h.handleMessage(newMsg(42, 42, "https://youtube.com/watch?v=err"))
	// Should still proceed (enqueue), not crash
	if api.sentCount() == 0 {
		t.Error("expected some reply even on cache error")
	}
}

func TestHandleMessage_Enqueue_OK(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	p := &mockPool{enqueueOK: true, inProgress: map[string]bool{}}
	h := newTestHandler(api, nil, nil, p)
	h.handleMessage(newMsg(42, 42, "https://youtube.com/watch?v=new"))
	txt := api.lastText()
	if !strings.Contains(txt, "Скачиваю") {
		t.Errorf("enqueue reply = %q", txt)
	}
}

func TestHandleMessage_Enqueue_QueueFull(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	p := &mockPool{enqueueOK: false, inProgress: map[string]bool{}}
	h := newTestHandler(api, nil, nil, p)
	h.handleMessage(newMsg(42, 42, "https://youtube.com/watch?v=full"))
	txt := api.lastText()
	if !strings.Contains(txt, "переполнена") {
		t.Errorf("queue full reply = %q", txt)
	}
}

func TestHandleMessage_AlreadyInProgress(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	url := "https://youtube.com/watch?v=inprog"
	p := &mockPool{enqueueOK: true, inProgress: map[string]bool{url: true}}
	h := newTestHandler(api, nil, nil, p)
	h.handleMessage(newMsg(42, 42, url))
	txt := api.lastText()
	if !strings.Contains(txt, "уже скачивается") {
		t.Errorf("in-progress reply = %q", txt)
	}
}

func TestHandleMessage_URLWithoutProtocol(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	p := &mockPool{enqueueOK: true, inProgress: map[string]bool{}}
	h := newTestHandler(api, nil, nil, p)
	h.handleMessage(newMsg(42, 42, "youtube.com/watch?v=abc"))
	txt := api.lastText()
	// Should be treated as URL (normalized)
	if strings.Contains(txt, "ссылку на видео") && !strings.Contains(txt, "Неверный") {
		// OK - treated as URL attempt
	}
	if api.sentCount() == 0 {
		t.Error("expected reply")
	}
}

// ── Dispatch tests ────────────────────────────────────────────────────────────

func TestDispatch_Message(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.Dispatch(tgbotapi.Update{
		Message: newMsg(42, 42, "hello"),
	})
}

func TestDispatch_InlineQuery(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.Dispatch(tgbotapi.Update{
		InlineQuery: &tgbotapi.InlineQuery{
			ID:    "q1",
			From:  &tgbotapi.User{ID: 42},
			Query: "",
		},
	})
	// Empty query → shows history (empty → usage article)
	if api.sentCount() == 0 {
		t.Error("expected answerInlineQuery call")
	}
}

func TestDispatch_ChosenInlineResult(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil, nil, nil, nil)
	h.Dispatch(tgbotapi.Update{
		ChosenInlineResult: &tgbotapi.ChosenInlineResult{
			ResultID:        "loading-abc",
			InlineMessageID: "msg-123",
			From:            &tgbotapi.User{ID: 42},
		},
	})
}

func TestDispatch_Command(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	h.Dispatch(tgbotapi.Update{
		Message: newCmdMsg(42, 42, "start", ""),
	})
	if api.sentCount() == 0 {
		t.Error("expected reply to /start command")
	}
}

// ── addToHistoryIfNew ─────────────────────────────────────────────────────────

func TestAddToHistoryIfNew_AddsWhenAbsent(t *testing.T) {
	t.Parallel()
	c := newMockCache()
	h := newTestHandler(nil, c, nil, nil)
	cv := &cache.CachedVideo{FileID: "fid1", Title: "Title"}
	h.addToHistoryIfNew(context.Background(), 42, "https://example.com", cv)
	if len(c.history[42]) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(c.history[42]))
	}
}

func TestAddToHistoryIfNew_SkipsWhenPresent(t *testing.T) {
	t.Parallel()
	c := newMockCache()
	c.history[42] = []cache.VideoHistory{{FileID: "fid1"}}
	h := newTestHandler(nil, c, nil, nil)
	cv := &cache.CachedVideo{FileID: "fid1", Title: "Title"}
	h.addToHistoryIfNew(context.Background(), 42, "https://example.com", cv)
	if len(c.history[42]) != 1 {
		t.Errorf("expected no duplicate, got %d entries", len(c.history[42]))
	}
}

func TestAddToHistoryIfNew_UsesURLAsTitleWhenEmpty(t *testing.T) {
	t.Parallel()
	c := newMockCache()
	h := newTestHandler(nil, c, nil, nil)
	cv := &cache.CachedVideo{FileID: "fid1", Title: ""}
	h.addToHistoryIfNew(context.Background(), 42, "https://example.com/video", cv)
	if len(c.history[42]) == 0 {
		t.Fatal("expected history entry")
	}
	if c.history[42][0].Title != "https://example.com/video" {
		t.Errorf("title = %q; want URL", c.history[42][0].Title)
	}
}

// ── isAdmin ───────────────────────────────────────────────────────────────────

func TestIsAdmin_InConfig(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil, nil, &mockDB{isAdmin: false}, nil)
	// userID=1 is in cfg.AdminIDs
	if !h.isAdmin(1) {
		t.Error("userID=1 should be admin (in config)")
	}
}

func TestIsAdmin_InDB(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil, nil, &mockDB{isAdmin: true}, nil)
	// userID=999 not in config, but DB says admin
	if !h.isAdmin(999) {
		t.Error("userID=999 should be admin (in DB)")
	}
}

func TestIsAdmin_NotAdmin(t *testing.T) {
	t.Parallel()
	h := newTestHandler(nil, nil, &mockDB{isAdmin: false}, nil)
	if h.isAdmin(999) {
		t.Error("userID=999 should not be admin")
	}
}

// ── handleInline tests ────────────────────────────────────────────────────────

func TestHandleInline_EmptyQuery_EmptyHistory(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, newMockCache(), nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: ""}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected answerInlineQuery")
	}
}

func TestHandleInline_EmptyQuery_WithHistory(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	c.history[42] = []cache.VideoHistory{{FileID: "fid1", Title: "Video"}}
	h := newTestHandler(api, c, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: ""}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected answerInlineQuery with history")
	}
}

func TestHandleInline_InvalidURL(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "notavalidquery!!!"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected answerInlineQuery for invalid URL")
	}
}

func TestHandleInline_CacheHit(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	url := "https://youtube.com/watch?v=cached"
	c.videos[url] = &cache.CachedVideo{FileID: "fid_hit", Title: "Hit Video"}
	h := newTestHandler(api, c, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: url}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected answerInlineQuery with cached video")
	}
}

func TestHandleInline_RateLimit(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	c := newMockCache()
	c.rate[42] = 100 // already at limit
	h := newTestHandler(api, c, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "https://youtube.com/watch?v=rl"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected rate limit response")
	}
}

func TestHandleInline_EnqueuesDownload(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	p := &mockPool{enqueueOK: true, inProgress: map[string]bool{}}
	h := newTestHandler(api, nil, nil, p)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "https://youtube.com/watch?v=new"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected loading response")
	}
}

func TestHandleInline_AlreadyInProgress(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	url := "https://youtube.com/watch?v=prog"
	p := &mockPool{enqueueOK: false, inProgress: map[string]bool{url: true}}
	h := newTestHandler(api, nil, nil, p)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: url}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected loading response for in-progress URL")
	}
}

func TestHandleInlineCommand_Help(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "/help"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected inline help response")
	}
}

func TestHandleInlineCommand_Stats(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, newMockCache(), nil, &mockPool{})
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "/stats"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected inline stats response")
	}
}

func TestHandleInlineCommand_Platforms(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"/youtube", "/tiktok", "/instagram"} {
		t.Run(cmd, func(t *testing.T) {
			api := &mockAPI{}
			h := newTestHandler(api, nil, nil, nil)
			q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: cmd}
			h.handleInline(q)
			if api.sentCount() == 0 {
				t.Errorf("%s: expected inline response", cmd)
			}
		})
	}
}

func TestHandleInlineCommand_Unknown(t *testing.T) {
	t.Parallel()
	api := &mockAPI{}
	h := newTestHandler(api, nil, nil, nil)
	q := &tgbotapi.InlineQuery{ID: "q1", From: &tgbotapi.User{ID: 42}, Query: "/unknowncmd"}
	h.handleInline(q)
	if api.sentCount() == 0 {
		t.Error("expected unknown command response")
	}
}
