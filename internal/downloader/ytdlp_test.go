package downloader

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSanitizeError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips ERROR: prefix",
			input: "ERROR: some error message",
			want:  "some error message",
		},
		{
			name:  "only available to signed-in users",
			input: "ERROR: This video is only available to signed-in users",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "only available for registered users",
			input: "only available for registered users",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "This video is private",
			input: "ERROR: This video is private",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "Private video",
			input: "Private video",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "members-only",
			input: "This is a members-only video",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "This video is not available",
			input: "This video is not available",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "Video unavailable",
			input: "Video unavailable",
			want:  "это видео недоступно для скачивания",
		},
		{
			name:  "truncates long messages",
			input: strings.Repeat("x", 400),
			want:  strings.Repeat("x", 300) + "…",
		},
		{
			name:  "short message preserved",
			input: "short error",
			want:  "short error",
		},
		{
			name:  "exactly 300 chars not truncated",
			input: strings.Repeat("a", 300),
			want:  strings.Repeat("a", 300),
		},
		{
			name:  "301 chars truncated",
			input: strings.Repeat("b", 301),
			want:  strings.Repeat("b", 300) + "…",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeError(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeError(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

// flagValue returns the token immediately following flag in args.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestBaseArgs(t *testing.T) {
	t.Parallel()
	d := &Downloader{maxSizeMB: 1900}
	args := d.baseArgs("video.%(ext)s")

	// Format selector must request the merged best video + best audio stream,
	// NOT the progressive `best` which caps long YouTube videos at 360p/720p.
	f, ok := flagValue(args, "-f")
	if !ok {
		t.Fatalf("baseArgs missing -f flag: %v", args)
	}
	if f == "best" {
		t.Errorf("-f is progressive %q; want merged selector (regression: caps quality)", f)
	}
	if f != "bv*+ba/b" {
		t.Errorf("-f = %q; want %q", f, "bv*+ba/b")
	}

	// Prefer h264 + ~1080p so Telegram streams the video inline without transcoding.
	sortArg, ok := flagValue(args, "-S")
	if !ok {
		t.Fatalf("baseArgs missing -S format-sort flag: %v", args)
	}
	if !strings.Contains(sortArg, "vcodec:h264") {
		t.Errorf("-S = %q; want it to prefer vcodec:h264", sortArg)
	}
	if !strings.Contains(sortArg, "res:1080") {
		t.Errorf("-S = %q; want it to target res:1080", sortArg)
	}

	// Merged DASH streams must be muxed into an mp4 container.
	if mf, ok := flagValue(args, "--merge-output-format"); !ok || mf != "mp4" {
		t.Errorf("--merge-output-format = %q (present=%v); want %q", mf, ok, "mp4")
	}

	// moov atom must be moved to the front (faststart) so Telegram can read the
	// video dimensions — otherwise merged 16:9 videos display squished to ~1:1.
	if ppa, ok := flagValue(args, "--postprocessor-args"); !ok || !strings.Contains(ppa, "+faststart") {
		t.Errorf("--postprocessor-args = %q (present=%v); want it to contain +faststart", ppa, ok)
	}

	// duration must be printed alongside the title so it can be sent to Telegram.
	if pr, ok := flagValue(args, "--print"); !ok || !strings.Contains(pr, "duration") {
		t.Errorf("--print = %q (present=%v); want it to include duration", pr, ok)
	}

	// Size limit must reflect the configured maxSizeMB.
	if !slices.Contains(args, "--max-filesize=1900m") {
		t.Errorf("missing --max-filesize=1900m in %v", args)
	}

	// Output template is passed through unchanged.
	if o, ok := flagValue(args, "-o"); !ok || o != "video.%(ext)s" {
		t.Errorf("-o = %q (present=%v); want %q", o, ok, "video.%(ext)s")
	}
}

func TestParseDurationSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"integer seconds", "212", 212},
		{"float seconds", "212.0", 212},
		{"float truncates down", "212.9", 212},
		{"leading/trailing space", "  120.5  ", 120},
		{"NA is zero", "NA", 0},
		{"empty is zero", "", 0},
		{"garbage is zero", "abc", 0},
		{"negative is zero", "-5", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDurationSeconds(tc.in); got != tc.want {
				t.Errorf("parseDurationSeconds(%q) = %d; want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestPlatformArgs(t *testing.T) {
	t.Parallel()
	d := &Downloader{vkUsername: "user", vkPassword: "pass"}

	tests := []struct {
		name        string
		url         string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "tiktok.com gets impersonate flag",
			url:         "https://www.tiktok.com/@user/video/123",
			wantContain: []string{"--impersonate", "chrome"},
		},
		{
			name:        "vm.tiktok.com gets impersonate flag",
			url:         "https://vm.tiktok.com/abcdef",
			wantContain: []string{"--impersonate", "chrome"},
		},
		{
			name:        "vk.com with credentials",
			url:         "https://vk.com/video123",
			wantContain: []string{"--username", "user", "--password", "pass"},
			wantAbsent:  []string{"--impersonate"},
		},
		{
			name:        "vk.ru with credentials",
			url:         "https://vk.ru/video123",
			wantContain: []string{"--username", "user", "--password", "pass"},
		},
		{
			name:        "vkvideo.ru with credentials",
			url:         "https://vkvideo.ru/video123",
			wantContain: []string{"--username"},
		},
		{
			name:       "youtube returns nil args",
			url:        "https://www.youtube.com/watch?v=abc",
			wantAbsent: []string{"--impersonate", "--username"},
		},
		{
			name:       "instagram returns nil args",
			url:        "https://instagram.com/p/abc",
			wantAbsent: []string{"--impersonate", "--username"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := d.platformArgs(tc.url)
			for _, want := range tc.wantContain {
				found := false
				for _, a := range args {
					if a == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("platformArgs(%q): want arg %q in %v", tc.url, want, args)
				}
			}
			for _, absent := range tc.wantAbsent {
				for _, a := range args {
					if a == absent {
						t.Errorf("platformArgs(%q): unexpected arg %q in %v", tc.url, absent, args)
					}
				}
			}
		})
	}
}

func TestPlatformArgs_NoCredentials(t *testing.T) {
	t.Parallel()
	d := &Downloader{} // no VK credentials
	args := d.platformArgs("https://vk.com/video123")
	if len(args) != 0 {
		t.Errorf("expected no args for VK without credentials, got %v", args)
	}
}

func TestNew_CreatesDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "dl_base")
	d := New(dir, 100, "", "", "")
	if d == nil {
		t.Fatal("New returned nil")
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Errorf("New should create base directory %q", dir)
	}
	if d.baseDir != dir {
		t.Errorf("baseDir = %q; want %q", d.baseDir, dir)
	}
	if d.maxSizeMB != 100 {
		t.Errorf("maxSizeMB = %d; want 100", d.maxSizeMB)
	}
}

