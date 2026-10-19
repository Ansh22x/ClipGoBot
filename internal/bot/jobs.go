package bot

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
)

// BackgroundJobs orchestrates the 3 background jobs: Expiry Checker, Temp Cleanup, and Quota Reset
type BackgroundJobs struct {
	cfg      *config.Config
	database *db.DB
	bot      *gotgbot.Bot
}

// NewBackgroundJobs initializes the background jobs manager
func NewBackgroundJobs(cfg *config.Config, database *db.DB, bot *gotgbot.Bot) *BackgroundJobs {
	return &BackgroundJobs{
		cfg:      cfg,
		database: database,
		bot:      bot,
	}
}

// Start launches all 3 background routines with graceful shutdown support
func (j *BackgroundJobs) Start(ctx context.Context) {
	go j.runExpiryChecker(ctx)
	go j.runTempCleanup(ctx)
	go j.runQuotaReset(ctx)
}

// 1. Expiry Checker Job: runs every 10 minutes
func (j *BackgroundJobs) runExpiryChecker(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expiredIDs, err := j.database.ResetExpiredUsers()
			if err != nil {
				GlobalLogBuffer.Append(fmt.Sprintf("ExpiryChecker error: %s", err.Error()))
				continue
			}
			if len(expiredIDs) > 0 {
				GlobalLogBuffer.Append(fmt.Sprintf("ExpiryChecker: demoted %d expired users to free tier", len(expiredIDs)))
				// Notify expired users
				for _, uid := range expiredIDs {
					msg := "⚠️ <b>Your VIP Subscription has expired.</b>\nYour account has been reverted to the Free tier (5 downloads/day). To renew, use /plans."
					_, _ = j.bot.SendMessage(uid, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
				}
			}

			// Group expirations
			expiredGroups, err := j.database.ResetExpiredGroups()
			if err == nil && len(expiredGroups) > 0 {
				GlobalLogBuffer.Append(fmt.Sprintf("ExpiryChecker: demoted %d expired groups to free tier", len(expiredGroups)))
			}
		}
	}
}

// 2. Temp Cleanup Job: runs every 30 minutes, deletes leftover files older than 1 hour
func (j *BackgroundJobs) runTempCleanup(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// Initial cleanup on launch
	j.cleanTempFiles()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.cleanTempFiles()
		}
	}
}

func (j *BackgroundJobs) cleanTempFiles() {
	threshold := time.Now().Add(-1 * time.Hour)
	removedCount := 0

	_ = filepath.WalkDir(j.cfg.DownloadDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(threshold) {
			if os.Remove(path) == nil {
				removedCount++
			}
		}
		return nil
	})

	if removedCount > 0 {
		GlobalLogBuffer.Append(fmt.Sprintf("TempCleanup: removed %d orphaned temp files", removedCount))
	}
}

// 3. Quota Reset Job: runs every 1 minute to check for daily quota resets
func (j *BackgroundJobs) runQuotaReset(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			affected, err := j.database.ResetDailyQuotas()
			if err != nil {
				GlobalLogBuffer.Append(fmt.Sprintf("QuotaReset error: %s", err.Error()))
			} else if affected > 0 {
				GlobalLogBuffer.Append(fmt.Sprintf("QuotaReset: reset daily quota for %d users", affected))
			}

			grpAffected, grpErr := j.database.ResetGroupDailyQuotas()
			if grpErr == nil && grpAffected > 0 {
				GlobalLogBuffer.Append(fmt.Sprintf("QuotaReset: reset daily quota for %d groups", grpAffected))
			}
		}
	}
}
