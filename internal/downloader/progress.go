package downloader

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ProgressTracker manages throttled progress reporting to avoid Telegram rate limits
type ProgressTracker struct {
	lastEditTime    time.Time
	lastPercent     float64
	minPercentStep  float64
	minInterval     time.Duration
	updateCallback  func(statusText string)
	mu              sync.Mutex
	isFinished      bool
}

// NewProgressTracker creates a tracker with anti-rate-limit checkpoint logic
func NewProgressTracker(onUpdate func(statusText string)) *ProgressTracker {
	return &ProgressTracker{
		lastEditTime:   time.Now().Add(-10 * time.Second), // ensure first update triggers
		lastPercent:    0,
		minPercentStep: 10.0,            // 10% step checkpoint
		minInterval:    2500 * time.Millisecond, // 2.5s interval checkpoint
		updateCallback: onUpdate,
	}
}

// ProgressInfo parsed from yt-dlp output
type ProgressInfo struct {
	Percent float64
	Speed   string
	ETA     string
}

var (
	// Matches: [download]  45.2% of 25.00MiB at  3.12MiB/s ETA 00:04
	stdProgressRegex = regexp.MustCompile(`\[download\]\s+([\d\.]+)%\s+of\s+~?[\d\.]+\w+\s+at\s+([^\s]+)\s+ETA\s+([^\s]+)`)
)

// ParseProgressLine parses stdout line from yt-dlp
func ParseProgressLine(line string) *ProgressInfo {
	line = strings.TrimSpace(line)

	// Check if pipe-delimited template: percent|speed|eta
	if strings.Contains(line, "|") {
		parts := strings.Split(line, "|")
		if len(parts) >= 3 {
			pStr := strings.TrimRight(strings.TrimSpace(parts[0]), "%")
			p, err := strconv.ParseFloat(pStr, 64)
			if err == nil {
				return &ProgressInfo{
					Percent: p,
					Speed:   strings.TrimSpace(parts[1]),
					ETA:     strings.TrimSpace(parts[2]),
				}
			}
		}
	}

	// Fallback to standard yt-dlp output
	matches := stdProgressRegex.FindStringSubmatch(line)
	if len(matches) == 4 {
		p, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return &ProgressInfo{
				Percent: p,
				Speed:   matches[2],
				ETA:     matches[3],
			}
		}
	}

	return nil
}

// Update evaluates if the progress meets checkpoint conditions and triggers updateCallback
func (pt *ProgressTracker) Update(info *ProgressInfo) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.isFinished {
		return
	}

	now := time.Now()
	percentDiff := info.Percent - pt.lastPercent
	timeDiff := now.Sub(pt.lastEditTime)

	// Checkpoint logic:
	// Only edit if (percent >= lastPercent + 10%) OR (time elapsed >= 2.5s AND percent != lastPercent)
	shouldUpdate := (percentDiff >= pt.minPercentStep) || (timeDiff >= pt.minInterval && info.Percent != pt.lastPercent)

	if shouldUpdate {
		pt.lastPercent = info.Percent
		pt.lastEditTime = now

		text := RenderProgressBar(info.Percent, info.Speed, info.ETA)
		if pt.updateCallback != nil {
			go pt.updateCallback(text)
		}
	}
}

// Finish triggers final 100% update
func (pt *ProgressTracker) Finish(statusText string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.isFinished = true
	if pt.updateCallback != nil {
		go pt.updateCallback(statusText)
	}
}

// RenderProgressBar generates a visual progress bar representation
func RenderProgressBar(percent float64, speed, eta string) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	const totalBlocks = 10
	filledBlocks := int((percent / 100.0) * float64(totalBlocks))
	if filledBlocks > totalBlocks {
		filledBlocks = totalBlocks
	}
	emptyBlocks := totalBlocks - filledBlocks

	bar := strings.Repeat("█", filledBlocks) + strings.Repeat("░", emptyBlocks)

	speedStr := speed
	if speedStr == "" || speedStr == "N/A" {
		speedStr = "-- MB/s"
	}
	etaStr := eta
	if etaStr == "" || etaStr == "N/A" {
		etaStr = "--:--"
	}

	return fmt.Sprintf("⬇️ <b>Downloading media...</b>\n\n<code>[%s] %.1f%%</code>\n⚡ <b>Speed:</b> %s\n⏳ <b>ETA:</b> %s",
		bar, percent, speedStr, etaStr)
}
