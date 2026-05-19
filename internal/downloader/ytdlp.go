package downloader

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result represents a successfully downloaded video.
type Result struct {
	FilePath string // absolute path to the .mp4 file
	Title    string // video title
	TmpDir   string // temp directory — caller must call Cleanup
}

// Downloader wraps the yt-dlp binary.
type Downloader struct {
	baseDir   string
	maxSizeMB int
}

func New(baseDir string, maxSizeMB int) *Downloader {
	_ = os.MkdirAll(baseDir, 0o755)
	return &Downloader{baseDir: baseDir, maxSizeMB: maxSizeMB}
}

// Download fetches the video at url.
// The caller MUST invoke Cleanup(result.TmpDir) when done with the file.
func (d *Downloader) Download(ctx context.Context, url string) (*Result, error) {
	tmpDir, err := os.MkdirTemp(d.baseDir, "dl-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	outputTemplate := "%(title).80s.%(ext)s"

	args := []string{
		"-f", "best",
		"--no-playlist",
		fmt.Sprintf("--max-filesize=%dm", d.maxSizeMB),
		"-o", outputTemplate,
		"--no-warnings",
		"--print", "title",
		"--no-simulate",
		url,
	}

	cmd := exec.CommandContext(ctx, "/usr/local/bin/yt-dlp", args...)
	cmd.Dir = tmpDir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check directory contents before cleanup
		entries, _ := os.ReadDir(tmpDir)
		fmt.Fprintf(os.Stderr, "yt-dlp failed, dir contents: %d files\n", len(entries))
		for _, e := range entries {
			fmt.Fprintf(os.Stderr, "  - %s\n", e.Name())
		}
		os.RemoveAll(tmpDir)
		stderrStr := stderr.String()
		stdoutStr := stdout.String()
		msg := strings.TrimSpace(stderrStr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp: %s (stderr: %s, stdout: %s)", sanitizeError(msg), stderrStr, stdoutStr)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		fmt.Fprintf(os.Stderr, "yt-dlp succeeded but no files found, dir: %s, err: %v\n", tmpDir, err)
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("yt-dlp produced no output (unsupported URL or file exceeds %d MB)", d.maxSizeMB)
	}

	filePath := filepath.Join(tmpDir, entries[0].Name())

	// --print title outputs the title as first line of stdout
	title := ""
	if raw := strings.TrimSpace(stdout.String()); raw != "" {
		title = strings.SplitN(raw, "\n", 2)[0]
		title = strings.TrimSpace(title)
	}
	if title == "" {
		title = strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	}

	return &Result{FilePath: filePath, Title: title, TmpDir: tmpDir}, nil
}

// Cleanup removes the temporary download directory.
func (d *Downloader) Cleanup(tmpDir string) {
	if tmpDir != "" {
		_ = os.RemoveAll(tmpDir)
	}
}

// IsAvailable checks whether yt-dlp is on PATH.
func IsAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// Version returns the installed yt-dlp version.
func Version(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "yt-dlp", "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// sanitizeError trims noisy yt-dlp stderr prefixes.
func sanitizeError(s string) string {
	s = strings.TrimPrefix(s, "ERROR: ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
