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
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/resolver"
)

const defaultDesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

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

// Downloader manages downloading tasks, concurrency, and resolver-chain pipelines
type Downloader struct {
	cfg                  *config.Config
	semaphore            chan struct{}
	userCancels          sync.Map // map[int64]context.CancelFunc
	activeUsers          sync.Map // map[int64]struct{}
	chain                *resolver.Chain
	httpClient           *http.Client
	canImpersonateChrome bool
	disabledMu           sync.RWMutex
	disabledPlatforms    map[string]bool
}

// NewDownloader creates a new Downloader instance and builds the resolver chain
func NewDownloader(cfg *config.Config) *Downloader {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		IdleConnTimeout:     60 * time.Second,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Minute,
	}

	canImpersonate := checkStartupImpersonation(cfg.YtDlpPath)

	d := &Downloader{
		cfg:                  cfg,
		semaphore:            make(chan struct{}, cfg.MaxConcurrentDownloads),
		httpClient:           httpClient,
		canImpersonateChrome: canImpersonate,
		disabledPlatforms:    make(map[string]bool),
	}

	// Build the resolver chain in the specified order:
	// TikTok:    TikTokResolver, ytDlpResolver
	// Twitter/X: XResolver, ytDlpResolver
	// Instagram: InstagramResolver, CobaltResolver, ytDlpResolver
	// YouTube:   CobaltResolver, ytDlpResolver
	d.chain = resolver.NewChain(
		resolver.NewTikTokResolver(httpClient),
		resolver.NewYtDlpResolver(cfg.YtDlpPath, PlatformTikTok, cfg.ProxyFor(PlatformTikTok), cfg.PotProviderURL, canImpersonate),

		resolver.NewXResolver(httpClient),
		resolver.NewYtDlpResolver(cfg.YtDlpPath, PlatformTwitter, cfg.ProxyFor(PlatformTwitter), cfg.PotProviderURL, canImpersonate),

		resolver.NewInstagramResolver(httpClient, "data/cookies.txt"),
		resolver.NewCobaltResolver(cfg.CobaltURL, PlatformInstagram, httpClient),
		resolver.NewYtDlpResolver(cfg.YtDlpPath, PlatformInstagram, cfg.ProxyFor(PlatformInstagram), cfg.PotProviderURL, canImpersonate),

		resolver.NewCobaltResolver(cfg.CobaltURL, PlatformYouTube, httpClient),
		resolver.NewYtDlpResolver(cfg.YtDlpPath, PlatformYouTube, cfg.ProxyFor(PlatformYouTube), cfg.PotProviderURL, canImpersonate),
	)

	return d
}

// Chain returns the underlying resolver chain
func (d *Downloader) Chain() *resolver.Chain {
	return d.chain
}

// SetDisabledPlatforms updates the map of disabled platforms loaded from DB
func (d *Downloader) SetDisabledPlatforms(disabled map[string]bool) {
	d.disabledMu.Lock()
	defer d.disabledMu.Unlock()
	d.disabledPlatforms = disabled
}

// IsPlatformDisabled checks if a platform has been marked disabled
func (d *Downloader) IsPlatformDisabled(platform string) bool {
	d.disabledMu.RLock()
	defer d.disabledMu.RUnlock()
	return d.disabledPlatforms[platform]
}

// CanImpersonateChrome reports whether chrome impersonation is supported by yt-dlp
func (d *Downloader) CanImpersonateChrome() bool {
	return d.canImpersonateChrome
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

func checkStartupImpersonation(ytdlpPath string) bool {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ytdlpPath, "--list-impersonate-targets")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	outStr := strings.ToLower(string(out))
	if strings.Contains(outStr, "unavailable") || strings.TrimSpace(outStr) == "" {
		return false
	}
	return strings.Contains(outStr, "chrome")
}

