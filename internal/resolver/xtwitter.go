package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var twitterStatusRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:(?:www\.|m\.|mobile\.)?(?:twitter\.com|x\.com))\/([a-zA-Z0-9_]+)\/status(?:es)?\/(\d+)`)

// ExtractTwitterStatus extracts user and status id from a Twitter/X URL
func ExtractTwitterStatus(rawURL string) (user, id string, err error) {
	rawURL = strings.TrimSpace(rawURL)
	rawURL = strings.TrimRight(rawURL, ".,!?:;\"')]>}")
	matches := twitterStatusRegex.FindStringSubmatch(rawURL)
	if len(matches) < 3 || matches[1] == "" || matches[2] == "" {
		return "", "", fmt.Errorf("could not extract status id from %s", rawURL)
	}
	return matches[1], matches[2], nil
}

// XResolver resolves Twitter/X video URLs via the FxTwitter API
type XResolver struct {
	client *http.Client
}

// NewXResolver creates a new XResolver
func NewXResolver(client *http.Client) *XResolver {
	if client == nil {
		client = http.DefaultClient
	}
	return &XResolver{client: client}
}

func (r *XResolver) Name() string {
	return "fxtwitter"
}

func (r *XResolver) Platform() string {
	return "Twitter/X"
}

func (r *XResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	user, id, err := ExtractTwitterStatus(rawURL)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("https://api.fxtwitter.com/%s/status/%s", user, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create fxtwitter request: %w", err)
	}

	req.Header.Set("User-Agent", defaultDesktopUA)
	req.Header.Set("Referer", "https://x.com/")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fxtwitter request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fxtwitter returned HTTP %d", resp.StatusCode)
	}

	var data struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Tweet   struct {
			Text  string `json:"text"`
			Media struct {
				Videos []struct {
					URL      string  `json:"url"`
					Bitrate  int     `json:"bitrate"`
					Duration float64 `json:"duration"`
					Width    int     `json:"width"`
					Height   int     `json:"height"`
				} `json:"videos"`
			} `json:"media"`
		} `json:"tweet"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode fxtwitter response: %w", err)
	}

	videos := data.Tweet.Media.Videos
	if len(videos) == 0 {
		return nil, fmt.Errorf("no videos found in tweet %s", id)
	}

	// Pick the entry with the highest bitrate
	bestIdx := 0
	highestBitrate := -1
	for i, v := range videos {
		if v.Bitrate > highestBitrate {
			highestBitrate = v.Bitrate
			bestIdx = i
		}
	}

	best := videos[bestIdx]
	return &Media{
		URL:      best.URL,
		Title:    data.Tweet.Text,
		Referer:  "https://x.com/",
		Duration: int(best.Duration),
		Width:    best.Width,
		Height:   best.Height,
	}, nil
}
