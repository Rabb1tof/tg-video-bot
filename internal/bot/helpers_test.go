package bot

import (
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// --- extractUser ---

func TestExtractUser(t *testing.T) {
	t.Parallel()
	user := &tgbotapi.User{ID: 42, FirstName: "Alice"}

	tests := []struct {
		name    string
		update  tgbotapi.Update
		wantID  int64
		wantNil bool
	}{
		{
			name: "message with from",
			update: tgbotapi.Update{
				Message: &tgbotapi.Message{From: user},
			},
			wantID: 42,
		},
		{
			name: "inline query",
			update: tgbotapi.Update{
				InlineQuery: &tgbotapi.InlineQuery{From: user},
			},
			wantID: 42,
		},
		{
			name: "chosen inline result",
			update: tgbotapi.Update{
				ChosenInlineResult: &tgbotapi.ChosenInlineResult{From: user},
			},
			wantID: 42,
		},
		{
			name:    "empty update returns nil",
			update:  tgbotapi.Update{},
			wantNil: true,
		},
		{
			name: "message without from returns nil",
			update: tgbotapi.Update{
				Message: &tgbotapi.Message{From: nil},
			},
			wantNil: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUser(tc.update)
			if tc.wantNil {
				if got != nil {
					t.Errorf("extractUser() = %v; want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("extractUser() = nil; want non-nil")
			}
			if got.ID != tc.wantID {
				t.Errorf("extractUser().ID = %d; want %d", got.ID, tc.wantID)
			}
		})
	}
}

// --- downloadWord ---

func TestDownloadWord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int64
		want string
	}{
		{0, "загрузок"},
		{1, "загрузка"},
		{2, "загрузки"},
		{3, "загрузки"},
		{4, "загрузки"},
		{5, "загрузок"},
		{10, "загрузок"},
		{11, "загрузок"},
		{12, "загрузок"},
		{19, "загрузок"},
		{20, "загрузок"},
		{21, "загрузка"},
		{22, "загрузки"},
		{100, "загрузок"},
		{101, "загрузка"},
		{111, "загрузок"},
		{1000001, "загрузка"},
	}
	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			got := downloadWord(tc.n)
			if got != tc.want {
				t.Errorf("downloadWord(%d) = %q; want %q", tc.n, got, tc.want)
			}
		})
	}
}

// --- formatUptime ---

func TestFormatUptime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0м"},
		{30 * time.Minute, "30м"},
		{59 * time.Minute, "59м"},
		{time.Hour, "1ч 0м"},
		{time.Hour + 30*time.Minute, "1ч 30м"},
		{23*time.Hour + 59*time.Minute, "23ч 59м"},
		{24 * time.Hour, "1д 0ч 0м"},
		{25*time.Hour + 15*time.Minute, "1д 1ч 15м"},
		{48 * time.Hour, "2д 0ч 0м"},
		{48*time.Hour + 3*time.Hour + 7*time.Minute, "2д 3ч 7м"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := formatUptime(tc.d)
			if got != tc.want {
				t.Errorf("formatUptime(%v) = %q; want %q", tc.d, got, tc.want)
			}
		})
	}
}

// --- isValidURL ---

func TestIsValidURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want bool
	}{
		{"https://youtube.com/watch?v=abc", true},
		{"http://example.com/path", true},
		{"https://tiktok.com/@user/video/123", true},
		{"https://vk.com/video-123", true},
		// empty → prepends "https://" → ParseRequestURI succeeds with scheme "https"
		{"", true},
		// space in URL → ParseRequestURI fails
		{"not a url", false},
		// ftp:// → prepends "https://" → scheme becomes "https" → valid
		{"ftp://example.com/file", true},
		// javascript: → prepends "https://javascript:alert(1)" → invalid port → false
		{"javascript:alert(1)", false},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			got := isValidURL(tc.url)
			if got != tc.want {
				t.Errorf("isValidURL(%q) = %v; want %v", tc.url, got, tc.want)
			}
		})
	}
}

// --- shortHash ---

func TestShortHash(t *testing.T) {
	t.Parallel()
	t.Run("deterministic", func(t *testing.T) {
		a := shortHash("https://youtube.com/watch?v=abc")
		b := shortHash("https://youtube.com/watch?v=abc")
		if a != b {
			t.Errorf("shortHash not deterministic: %q != %q", a, b)
		}
	})
	t.Run("different inputs produce different hashes", func(t *testing.T) {
		a := shortHash("https://youtube.com/watch?v=abc")
		b := shortHash("https://youtube.com/watch?v=xyz")
		if a == b {
			t.Errorf("shortHash collision for different inputs: both %q", a)
		}
	})
	t.Run("hex string length is 8", func(t *testing.T) {
		h := shortHash("any string")
		if len(h) != 8 {
			t.Errorf("shortHash len = %d; want 8", len(h))
		}
	})
	t.Run("empty string produces valid hash", func(t *testing.T) {
		h := shortHash("")
		if len(h) != 8 {
			t.Errorf("shortHash('') len = %d; want 8", len(h))
		}
	})
}
