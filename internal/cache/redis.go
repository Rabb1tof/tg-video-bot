package cache

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// VideoHistory represents a video in user's history.
type VideoHistory struct {
	FileID  string
	Title   string
	AddedAt time.Time
}

// Cache wraps Redis with high-level bot operations.
type Cache struct {
	client *redis.Client
	ttl    time.Duration
	hotTTL time.Duration
}

func New(addr, password string, db, poolSize int, ttl, hotTTL time.Duration) *Cache {
	return &Cache{
		client: redis.NewClient(&redis.Options{
			Addr:         addr,
			Password:     password,
			DB:           db,
			PoolSize:     poolSize,
			MinIdleConns: poolSize / 4,
		}),
		ttl:    ttl,
		hotTTL: hotTTL,
	}
}

func (c *Cache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

// GetFileID returns the Telegram file_id cached for the URL, or "" if not found.
func (c *Cache) GetFileID(ctx context.Context, url string) (string, error) {
	val, err := c.client.Get(ctx, fileKey(url)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cache get: %w", err)
	}
	return val, nil
}

// SetFileID stores a file_id for a URL with the configured TTL.
func (c *Cache) SetFileID(ctx context.Context, url, fileID string) error {
	return c.client.Set(ctx, fileKey(url), fileID, c.ttl).Err()
}

// RefreshFileIDTTL продлевает TTL file_id если видео находится в топ-3 истории пользователя.
// Вызывается при каждом cache-хите.
func (c *Cache) RefreshFileIDTTL(ctx context.Context, url string, userID int64) error {
	fileID, err := c.client.Get(ctx, fileKey(url)).Result()
	if err != nil {
		return nil
	}

	// Проверяем, есть ли этот fileID в топ-3 истории пользователя
	key := fmt.Sprintf("history:%d", userID)
	vals, err := c.client.LRange(ctx, key, 0, 2).Result()
	if err != nil {
		return nil
	}

	isHot := false
	for _, val := range vals {
		var v VideoHistory
		if json.Unmarshal([]byte(val), &v) == nil && v.FileID == fileID {
			isHot = true
			break
		}
	}

	ttl := c.ttl
	if isHot {
		ttl = c.hotTTL
	}
	return c.client.Expire(ctx, fileKey(url), ttl).Err()
}

// IncrRateLimit increments the per-user hourly request counter.
// The counter resets automatically after 1 hour.
func (c *Cache) IncrRateLimit(ctx context.Context, userID int64) (int64, error) {
	key := fmt.Sprintf("rate:%d", userID)
	pipe := c.client.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("rate limit pipeline: %w", err)
	}
	return incr.Val(), nil
}

// CountCached returns the approximate number of cached file_ids.
func (c *Cache) CountCached(ctx context.Context) (int64, error) {
	keys, err := c.client.Keys(ctx, "file:*").Result()
	if err != nil {
		return 0, err
	}
	return int64(len(keys)), nil
}

func fileKey(url string) string {
	h := sha256.Sum256([]byte(url))
	return fmt.Sprintf("file:%x", h[:16])
}

// AddVideoHistory adds a video to user's history.
func (c *Cache) AddVideoHistory(ctx context.Context, userID int64, video VideoHistory) error {
	key := fmt.Sprintf("history:%d", userID)
	data, err := json.Marshal(video)
	if err != nil {
		return fmt.Errorf("marshal video history: %w", err)
	}

	// Add to the beginning of the list (LPUSH)
	if err := c.client.LPush(ctx, key, data).Err(); err != nil {
		return fmt.Errorf("lpush video history: %w", err)
	}

	// Keep only last 50 videos
	if err := c.client.LTrim(ctx, key, 0, 49).Err(); err != nil {
		return fmt.Errorf("ltrim video history: %w", err)
	}

	// Set expiration to 30 days
	if err := c.client.Expire(ctx, key, 30*24*time.Hour).Err(); err != nil {
		return fmt.Errorf("expire video history: %w", err)
	}

	return nil
}

// GetVideoHistory retrieves user's video history.
func (c *Cache) GetVideoHistory(ctx context.Context, userID int64) ([]VideoHistory, error) {
	key := fmt.Sprintf("history:%d", userID)
	vals, err := c.client.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		if err == redis.Nil {
			return []VideoHistory{}, nil
		}
		return nil, fmt.Errorf("lrange video history: %w", err)
	}

	var history []VideoHistory
	for _, val := range vals {
		var video VideoHistory
		if err := json.Unmarshal([]byte(val), &video); err != nil {
			continue // Skip invalid entries
		}
		history = append(history, video)
	}

	return history, nil
}

// ClearVideoHistory clears user's video history.
func (c *Cache) ClearVideoHistory(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("history:%d", userID)
	return c.client.Del(ctx, key).Err()
}

// IncrInlineVersion увеличивает версию inline-кэша пользователя.
// Вызывается после успешной загрузки — Telegram увидит новые result_id
// и не будет отдавать кэшированный пустой список.
func (c *Cache) IncrInlineVersion(ctx context.Context, userID int64) error {
	key := fmt.Sprintf("inline_ver:%d", userID)
	pipe := c.client.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 30*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// GetInlineVersion возвращает текущую версию inline-кэша пользователя.
func (c *Cache) GetInlineVersion(ctx context.Context, userID int64) int64 {
	key := fmt.Sprintf("inline_ver:%d", userID)
	v, _ := c.client.Get(ctx, key).Int64()
	return v
}
