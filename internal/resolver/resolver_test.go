package resolver

import (
	"context"
	"errors"
	"testing"
)

func TestShortcodeToID(t *testing.T) {
	tests := []struct {
		name      string
		shortcode string
		want      string
		wantErr   bool
	}{
		{
			name:      "single char B (index 1)",
			shortcode: "B",
			want:      "1",
			wantErr:   false,
		},
		{
			name:      "two chars BA (1*64 + 0)",
			shortcode: "BA",
			want:      "64",
			wantErr:   false,
		},
		{
			name:      "single char C (index 2)",
			shortcode: "C",
			want:      "2",
			wantErr:   false,
		},
		{
			name:      "two chars D_ (3*64 + 63)",
			shortcode: "D_",
			want:      "255",
			wantErr:   false,
		},
		{
			name:      "known shortcode DgJQVGYHjTc",
			shortcode: "DgJQVGYHjTc",
			want:      "4037830359614633180",
			wantErr:   false,
		},
		{
			name:      "empty string",
			shortcode: "",
			want:      "",
			wantErr:   true,
		},
		{
			name:      "invalid character $",
			shortcode: "AB$CD",
			want:      "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShortcodeToID(tt.shortcode)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ShortcodeToID(%q) error = %v, wantErr %v", tt.shortcode, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ShortcodeToID(%q) = %q, want %q", tt.shortcode, got, tt.want)
			}
		})
	}
}

func TestExtractTwitterStatus(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantUser string
		wantID   string
		wantErr  bool
	}{
		{
			name:     "standard twitter.com",
			url:      "https://twitter.com/jack/status/20",
			wantUser: "jack",
			wantID:   "20",
			wantErr:  false,
		},
		{
			name:     "x.com with query string",
			url:      "https://x.com/elonmusk/status/1789012345678901234?s=20&t=abcdef",
			wantUser: "elonmusk",
			wantID:   "1789012345678901234",
			wantErr:  false,
		},
		{
			name:     "mobile twitter with trailing slash and punctuation",
			url:      "https://mobile.twitter.com/user_name/statuses/9876543210/...",
			wantUser: "user_name",
			wantID:   "9876543210",
			wantErr:  false,
		},
		{
			name:     "no status id",
			url:      "https://twitter.com/explore",
			wantUser: "",
			wantID:   "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, id, err := ExtractTwitterStatus(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractTwitterStatus(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if user != tt.wantUser || id != tt.wantID {
				t.Errorf("ExtractTwitterStatus(%q) = (%q, %q), want (%q, %q)", tt.url, user, id, tt.wantUser, tt.wantID)
			}
		})
	}
}

func TestExtractInstagramShortcodeAndTarget(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		wantShortcode string
		wantID        string
		wantErr       bool
	}{
		{
			name:          "reel with query param",
			url:           "https://www.instagram.com/reel/DgJQVGYHjTc/?utm_source=ig_web_copy_link",
			wantShortcode: "DgJQVGYHjTc",
			wantID:        "4037830359614633180",
			wantErr:       false,
		},
		{
			name:          "post with trailing slash",
			url:           "https://instagram.com/p/B_987654/",
			wantShortcode: "B_987654",
			wantID:        "8793894137464",
			wantErr:       false,
		},
		{
			name:          "reels plural with punctuation",
			url:           "https://www.instagram.com/reels/C-xyz123/).",
			wantShortcode: "C-xyz123",
			wantID:        "13110166379959",
			wantErr:       false,
		},
		{
			name:          "tv video",
			url:           "https://m.instagram.com/tv/BA/",
			wantShortcode: "BA",
			wantID:        "64",
			wantErr:       false,
		},
		{
			name:          "stories numeric id",
			url:           "https://www.instagram.com/stories/some_creator/3216549871234567890/?view_single=true",
			wantShortcode: "3216549871234567890",
			wantID:        "3216549871234567890",
			wantErr:       false,
		},
		{
			name:          "invalid instagram url",
			url:           "https://www.instagram.com/explore/",
			wantShortcode: "",
			wantID:        "",
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc, err := ExtractInstagramShortcode(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractInstagramShortcode(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if sc != tt.wantShortcode {
				t.Errorf("ExtractInstagramShortcode(%q) = %q, want %q", tt.url, sc, tt.wantShortcode)
			}

			id, err := ExtractInstagramTarget(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractInstagramTarget(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if id != tt.wantID {
				t.Errorf("ExtractInstagramTarget(%q) = %q, want %q", tt.url, id, tt.wantID)
			}
		})
	}
}

type mockResolver struct {
	name     string
	platform string
	res      *Media
	err      error
	called   int
}

func (m *mockResolver) Name() string { return m.name }
func (m *mockResolver) Platform() string { return m.platform }
func (m *mockResolver) Resolve(ctx context.Context, rawURL string) (*Media, error) {
	m.called++
	return m.res, m.err
}

func TestChainResolveAndHealth(t *testing.T) {
	r1 := &mockResolver{name: "res1", platform: "TikTok", err: errors.New("fail 1")}
	r2 := &mockResolver{name: "res2", platform: "TikTok", res: &Media{URL: "https://example.com/video.mp4"}}

	chain := NewChain(r1, r2)

	// First attempt: r1 fails, r2 succeeds
	m, err := chain.Resolve(context.Background(), "TikTok", "https://tiktok.com/@user/video/1")
	if err != nil {
		t.Fatalf("expected success from r2, got: %v", err)
	}
	if m.URL != "https://example.com/video.mp4" {
		t.Errorf("got URL %q, want https://example.com/video.mp4", m.URL)
	}
	if r1.called != 1 || r2.called != 1 {
		t.Errorf("call counts: r1=%d, r2=%d", r1.called, r2.called)
	}

	// Fail r1 two more times to trip circuit breaker (3 fails = broken for 15m)
	r2.err = errors.New("r2 temporary fail")
	r2.res = nil
	_, _ = chain.Resolve(context.Background(), "TikTok", "url")
	_, _ = chain.Resolve(context.Background(), "TikTok", "url")

	if !chain.Broken("res1") {
		t.Errorf("expected res1 to be broken after 3 fails")
	}

	// Next resolve should skip res1 and only call res2
	r1CallsBefore := r1.called
	_, _ = chain.Resolve(context.Background(), "TikTok", "url")
	if r1.called != r1CallsBefore {
		t.Errorf("expected res1 to be skipped when broken, but was called (%d -> %d)", r1CallsBefore, r1.called)
	}

	// Platform should also be marked broken after consecutive failures
	if !chain.Broken("TikTok") {
		t.Errorf("expected platform TikTok to be marked broken")
	}
}
