package downloader

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

func (d *Downloader) getCookieArgs(useCookies bool) []string {
	if !useCookies {
		return nil
	}

	// Auto-populate cookies file if COOKIES_DATA env variable is provided and differs from disk
	if raw := os.Getenv("COOKIES_DATA"); strings.TrimSpace(raw) != "" {
		dataBytes := []byte(strings.TrimSpace(raw) + "\n")
		h := sha256.Sum256(dataBytes)
		newSHA := hex.EncodeToString(h[:])

		for _, target := range []string{"data/cookies.txt", "/app/data/cookies.txt"} {
			dir := filepath.Dir(target)
			shaFile := filepath.Join(dir, ".cookies.sha")

			existing, readErr := os.ReadFile(target)
			needsWrite := readErr != nil || len(existing) == 0
			if !needsWrite {
				storedSHA, _ := os.ReadFile(shaFile)
				if strings.TrimSpace(string(storedSHA)) != newSHA || !bytes.Equal(existing, dataBytes) {
					needsWrite = true
				}
			}

			if needsWrite {
				_ = os.MkdirAll(dir, 0755)
				if err := os.WriteFile(target, dataBytes, 0644); err == nil {
					_ = os.WriteFile(shaFile, []byte(newSHA), 0644)
					log.Printf("[downloader] Synced %s from COOKIES_DATA (sha256: %s)", target, newSHA[:8])
				}
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

// ValidateCookiesStartup checks the active cookies file against Instagram simulation without aborting startup
func (d *Downloader) ValidateCookiesStartup(ctx context.Context) {
	cookieLocations := []string{
		"cookies.txt",
		"data/cookies.txt",
		"/app/data/cookies.txt",
	}
	var cookieFile string
	for _, p := range cookieLocations {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			cookieFile = p
			break
		}
	}
	if cookieFile == "" {
		log.Println("[downloader] No cookies file found at startup.")
		return
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, d.cfg.YtDlpPath, "--cookies", cookieFile, "--simulate", "--no-warnings", "https://www.instagram.com/")
	if err := cmd.Run(); err != nil {
		log.Printf("[downloader] WARNING: Startup cookie validation check failed for %s: %v (cookies may be invalid/expired, continuing startup)", cookieFile, err)
	} else {
		log.Printf("[downloader] Startup cookie validation successful using %s", cookieFile)
	}
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
		"--remote-components", "ejs:github",
		"--geo-bypass",
	}
	if d.cfg.PotProviderURL != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.cfg.PotProviderURL)
	}
	args = append(args, "--extractor-args", "youtube:player-client=mweb,tv,web_safari")
	if proxy := d.cfg.GetProxyURL(); proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	args = append(args, d.getCookieArgs(true)...)
	args = append(args, url)

	cmd := exec.CommandContext(ctx, d.cfg.YtDlpPath, args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			log.Printf("[downloader] FetchMetadata primary error: %v, stderr: %s", err, string(exitErr.Stderr))
		} else {
			log.Printf("[downloader] FetchMetadata primary error: %v", err)
		}

		// Fallback attempt with ios,tv without cookies
		fbArgs := []string{
			"--dump-single-json",
			"--no-warnings",
			"--no-playlist",
			"--skip-download",
			"--force-ipv4",
			"--no-check-certificates",
			"--socket-timeout", "15",
			"--retries", "3",
			"--js-runtimes", "deno",
			"--remote-components", "ejs:github",
			"--geo-bypass",
			"--extractor-args", "youtube:player-client=ios,tv",
		}
		if proxy := d.cfg.GetProxyURL(); proxy != "" {
			fbArgs = append(fbArgs, "--proxy", proxy)
		}
		fbArgs = append(fbArgs, url)
		fbCmd := exec.CommandContext(ctx, d.cfg.YtDlpPath, fbArgs...)
		output, err = fbCmd.Output()
		if err != nil {
			if errors.As(err, &exitErr) {
				log.Printf("[downloader] FetchMetadata fallback error: %v, stderr: %s", err, string(exitErr.Stderr))
			} else {
				log.Printf("[downloader] FetchMetadata fallback error: %v", err)
			}
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

	// Pre-run cleanup: remove existing stale artifacts for this user
	cleanPatterns := []string{
		filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.mp4", userID)),
		filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.info.json", userID)),
		filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.jpg", userID)),
	}
	for _, pat := range cleanPatterns {
		if matches, err := filepath.Glob(pat); err == nil {
			for _, m := range matches {
				_ = os.Remove(m)
			}
		}
	}

	// 4. Setup progress tracker
	tracker := NewProgressTracker(onProgress)
	outputPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%%(id)s.%%(ext)s", userID))

	// Fast Format Selector:
	// For YouTube: up to 1080p, bounded approx size for Telegram
	formatSelector := "bv*[height<=?1080][filesize_approx<=?1600M]+ba/b[height<=?1080][filesize_approx<=?1600M]/bv*[filesize_approx<=?1600M]+ba/b/best"
	if platform != PlatformYouTube {
		formatSelector = "b/bv*+ba/best"
	}

	type strategy struct {
		Name        string
		ClientArg   string
		UsePot      bool
		UseCookies  bool
		Impersonate bool
	}

	var clientStrategies []strategy
	hasCookies := len(d.getCookieArgs(true)) > 0

	if platform == PlatformYouTube {
		// 1. Mobile Web & TV client with PO Token & cookies (if available)
		clientStrategies = append(clientStrategies, strategy{
			Name:        "mweb,tv (POT+cookies)",
			ClientArg:   "youtube:player-client=mweb,tv,web_safari",
			UsePot:      true,
			UseCookies:  hasCookies,
			Impersonate: true,
		})

		// 2. iOS client (avoids web bot guard checks)
		clientStrategies = append(clientStrategies, strategy{
			Name:        "ios,tv (cookies)",
			ClientArg:   "youtube:player-client=ios,tv",
			UsePot:      false,
			UseCookies:  hasCookies,
			Impersonate: true,
		})

		// 3. TV client
		clientStrategies = append(clientStrategies, strategy{
			Name:        "tv,mweb",
			ClientArg:   "youtube:player-client=tv,mweb",
			UsePot:      false,
			UseCookies:  hasCookies,
			Impersonate: true,
		})

		// If cookies were present and attempts 1-3 failed, try WITHOUT cookies!
		// (Crucial fallback if cookies file is expired, invalid, or flagged)
		if hasCookies {
			clientStrategies = append(clientStrategies, strategy{
				Name:        "ios,tv (no cookies)",
				ClientArg:   "youtube:player-client=ios,tv",
				UsePot:      false,
				UseCookies:  false,
				Impersonate: true,
			})
			clientStrategies = append(clientStrategies, strategy{
				Name:        "mweb,tv (no cookies)",
				ClientArg:   "youtube:player-client=mweb,tv,web_safari",
				UsePot:      true,
				UseCookies:  false,
				Impersonate: true,
			})
		}

		// 4. Fallback: default client chain without forcing client args or impersonate
		clientStrategies = append(clientStrategies, strategy{
			Name:        "default yt-dlp",
			ClientArg:   "",
			UsePot:      false,
			UseCookies:  false,
			Impersonate: false,
		})
	} else if platform == PlatformTikTok {
		clientStrategies = []strategy{
			{Name: "tiktok api", ClientArg: "tiktok:api_hostname=api22-normal-c-useast2a.tiktokv.com", UseCookies: hasCookies, Impersonate: true},
			{Name: "default tiktok", ClientArg: "", UseCookies: false, Impersonate: false},
		}
	} else {
		clientStrategies = []strategy{
			{Name: "default", ClientArg: "", UseCookies: hasCookies, Impersonate: true},
		}
	}

	var downloadErr error
	var lastOutputLines []string
	var lastAttemptStderr []string

	for attemptIdx, strat := range clientStrategies {
		args := []string{
			"--newline",
			"--progress-template", "%(progress._percent_str)s|%(progress._speed_str)s|%(progress._eta_str)s",
			"-f", formatSelector,
			"--merge-output-format", "mp4",
			"--force-ipv4",
			"--no-check-certificates",
			"--add-header", "Accept-Language: en-US,en;q=0.9",
			"--buffer-size", "64K",
			"--no-part",
			"--no-mtime",
			"--socket-timeout", "20",
			"--retries", "5",
			"--fragment-retries", "5",
			"--write-info-json",
			"--write-thumbnail",
			"--convert-thumbnails", "jpg",
			"--no-playlist",
			"--geo-bypass",
			"--js-runtimes", "deno",
			"--remote-components", "ejs:github",
		}
		if strat.Impersonate {
			args = append(args, "--impersonate", "chrome")
		}
		args = append(args, "--concurrent-fragments", concurrentFragments(platform))
		if proxy := d.cfg.GetProxyURL(); proxy != "" {
			args = append(args, "--proxy", proxy)
		}
		if strat.UsePot && d.cfg.PotProviderURL != "" {
			args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.cfg.PotProviderURL)
		}
		if strat.ClientArg != "" {
			args = append(args, "--extractor-args", strat.ClientArg)
		}
		if cookieArgs := d.getCookieArgs(strat.UseCookies); len(cookieArgs) > 0 {
			args = append(args, cookieArgs...)
		}
		args = append(args, "-o", outputPattern, targetURL)

		cmd := exec.CommandContext(userCtx, d.cfg.YtDlpPath, args...)
		stdoutPipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			downloadErr = pipeErr
			continue
		}
		stderrPipe, stderrPipeErr := cmd.StderrPipe()
		if stderrPipeErr != nil {
			downloadErr = stderrPipeErr
			continue
		}

		if startErr := cmd.Start(); startErr != nil {
			downloadErr = startErr
			continue
		}

		attemptStderr := make([]string, 0, 20)
		var stderrMu sync.Mutex
		var stderrWg sync.WaitGroup
		stderrWg.Add(1)
		go func() {
			defer stderrWg.Done()
			stderrScanner := bufio.NewScanner(stderrPipe)
			for stderrScanner.Scan() {
				line := strings.TrimSpace(stderrScanner.Text())
				if line != "" {
					stderrMu.Lock()
					if len(attemptStderr) >= 20 {
						attemptStderr = attemptStderr[1:]
					}
					attemptStderr = append(attemptStderr, line)
					stderrMu.Unlock()
				}
			}
		}()

		lastOutputLines = nil
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				if len(lastOutputLines) >= 12 {
					lastOutputLines = lastOutputLines[1:]
				}
				lastOutputLines = append(lastOutputLines, trimmed)
			}

			if info := ParseProgressLine(line); info != nil {
				tracker.Update(info)
			}
		}

		waitErr := cmd.Wait()
		stderrWg.Wait()

		if userCtx.Err() != nil {
			return nil, errors.New("ERR_CANCELLED")
		}

		stderrMu.Lock()
		lastAttemptStderr = attemptStderr
		stderrMu.Unlock()

		if waitErr == nil {
			// Succeeded!
			downloadErr = nil
			break
		}

		downloadErr = waitErr
		log.Printf("[downloader attempt %d failed (%s)]: %v (stdout: %s | stderr: %s)", attemptIdx+1, strat.Name, waitErr, strings.Join(lastOutputLines, " | "), strings.Join(lastAttemptStderr, " | "))
	}

	var meta MediaMetadata
	var fallbackDownloadedPath string

	if downloadErr != nil {
		if platform == PlatformTikTok {
			log.Printf("[downloader] yt-dlp failed on TikTok, falling back to TikWM API: %s", targetURL)
			dstPath := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_tw_%d.mp4", userID, time.Now().UnixNano()))
			tikMeta, tikErr := d.downloadTikWM(userCtx, targetURL, dstPath)
			if tikErr == nil {
				downloadErr = nil
				meta = *tikMeta
				fallbackDownloadedPath = dstPath
			} else {
				log.Printf("[downloader] TikWM fallback failed: %v", tikErr)
			}
		} else if platform == PlatformInstagram {
			log.Printf("[downloader] yt-dlp failed on Instagram, falling back to embed scraper: %s", targetURL)
			dstPath := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_ig_%d.mp4", userID, time.Now().UnixNano()))
			igMeta, igErr := d.downloadInstagramEmbed(userCtx, targetURL, dstPath)
			if igErr == nil {
				downloadErr = nil
				meta = *igMeta
				fallbackDownloadedPath = dstPath
			} else {
				log.Printf("[downloader] Instagram embed fallback failed: %v", igErr)
			}
		}
	}

	if downloadErr != nil {
		log.Printf("[downloader] FINAL FAILURE for %s. Full stderr buffer (%d lines):\n%s", targetURL, len(lastAttemptStderr), strings.Join(lastAttemptStderr, "\n"))
		last6 := lastAttemptStderr
		if len(last6) > 6 {
			last6 = last6[len(last6)-6:]
		}
		errDetails := strings.Join(last6, " | ")
		if errDetails == "" {
			if len(lastOutputLines) > 6 {
				errDetails = strings.Join(lastOutputLines[len(lastOutputLines)-6:], " | ")
			} else {
				errDetails = strings.Join(lastOutputLines, " | ")
			}
		}
		return nil, fmt.Errorf("ERR_DOWNLOAD_FAILED: %s", errDetails)
	}

	tracker.Finish("⚡ <b>Download complete! Preparing to upload...</b>")

	// 5. Read metadata directly from generated .info.json (Single-pass extraction)
	if fallbackDownloadedPath == "" {
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
	if fallbackDownloadedPath != "" {
		targetFilePath = fallbackDownloadedPath
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

// downloadInstagramEmbed fetches embed captioned page and extracts video stream as fallback
func (d *Downloader) downloadInstagramEmbed(ctx context.Context, targetURL, dstPath string) (*MediaMetadata, error) {
	// 1. Extract Instagram shortcode / ID
	matches := igRegex.FindStringSubmatch(targetURL)
	var id string
	if len(matches) > 1 && matches[1] != "" {
		id = matches[1]
	} else {
		fbRegex := regexp.MustCompile(`(?i)(?:reel|reels|p|tv)\/([a-zA-Z0-9_-]+)`)
		fbMatches := fbRegex.FindStringSubmatch(targetURL)
		if len(fbMatches) > 1 && fbMatches[1] != "" {
			id = fbMatches[1]
		}
	}
	if id == "" {
		return nil, fmt.Errorf("could not extract Instagram ID from %s", targetURL)
	}

	embedURL := fmt.Sprintf("https://www.instagram.com/reel/%s/embed/captioned/", id)
	req, err := http.NewRequestWithContext(ctx, "GET", embedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create embed request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 45 * time.Second}
	if p := d.cfg.GetProxyURL(); p != "" {
		if proxyParsed, pErr := url.Parse(p); pErr == nil {
			client.Transport = &http.Transport{
				Proxy: http.ProxyURL(proxyParsed),
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch instagram embed page: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instagram embed returned HTTP %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read embed response body: %w", err)
	}
	htmlContent := string(bodyBytes)

	// Extract video URL and caption from embed HTML
	videoURL, caption := extractInstagramEmbedData(htmlContent)
	if videoURL == "" {
		return nil, fmt.Errorf("could not extract video URL from embed page for %s", id)
	}

	// Download video stream to dstPath
	dlReq, err := http.NewRequestWithContext(ctx, "GET", videoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create video stream request: %w", err)
	}
	dlReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	dlReq.Header.Set("Referer", "https://www.instagram.com/")

	dlResp, err := client.Do(dlReq)
	if err != nil {
		return nil, fmt.Errorf("failed to download video stream: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instagram video stream returned HTTP %d", dlResp.StatusCode)
	}

	out, err := os.Create(dstPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create destination file: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, dlResp.Body); err != nil {
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("failed to write video stream: %w", err)
	}

	if caption == "" {
		caption = fmt.Sprintf("Instagram Reel %s", id)
	}

	return &MediaMetadata{
		ID:    id,
		Title: caption,
		Ext:   "mp4",
	}, nil
}

func extractInstagramEmbedData(htmlContent string) (videoURL, caption string) {
	if idx := strings.Index(htmlContent, "contextJSON"); idx != -1 {
		sub := htmlContent[idx:]
		blockEnd := len(sub)
		if end := strings.Index(sub, "</script>"); end != -1 && end < blockEnd {
			blockEnd = end
		}
		block := sub[:blockEnd]
		videoURL = extractVideoURLString(block)
		caption = extractCaptionString(block)
	}

	if videoURL == "" {
		videoURL = extractVideoURLString(htmlContent)
	}
	if caption == "" {
		caption = extractCaptionString(htmlContent)
	}

	return videoURL, caption
}

var (
	igVideoURLRegexes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:\\"|")video_url(?:\\"|")\s*:\s*(?:\\"|")([^"\\ ]+)(?:\\"|")`),
		regexp.MustCompile(`(?i)"video_url"\s*:\s*"([^"]+)"`),
		regexp.MustCompile(`(?i)<video[^>]+src=["']([^"']+)["']`),
	}
	igCaptionRegexes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:\\"|")text(?:\\"|")\s*:\s*(?:\\"|")([^"\\]+)(?:\\"|")`),
		regexp.MustCompile(`(?i)(?:\\"|")caption(?:\\"|")\s*:\s*(?:\\"|")([^"\\]+)(?:\\"|")`),
	}
)

func extractVideoURLString(content string) string {
	for _, re := range igVideoURLRegexes {
		if m := re.FindStringSubmatch(content); len(m) > 1 && m[1] != "" {
			u := m[1]
			u = strings.ReplaceAll(u, `\/`, `/`)
			u = strings.ReplaceAll(u, `\\/`, `/`)
			u = strings.ReplaceAll(u, `\u0026`, `&`)
			u = strings.ReplaceAll(u, `&amp;`, `&`)
			u = html.UnescapeString(u)
			u = strings.TrimSpace(u)
			if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				return u
			}
		}
	}
	return ""
}

func extractCaptionString(content string) string {
	for _, re := range igCaptionRegexes {
		if m := re.FindStringSubmatch(content); len(m) > 1 && m[1] != "" {
			c := m[1]
			c = strings.ReplaceAll(c, `\/`, `/`)
			c = strings.ReplaceAll(c, `\n`, " ")
			c = html.UnescapeString(c)
			c = strings.TrimSpace(c)
			if len(c) > 200 {
				c = c[:200]
			}
			if c != "" {
				return c
			}
		}
	}
	return ""
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

func concurrentFragments(platform string) string {
	switch platform {
	case PlatformInstagram:
		return "3"
	case PlatformYouTube:
		return "4"
	case PlatformTikTok:
		return "8"
	default:
		return "4"
	}
}
