package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const defaultDesktopUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// TikTokResolver resolves TikTok video URLs via the TikWM API
type TikTokResolver struct {
	client *http.Client
}

// NewTikTokResolver creates a new TikTokResolver
func NewTikTokResolver(client *http.Client) *TikTokResolver {
	if client == nil {
		client = http.DefaultClient
	}
	return &TikTokResolver{client: client}
}

func (r *TikTokResolver) Name() string {
	return "tikwm"
}

func (r *TikTokResolver) Platform() string {
	return "TikTok"
}

func (r *TikTokResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	apiURL := "https://www.tikwm.com/api/?url=" + url.QueryEscape(rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create tikwm request: %w", err)
	}

	req.Header.Set("User-Agent", defaultDesktopUA)
	req.Header.Set("Referer", "https://www.tiktok.com/")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tikwm request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tikwm returned HTTP %d", resp.StatusCode)
	}

	var data struct {
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

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode tikwm response: %w", err)
	}

	if data.Code != 0 || (data.Data.HDPlay == "" && data.Data.Play == "") {
		return nil, fmt.Errorf("tikwm error (code %d): %s", data.Code, data.Msg)
	}

	videoURL := data.Data.HDPlay
	if videoURL == "" {
		videoURL = data.Data.Play
	}

	return &Media{
		URL:      videoURL,
		Title:    data.Data.Title,
		Referer:  "https://www.tiktok.com/",
		Duration: data.Data.Duration,
		Width:    data.Data.Width,
		Height:   data.Data.Height,
	}, nil
}