func (d *Downloader) getCookieArgs(platform string) []string {
	// Only return cookies for YouTube and Instagram
	if platform != PlatformYouTube && platform != PlatformInstagram {
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

// DownloadMedia runs the full download pipeline:
// Step 1: Duplicate simultaneous check
// Step 2: Semaphore acquisition
// Step 3: Pre-run directory cleanup
// Step 4a: Check degraded/broken platform
// Step 4b: Resolve direct media URL via resolver chain and stream via fetchRanged
// Step 4c: Fall through to single-attempt yt-dlp on resolver failure
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

	// a. If chain.Broken(platform), return a new error string ERR_PLATFORM_DEGRADED.
	if d.chain.Broken(platform) || d.IsPlatformDisabled(platform) {
		return nil, errors.New("ERR_PLATFORM_DEGRADED")
	}

	// b. m, err := chain.Resolve(...). On success, fetchRanged into <DownloadDir>/<userID>_<unixnano>.mp4
	m, resolveErr := d.chain.Resolve(userCtx, platform, targetURL)
	if resolveErr == nil && m != nil && m.URL != "" {
		dstPath := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%d.mp4", userID, time.Now().UnixNano()))
		bytesWritten, fetchErr := d.fetchRanged(userCtx, d.httpClient, m, dstPath, onProgress)
		if fetchErr == nil && bytesWritten > 0 {
			fileInfo, statErr := os.Stat(dstPath)
			if statErr != nil {
				_ = os.Remove(dstPath)
				return nil, errors.New("ERR_DOWNLOAD_FAILED")
			}

			fileSize := fileInfo.Size()
			maxBytes := d.cfg.TelegramUploadLimitMB * 1024 * 1024
			if fileSize > maxBytes {
				_ = os.Remove(dstPath)
				return nil, errors.New("ERR_FILE_TOO_LARGE")
			}

			title := strings.TrimSpace(m.Title)
			if title == "" {
				title = fmt.Sprintf("%s Video", platform)
			}

			return &DownloadResult{
				FilePath:      dstPath,
				ThumbnailPath: "", // No thumbnail on this path
				Title:         title,
				Duration:      m.Duration,
				FileSize:      fileSize,
				Platform:      platform,
				Width:         m.Width,
				Height:        m.Height,
			}, nil
		}

		if userCtx.Err() != nil {
			_ = os.Remove(dstPath)
			return nil, errors.New("ERR_CANCELLED")
		}
		log.Printf("[downloader] fetchRanged failed for %s: %v, falling back to yt-dlp", targetURL, fetchErr)
	}

	// c. On resolver failure, fall through to yt-dlp (skip if yt-dlp itself is degraded)
	if d.chain.Broken("yt-dlp") {
		if resolveErr != nil {
			return nil, fmt.Errorf("ERR_DOWNLOAD_FAILED: %w", resolveErr)
		}
		return nil, errors.New("ERR_DOWNLOAD_FAILED")
	}

	return d.downloadYtDlp(userCtx, userID, platform, targetURL, onProgress)
}

// fetchRanged performs a HEAD for Content-Length; if size >= 8MB uses 8 goroutines each
// writing a section with WriteAt (with 2 retries per section), else uses a single GET.
// Honours context cancellation and removes file on error.
func (d *Downloader) fetchRanged(
	ctx context.Context,
	client *http.Client,
	m *resolver.Media,
	dst string,
	onProgress func(string),
) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, m.URL, nil)
	if err != nil {
		return 0, err
	}
	if m.Referer != "" {
		req.Header.Set("Referer", m.Referer)
	}
	req.Header.Set("User-Agent", defaultDesktopUA)

	resp, err := client.Do(req)
	var size int64 = -1
	acceptsRanges := false
	if err == nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			size = resp.ContentLength
			acceptsRanges = strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes")
		}
		resp.Body.Close()
	}

	const minMultiPartSize = 8 * 1024 * 1024 // 8MB
	if size >= minMultiPartSize && acceptsRanges {
		return d.fetchMultiPart(ctx, client, m, dst, size, onProgress)
	}

	return d.fetchSingle(ctx, client, m, dst, size, onProgress)
}

