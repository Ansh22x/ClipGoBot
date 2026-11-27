package resolver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const (
	igAlphabet       = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	defaultMobileUA  = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.6723.107 Mobile Safari/537.36"
	ErrIgRateLimit   = "ERR_IG_RATE_LIMIT"
	ErrIgAuth        = "ERR_IG_AUTH"
)

var (
	igShortcodeRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:(?:www\.|m\.)?instagram\.com)\/(?:p|reel|reels|tv)\/([a-zA-Z0-9_-]+)`)
	igStoriesRegex   = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:(?:www\.|m\.)?instagram\.com)\/stories\/[^\/\s]+\/(\d+)`)
)

// ShortcodeToID converts an Instagram shortcode to a numeric media ID
func ShortcodeToID(sc string) (string, error) {
	return shortcodeToID(sc)
}

func shortcodeToID(sc string) (string, error) {
	if sc == "" {
		return "", errors.New("empty shortcode")
	}

	n := big.NewInt(0)
	base := big.NewInt(64)

	for _, r := range sc {
		idx := strings.IndexRune(igAlphabet, r)
		if idx == -1 {
			return "", fmt.Errorf("invalid character %q in shortcode %q", r, sc)
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	return n.String(), nil
}

// ExtractInstagramTarget extracts the numeric media ID or shortcode from an Instagram URL
func ExtractInstagramTarget(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimRight(rawURL, ".,!?:;\"')]>}")

	// Check stories numeric ID first
	if m := igStoriesRegex.FindStringSubmatch(rawURL); len(m) > 1 && m[1] != "" {
		return m[1], nil
	}

	// Check standard posts/reels/tv shortcodes
	if m := igShortcodeRegex.FindStringSubmatch(rawURL); len(m) > 1 && m[1] != "" {
		return shortcodeToID(m[1])
	}

	return "", fmt.Errorf("could not extract instagram shortcode or media id from %s", rawURL)
}

// ExtractInstagramShortcode extracts the raw shortcode string from URL
func ExtractInstagramShortcode(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimRight(rawURL, ".,!?:;\"')]>}")

	if m := igStoriesRegex.FindStringSubmatch(rawURL); len(m) > 1 && m[1] != "" {
		return m[1], nil
	}
	if m := igShortcodeRegex.FindStringSubmatch(rawURL); len(m) > 1 && m[1] != "" {
		return m[1], nil
	}
	return "", fmt.Errorf("could not extract instagram shortcode from %s", rawURL)
}

// InstagramResolver resolves Instagram media via the mobile API v1
type InstagramResolver struct {
	client     *http.Client
	cookieFile string
}

// NewInstagramResolver creates a new InstagramResolver
func NewInstagramResolver(client *http.Client, cookieFile string) *InstagramResolver {
	if client == nil {
		client = http.DefaultClient
	}
	return &InstagramResolver{
		client:     client,
		cookieFile: cookieFile,
	}
}

func (r *InstagramResolver) Name() string {
	return "instagram_api"
}

func (r *InstagramResolver) Platform() string {
	return "Instagram"
}

func (r *InstagramResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	mediaID, err := ExtractInstagramTarget(rawURL)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("https://i.instagram.com/api/v1/media/%s/info/", mediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create instagram request: %w", err)
	}

	req.Header.Set("User-Agent", defaultMobileUA)
	req.Header.Set("X-IG-App-ID", "936619743392459")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://www.instagram.com/")
	req.Header.Set("Accept", "*/*")

	// If cookie file exists, add Cookie header containing only sessionid
	if sessID := r.parseSessionID(); sessID != "" {
		req.Header.Set("Cookie", fmt.Sprintf("sessionid=%s", sessID))
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("instagram request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests { // 429
		return nil, errors.New(ErrIgRateLimit)
	}
	if resp.StatusCode == http.StatusUnauthorized { // 401
		return nil, errors.New(ErrIgAuth)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instagram api returned HTTP %d", resp.StatusCode)
	}

	var data struct {
		Items []struct {
			Caption struct {
				Text string `json:"text"`
			} `json:"caption"`
			VideoDuration float64 `json:"video_duration"`
			VideoVersions []struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"video_versions"`
			CarouselMedia []struct {
				VideoVersions []struct {
					URL    string `json:"url"`
					Width  int    `json:"width"`
					Height int    `json:"height"`
				} `json:"video_versions"`
			} `json:"carousel_media"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode instagram response: %w", err)
	}

	if len(data.Items) == 0 {
		return nil, fmt.Errorf("no items returned for instagram media %s", mediaID)
	}

	item := data.Items[0]
	var videoURL string
	var width, height int

	if len(item.VideoVersions) > 0 {
		videoURL = item.VideoVersions[0].URL
		width = item.VideoVersions[0].Width
		height = item.VideoVersions[0].Height
	} else if len(item.CarouselMedia) > 0 {
		for _, cm := range item.CarouselMedia {
			if len(cm.VideoVersions) > 0 {
				videoURL = cm.VideoVersions[0].URL
				width = cm.VideoVersions[0].Width
				height = cm.VideoVersions[0].Height
				break
			}
		}
	}

	if videoURL == "" {
		return nil, fmt.Errorf("no video version found in instagram media %s", mediaID)
	}

	return &Media{
		URL:      videoURL,
		Title:    item.Caption.Text,
		Referer:  "https://www.instagram.com/",
		Duration: int(item.VideoDuration),
		Width:    width,
		Height:   height,
	}, nil
}

func (r *InstagramResolver) parseSessionID() string {
	candidates := []string{r.cookieFile, "data/cookies.txt", "/app/data/cookies.txt", "cookies.txt"}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) >= 7 {
				name := parts[5]
				val := parts[6]
				if name == "sessionid" && val != "" {
					return val
				}
			}
		}
	}
	return ""
}
