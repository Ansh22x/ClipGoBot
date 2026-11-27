package resolver

import (
	"context"
	"net/http"
)

// YouTubeResolver is a thin wrapper over CobaltResolver for YouTube
type YouTubeResolver struct {
	cobalt *CobaltResolver
}

// NewYouTubeResolver creates a new YouTubeResolver backed by Cobalt
func NewYouTubeResolver(cobaltURL string, client *http.Client) *YouTubeResolver {
	return &YouTubeResolver{
		cobalt: NewCobaltResolver(cobaltURL, "YouTube", client),
	}
}

func (r *YouTubeResolver) Name() string {
	return "cobalt"
}

func (r *YouTubeResolver) Platform() string {
	return "YouTube"
}

func (r *YouTubeResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	return r.cobalt.Resolve(ctx, rawURL)
}