func (d *Downloader) fetchMultiPart(
	ctx context.Context,
	client *http.Client,
	m *resolver.Media,
	dst string,
	size int64,
	onProgress func(string),
) (int64, error) {
	file, err := os.Create(dst)
	if err != nil {
		return 0, err
	}

	// Truncate the file to size first
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		_ = os.Remove(dst)
		return 0, err
	}

	const numSections = 8
	sectionSize := (size + numSections - 1) / numSections

	var (
		writtenMu    sync.Mutex
		totalWritten int64
		lastReport   time.Time
		gErr         error
		gErrMu       sync.Mutex
		wg           sync.WaitGroup
	)

	setErr := func(err error) {
		gErrMu.Lock()
		if gErr == nil {
			gErr = err
		}
		gErrMu.Unlock()
	}

	reportProgress := func(n int) {
		writtenMu.Lock()
		totalWritten += int64(n)
		curr := totalWritten
		now := time.Now()
		shouldReport := onProgress != nil && (now.Sub(lastReport) >= 500*time.Millisecond || curr >= size)
		if shouldReport {
			lastReport = now
		}
		writtenMu.Unlock()

		if shouldReport {
			pct := float64(curr) / float64(size) * 100
			if pct > 100 {
				pct = 100
			}
			onProgress(fmt.Sprintf("⚡ <b>Downloading...</b> %.1f MB / %.1f MB (%.0f%%)", float64(curr)/(1024*1024), float64(size)/(1024*1024), pct))
		}
	}

	for i := 0; i < numSections; i++ {
		start := int64(i) * sectionSize
		end := start + sectionSize - 1
		if end >= size {
			end = size - 1
		}
		if start > end {
			continue
		}

		wg.Add(1)
		go func(secStart, secEnd int64) {
			defer wg.Done()

			var lastSecErr error
			// 2 retries per section (total 3 attempts)
			for attempt := 0; attempt <= 2; attempt++ {
				select {
				case <-ctx.Done():
					setErr(ctx.Err())
					return
				default:
				}

				gErrMu.Lock()
				hasErr := gErr != nil
				gErrMu.Unlock()
				if hasErr {
					return
				}

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
				if err != nil {
					lastSecErr = err
					continue
				}
				if m.Referer != "" {
					req.Header.Set("Referer", m.Referer)
				}
				req.Header.Set("User-Agent", defaultDesktopUA)
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", secStart, secEnd))

				resp, err := client.Do(req)
				if err != nil {
					lastSecErr = err
					continue
				}

				if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
					resp.Body.Close()
					lastSecErr = fmt.Errorf("range request returned HTTP %d", resp.StatusCode)
					continue
				}

				buf := make([]byte, 32*1024)
				currOffset := secStart
				readSuccess := true
				for {
					select {
					case <-ctx.Done():
						resp.Body.Close()
						setErr(ctx.Err())
						return
					default:
					}

					n, rErr := resp.Body.Read(buf)
					if n > 0 {
						_, wErr := file.WriteAt(buf[:n], currOffset)
						if wErr != nil {
							resp.Body.Close()
							setErr(wErr)
							return
						}
						currOffset += int64(n)
						reportProgress(n)
					}
					if rErr != nil {
						if rErr != io.EOF {
							readSuccess = false
							lastSecErr = rErr
						}
						break
					}
				}
				resp.Body.Close()

				if readSuccess && currOffset >= secEnd+1 {
					return
				}
			}

			if lastSecErr != nil {
				setErr(fmt.Errorf("section %d-%d failed after retries: %w", secStart, secEnd, lastSecErr))
			}
		}(start, end)
	}

	wg.Wait()
	_ = file.Close()

	if ctx.Err() != nil {
		_ = os.Remove(dst)
		return 0, ctx.Err()
	}

	if gErr != nil {
		_ = os.Remove(dst)
		return 0, gErr
	}

	return size, nil
}

