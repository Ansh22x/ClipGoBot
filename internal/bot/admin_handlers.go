package bot

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// 4. /ban <user_id or @username> [reason] or reply to user message
func (h *AdminHandler) HandleBan(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	var targetID int64
	var targetUsername string
	var reason = "Violation of terms"

	// Option A: Admin replied to a user's message
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		targetID = msg.ReplyToMessage.From.Id
		targetUsername = msg.ReplyToMessage.From.Username
		if len(args) >= 2 {
			reason = strings.Join(args[1:], " ")
		}
	} else if len(args) >= 2 {
		// Option B: Admin specified ID or @username
		targetInput := strings.TrimSpace(args[1])
		if id, err := strconv.ParseInt(targetInput, 10, 64); err == nil {
			targetID = id
		} else {
			targetUsername = strings.TrimPrefix(targetInput, "@")
		}
		if len(args) >= 3 {
			reason = strings.Join(args[2:], " ")
		}
	} else {
		helpText := `ℹ️ <b>Usage:</b>
• <code>/ban &lt;user_id&gt; [reason]</code>
• <code>/ban @username [reason]</code>
• Reply to any user message with <code>/ban [reason]</code>`
		_, err := msg.Reply(b, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	bannedUser, err := h.database.BanUserFlexible(targetID, targetUsername)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Error banning user: %s", err.Error()), nil)
		return err
	}

	// If in a group chat, also attempt to ban the member from Telegram chat
	if ctx.EffectiveChat.Type != "private" {
		_, _ = b.BanChatMember(ctx.EffectiveChat.Id, bannedUser.TelegramID, nil)
	}

	usernameDisplay := "None"
	if bannedUser.Username != "" {
		usernameDisplay = "@" + bannedUser.Username
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d banned user %d (%s). Reason: %s", ctx.EffectiveUser.Id, bannedUser.TelegramID, usernameDisplay, reason))
	replyText := fmt.Sprintf("⛔ <b>User Banned!</b>\n\n🆔 <b>ID:</b> <code>%d</code>\n👤 <b>Username:</b> %s\n📝 <b>Reason:</b> %s",
		bannedUser.TelegramID, usernameDisplay, htmlEscape(reason))

	_, err = msg.Reply(b, replyText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 5. /unban <user_id or @username> or reply to user message
func (h *AdminHandler) HandleUnban(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	var targetID int64
	var targetUsername string

	// Option A: Admin replied to a user's message
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		targetID = msg.ReplyToMessage.From.Id
		targetUsername = msg.ReplyToMessage.From.Username
	} else if len(args) >= 2 {
		// Option B: Admin specified ID or @username
		targetInput := strings.TrimSpace(args[1])
		if id, err := strconv.ParseInt(targetInput, 10, 64); err == nil {
			targetID = id
		} else {
			targetUsername = strings.TrimPrefix(targetInput, "@")
		}
	} else {
		helpText := `ℹ️ <b>Usage:</b>
• <code>/unban &lt;user_id&gt;</code>
• <code>/unban @username</code>
• Reply to any user message with <code>/unban</code>`
		_, err := msg.Reply(b, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	unbannedUser, err := h.database.UnbanUserFlexible(targetID, targetUsername)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Error unbanning user: %s", err.Error()), nil)
		return err
	}

	// If in a group chat, also unban the member in Telegram chat
	if ctx.EffectiveChat.Type != "private" {
		_, _ = b.UnbanChatMember(ctx.EffectiveChat.Id, unbannedUser.TelegramID, &gotgbot.UnbanChatMemberOpts{OnlyIfBanned: true})
	}

	usernameDisplay := "None"
	if unbannedUser.Username != "" {
		usernameDisplay = "@" + unbannedUser.Username
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d unbanned user %d (%s)", ctx.EffectiveUser.Id, unbannedUser.TelegramID, usernameDisplay))
	replyText := fmt.Sprintf("✅ <b>User Unbanned!</b>\n\n🆔 <b>ID:</b> <code>%d</code>\n👤 <b>Username:</b> %s\nStatus restored to <code>active</code>.",
		unbannedUser.TelegramID, usernameDisplay)

	_, err = msg.Reply(b, replyText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 6. /user <user_id or @username> or reply
func (h *AdminHandler) HandleUser(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	args := ctx.Args()

	var usr *db.User
	var err error

	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		usr, err = h.database.GetUser(msg.ReplyToMessage.From.Id)
	} else if len(args) >= 2 {
		targetInput := strings.TrimSpace(args[1])
		if id, parseErr := strconv.ParseInt(targetInput, 10, 64); parseErr == nil {
			usr, err = h.database.GetUser(id)
		} else {
			usr, err = h.database.GetUserByUsername(targetInput)
		}
	} else {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/user &lt;user_id or @username&gt;</code> or reply to a message", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}
	if usr == nil {
		_, err := msg.Reply(b, "❌ User not found in database.", nil)
		return err
	}

	st, _ := h.database.GetUserStats(usr.TelegramID)
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

// 11. /backup: Send database file to Admin
func (h *AdminHandler) HandleBackup(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	file, err := os.Open(h.cfg.DBPath)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to open database file: %s", err.Error()), nil)
		return err
	}
	defer file.Close()

	caption := fmt.Sprintf("💾 <b>Database Backup</b>\n📅 <i>Generated on %s</i>\n\nTo restore this backup at any time, reply to this file with <code>/restore</code>.",
		time.Now().UTC().Format("02 Jan 2006 15:04:05 UTC"))

	_, err = b.SendDocument(ctx.EffectiveChat.Id, gotgbot.InputFileByReader("bot.db", file), &gotgbot.SendDocumentOpts{
		Caption:   caption,
		ParseMode: "HTML",
	})
	return err
}

// 12. /restore: Restore database from document reply
func (h *AdminHandler) HandleRestore(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage

	var targetDoc *gotgbot.Document
	if msg.Document != nil {
		targetDoc = msg.Document
	} else if msg.ReplyToMessage != nil && msg.ReplyToMessage.Document != nil {
		targetDoc = msg.ReplyToMessage.Document
	}

	if targetDoc == nil {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> Send your <code>bot.db</code> file or reply to a backup with <code>/restore</code>.", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	tgFile, err := b.GetFile(targetDoc.FileId, nil)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to fetch file from Telegram: %s", err.Error()), nil)
		return err
	}

	resp, err := http.Get(tgFile.URL(b, nil))
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to download backup: %s", err.Error()), nil)
		return err
	}
	defer resp.Body.Close()

	// Backup existing db first if it exists
	if _, err := os.Stat(h.cfg.DBPath); err == nil {
		_ = os.Rename(h.cfg.DBPath, h.cfg.DBPath+".bak")
	}

	_ = os.MkdirAll(filepath.Dir(h.cfg.DBPath), 0755)
	out, err := os.Create(h.cfg.DBPath)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to create database file: %s", err.Error()), nil)
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to write database: %s", err.Error()), nil)
		return err
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d restored database from backup file", ctx.EffectiveUser.Id))
	_, err = msg.Reply(b, "✅ <b>Database restored successfully!</b>\nAll users, license keys, and quotas have been recovered.", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 13. /setcookies: Save cookies.txt from document
func (h *AdminHandler) HandleSetCookies(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage

	var targetDoc *gotgbot.Document
	if msg.Document != nil {
		targetDoc = msg.Document
	} else if msg.ReplyToMessage != nil && msg.ReplyToMessage.Document != nil {
		targetDoc = msg.ReplyToMessage.Document
	}

	if targetDoc == nil {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> Send your <code>cookies.txt</code> file or reply to it with <code>/setcookies</code>.", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	tgFile, err := b.GetFile(targetDoc.FileId, nil)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to fetch file: %s", err.Error()), nil)
		return err
	}

	resp, err := http.Get(tgFile.URL(b, nil))
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to download cookies: %s", err.Error()), nil)
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		_, err := msg.Reply(b, fmt.Sprintf("❌ Failed to read cookies: %s", err.Error()), nil)
		return err
	}

	targets := []string{
		"cookies.txt",
		"data/cookies.txt",
		"/app/data/cookies.txt",
	}
	for _, t := range targets {
		_ = os.MkdirAll(filepath.Dir(t), 0755)
		_ = os.WriteFile(t, data, 0644)
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Admin %d installed new cookies.txt (%d bytes)", ctx.EffectiveUser.Id, len(data)))
	_, err = msg.Reply(b, fmt.Sprintf("✅ <b>Cookies installed successfully!</b>\nSaved %d bytes. YouTube downloads will now use this authenticated session to bypass 429 rate limits.", len(data)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 14. /checkcookies: Check cookies status
func (h *AdminHandler) HandleCheckCookies(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	targets := []string{"cookies.txt", "data/cookies.txt", "/app/data/cookies.txt"}
	var foundPath string
	var size int64
	for _, t := range targets {
		if fi, err := os.Stat(t); err == nil && fi.Size() > 0 {
			foundPath = t
			size = fi.Size()
			break
		}
	}

	if foundPath == "" {
		replyText := `❌ <b>No cookies.txt installed!</b>

To fix YouTube 429 Rate Limits:
1. Export cookies from a free dummy Google/YouTube account using a browser extension (like "Get cookies.txt LOCALLY").
2. Send the <code>cookies.txt</code> file here with caption <code>/setcookies</code>.
3. Or in Render dashboard, add environment variable:
   <code>COOKIES_DATA</code> = &lt;paste content of cookies.txt&gt;`
		_, err := msg.Reply(b, replyText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	replyText := fmt.Sprintf("🍪 <b>Cookies Status: Active</b>\n\n📁 <b>Location:</b> <code>%s</code>\n📏 <b>Size:</b> %d bytes\n⚡ YouTube requests are authenticated to bypass 429 blocks.", foundPath, size)
	_, err := msg.Reply(b, replyText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}
