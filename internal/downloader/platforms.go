package downloader

import (
	"regexp"
	"strings"
)

// Supported media platforms
const (
	PlatformYouTube   = "YouTube"
	PlatformInstagram = "Instagram"
	PlatformTikTok    = "TikTok"
	PlatformTwitter   = "Twitter/X"
)

// Regex patterns for the 4 platforms
var (
	ytRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:www\.|m\.)?(?:youtube\.com\/(?:watch\?(?:.*&)?v=|shorts\/|embed\/|v\/|live\/)|youtu\.be\/)([a-zA-Z0-9_-]{11})[^\s]*`)
	igRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:www\.)?instagram\.com\/(?:p|reel|reels|tv|stories\/[^\/\s]+)\/([a-zA-Z0-9_-]+)[^\s]*`)
	ttRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:(?:www|vm|vt|m)\.)?tiktok\.com\/(?:@[\w.-]+\/video\/\d+|t\/[a-zA-Z0-9]+|[\w.-]+\S*)[^\s]*`)
	twRegex = regexp.MustCompile(`(?i)(?:https?:\/\/)?(?:www\.)?(?:twitter\.com|x\.com)\/[a-zA-Z0-9_]+\/status(?:es)?\/(\d+)[^\s]*`)
)

// ExtractedLink represents a detected media link and its target platform
type ExtractedLink struct {
	Platform string
	URL      string
}

// DetectLink extracts any supported URL from raw message text
func DetectLink(text string) *ExtractedLink {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// 1. YouTube
	if loc := ytRegex.FindString(text); loc != "" {
		return &ExtractedLink{
			Platform: PlatformYouTube,
			URL:      cleanURL(loc),
		}
	}

	// 2. Instagram
	if loc := igRegex.FindString(text); loc != "" {
		return &ExtractedLink{
			Platform: PlatformInstagram,
			URL:      cleanURL(loc),
		}
	}

	// 3. TikTok
	if loc := ttRegex.FindString(text); loc != "" {
		return &ExtractedLink{
			Platform: PlatformTikTok,
			URL:      cleanURL(loc),
		}
	}

	// 4. Twitter / X
	if loc := twRegex.FindString(text); loc != "" {
		return &ExtractedLink{
			Platform: PlatformTwitter,
			URL:      cleanURL(loc),
		}
	}

	return nil
}

func cleanURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return raw
}