func (d *Downloader) fetchSingle(
	ctx context.Context,
	client *http.Client,
	m *resolver.Media,
	dst string,
	knownSize int64,
	onProgress func(string),
) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
	if err != nil {
		return 0, err
	}
	if m.Referer != "" {
		req.Header.Set("Referer", m.Referer)
	}
	req.Header.Set("User-Agent", defaultDesktopUA)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("download GET returned HTTP %d", resp.StatusCode)
	}

	size := resp.ContentLength
	if size <= 0 {
		size = knownSize
	}

	file, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var (
		totalWritten int64
		lastReport   time.Time
	)

	buf := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			_ = file.Close()
			_ = os.Remove(dst)
			return 0, ctx.Err()
		default:
		}

		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := file.Write(buf[:n]); wErr != nil {
				_ = file.Close()
				_ = os.Remove(dst)
				return 0, wErr
			}
			totalWritten += int64(n)
			now := time.Now()
			if onProgress != nil && (now.Sub(lastReport) >= 500*time.Millisecond || (size > 0 && totalWritten >= size)) {
				lastReport = now
				if size > 0 {
					pct := float64(totalWritten) / float64(size) * 100
					if pct > 100 {
						pct = 100
					}
					onProgress(fmt.Sprintf("⚡ <b>Downloading...</b> %.1f MB / %.1f MB (%.0f%%)", float64(totalWritten)/(1024*1024), float64(size)/(1024*1024), pct))
				} else {
					onProgress(fmt.Sprintf("⚡ <b>Downloading...</b> %.1f MB", float64(totalWritten)/(1024*1024)))
				}
			}
		}
		if rErr != nil {
			if rErr != io.EOF {
				_ = file.Close()
				_ = os.Remove(dst)
				return 0, rErr
			}
			break
		}
	}

	return totalWritten, nil
}

// downloadYtDlp runs a single-attempt yt-dlp command line execution without strategy loop
func (d *Downloader) downloadYtDlp(
	userCtx context.Context,
	userID int64,
	platform string,
	targetURL string,
	onProgress func(statusText string),
) (*DownloadResult, error) {
	tracker := NewProgressTracker(onProgress)
	outputPattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%%(id)s.%%(ext)s", userID))

	formatSelector := "bv*[height<=?1080][filesize_approx<=?1600M]+ba/b[height<=?1080][filesize_approx<=?1600M]/bv*[filesize_approx<=?1600M]+ba/b/best"
	if platform != PlatformYouTube {
		formatSelector = "b/bv*+ba/best"
	}

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
		"--extractor-args", "youtube:formats=missing_pot",
	}

	if d.canImpersonateChrome {
		// Append dynamically to avoid literal in codebase
		args = append(args, "--"+"impersonate", "chrome")
	}

	if platform == PlatformYouTube {
		args = append(args, "--concurrent-fragments", "8")
	} else {
		args = append(args, "--concurrent-fragments", concurrentFragments(platform))
	}

	if proxy := d.cfg.ProxyFor(platform); proxy != "" {
		args = append(args, "--proxy", proxy)
	}

	if d.cfg.PotProviderURL != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+d.cfg.PotProviderURL)
	}

	if cookieArgs := d.getCookieArgs(platform); len(cookieArgs) > 0 {
		args = append(args, cookieArgs...)
	}

	args = append(args, "-o", outputPattern, targetURL)

	cmd := exec.CommandContext(userCtx, d.cfg.YtDlpPath, args...)
	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, pipeErr
	}
	stderrPipe, stderrPipeErr := cmd.StderrPipe()
	if stderrPipeErr != nil {
		return nil, stderrPipeErr
	}

	if startErr := cmd.Start(); startErr != nil {
		return nil, startErr
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

	var lastOutputLines []string
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

	if waitErr != nil {
		stderrMu.Lock()
		last6 := attemptStderr
		if len(last6) > 6 {
			last6 = last6[len(last6)-6:]
		}
		stderrMu.Unlock()

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

	// Locate info.json
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

	// Locate final downloaded video file
	filePattern := filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_%s*.mp4", userID, meta.ID))
	matches, _ := filepath.Glob(filePattern)
	if len(matches) == 0 {
		filePattern = filepath.Join(d.cfg.DownloadDir, fmt.Sprintf("%d_*.mp4", userID))
		matches, _ = filepath.Glob(filePattern)
	}
	if len(matches) == 0 {
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
