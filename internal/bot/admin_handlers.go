package bot

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
	"tg-media-bot/internal/license"
)

// LogRingBuffer keeps the last N log entries in memory
type LogRingBuffer struct {
	mu      sync.RWMutex
	entries []string
	maxSize int
}

var GlobalLogBuffer = &LogRingBuffer{maxSize: 100}

func (rb *LogRingBuffer) Append(entry string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	entryWithTime := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), entry)
	if len(rb.entries) >= rb.maxSize {
		rb.entries = rb.entries[1:]
	}
	rb.entries = append(rb.entries, entryWithTime)
}

func (rb *LogRingBuffer) Last(n int) []string {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	if n <= 0 || n > len(rb.entries) {
		n = len(rb.entries)
	}
	start := len(rb.entries) - n
	result := make([]string, n)
	copy(result, rb.entries[start:])
	return result
}

// AdminHandler manages admin commands
type AdminHandler struct {
	cfg       *config.Config
	database  *db.DB
	startTime time.Time
}

// NewAdminHandler creates a new AdminHandler
func NewAdminHandler(cfg *config.Config, database *db.DB) *AdminHandler {
	return &AdminHandler{
		cfg:       cfg,
		database:  database,
		startTime: time.Now(),
	}
}

// 1. /genkey <plan> [duration_days] [count]
func (h *AdminHandler) HandleGenKey(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		helpText := `ℹ️ <b>Usage:</b> <code>/genkey &lt;plan&gt; [duration_days] [count]</code>
<b>Available Plans:</b> <code>trial</code>, <code>daily</code>, <code>weekly</code>, <code>monthly</code>, <code>lifetime</code>
<b>Example:</b> <code>/genkey monthly 30 3</code>`
		_, err := msg.Reply(b, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	planName := strings.ToLower(args[1])
	planCfg, ok := license.Plans[planName]
	durationDays := 30
	if ok {
		durationDays = planCfg.DurationDays
	}

	count := 1

	if len(args) >= 3 {
		if d, err := strconv.Atoi(args[2]); err == nil && d > 0 {
			durationDays = d
		}
	}
	if len(args) >= 4 {
		if c, err := strconv.Atoi(args[3]); err == nil && c > 0 && c <= 20 {
			count = c
		}
	}

	var generatedKeys []string
	for i := 0; i < count; i++ {
		key, err := license.GenerateKey("PREM")
		if err != nil {
			_, err := msg.Reply(b, ErrInternalServer, nil)
			return err
		}
		if err := h.database.CreateLicense(key, planName, durationDays, 1); err != nil {
			_, err := msg.Reply(b, ErrInternalServer, nil)
			return err
		}
		generatedKeys = append(generatedKeys, key)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("✅ <b>Generated %d key(s) for plan '<code>%s</code>' (%d days):</b>\n\n", count, planName, durationDays))
	for i, k := range generatedKeys {
		sb.WriteString(fmt.Sprintf("%d. <code>%s</code>\n", i+1, k))
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d generated %d key(s) for plan '%s'", ctx.EffectiveUser.Id, count, planName))
	_, err := msg.Reply(b, sb.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 2. /revokekey <key>
func (h *AdminHandler) HandleRevokeKey(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/revokekey &lt;KEY&gt;</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	key := strings.TrimSpace(args[1])
	if err := h.database.RevokeLicense(key); err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to revoke key: %s", err.Error()), nil)
		return err
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d revoked key '%s'", ctx.EffectiveUser.Id, key))
	_, err := msg.Reply(b, fmt.Sprintf("✅ License key <code>%s</code> has been revoked.", key), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 3. /listkeys [active|redeemed|all]
func (h *AdminHandler) HandleListKeys(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	status := "all"
	if len(args) >= 2 {
		status = strings.ToLower(args[1])
	}

	keys, err := h.database.ListLicenses(status, 15)
	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}

	if len(keys) == 0 {
		_, err := msg.Reply(b, fmt.Sprintf("ℹ️ No license keys found with status: <code>%s</code>", status), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔑 <b>Recent License Keys (%s):</b>\n\n", strings.ToUpper(status)))
	for i, k := range keys {
		statusIcon := "🟢"
		if k.Status == "redeemed" {
			statusIcon = "🟡"
		} else if k.Status == "revoked" || k.Status == "expired" {
			statusIcon = "🔴"
		}
		sb.WriteString(fmt.Sprintf("%d. %s <code>%s</code> | %s (%dd) | used: %d/%d\n",
			i+1, statusIcon, k.Key, strings.ToUpper(k.Plan), k.DurationDays, k.ActivationCount, k.MaxActivations))
	}

	_, err = msg.Reply(b, sb.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 4. /ban <user_id> [reason]
func (h *AdminHandler) HandleBan(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/ban &lt;user_id&gt; [reason]</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	targetID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, err := msg.Reply(b, "❌ Invalid user ID.", nil)
		return err
	}

	reason := "Violation of terms"
	if len(args) >= 3 {
		reason = strings.Join(args[2:], " ")
	}

	if err := h.database.SetUserStatus(targetID, "banned"); err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Error: %s", err.Error()), nil)
		return err
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d banned user %d. Reason: %s", ctx.EffectiveUser.Id, targetID, reason))
	_, err = msg.Reply(b, fmt.Sprintf("⛔ User <code>%d</code> has been banned.\n<b>Reason:</b> %s", targetID, htmlEscape(reason)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 5. /unban <user_id>
func (h *AdminHandler) HandleUnban(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/unban &lt;user_id&gt;</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	targetID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, err := msg.Reply(b, "❌ Invalid user ID.", nil)
		return err
	}

	if err := h.database.SetUserStatus(targetID, "active"); err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Error: %s", err.Error()), nil)
		return err
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d unbanned user %d", ctx.EffectiveUser.Id, targetID))
	_, err = msg.Reply(b, fmt.Sprintf("✅ User <code>%d</code> has been unbanned.", targetID), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 6. /user <user_id>
func (h *AdminHandler) HandleUser(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/user &lt;user_id&gt;</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	targetID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, err := msg.Reply(b, "❌ Invalid user ID.", nil)
		return err
	}

	usr, err := h.database.GetUser(targetID)
	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}
	if usr == nil {
		_, err := msg.Reply(b, "❌ User not found in database.", nil)
		return err
	}

	st, _ := h.database.GetUserStats(targetID)
	totalDownloads := 0
	if st != nil {
		totalDownloads = st.TotalDownloads
	}

	expStr := "Never"
	if usr.ExpiresAt != nil {
		expStr = usr.ExpiresAt.Format("02 Jan 2006 15:04 UTC")
	}

	keyStr := "None"
	if usr.LicenseKey.Valid && usr.LicenseKey.String != "" {
		keyStr = usr.LicenseKey.String
	}

	userCard := fmt.Sprintf(`👤 <b>User Profile:</b> <code>%d</code>

• <b>Username:</b> @%s
• <b>Status:</b> <code>%s</code>
• <b>Plan:</b> <code>%s</code>
• <b>Bound Key:</b> <code>%s</code>
• <b>Quota Today:</b> %d / %d
• <b>Total Downloads:</b> %d
• <b>Plan Expiry:</b> %s
• <b>Registered:</b> %s`,
		usr.TelegramID, usr.Username, usr.Status, strings.ToUpper(usr.Plan),
		keyStr, usr.DailyDownloads, usr.DailyQuota, totalDownloads,
		expStr, usr.RegisteredAt.Format("02 Jan 2006"))

	_, err = msg.Reply(b, userCard, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 7. /setquota <user_id> <quota>
func (h *AdminHandler) HandleSetQuota(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 3 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/setquota &lt;user_id&gt; &lt;quota&gt;</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	targetID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, err := msg.Reply(b, "❌ Invalid user ID.", nil)
		return err
	}

	quota, err := strconv.Atoi(args[2])
	if err != nil || quota < 0 {
		_, err := msg.Reply(b, "❌ Quota must be a positive integer.", nil)
		return err
	}

	if err := h.database.SetUserQuota(targetID, quota); err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Error: %s", err.Error()), nil)
		return err
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d updated quota for user %d to %d", ctx.EffectiveUser.Id, targetID, quota))
	_, err = msg.Reply(b, fmt.Sprintf("✅ Daily quota for user <code>%d</code> updated to <b>%d</b>.", targetID, quota), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 8. /broadcast <message>
func (h *AdminHandler) HandleBroadcast(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/broadcast &lt;message text&gt;</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	broadcastContent := strings.TrimSpace(msg.Text[len(args[0]):])
	if broadcastContent == "" {
		_, err := msg.Reply(b, "❌ Broadcast message cannot be empty.", nil)
		return err
	}

	userIDs, err := h.database.GetAllActiveUserIDs()
	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}

	statusMsg, _ := msg.Reply(b, fmt.Sprintf("📢 Broadcasting to %d users in background...", len(userIDs)), nil)

	go func() {
		successCount := 0
		failCount := 0

		formattedText := fmt.Sprintf("📢 <b>Announcement:</b>\n\n%s", broadcastContent)

		for _, uid := range userIDs {
			_, err := b.SendMessage(uid, formattedText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			if err != nil {
				failCount++
			} else {
				successCount++
			}
			time.Sleep(50 * time.Millisecond) // avoid Telegram global flood limits
		}

		resultText := fmt.Sprintf("✅ <b>Broadcast Completed!</b>\n\n📤 <b>Delivered:</b> %d\n❌ <b>Failed:</b> %d", successCount, failCount)
		if statusMsg != nil {
			_, _, _ = statusMsg.EditText(b, resultText, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
		}
		GlobalLogBuffer.Append(fmt.Sprintf("Broadcast completed: %d sent, %d failed", successCount, failCount))
	}()

	return nil
}

// 9. /stats_admin or /system
func (h *AdminHandler) HandleStatsAdmin(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage

	st, err := h.database.GetGlobalStats()
	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := float64(m.Alloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024
	uptime := time.Since(h.startTime).Truncate(time.Second)

	systemStats := fmt.Sprintf(`🖥️ <b>System & Bot Analytics</b>

👥 <b>Total Registered Users:</b> %d
🌟 <b>Active VIP Subscribers:</b> %d
📥 <b>Total Downloads (All-Time):</b> %d
📅 <b>Downloads Completed Today:</b> %d

⏱️ <b>Bot Uptime:</b> <code>%s</code>
💾 <b>Memory Alloc:</b> <code>%.2f MB</code> (Sys: <code>%.2f MB</code>)
🧵 <b>Goroutines:</b> <code>%d</code>
⚙️ <b>Go Version:</b> <code>%s</code>`,
		st.TotalUsers, st.ActivePremiumUsers, st.TotalDownloadsAllTime, st.TotalDownloadsToday,
		uptime.String(), allocMB, sysMB, runtime.NumGoroutine(), runtime.Version())

	_, err = msg.Reply(b, systemStats, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 10. /logs [n_lines]
func (h *AdminHandler) HandleLogs(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	n := 15
	if len(args) >= 2 {
		if val, err := strconv.Atoi(args[1]); err == nil && val > 0 {
			n = val
		}
	}

	lines := GlobalLogBuffer.Last(n)
	if len(lines) == 0 {
		_, err := msg.Reply(b, "ℹ️ No recent application logs available.", nil)
		return err
	}

	logText := fmt.Sprintf("📜 <b>Recent Application Logs (Last %d):</b>\n\n<code>%s</code>",
		len(lines), htmlEscape(strings.Join(lines, "\n")))

	_, err := msg.Reply(b, logText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}