func TestNew_WithCookiesAndVK(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	d := New(dir, 200, "/tmp/cookies.txt", "vkuser", "vkpass")
	if d.cookiesFile != "/tmp/cookies.txt" {
		t.Errorf("cookiesFile = %q", d.cookiesFile)
	}
	if d.vkUsername != "vkuser" {
		t.Errorf("vkUsername = %q", d.vkUsername)
	}
	if d.vkPassword != "vkpass" {
		t.Errorf("vkPassword = %q", d.vkPassword)
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()
	d := &Downloader{}

	t.Run("removes existing directory", func(t *testing.T) {
		tmp := t.TempDir()
		// create a file inside to confirm it's non-empty
		f := filepath.Join(tmp, "test.mp4")
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		d.Cleanup(tmp)
		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Errorf("expected directory %q to be removed", tmp)
		}
	})

	t.Run("no-op on empty string", func(t *testing.T) {
		// should not panic
		d.Cleanup("")
	})

	t.Run("no-op on non-existent path", func(t *testing.T) {
		d.Cleanup("/nonexistent/path/that/does/not/exist")
	})
}

func TestIsAvailable(t *testing.T) {
	t.Parallel()
	// Either true or false depending on environment — just ensure no panic
	_ = IsAvailable()
}

func TestVersion_NoYtDlp(t *testing.T) {
	t.Parallel()
	// If yt-dlp is not on PATH, should return "unknown" without panic
	v := Version(context.Background())
	if v == "" {
		t.Error("Version should return non-empty string")
	}
	// Either a real version string or "unknown"
}

func TestUnavailableError(t *testing.T) {
	t.Parallel()
	err := &UnavailableError{msg: "это видео недоступно для скачивания"}
	if err.Error() != "это видео недоступно для скачивания" {
		t.Errorf("UnavailableError.Error() = %q", err.Error())
	}
}
