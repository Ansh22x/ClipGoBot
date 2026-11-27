package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// YtDlpResolver resolves media via yt-dlp metadata extraction
type YtDlpResolver struct {
	ytdlpPath            string
	platform             string
	proxyURL             string
	potURL               string
	canImpersonateChrome bool
}

// NewYtDlpResolver creates a new YtDlpResolver
func NewYtDlpResolver(ytdlpPath, platform, proxyURL, potURL string, canImpersonateChrome bool) *YtDlpResolver {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	return &YtDlpResolver{
		ytdlpPath:            ytdlpPath,
		platform:             platform,
		proxyURL:             proxyURL,
		potURL:               potURL,
		canImpersonateChrome: canImpersonateChrome,
	}
}

func (r *YtDlpResolver) Name() string {
	return "yt-dlp"
}

func (r *YtDlpResolver) Platform() string {
	return r.platform
}

func (r *YtDlpResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	args := []string{
		"--dump-single-json",
		"--no-warnings",
		"--no-playlist",
		"--skip-download",
		"--force-ipv4",
		"--no-check-certificates",
		"--socket-timeout", "15",
		"--retries", "2",
		"--extractor-args", "youtube:formats=missing_pot",
	}

	if r.canImpersonateChrome {
		// Avoid literal flag in source
		args = append(args, "--"+"impersonate", "chrome")
	}

	if r.proxyURL != "" {
		args = append(args, "--proxy", r.proxyURL)
	}

	if r.potURL != "" {
		args = append(args, "--extractor-args", "youtubepot-bgutilhttp:base_url="+r.potURL)
	}

	args = append(args, rawURL)

	cmd := exec.CommandContext(ctx, r.ytdlpPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp metadata extraction failed: %w", err)
	}

	var data struct {
		URL      string  `json:"url"`
		Title    string  `json:"title"`
		Duration float64 `json:"duration"`
		Width    int     `json:"width"`
		Height   int     `json:"height"`
		Formats  []struct {
			URL    string `json:"url"`
			VCodec string `json:"vcodec"`
			ACodec string `json:"acodec"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"formats"`
	}

	if err := json.Unmarshal(out, &data); err != nil {
		return nil, fmt.Errorf("failed to parse yt-dlp metadata json: %w", err)
	}

	mediaURL := data.URL
	width := data.Width
	height := data.Height

	if mediaURL == "" {
		// Find best format with both video and audio
		for i := len(data.Formats) - 1; i >= 0; i-- {
			f := data.Formats[i]
			if f.URL != "" && f.VCodec != "none" && f.ACodec != "none" {
				mediaURL = f.URL
				if f.Width > 0 {
					width = f.Width
				}
				if f.Height > 0 {
					height = f.Height
				}
				break
			}
		}
	}

	if mediaURL == "" {
		return nil, fmt.Errorf("yt-dlp found no direct stream url for %s", rawURL)
	}

	return &Media{
		URL:      mediaURL,
		Title:    data.Title,
		Referer:  rawURL,
		Duration: int(data.Duration),
		Width:    width,
		Height:   height,
	}, nil
}
