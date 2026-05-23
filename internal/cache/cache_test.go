package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestCache spins up an in-memory Redis server and returns a Cache wired to it.
// The server is automatically stopped when t finishes.
func newTestCache(t *testing.T) *Cache {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Cache{
		client: client,
		ttl:    time.Hour,
		hotTTL: 7 * 24 * time.Hour,
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	c := New(mr.Addr(), "", 0, 4, time.Hour, 7*24*time.Hour)
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.ttl != time.Hour {
		t.Errorf("ttl = %v; want 1h", c.ttl)
	}
	if c.hotTTL != 7*24*time.Hour {
		t.Errorf("hotTTL = %v; want 168h", c.hotTTL)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping after New: %v", err)
	}
}

func TestPing(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// --- GetCachedVideo / SetCachedVideo ---

func TestSetAndGetCachedVideo(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()

	url := "https://youtube.com/watch?v=abc"
	fileID := "file_id_xyz"
	title := "My Video"

	if err := c.SetCachedVideo(ctx, url, fileID, title); err != nil {
		t.Fatalf("SetCachedVideo: %v", err)
	}

	got, err := c.GetCachedVideo(ctx, url)
	if err != nil {
		t.Fatalf("GetCachedVideo: %v", err)
	}
	if got == nil {
		t.Fatal("GetCachedVideo: expected non-nil result")
	}
	if got.FileID != fileID {
		t.Errorf("FileID = %q; want %q", got.FileID, fileID)
	}
	if got.Title != title {
		t.Errorf("Title = %q; want %q", got.Title, title)
	}
}

func TestGetCachedVideo_Miss(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	got, err := c.GetCachedVideo(context.Background(), "https://not-cached.example.com")
	if err != nil {
		t.Fatalf("GetCachedVideo: %v", err)
	}
	if got != nil {
		t.Errorf("GetCachedVideo miss: expected nil, got %+v", got)
	}
}

func TestGetCachedVideo_BackwardCompat(t *testing.T) {
	t.Parallel()
	// Old format: raw file_id string (not JSON) stored in Redis
	c := newTestCache(t)
	ctx := context.Background()

	url := "https://old.example.com/video"
	key := fileKey(url)
	// Store bare string (legacy format)
	if err := c.client.Set(ctx, key, "raw_file_id_legacy", time.Hour).Err(); err != nil {
		t.Fatalf("set legacy value: %v", err)
	}

	got, err := c.GetCachedVideo(ctx, url)
	if err != nil {
		t.Fatalf("GetCachedVideo: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil for legacy entry")
	}
	if got.FileID != "raw_file_id_legacy" {
		t.Errorf("FileID = %q; want legacy value", got.FileID)
	}
}

// --- fileKey ---

func TestFileKey_Deterministic(t *testing.T) {
	t.Parallel()
	a := fileKey("https://youtube.com/watch?v=abc")
	b := fileKey("https://youtube.com/watch?v=abc")
	if a != b {
		t.Errorf("fileKey not deterministic: %q != %q", a, b)
	}
}

func TestFileKey_Unique(t *testing.T) {
	t.Parallel()
	a := fileKey("https://youtube.com/watch?v=abc")
	b := fileKey("https://youtube.com/watch?v=xyz")
	if a == b {
		t.Error("fileKey collision for different URLs")
	}
}

func TestFileKey_HasPrefix(t *testing.T) {
	t.Parallel()
	k := fileKey("https://example.com")
	if len(k) < 5 || k[:5] != "file:" {
		t.Errorf("fileKey = %q; expected prefix 'file:'", k)
	}
}

// --- IncrRateLimit ---

func TestIncrRateLimit(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(42)

	first, err := c.IncrRateLimit(ctx, userID)
	if err != nil {
		t.Fatalf("IncrRateLimit #1: %v", err)
	}
	if first != 1 {
		t.Errorf("first increment = %d; want 1", first)
	}

	second, err := c.IncrRateLimit(ctx, userID)
	if err != nil {
		t.Fatalf("IncrRateLimit #2: %v", err)
	}
	if second != 2 {
		t.Errorf("second increment = %d; want 2", second)
	}
}

func TestIncrRateLimit_DifferentUsers(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()

	v1, _ := c.IncrRateLimit(ctx, 1)
	v2, _ := c.IncrRateLimit(ctx, 2)
	if v1 != 1 || v2 != 1 {
		t.Errorf("different users should have independent counters: user1=%d user2=%d", v1, v2)
	}
}

// --- CountCached ---

func TestCountCached(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()

	n, err := c.CountCached(ctx)
	if err != nil {
		t.Fatalf("CountCached: %v", err)
	}
	if n != 0 {
		t.Errorf("CountCached empty cache = %d; want 0", n)
	}

	_ = c.SetCachedVideo(ctx, "https://a.example.com", "fid1", "title1")
	_ = c.SetCachedVideo(ctx, "https://b.example.com", "fid2", "title2")

	n, err = c.CountCached(ctx)
	if err != nil {
		t.Fatalf("CountCached after 2 inserts: %v", err)
	}
	if n != 2 {
		t.Errorf("CountCached = %d; want 2", n)
	}
}

// --- VideoHistory ---

func TestAddAndGetVideoHistory(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(100)

	videos := []VideoHistory{
		{FileID: "fid1", Title: "Video 1", AddedAt: time.Now()},
		{FileID: "fid2", Title: "Video 2", AddedAt: time.Now()},
	}
	for _, v := range videos {
		if err := c.AddVideoHistory(ctx, userID, v); err != nil {
			t.Fatalf("AddVideoHistory: %v", err)
		}
	}

	history, err := c.GetVideoHistory(ctx, userID)
	if err != nil {
		t.Fatalf("GetVideoHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("GetVideoHistory len = %d; want 2", len(history))
	}
	// LPUSH → newest first
	if history[0].FileID != "fid2" {
		t.Errorf("history[0].FileID = %q; want fid2 (newest first)", history[0].FileID)
	}
}

func TestGetVideoHistory_Empty(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	history, err := c.GetVideoHistory(context.Background(), 999)
	if err != nil {
		t.Fatalf("GetVideoHistory: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("GetVideoHistory empty = %d; want 0", len(history))
	}
}

func TestVideoHistory_Capped50(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(200)

	for i := 0; i < 60; i++ {
		_ = c.AddVideoHistory(ctx, userID, VideoHistory{
			FileID: "fid" + string(rune('a'+i%26)),
			Title:  "Video",
		})
	}
	history, err := c.GetVideoHistory(ctx, userID)
	if err != nil {
		t.Fatalf("GetVideoHistory: %v", err)
	}
	if len(history) > 50 {
		t.Errorf("history len = %d; want <= 50", len(history))
	}
}

func TestGetVideoHistory_SkipsInvalidJSON(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(600)

	key := fmt.Sprintf("history:%d", userID)
	// Push one valid and one invalid JSON entry directly
	_ = c.client.RPush(ctx, key, `{"FileID":"valid","Title":"Valid"}`)
	_ = c.client.RPush(ctx, key, `not-valid-json`)

	history, err := c.GetVideoHistory(ctx, userID)
	if err != nil {
		t.Fatalf("GetVideoHistory: %v", err)
	}
	if len(history) != 1 {
		t.Errorf("GetVideoHistory should skip invalid JSON: got %d entries, want 1", len(history))
	}
	if history[0].FileID != "valid" {
		t.Errorf("first entry FileID = %q; want 'valid'", history[0].FileID)
	}
}

func TestClearVideoHistory(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(300)

	_ = c.AddVideoHistory(ctx, userID, VideoHistory{FileID: "fid", Title: "Video"})
	if err := c.ClearVideoHistory(ctx, userID); err != nil {
		t.Fatalf("ClearVideoHistory: %v", err)
	}
	history, _ := c.GetVideoHistory(ctx, userID)
	if len(history) != 0 {
		t.Errorf("after Clear, history len = %d; want 0", len(history))
	}
}

// --- IncrInlineVersion / GetInlineVersion ---

func TestInlineVersion(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(400)

	v := c.GetInlineVersion(ctx, userID)
	if v != 0 {
		t.Errorf("initial inline version = %d; want 0", v)
	}

	if err := c.IncrInlineVersion(ctx, userID); err != nil {
		t.Fatalf("IncrInlineVersion: %v", err)
	}
	v = c.GetInlineVersion(ctx, userID)
	if v != 1 {
		t.Errorf("after first incr, version = %d; want 1", v)
	}

	_ = c.IncrInlineVersion(ctx, userID)
	_ = c.IncrInlineVersion(ctx, userID)
	v = c.GetInlineVersion(ctx, userID)
	if v != 3 {
		t.Errorf("after 3 incrs, version = %d; want 3", v)
	}
}

func TestInlineVersion_DifferentUsers(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()

	_ = c.IncrInlineVersion(ctx, 1)
	_ = c.IncrInlineVersion(ctx, 1)
	_ = c.IncrInlineVersion(ctx, 2)

	v1 := c.GetInlineVersion(ctx, 1)
	v2 := c.GetInlineVersion(ctx, 2)
	if v1 != 2 || v2 != 1 {
		t.Errorf("user1 version=%d (want 2), user2 version=%d (want 1)", v1, v2)
	}
}

// --- RefreshFileIDTTL ---

func TestRefreshFileIDTTL_ColdVideo(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(501)

	url := "https://cold.example.com/video"
	// Store video but do NOT add to history (cold)
	_ = c.SetCachedVideo(ctx, url, "coldfid", "Cold Video")

	// Other video in history (not this one)
	_ = c.AddVideoHistory(ctx, userID, VideoHistory{FileID: "otherfid", Title: "Other"})

	if err := c.RefreshFileIDTTL(ctx, url, userID); err != nil {
		t.Errorf("RefreshFileIDTTL cold video: %v", err)
	}
}

func TestRefreshFileIDTTL_HotVideo(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(500)

	url := "https://hot.example.com/video"
	_ = c.SetCachedVideo(ctx, url, "hotfid", "Hot Video")

	// Add to history so the video is "hot" (in top-3)
	_ = c.AddVideoHistory(ctx, userID, VideoHistory{FileID: "hotfid", Title: "Hot Video"})

	// Should not return error even for hot video
	if err := c.RefreshFileIDTTL(ctx, url, userID); err != nil {
		t.Errorf("RefreshFileIDTTL: %v", err)
	}
}

func TestRefreshFileIDTTL_LegacyBareString(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	ctx := context.Background()
	userID := int64(502)

	url := "https://legacy-refresh.example.com/video"
	key := fileKey(url)
	// Store bare string (legacy format) so json.Unmarshal fails → backward compat
	if err := c.client.Set(ctx, key, "legacy_fid_bare", time.Hour).Err(); err != nil {
		t.Fatalf("set legacy value: %v", err)
	}
	// Add this fid to history (hot) to exercise the isHot = true branch
	_ = c.AddVideoHistory(ctx, userID, VideoHistory{FileID: "legacy_fid_bare", Title: "Legacy"})

	if err := c.RefreshFileIDTTL(ctx, url, userID); err != nil {
		t.Errorf("RefreshFileIDTTL legacy bare string: %v", err)
	}
}

func TestRefreshFileIDTTL_MissingKey(t *testing.T) {
	t.Parallel()
	c := newTestCache(t)
	// Should silently return nil for missing key
	err := c.RefreshFileIDTTL(context.Background(), "https://not-cached.example.com", 999)
	if err != nil {
		t.Errorf("RefreshFileIDTTL on missing key: %v", err)
	}
}
