package bot

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
	"tg-media-bot/internal/downloader"
	"tg-media-bot/internal/license"
)

// UserHandler manages user-facing commands and media download triggers
type UserHandler struct {
	cfg        *config.Config
	database   *db.DB
	dl         *downloader.Downloader
	middleware *MiddlewareManager
	startTime  time.Time
}

// NewUserHandler creates a new UserHandler
func NewUserHandler(cfg *config.Config, database *db.DB, dl *downloader.Downloader, mw *MiddlewareManager) *UserHandler {
	return &UserHandler{
		cfg:        cfg,
		database:   database,
		dl:         dl,
		middleware: mw,
		startTime:  time.Now(),
	}
}

// 1. /start command
func (h *UserHandler) HandleStart(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser

	greeting := fmt.Sprintf(`👋 <b>Welcome, %s!</b>

I am your <b>High-Speed Media Downloader Bot</b>. 🚀
I can effortlessly download videos, shorts, reels, and clips from:
• <b>YouTube</b> (Videos, Shorts)
• <b>Instagram</b> (Reels, Posts, Stories)
• <b>TikTok</b> (Videos, Clips without watermark)
• <b>Twitter / X</b> (Videos, GIFs)

⚡ <b>How to Use:</b>
Simply <b>send or paste any media link</b> directly into this chat! No prefix or /download command needed.

📌 <b>Commands:</b>
/help - View full guide and options
/status - Check your subscription and daily quota
/redeem &lt;key&gt; - Redeem a license key
/activategroup &lt;key&gt; - Activate VIP for a whole group
/plans - View VIP subscription plans & buy VIP
/stats - Your download statistics
/ping - Bot health and response latency
/cancel - Abort your ongoing download`, htmlEscape(user.FirstName))

	_, err := msg.Reply(b, greeting, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 2. /help command
func (h *UserHandler) HandleHelp(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage

	helpText := `📖 <b>User Guide & Commands</b>

1️⃣ <b>Downloading Media:</b>
Just send any link from YouTube, Instagram, TikTok, or Twitter/X.
The bot will automatically detect it, download the highest quality MP4, and upload it back to you.

2️⃣ <b>Available Commands:</b>
• /start - Start the bot & menu
• /help - Display this manual
• /status - Check daily quota remaining & plan expiry
• /redeem &lt;KEY&gt; - Activate a premium subscription key
• /activategroup &lt;KEY&gt; - Activate VIP downloads for this entire group
• /plans - See pricing and duration of premium tiers (click to buy)
• /stats - Personal download count and data metrics
• /ping - Check system latency and server status
• /cancel - Cancel an active download in progress

3️⃣ <b>Limits & Rules:</b>
• Free tier users have 5 downloads per 24 hours.
• Group VIP plans provide high collective quotas for all group members!
• Maximum file size via standard Telegram API is 50 MB.
• Upgrade via /plans to increase your quota and unlock priority speed!`

	_, err := msg.Reply(b, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 3. /status command
func (h *UserHandler) HandleStatus(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser

	u, err := h.database.GetUser(user.Id)
	if err != nil || u == nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}

	expiryStr := "Never (Free Tier)"
	if u.Plan == "lifetime" {
		expiryStr = "Permanent (Lifetime VIP)"
	} else if u.ExpiresAt != nil {
		expiryStr = u.ExpiresAt.Format("02 Jan 2006, 15:04 UTC")
		if time.Now().UTC().After(*u.ExpiresAt) {
			expiryStr += " (Expired)"
		}
	}

	keyMasked := "None"
	if u.LicenseKey.Valid && u.LicenseKey.String != "" {
		k := u.LicenseKey.String
		if len(k) > 8 {
			keyMasked = k[:4] + "-****-****-" + k[len(k)-4:]
		} else {
			keyMasked = k
		}
	}

	remainingQuota := u.DailyQuota - u.DailyDownloads
	if remainingQuota < 0 {
		remainingQuota = 0
	}

	var groupText string
	if ctx.EffectiveChat.Type != "private" {
		g, _ := h.database.GetGroup(ctx.EffectiveChat.Id)
		if g != nil && g.Plan != "free" {
			gExp := "Permanent (Lifetime VIP)"
			if g.ExpiresAt != nil {
				gExp = g.ExpiresAt.Format("02 Jan 2006, 15:04 UTC")
			}
			gRemaining := g.DailyQuota - g.DailyDownloads
			if gRemaining < 0 {
				gRemaining = 0
			}
			groupText = fmt.Sprintf("\n\n👥 <b>Group VIP Status:</b>\n🏷️ <b>Plan:</b> <code>%s</code>\n📅 <b>Expires:</b> %s\n🔋 <b>Group Quota Remaining:</b> <b>%d / %d</b>",
				strings.ToUpper(g.Plan), gExp, gRemaining, g.DailyQuota)
		} else {
			groupText = "\n\n👥 <b>Group Status:</b> Free tier (Use <code>/activategroup &lt;KEY&gt;</code> to activate VIP for this group!)"
		}
	}

	statusText := fmt.Sprintf(`📊 <b>Account Status</b>

👤 <b>User:</b> %s (<code>%d</code>)
🏷️ <b>Personal Plan:</b> <code>%s</code>
⚡ <b>Status:</b> <code>%s</code>
🔑 <b>License:</b> <code>%s</code>
📅 <b>Expires:</b> %s

📈 <b>Personal Quota:</b> %d / %d used
🔋 <b>Remaining Today:</b> <b>%d downloads</b>%s`,
		htmlEscape(user.FirstName), user.Id, strings.ToUpper(u.Plan), strings.ToUpper(u.Status),
		keyMasked, expiryStr, u.DailyDownloads, u.DailyQuota, remainingQuota, groupText)

	_, err = msg.Reply(b, statusText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 4. /redeem command
func (h *UserHandler) HandleRedeem(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	args := ctx.Args()

	if len(args) < 2 {
		_, err := msg.Reply(b, "ℹ️ <b>Usage:</b> <code>/redeem &lt;LICENSE_KEY&gt;</code>\nExample: <code>/redeem PREM-XXXX-XXXX-XXXX</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	rawKey := strings.TrimSpace(args[1])
	res, err := license.RedeemKey(h.database, user.Id, rawKey)
	if err != nil {
		errMsg := ResolveErrorMessage(err.Error())
		_, replyErr := msg.Reply(b, errMsg, nil)
		return replyErr
	}

	expStr := "Lifetime"
	if res.ExpiresAt != nil {
		expStr = res.ExpiresAt.Format("02 Jan 2006, 15:04 UTC")
	}

	successMsg := fmt.Sprintf(`🎉 <b>License Activated Successfully!</b>

🏷️ <b>Activated Plan:</b> <code>%s</code>
⏱️ <b>Duration:</b> %d days
🔋 <b>New Daily Quota:</b> %d downloads/day
📅 <b>Valid Until:</b> %s

Enjoy your high-speed downloads! Send any media link to begin.`,
		strings.ToUpper(res.Plan), res.DurationDays, res.DailyQuota, expStr)

	_, err = msg.Reply(b, successMsg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 5. /plans command
func (h *UserHandler) HandlePlans(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage

	plansMsg := `⭐ <b>Available VIP Subscription Plans</b>

1️⃣ <b>Daily Pass</b>
• Duration: 24 Hours
• Quota: 30 downloads / day
• Max Quality & Fast Speed

2️⃣ <b>Weekly VIP</b>
• Duration: 7 Days
• Quota: 60 downloads / day
• Priority Download Queue

3️⃣ <b>Monthly Pro</b>
• Duration: 30 Days
• Quota: 120 downloads / day
• Priority Server Bandwidth

4️⃣ <b>Lifetime Ultra</b>
• Duration: Permanent
• Quota: Unlimited downloads
• VIP Badge & Dedicated Speed

💡 <b>Select a plan below to request purchase:</b> The owner will be notified immediately!`

	buttons := gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{
				{Text: "💎 Buy Daily Pass", CallbackData: "buy_plan:daily"},
				{Text: "⭐ Buy Weekly VIP", CallbackData: "buy_plan:weekly"},
			},
			{
				{Text: "🔥 Buy Monthly Pro", CallbackData: "buy_plan:monthly"},
				{Text: "👑 Buy Lifetime Ultra", CallbackData: "buy_plan:lifetime"},
			},
		},
	}

	if len(h.cfg.AdminIDs) > 0 {
		buttons.InlineKeyboard = append(buttons.InlineKeyboard, []gotgbot.InlineKeyboardButton{
			{Text: "💬 Contact Owner Directly", Url: fmt.Sprintf("tg://user?id=%d", h.cfg.AdminIDs[0])},
		})
	}

	_, err := msg.Reply(b, plansMsg, &gotgbot.SendMessageOpts{
		ParseMode:   "HTML",
		ReplyMarkup: buttons,
	})
	return err
}

// HandleBuyPlanCallback processes user clicking a plan purchase button and alerts owner
func (h *UserHandler) HandleBuyPlanCallback(b *gotgbot.Bot, ctx *ext.Context) error {
	cq := ctx.Update.CallbackQuery
	if cq == nil {
		return nil
	}
	data := cq.Data
	if !strings.HasPrefix(data, "buy_plan:") {
		return nil
	}
	planName := strings.TrimPrefix(data, "buy_plan:")
	user := cq.From

	_, _ = cq.Answer(b, &gotgbot.AnswerCallbackQueryOpts{
		Text:      "✅ Request sent to owner! You will be contacted shortly.",
		ShowAlert: true,
	})

	planCfg, ok := license.Plans[planName]
	displayName := strings.ToUpper(planName)
	durationDays := 30
	if ok {
		displayName = planCfg.Name
		durationDays = planCfg.DurationDays
	}

	// 1. Notify User in chat
	userMsg := fmt.Sprintf(`✅ <b>Purchase Request Submitted!</b>

You selected: <b>%s</b>
Our administrator has been notified with your User ID (<code>%d</code>).
The owner will message you shortly with payment details and your activation key.`, displayName, user.Id)

	_, _ = b.SendMessage(user.Id, userMsg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})

	// 2. Alert Owner / Admins with user ID, name, username, and quick action buttons
	usernameStr := "None"
	if user.Username != "" {
		usernameStr = "@" + user.Username
	}

	adminAlert := fmt.Sprintf(`🔔 <b>NEW VIP PURCHASE REQUEST!</b>

👤 <b>User:</b> <a href="tg://user?id=%d">%s</a>
🏷️ <b>Username:</b> %s
🆔 <b>User ID:</b> <code>%d</code>
💎 <b>Requested Plan:</b> <code>%s</code> (%d days)
📅 <b>Timestamp:</b> %s

⚡ <b>Quick Actions:</b>
• Generate Key: <code>/genkey %s %d 1</code>
• User Profile: <code>/user %d</code>`,
		user.Id, htmlEscape(user.FirstName), usernameStr, user.Id,
		displayName, durationDays, time.Now().UTC().Format("02 Jan 2006, 15:04 UTC"),
		planName, durationDays, user.Id)

	adminButtons := gotgbot.InlineKeyboardMarkup{
		InlineKeyboard: [][]gotgbot.InlineKeyboardButton{
			{
				{Text: "💬 Message User", Url: fmt.Sprintf("tg://user?id=%d", user.Id)},
			},
		},
	}

	for _, adminID := range h.cfg.AdminIDs {
		_, _ = b.SendMessage(adminID, adminAlert, &gotgbot.SendMessageOpts{
			ParseMode:   "HTML",
			ReplyMarkup: adminButtons,
		})
	}

	GlobalLogBuffer.Append(fmt.Sprintf("Purchase request from user %d (%s) for plan '%s'", user.Id, usernameStr, planName))
	return nil
}

// HandleActivateGroup handles /activategroup <KEY> in a group or /activategroup <chat_id> <KEY> in DM
func (h *UserHandler) HandleActivateGroup(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	chat := ctx.EffectiveChat
	args := ctx.Args()

	var targetChatID int64
	var targetTitle string
	var rawKey string

	if chat.Type == "group" || chat.Type == "supergroup" {
		if len(args) < 2 {
			_, err := msg.Reply(b, "ℹ️ <b>Usage in group:</b> <code>/activategroup &lt;LICENSE_KEY&gt;</code>\nExample: <code>/activategroup PREM-XXXX-XXXX-XXXX</code>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return err
		}
		targetChatID = chat.Id
		targetTitle = chat.Title
		rawKey = args[1]
	} else {
		// DM usage: /activategroup <chat_id> <key>
		if len(args) < 3 {
			helpText := `ℹ️ <b>Activating Bot for a Group:</b>

1. Add @` + b.Username + ` to your Telegram group.
2. Inside that group, type:
<code>/activategroup &lt;LICENSE_KEY&gt;</code>

Or activate from here:
<code>/activategroup &lt;group_chat_id&gt; &lt;LICENSE_KEY&gt;</code>`
			_, err := msg.Reply(b, helpText, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return err
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			_, err := msg.Reply(b, "❌ Invalid group chat ID.", nil)
			return err
		}
		targetChatID = id
		targetTitle = fmt.Sprintf("Group %d", id)
		rawKey = args[2]
	}

	res, err := license.RedeemGroupKey(h.database, targetChatID, targetTitle, user.Id, rawKey)
	if err != nil {
		errMsg := ResolveErrorMessage(err.Error())
		_, replyErr := msg.Reply(b, errMsg, nil)
		return replyErr
	}

	expStr := "Permanent (Lifetime VIP)"
	if res.ExpiresAt != nil {
		expStr = res.ExpiresAt.Format("02 Jan 2006, 15:04 UTC")
	}

	successMsg := fmt.Sprintf(`🎉 <b>Group VIP Activated Successfully!</b>

🏷️ <b>Activated Plan:</b> <code>%s</code>
⏱️ <b>Duration:</b> %d days
🔋 <b>Group Daily Quota:</b> %d downloads / day
📅 <b>Valid Until:</b> %s
👥 <b>Activated By:</b> %s

All members in this group can now download videos with VIP speed and limits!`,
		strings.ToUpper(res.Plan), res.DurationDays, res.DailyQuota, expStr, htmlEscape(user.FirstName))

	_, err = msg.Reply(b, successMsg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 6. /stats command
func (h *UserHandler) HandleStats(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser

	st, err := h.database.GetUserStats(user.Id)
	if err != nil {
		_, err := msg.Reply(b, ErrInternalServer, nil)
		return err
	}

	mbDownloaded := float64(st.TotalBytes) / (1024 * 1024)
	gbDownloaded := mbDownloaded / 1024

	var dataStr string
	if gbDownloaded >= 1.0 {
		dataStr = fmt.Sprintf("%.2f GB", gbDownloaded)
	} else {
		dataStr = fmt.Sprintf("%.1f MB", mbDownloaded)
	}

	statsMsg := fmt.Sprintf(`📊 <b>Your Download Statistics</b>

📥 <b>Total Downloads:</b> %d
📅 <b>Downloaded Today:</b> %d
🌐 <b>Favorite Platform:</b> %s
💾 <b>Total Data Transferred:</b> %s`,
		st.TotalDownloads, st.TodayDownloads, st.FavoritePlatform, dataStr)

	_, err = msg.Reply(b, statsMsg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

// 7. /ping command
func (h *UserHandler) HandlePing(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	start := time.Now()

	pingMsg, err := msg.Reply(b, "🏓 <b>Pinging server...</b>", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	if err != nil {
		return err
	}

	latency := time.Since(start).Milliseconds()
	uptime := time.Since(h.startTime).Truncate(time.Second)

	replyText := fmt.Sprintf(`🟢 <b>Pong!</b>

⚡ <b>API Latency:</b> <code>%d ms</code>
⏱️ <b>Server Uptime:</b> <code>%s</code>
⚙️ <b>Engine:</b> Go + yt-dlp + FFmpeg`, latency, uptime.String())

	_, _, err = pingMsg.EditText(b, replyText, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
	return err
}

// 8. /cancel command
func (h *UserHandler) HandleCancel(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser

	if h.dl.CancelUserDownload(user.Id) {
		_, err := msg.Reply(b, ErrCancelled, nil)
		return err
	}

	_, err := msg.Reply(b, "ℹ️ You do not have any active download in progress.", nil)
	return err
}

// HandleRawLink implements the 13-step media download pipeline
func (h *UserHandler) HandleRawLink(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	user := ctx.EffectiveUser
	if msg == nil || user == nil {
		return nil
	}

	// Step 1: Detect link
	extracted := downloader.DetectLink(msg.Text)
	if extracted == nil {
		return nil // Not a media link, ignore
	}

	// Step 2: Middleware checks (Auth, Ban, Group VIP / User Quota)
	var isGroupVIP bool
	if ctx.EffectiveChat.Type != "private" {
		grp, grpErr := h.middleware.CheckGroupQuota(ctx.EffectiveChat.Id)
		if grpErr != nil && grpErr.Error() == "ERR_QUOTA_EXCEEDED" {
			_, replyErr := msg.Reply(b, "⚠️ This group has reached its daily VIP download quota.", nil)
			return replyErr
		}
		if grp != nil && grp.Plan != "free" {
			isGroupVIP = true
		}
	}

	if !isGroupVIP {
		u, err := h.middleware.CheckQuota(user.Id)
		if err != nil {
			if err.Error() == "ERR_QUOTA_EXCEEDED" {
				_, replyErr := msg.Reply(b, ErrQuotaExceeded, nil)
				return replyErr
			}
			_, replyErr := msg.Reply(b, ErrInternalServer, nil)
			return replyErr
		}

		if u.Status == "banned" {
			_, err := msg.Reply(b, ErrBanned, nil)
			return err
		}
	}

	// Step 3: Check if already downloading
	if h.dl.HasActiveDownload(user.Id) {
		_, err := msg.Reply(b, ErrAlreadyDownloading, nil)
		return err
	}

	// Record initial DB entry
	dbRecordID, _ := h.database.InsertDownload(user.Id, extracted.URL, extracted.Platform)

	// Step 4: Send initial status message
	statusMsg, err := msg.Reply(b, fmt.Sprintf("🔍 <b>Analyzing %s link...</b>", extracted.Platform), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	if err != nil {
		return err
	}

	// Progress update callback with throttling
	progressCallback := func(statusText string) {
		_, _, _ = statusMsg.EditText(b, statusText, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
	}

	// Step 5 to 10: Run download pipeline
	ctxTimeout, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	result, err := h.dl.DownloadMedia(ctxTimeout, user.Id, extracted.URL, extracted.Platform, progressCallback)
	if err != nil {
		rawErr := err.Error()
		var errMsg string
		if strings.Contains(rawErr, "Sign in to confirm you’re not a bot") || strings.Contains(rawErr, "Sign in to confirm") {
			errMsg = "❌ <b>YouTube Anti-Bot Check:</b> YouTube blocked this server request asking for sign-in. Try another video or contact admin to provide cookies.txt."
		} else if strings.Contains(rawErr, "Video unavailable") || strings.Contains(rawErr, "Private video") {
			errMsg = "❌ <b>Video Unavailable:</b> This video is private, deleted, or region-restricted."
		} else if strings.Contains(rawErr, "Too Many Requests") || strings.Contains(rawErr, "429") {
			errMsg = "⏳ <b>YouTube Rate Limit:</b> Server IP received 429 Too Many Requests from YouTube. Please wait a few minutes."
		} else if strings.HasPrefix(rawErr, "ERR_DOWNLOAD_FAILED") {
			errMsg = ErrDownloadFailed
		} else {
			errMsg = ResolveErrorMessage(rawErr)
		}
		_, _, _ = statusMsg.EditText(b, errMsg, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
		_ = h.database.UpdateDownloadFailed(dbRecordID, rawErr)
		return nil
	}

	// Cleanup downloaded file on exit
	defer func() {
		if result.FilePath != "" {
			_ = os.Remove(result.FilePath)
		}
		if result.ThumbnailPath != "" {
			_ = os.Remove(result.ThumbnailPath)
		}
	}()

	// Step 11: File size validation
	maxBytes := h.cfg.TelegramUploadLimitMB * 1024 * 1024
	if result.FileSize > maxBytes {
		_, _, _ = statusMsg.EditText(b, ErrFileTooLarge, nil)
		_ = h.database.UpdateDownloadFailed(dbRecordID, "file exceeds upload limit")
		return nil
	}

	// Step 12: Send video to Telegram
	_, _, _ = statusMsg.EditText(b, "📤 <b>Uploading video to Telegram...</b>", &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})

	videoFile, err := os.Open(result.FilePath)
	if err != nil {
		_, _, _ = statusMsg.EditText(b, ErrDownloadFailed, nil)
		_ = h.database.UpdateDownloadFailed(dbRecordID, "failed to open downloaded file")
		return nil
	}
	defer videoFile.Close()

	caption := fmt.Sprintf("🎬 <b>%s</b>\n\n🌐 <b>Platform:</b> %s\n⏱️ <b>Duration:</b> %s\n⚡ <i>Downloaded via @%s</i>",
		htmlEscape(result.Title), result.Platform, formatDuration(result.Duration), b.Username)

	sendVideoOpts := &gotgbot.SendVideoOpts{
		Caption:           caption,
		ParseMode:         "HTML",
		Duration:          int64(result.Duration),
		SupportsStreaming: true,
	}

	// Attach thumbnail if present
	if result.ThumbnailPath != "" {
		if thumbFile, err := os.Open(result.ThumbnailPath); err == nil {
			defer thumbFile.Close()
			sendVideoOpts.Thumbnail = gotgbot.InputFileByReader("thumb.jpg", thumbFile)
		}
	}

	_, err = b.SendVideo(ctx.EffectiveChat.Id, gotgbot.InputFileByReader("video.mp4", videoFile), sendVideoOpts)
	if err != nil {
		_, _, _ = statusMsg.EditText(b, ErrDownloadFailed, nil)
		_ = h.database.UpdateDownloadFailed(dbRecordID, err.Error())
		return nil
	}

	// Step 13: Finalization & Cleanup
	_, _ = statusMsg.Delete(b, nil)
	_ = h.database.UpdateDownloadSuccess(dbRecordID, result.Title, result.FileSize, result.Duration)
	if isGroupVIP {
		_ = h.database.IncrementGroupDownload(ctx.EffectiveChat.Id)
	} else {
		_ = h.database.IncrementUserDownload(user.Id)
	}

	return nil
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

func formatDuration(seconds int) string {
	if seconds <= 0 {
		return "--:--"
	}
	m := seconds / 60
	s := seconds % 60
	if m >= 60 {
		h := m / 60
		m = m % 60
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}
