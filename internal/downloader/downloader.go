package downloader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	Width         int
	Height        int
}

// MediaMetadata extracted from yt-dlp JSON dump
type MediaMetadata struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
	Filesize  int64   `json:"filesize"`
	Ext       string  `json:"ext"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
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
		"--impersonate", "chrome",
		"--socket-timeout", "15",
		"--retries", "3",
		"--js-runtimes", "deno",
		"--geo-bypass",
	}
	if d.cfg.PotProviderURL != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.cfg.PotProviderURL)
	}
	args = append(args, "--extractor-args", "youtube:player_client=web_safari,tv;fetch_pot=always")
	if proxy := d.cfg.GetProxyURL(); proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, getCookieArgs()...)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, d.cfg.YtDlpPath, args...)
	output, err := cmd.Output()
	if err != nil {
		// Fallback attempt with android_vr
		fbArgs := []string{
			"--dump-single-json",
			"--no-warnings",
			"--no-playlist",
			"--skip-download",
			"--force-ipv4",
			"--no-check-certificates",
			"--impersonate", "chrome",
			"--socket-timeout", "15",
			"--retries", "3",
			"--js-runtimes", "deno",
			"--geo-bypass",
		}
		if d.cfg.PotProviderURL != "" {
			fbArgs = append(fbArgs, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.cfg.PotProviderURL)
		}
		fbArgs = append(fbArgs, "--extractor-args", "youtube:player_client=android_vr;fetch_pot=always")
		if proxy := d.cfg.GetProxyURL(); proxy != "" {
			fbArgs = append(fbArgs, "--proxy", proxy)
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
	// For YouTube: up to 1080p, bounded approx size for Telegram
	formatSelector := "bv*[height<=1080][filesize_approx<1600M]+ba/b[height<=1080][filesize_approx<1600M]/bv*[filesize_approx<1600M]+ba/b/best"
	if platform != PlatformYouTube {
		formatSelector = "b/bv*+ba/best"
	}

	// Define player client strategies to try
	var clientStrategies []struct {
		ClientArg string
	}

	if platform == PlatformYouTube {
		potBase := ""
		if d.cfg.PotProviderURL != "" {
			potBase = ";youtubepot-bgutilhttp:base_url=" + d.cfg.PotProviderURL
		}
		clientStrategies = []struct{ ClientArg string }{
			{ClientArg: "youtube:player_client=web_safari,tv;fetch_pot=always" + potBase},
			{ClientArg: "youtube:player_client=android_vr;fetch_pot=always" + potBase},
			{ClientArg: "youtube:player_client=android,mweb"},
			{ClientArg: "youtube:player_client=web,mweb"},
			{ClientArg: ""},
		}
	} else if platform == PlatformTikTok {
		clientStrategies = []struct{ ClientArg string }{
			{ClientArg: "tiktok:api_hostname=api22-normal-c-useast2a.tiktokv.com"},
			{ClientArg: ""},
		}
	} else {
		clientStrategies = []struct{ ClientArg string }{{ClientArg: ""}}
	}

	var downloadErr error
	var lastOutputLines []string

	for attemptIdx, strat := range clientStrategies {
		args := []string{
			"--newline",
			"--progress-template", "%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s",
			"-f", formatSelector,
			"--merge-output-format", "mp4",
			"--force-ipv4",
			"--no-check-certificates",
			"--add-header", "Accept-Language: en-US,en;q=0.9",
			"--impersonate", "chrome",
			"--concurrent-fragments", "16",
			"--buffer-size", "64K",
			"--http-chunk-size", "10M",
			"--no-part",
			"--no-mtime",
			"--no-cache-dir",
			"--socket-timeout", "20",
			"--retries", "5",
			"--fragment-retries", "5",
			"--write-info-json",
			"--write-thumbnail",
			"--convert-thumbnails", "jpg",
			"--no-playlist",
			"--geo-bypass",
			"--js-runtimes", "deno",
		}
		if proxy := d.cfg.GetProxyURL(); proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		if strat.ClientArg != "" {
			args = append(args, "--extractor-args", strat.ClientArg)
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
		log.Printf("[yt-dlp attempt %d failed] %v (client=%s): %s", attemptIdx+1, waitErr, strat.ClientArg, strings.Join(lastOutputLines, " | "))
	}

	var meta MediaMetadata
	var tikwmDownloadedPath string

	if downloadErr != nil {
		if platform == PlatformTikTok {
			log.Printf("[downloader] yt-dlp failed on TikTok, falling back to TikWM API: %s", targetURL)
			dstPath := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_tw_%d.mp4", userID, time.Now().UnixNano()))
			tikMeta, tikErr := d.downloadTikWM(userCtx, targetURL, dstPath)
			if tikErr == nil {
				downloadErr = nil
				meta = *tikMeta
				tikwmDownloadedPath = dstPath
			} else {
				log.Printf("[downloader] TikWM fallback failed: %v", tikErr)
			}
		}
	}

	if downloadErr != nil {
		errDetails := strings.Join(lastOutputLines, " | ")
		return nil, fmt.Errorf("ERR_DOWNLOAD_FAILED: %s", errDetails)
	}

	tracker.Finish("⚡ <b>Download complete! Preparing to upload...</b>")

	// 5. Read metadata directly from generated .info.json (Single-pass extraction)
	if tikwmDownloadedPath == "" {
		jsonPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.info.json", userID))
		if jsonMatches, _ := filepath.Glob(jsonPattern); len(jsonMatches) > 0 {
			if rawJSON, readErr := os.ReadFile(jsonMatches[0]); readErr == nil {
				_ = json.Unmarshal(rawJSON, &meta)
			}
			for _, jm := range jsonMatches {
				_ = os.Remove(jm)
			}
		}
	}

	// 6. Locate final downloaded video file
	var targetFilePath string
	if tikwmDownloadedPath != "" {
		targetFilePath = tikwmDownloadedPath
	} else {
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
		targetFilePath = matches[0]
	}

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
		Width:         meta.Width,
		Height:        meta.Height,
	}, nil
}

// downloadTikWM downloads TikTok video without watermark via tikwm API as high-speed fallback
func (d *Downloader) downloadTikWM(ctx context.Context, targetURL, dstPath string) (*MediaMetadata, error) {
	reqURL := "https://www.tikwm.com/api/?url=" + urlQueryEscape(targetURL)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Title    string `json:"title"`
			Duration int    `json:"duration"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Play     string `json:"play"`
			HDPlay   string `json:"hdplay"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Code != 0 || (r.Data.HDPlay == "" && r.Data.Play == "") {
		return nil, fmt.Errorf("tikwm returned code %d: %s", r.Code, r.Msg)
	}

	link := r.Data.HDPlay
	if link == "" {
		link = r.Data.Play
	}

	dlReq, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return nil, err
	}
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	dlResp, err := client.Do(dlReq)
	if err != nil {
		return nil, err
	}
	defer dlResp.Body.Close()

	out, err := os.Create(dstPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	if _, err := io.Copy(out, dlResp.Body); err != nil {
		_ = os.Remove(dstPath)
		return nil, err
	}

	return &MediaMetadata{
		Title:    r.Data.Title,
		Duration: float64(r.Data.Duration),
		Width:    r.Data.Width,
		Height:   r.Data.Height,
	}, nil
}

func urlQueryEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
