package downloader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"tg-media-bot/internal/config"
)

// DownloadResult contains media info after successful download
type DownloadResult struct {
	FilePath      string
	ThumbnailPath string
	Title         string
	Duration      int
	FileSize      int64
	Platform      string
}

// MediaMetadata extracted from yt-dlp JSON dump
type MediaMetadata struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
	Filesize  int64   `json:"filesize"`
	Ext       string  `json:"ext"`
}

// Downloader manages downloading tasks, concurrency, and process lifecycles
type Downloader struct {
	cfg            *config.Config
	semaphore      chan struct{}
	userCancels    sync.Map // map[int64]context.CancelFunc
	activeUsers    sync.Map // map[int64]struct{}
}

// NewDownloader creates a new Downloader instance
func NewDownloader(cfg *config.Config) *Downloader {
	return &Downloader{
		cfg:       cfg,
		semaphore: make(chan struct{}, cfg.MaxConcurrentDownloads),
	}
}

// HasActiveDownload checks if a user currently has a running download task
func (d *Downloader) HasActiveDownload(userID int64) bool {
	_, ok := d.activeUsers.Load(userID)
	return ok
}

// CancelUserDownload aborts the current download for the user if any exists
func (d *Downloader) CancelUserDownload(userID int64) bool {
	if cancelVal, ok := d.userCancels.Load(userID); ok {
		if cancel, isFunc := cancelVal.(context.CancelFunc); isFunc {
			cancel()
			d.userCancels.Delete(userID)
			d.activeUsers.Delete(userID)
			return true
		}
	}
	return false
}

func getCookieArgs() []string {
	// Auto-populate cookies file if COOKIES_DATA env variable is provided
	if raw := os.Getenv("COOKIES_DATA"); strings.TrimSpace(raw) != "" {
		for _, target := range []string{"data/cookies.txt", "/app/data/cookies.txt"} {
			if _, err := os.Stat(target); os.IsNotExist(err) {
				_ = os.MkdirAll(filepath.Dir(target), 0755)
				_ = os.WriteFile(target, []byte(raw), 0644)
				break
			}
		}
	}

	cookieLocations := []string{
		"cookies.txt",
		"data/cookies.txt",
		"/app/data/cookies.txt",
	}
	for _, p := range cookieLocations {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return []string{"--cookies", p}
		}
	}
	return nil
}

// FetchMetadata retrieves video metadata without downloading video stream
func (d *Downloader) FetchMetadata(ctx context.Context, url string) (*MediaMetadata, error) {
	// Shell injection prevention: slice arguments strictly without invoking a shell
	args := []string{
		"--dump-single-json",
		"--no-warnings",
		"--no-playlist",
		"--skip-download",
		"--force-ipv4",
		"--no-check-certificates",
		"--extractor-args", "youtube:player_client=android,mweb",
		"--js-runtimes", "node",
		"--geo-bypass",
	}
	if d.cfg.ProxyURL != "" {
		args = append(args, "--proxy", d.cfg.ProxyURL)
	}
	args = append(args, getCookieArgs()...)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, d.cfg.YtDlpPath, args...)
	output, err := cmd.Output()
	if err != nil {
		// Fallback attempt with web_embedded
		fbArgs := []string{
			"--dump-single-json",
			"--no-warnings",
			"--no-playlist",
			"--skip-download",
			"--force-ipv4",
			"--no-check-certificates",
			"--extractor-args", "youtube:player_client=web_embedded,android",
			"--js-runtimes", "node",
			"--geo-bypass",
		}
		if d.cfg.ProxyURL != "" {
			fbArgs = append(fbArgs, "--proxy", d.cfg.ProxyURL)
		}
		fbArgs = append(fbArgs, getCookieArgs()...)
		fbArgs = append(fbArgs, url)
		fbCmd := exec.CommandContext(ctx, d.cfg.YtDlpPath, fbArgs...)
		output, err = fbCmd.Output()
		if err != nil {
			return nil, fmt.Errorf("failed to fetch metadata: %w", err)
		}
	}

	var meta MediaMetadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse metadata json: %w", err)
	}

	return &meta, nil
}

