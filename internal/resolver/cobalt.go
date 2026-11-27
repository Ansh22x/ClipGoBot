package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// CobaltResolver resolves media URLs using a self-hosted or public Cobalt instance
type CobaltResolver struct {
	cobaltURL string
	platform  string
	client    *http.Client
}

// NewCobaltResolver creates a new CobaltResolver
func NewCobaltResolver(cobaltURL, platform string, client *http.Client) *CobaltResolver {
	if client == nil {
		client = http.DefaultClient
	}
	cobaltURL = strings.TrimRight(cobaltURL, "/")
	if cobaltURL == "" {
		cobaltURL = "http://cobalt:9000"
	}
	return &CobaltResolver{
		cobaltURL: cobaltURL,
		platform:  platform,
		client:    client,
	}
}

func (r *CobaltResolver) Name() string {
	return "cobalt"
}

func (r *CobaltResolver) Platform() string {
	return r.platform
}

func (r *CobaltResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	reqBody, err := json.Marshal(map[string]string{
		"url": rawURL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to encode cobalt request: %w", err)
	}

	apiURL := r.cobaltURL + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create cobalt request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cobalt request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cobalt returned HTTP %d", resp.StatusCode)
	}

	var data struct {
		Status   string `json:"status"`
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Text     string `json:"text"`
		Error    struct {
			Code string `json:"code"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode cobalt response: %w", err)
	}

	// Only accept status=="tunnel" or "redirect" with a non-empty url
	if (data.Status != "tunnel" && data.Status != "redirect") || data.URL == "" {
		return nil, fmt.Errorf("cobalt rejected url: status=%s code=%s text=%s", data.Status, data.Error.Code, data.Text)
	}

	return &Media{
		URL:     data.URL,
		Title:   data.Filename,
		Referer: rawURL,
	}, nil
}
