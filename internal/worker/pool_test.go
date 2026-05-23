package worker

import (
	"log/slog"
	"os"
	"testing"
)

// newTestPool creates a Pool with nil external deps — safe for testing
// methods that only touch sync.Map / jobs channel.
func newTestPool(size int) *Pool {
	return &Pool{
		size: size,
		jobs: make(chan Job, size*4),
		log:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// --- truncateCaption ---

func TestTruncateCaption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short string unchanged", "hello", "hello"},
		{"exactly 1000 chars unchanged", string(make([]byte, 1000)), string(make([]byte, 1000))},
		{"1001 chars truncated with ellipsis", string(make([]byte, 1001)), string(make([]byte, 997)) + "..."},
		{"empty string unchanged", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateCaption(tc.input)
			if got != tc.want {
				t.Errorf("truncateCaption(%d chars) = %d chars; want %d", len(tc.input), len(got), len(tc.want))
			}
		})
	}
}

// --- Enqueue / IsInProgress / QueueLen ---

func TestEnqueue_Basic(t *testing.T) {
	t.Parallel()
	p := newTestPool(2)

	job := Job{URL: "https://example.com/video", UserID: 1}
	if !p.Enqueue(job) {
		t.Fatal("first Enqueue should return true")
	}
	if p.QueueLen() != 1 {
		t.Errorf("QueueLen = %d; want 1", p.QueueLen())
	}
}

func TestEnqueue_Deduplication(t *testing.T) {
	t.Parallel()
	p := newTestPool(2)

	job := Job{URL: "https://example.com/video", UserID: 1}
	if !p.Enqueue(job) {
		t.Fatal("first Enqueue should succeed")
	}
	if p.Enqueue(job) {
		t.Error("duplicate Enqueue should return false")
	}
	if p.QueueLen() != 1 {
		t.Errorf("QueueLen = %d; want 1 (no duplicate)", p.QueueLen())
	}
}

func TestEnqueue_QueueFull(t *testing.T) {
	t.Parallel()
	// size=1 → jobs channel capacity = 1*4 = 4
	p := newTestPool(1)
	capacity := cap(p.jobs) // 4

	// fill up the channel
	for i := 0; i < capacity; i++ {
		ok := p.Enqueue(Job{URL: fakeURL(i), UserID: int64(i)})
		if !ok {
			t.Fatalf("Enqueue %d should succeed (capacity=%d)", i, capacity)
		}
	}
	// next one must fail (channel full)
	if p.Enqueue(Job{URL: fakeURL(capacity), UserID: 99}) {
		t.Error("Enqueue on full queue should return false")
	}
}

func TestIsInProgress(t *testing.T) {
	t.Parallel()
	p := newTestPool(4)

	url := "https://example.com/video"
	if p.IsInProgress(url) {
		t.Error("IsInProgress before enqueue should be false")
	}
	p.Enqueue(Job{URL: url, UserID: 1})
	if !p.IsInProgress(url) {
		t.Error("IsInProgress after enqueue should be true")
	}
}

func TestIsInProgress_AfterQueueFull(t *testing.T) {
	t.Parallel()
	// When queue is full, Enqueue removes from inProgress
	p := newTestPool(1)
	// fill the channel
	for i := 0; i < cap(p.jobs); i++ {
		p.Enqueue(Job{URL: fakeURL(i), UserID: int64(i)})
	}

	url := "https://overflow.example.com"
	p.Enqueue(Job{URL: url, UserID: 99})
	// should NOT be in progress — was rolled back
	if p.IsInProgress(url) {
		t.Error("IsInProgress should be false when Enqueue returned false due to full queue")
	}
}

func TestQueueLen(t *testing.T) {
	t.Parallel()
	p := newTestPool(4)
	if p.QueueLen() != 0 {
		t.Errorf("initial QueueLen = %d; want 0", p.QueueLen())
	}
	for i := 0; i < 3; i++ {
		p.Enqueue(Job{URL: fakeURL(i), UserID: int64(i)})
	}
	if p.QueueLen() != 3 {
		t.Errorf("QueueLen = %d; want 3", p.QueueLen())
	}
}

// --- SetInlineMessageID / GetInlineMessageID ---

func TestSetInlineMessageID_NotFound(t *testing.T) {
	t.Parallel()
	p := newTestPool(4)
	// resultID not associated with any in-progress job → returns false
	found := p.SetInlineMessageID("loading-abc", "msg-123")
	if found {
		t.Error("SetInlineMessageID with no matching job should return false")
	}
	_, ok := p.GetInlineMessageID("loading-abc")
	if ok {
		t.Error("GetInlineMessageID should return false when not stored")
	}
}

func TestSetInlineMessageID_Found(t *testing.T) {
	t.Parallel()
	p := newTestPool(4)
	resultID := "loading-abc123"

	// Manually store a job in inProgress with a ResultID
	job := Job{URL: "https://example.com/vid", UserID: 1, ResultID: resultID}
	p.inProgress.Store(job.URL, job)

	found := p.SetInlineMessageID(resultID, "inline-msg-999")
	if !found {
		t.Error("SetInlineMessageID should return true when job with matching ResultID is in progress")
	}

	msgID, ok := p.GetInlineMessageID(resultID)
	if !ok {
		t.Fatal("GetInlineMessageID should return true after SetInlineMessageID")
	}
	if msgID != "inline-msg-999" {
		t.Errorf("GetInlineMessageID = %q; want %q", msgID, "inline-msg-999")
	}
}

func TestGetInlineMessageID_Missing(t *testing.T) {
	t.Parallel()
	p := newTestPool(2)
	_, ok := p.GetInlineMessageID("nonexistent")
	if ok {
		t.Error("GetInlineMessageID for unknown key should return false")
	}
}

// fakeURL generates a unique URL string for test cases.
func fakeURL(i int) string {
	return "https://example.com/video/" + string(rune('a'+i%26)) + string(rune('0'+i%10))
}