// DownloadMedia runs the full yt-dlp download pipeline with progress callback
func (d *Downloader) DownloadMedia(
	ctx context.Context,
	userID int64,
	targetURL string,
	platform string,
	onProgress func(statusText string),
) (*DownloadResult, error) {
	// 1. Prevent duplicate simultaneous downloads by the same user
	if _, loaded := d.activeUsers.LoadOrStore(userID, struct{}{}); loaded {
		return nil, errors.New("ERR_ALREADY_DOWNLOADING")
	}
	defer d.activeUsers.Delete(userID)

	// Create user-cancellable child context
	userCtx, cancel := context.WithCancel(ctx)
	d.userCancels.Store(userID, cancel)
	defer func() {
		cancel()
		d.userCancels.Delete(userID)
	}()

	// 2. Acquire semaphore slot
	select {
	case d.semaphore <- struct{}{}:
		defer func() { <-d.semaphore }()
	case <-userCtx.Done():
		return nil, errors.New("ERR_CANCELLED")
	}

	// 3. Ensure download directory exists
	if err := os.MkdirAll(d.cfg.DownloadDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create download dir: %w", err)
	}

	// 4. Setup progress tracker
	tracker := NewProgressTracker(onProgress)
	outputPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%%(id)s.%%(ext)s", userID))

	// Fast Format Selector:
	// 1. First priority: Pre-muxed single format 'b' or progressive MP4 (both audio & video combined in 1 stream).
	//    Extremely fast for Instagram Reels, TikTok, Twitter/X, and progressive YouTube shorts (no FFmpeg remuxing needed).
	// 2. Second priority: Best MP4 video + M4A audio merged into MP4 container.
	// 3. Final fallback: Any best video + best audio merged into MP4.
	formatSelector := "b/best[ext=mp4][vcodec!=none][acodec!=none]/bestvideo[ext=mp4]+bestaudio[ext=m4a]/bestvideo+bestaudio/best"

	// Define player client strategies to try (android/mweb first for bypass of 429 & fast progressive streams)
	clientStrategies := []string{
		"youtube:player_client=android,mweb",
		"youtube:player_client=web,mweb",
		"youtube:player_client=ios,android",
		"youtube:player_client=web_embedded,android",
		"", // default fallback
	}
	if platform != PlatformYouTube {
		clientStrategies = []string{""} // not applicable to non-YouTube
	}

	var downloadErr error
	var lastOutputLines []string

	for attemptIdx, clientArg := range clientStrategies {
		args := []string{
			"--newline",
			"--progress-template", "%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s",
			"-f", formatSelector,
			"--merge-output-format", "mp4",
			"--force-ipv4",                // Critical: prevents datacenter shared IPv6 429 blocks
			"--no-check-certificates",
			"--add-header", "Accept-Language: en-US,en;q=0.9",
			"--concurrent-fragments", "5", // Multi-threaded chunk downloader (3x-5x speedup on HLS/DASH)
			"--buffer-size", "64K",        // High throughput memory buffer
			"--http-chunk-size", "10M",    // Chunk streaming optimization
			"--no-mtime",                  // Avoid filesystem timestamp latency
			"--no-cache-dir",              // Avoid disk cache overhead
			"--socket-timeout", "15",      // Rapid timeout & retry on stalled connections
			"--retries", "3",
			"--fragment-retries", "3",
			"--write-info-json",           // Concurrently extract metadata in single pass (eliminates duplicate probe)
			"--write-thumbnail",
			"--convert-thumbnails", "jpg",
			"--no-playlist",
			"--geo-bypass",
			"--js-runtimes", "node",
			"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
		}
		if d.cfg.ProxyURL != "" {
			args = append(args, "--proxy", d.cfg.ProxyURL)
		}
		if clientArg != "" {
			args = append(args, "--extractor-args", clientArg)
		}
		args = append(args, getCookieArgs()...)
		args = append(args, "-o", outputPattern, targetURL)

		cmd := exec.CommandContext(userCtx, d.cfg.YtDlpPath, args...)
		stdoutPipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			downloadErr = pipeErr
			continue
		}
		cmd.Stderr = cmd.Stdout

		if startErr := cmd.Start(); startErr != nil {
			downloadErr = startErr
			continue
		}

		lastOutputLines = nil
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if len(lastOutputLines) >= 8 {
				lastOutputLines = lastOutputLines[1:]
			}
			lastOutputLines = append(lastOutputLines, line)

			if info := ParseProgressLine(line); info != nil {
				tracker.Update(info)
			}
		}

		waitErr := cmd.Wait()
		if userCtx.Err() != nil {
			return nil, errors.New("ERR_CANCELLED")
		}

		if waitErr == nil {
			// Succeeded!
			downloadErr = nil
			break
		}

		downloadErr = waitErr
		log.Printf("[yt-dlp attempt %d failed] %v (client=%s): %s", attemptIdx+1, waitErr, clientArg, strings.Join(lastOutputLines, " | "))
	}

	if downloadErr != nil {
		errDetails := strings.Join(lastOutputLines, " | ")
		return nil, fmt.Errorf("ERR_DOWNLOAD_FAILED: %s", errDetails)
	}

	tracker.Finish("⚡ <b>Download complete! Preparing to upload...</b>")

	// 5. Read metadata directly from generated .info.json (Single-pass extraction)
	var meta MediaMetadata
	jsonPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.info.json", userID))
	if jsonMatches, _ := filepath.Glob(jsonPattern); len(jsonMatches) > 0 {
		if rawJSON, readErr := os.ReadFile(jsonMatches[0]); readErr == nil {
			_ = json.Unmarshal(rawJSON, &meta)
		}
		for _, jm := range jsonMatches {
			_ = os.Remove(jm)
		}
	}

	// 6. Locate final downloaded video file
	filePattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%s*.mp4", userID, meta.ID))
	matches, _ := filepath.Glob(filePattern)
	if len(matches) == 0 {
		filePattern = filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.mp4", userID))
		matches, _ = filepath.Glob(filePattern)
	}
	if len(matches) == 0 {
		// Fallback glob if saved under other container
		allMatches, _ := filepath.Glob(filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.*", userID)))
		for _, m := range allMatches {
			ext := strings.ToLower(filepath.Ext(m))
			if ext == ".mp4" || ext == ".mkv" || ext == ".webm" || ext == ".mov" || ext == ".ts" {
				matches = append(matches, m)
				break
			}
		}
	}

	if len(matches) == 0 {
		return nil, errors.New("ERR_DOWNLOAD_FAILED")
	}

	targetFilePath := matches[0]
	fileInfo, err := os.Stat(targetFilePath)
	if err != nil {
		return nil, errors.New("ERR_DOWNLOAD_FAILED")
	}

	fileSize := fileInfo.Size()
	maxBytes := d.cfg.TelegramUploadLimitMB * 1024 * 1024
	if fileSize > maxBytes {
		_ = os.Remove(targetFilePath)
		return nil, errors.New("ERR_FILE_TOO_LARGE")
	}

	// Locate thumbnail if available
	thumbPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.jpg", userID))
	thumbMatches, _ := filepath.Glob(thumbPattern)
	var thumbPath string
	if len(thumbMatches) > 0 {
		thumbPath = thumbMatches[0]
	}

	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = fmt.Sprintf("%s Video", platform)
	}

	return &DownloadResult{
		FilePath:      targetFilePath,
		ThumbnailPath: thumbPath,
		Title:         title,
		Duration:      int(meta.Duration),
		FileSize:      fileSize,
		Platform:      platform,
	}, nil
}
