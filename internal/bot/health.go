package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"tg-media-bot/internal/config"
)

// HealthResponse represents the detailed /healthz payload
type HealthResponse struct {
	Status        string            `json:"status"`
	YtDlp         YtDlpHealth       `json:"yt_dlp"`
	Impersonation ImpersonateHealth `json:"impersonation"`
	Cookies       CookiesHealth     `json:"cookies"`
	Bgutil        BgutilHealth      `json:"bgutil"`
}

type YtDlpHealth struct {
	Version string `json:"version"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

type ImpersonateHealth struct {
	Available bool     `json:"available"`
	Targets   []string `json:"targets,omitempty"`
	Status    string   `json:"status"`
	Error     string   `json:"error,omitempty"`
}

type CookiesHealth struct {
	Path        string `json:"path,omitempty"`
	Exists      bool   `json:"exists"`
	Valid       bool   `json:"valid"`
	CookieCount int    `json:"cookie_count"`
	Error       string `json:"error,omitempty"`
}

type BgutilHealth struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// HealthHandler returns an HTTP handler for /healthz reporting detailed component statuses
func HealthHandler(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		ytHealth := checkYtDlp(ctx, cfg.YtDlpPath)
		impHealth := checkImpersonation(ctx, cfg.YtDlpPath)
		cookieHealth := checkCookies()
		bgHealth := checkBgutil(ctx, cfg.PotProviderURL)

		overallStatus := "ok"
		if ytHealth.Status != "ok" {
			overallStatus = "error"
		} else if !impHealth.Available || !cookieHealth.Valid || !bgHealth.Reachable {
			overallStatus = "degraded"
		}

		resp := HealthResponse{
			Status:        overallStatus,
			YtDlp:         ytHealth,
			Impersonation: impHealth,
			Cookies:       cookieHealth,
			Bgutil:        bgHealth,
		}

		w.Header().Set("Content-Type", "application/json")
		if overallStatus == "error" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func checkYtDlp(ctx context.Context, ytdlpPath string) YtDlpHealth {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	cmd := exec.CommandContext(ctx, ytdlpPath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return YtDlpHealth{
			Status: "error",
			Error:  err.Error(),
		}
	}
	return YtDlpHealth{
		Version: strings.TrimSpace(string(out)),
		Status:  "ok",
	}
}

func checkImpersonation(ctx context.Context, ytdlpPath string) ImpersonateHealth {
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}
	cmd := exec.CommandContext(ctx, ytdlpPath, "--list-impersonate-targets")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ImpersonateHealth{
			Available: false,
			Status:    "error",
			Error:     err.Error(),
		}
	}
	outStr := string(out)
	if strings.Contains(strings.ToLower(outStr), "unavailable") || strings.TrimSpace(outStr) == "" {
		return ImpersonateHealth{
			Available: false,
			Status:    "unavailable",
			Error:     strings.TrimSpace(outStr),
		}
	}

	lines := strings.Split(outStr, "\n")
	var targets []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" && !strings.HasPrefix(trimmed, "Available") && !strings.HasPrefix(trimmed, "[") {
			parts := strings.Fields(trimmed)
			if len(parts) > 0 {
				targets = append(targets, parts[0])
			}
		}
	}

	return ImpersonateHealth{
		Available: len(targets) > 0 || strings.Contains(strings.ToLower(outStr), "chrome"),
		Targets:   targets,
		Status:    "ok",
	}
}

func checkCookies() CookiesHealth {
	cookieCandidates := []string{
		"/app/data/cookies.txt",
		"data/cookies.txt",
		"cookies.txt",
	}

	var foundPath string
	for _, p := range cookieCandidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			foundPath = p
			break
		}
	}

	if foundPath == "" {
		return CookiesHealth{
			Exists: false,
			Valid:  false,
			Error:  "no cookie file found or file is empty",
		}
	}

	f, err := os.Open(foundPath)
	if err != nil {
		return CookiesHealth{
			Path:   foundPath,
			Exists: true,
			Valid:  false,
			Error:  err.Error(),
		}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	validCount := 0
	parseErrors := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "# ") || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 7 {
			parts = strings.Fields(line)
			if len(parts) < 7 {
				parseErrors++
				continue
			}
		}
		validCount++
	}

	if err := scanner.Err(); err != nil {
		return CookiesHealth{
			Path:        foundPath,
			Exists:      true,
			Valid:       false,
			CookieCount: validCount,
			Error:       err.Error(),
		}
	}

	if validCount == 0 {
		return CookiesHealth{
			Path:        foundPath,
			Exists:      true,
			Valid:       false,
			CookieCount: 0,
			Error:       fmt.Sprintf("cookie file contains 0 valid Netscape cookie entries (format errors: %d)", parseErrors),
		}
	}

	return CookiesHealth{
		Path:        foundPath,
		Exists:      true,
		Valid:       true,
		CookieCount: validCount,
	}
}

func checkBgutil(ctx context.Context, potProviderURL string) BgutilHealth {
	if potProviderURL == "" {
		return BgutilHealth{
			URL:       "",
			Reachable: false,
			Error:     "POT_PROVIDER_URL is not configured",
		}
	}

	pingURL := strings.TrimRight(potProviderURL, "/") + "/ping"
	req, err := http.NewRequestWithContext(ctx, "GET", pingURL, nil)
	if err != nil {
		return BgutilHealth{
			URL:       pingURL,
			Reachable: false,
			Error:     err.Error(),
		}
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return BgutilHealth{
			URL:       pingURL,
			Reachable: false,
			Error:     err.Error(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return BgutilHealth{
			URL:       pingURL,
			Reachable: true,
		}
	}

	return BgutilHealth{
		URL:       pingURL,
		Reachable: false,
		Error:     fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}
