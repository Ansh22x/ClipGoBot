package bot

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/message"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
	"tg-media-bot/internal/downloader"
)

// BotEngine orchestrates gotgbot, handlers, middleware, and background workers
type BotEngine struct {
	cfg        *config.Config
	database   *db.DB
	dl         *downloader.Downloader
	bot        *gotgbot.Bot
	updater    *ext.Updater
	dispatcher *ext.Dispatcher
}

// NewBotEngine initializes the bot instance, dispatcher, and dependencies
func NewBotEngine(cfg *config.Config, database *db.DB) (*BotEngine, error) {
	if cfg.BotToken == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required but was not provided")
	}

	b, err := gotgbot.NewBot(cfg.BotToken, &gotgbot.BotOpts{
		BotClient: &gotgbot.BaseBotClient{
			Client: http.Client{
				Timeout: 120 * time.Second, // Long timeout for large file uploads
			},
			DefaultRequestOpts: &gotgbot.RequestOpts{
				Timeout: 120 * time.Second,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			log.Printf("[Bot Error] Handling update failed: %v", err)
			GlobalLogBuffer.Append(fmt.Sprintf("Update error: %v", err))
			return ext.DispatcherActionNoop
		},
		MaxRoutines: ext.DefaultMaxRoutines,
	})

	updater := ext.NewUpdater(dispatcher, nil)
	dl := downloader.NewDownloader(cfg)

	engine := &BotEngine{
		cfg:        cfg,
		database:   database,
		dl:         dl,
		bot:        b,
		updater:    updater,
		dispatcher: dispatcher,
	}

	engine.registerHandlers()
	return engine, nil
}

func (e *BotEngine) registerHandlers() {
	mw := NewMiddlewareManager(e.cfg, e.database)
	userH := NewUserHandler(e.cfg, e.database, e.dl, mw)
	adminH := NewAdminHandler(e.cfg, e.database)

	d := e.dispatcher

	// User Commands (wrapped in Auth & Rate Limit middleware)
	d.AddHandler(handlers.NewCommand("start", mw.AuthAndRateLimit(userH.HandleStart)))
	d.AddHandler(handlers.NewCommand("help", mw.AuthAndRateLimit(userH.HandleHelp)))
	d.AddHandler(handlers.NewCommand("status", mw.AuthAndRateLimit(userH.HandleStatus)))
	d.AddHandler(handlers.NewCommand("redeem", mw.AuthAndRateLimit(userH.HandleRedeem)))
	d.AddHandler(handlers.NewCommand("activategroup", mw.AuthAndRateLimit(userH.HandleActivateGroup)))
	d.AddHandler(handlers.NewCommand("plans", mw.AuthAndRateLimit(userH.HandlePlans)))
	d.AddHandler(handlers.NewCommand("stats", mw.AuthAndRateLimit(userH.HandleStats)))
	d.AddHandler(handlers.NewCommand("ping", mw.AuthAndRateLimit(userH.HandlePing)))
	d.AddHandler(handlers.NewCommand("cancel", mw.AuthAndRateLimit(userH.HandleCancel)))

	// Callback query handler for VIP purchase requests
	d.AddHandler(handlers.NewCallback(callbackquery.Prefix("buy_plan:"), userH.HandleBuyPlanCallback))

	// 10 Admin Commands (wrapped in Admin authorization check)
	d.AddHandler(handlers.NewCommand("genkey", mw.RequireAdmin(adminH.HandleGenKey)))
	d.AddHandler(handlers.NewCommand("revokekey", mw.RequireAdmin(adminH.HandleRevokeKey)))
	d.AddHandler(handlers.NewCommand("listkeys", mw.RequireAdmin(adminH.HandleListKeys)))
	d.AddHandler(handlers.NewCommand("ban", mw.RequireAdmin(adminH.HandleBan)))
	d.AddHandler(handlers.NewCommand("unban", mw.RequireAdmin(adminH.HandleUnban)))
	d.AddHandler(handlers.NewCommand("user", mw.RequireAdmin(adminH.HandleUser)))
	d.AddHandler(handlers.NewCommand("setquota", mw.RequireAdmin(adminH.HandleSetQuota)))
	d.AddHandler(handlers.NewCommand("broadcast", mw.RequireAdmin(adminH.HandleBroadcast)))
	d.AddHandler(handlers.NewCommand("stats_admin", mw.RequireAdmin(adminH.HandleStatsAdmin)))
	d.AddHandler(handlers.NewCommand("system", mw.RequireAdmin(adminH.HandleStatsAdmin)))
	d.AddHandler(handlers.NewCommand("logs", mw.RequireAdmin(adminH.HandleLogs)))
	d.AddHandler(handlers.NewCommand("backup", mw.RequireAdmin(adminH.HandleBackup)))
	d.AddHandler(handlers.NewCommand("restore", mw.RequireAdmin(adminH.HandleRestore)))
	d.AddHandler(handlers.NewCommand("setcookies", mw.RequireAdmin(adminH.HandleSetCookies)))
	d.AddHandler(handlers.NewCommand("checkcookies", mw.RequireAdmin(adminH.HandleCheckCookies)))

	// Raw link detection handler (matches text messages that are not commands)
	d.AddHandler(handlers.NewMessage(message.Text, mw.AuthAndRateLimit(func(b *gotgbot.Bot, ctx *ext.Context) error {
		if strings.HasPrefix(ctx.EffectiveMessage.Text, "/") {
			return nil // Let command handlers process commands
		}
		return userH.HandleRawLink(b, ctx)
	})))
}

func (e *BotEngine) registerTelegramCommands() {
	// Register user commands visible to all users
	userCmds := []gotgbot.BotCommand{
		{Command: "start", Description: "Start the bot & main menu"},
		{Command: "help", Description: "How to download & command guide"},
		{Command: "status", Description: "Check your subscription & quota"},
		{Command: "plans", Description: "View VIP plans & purchase access"},
		{Command: "redeem", Description: "Redeem a license key"},
		{Command: "activategroup", Description: "Activate VIP features for this group"},
		{Command: "stats", Description: "View your personal download stats"},
		{Command: "ping", Description: "Check server status & latency"},
		{Command: "cancel", Description: "Abort your active download"},
	}
	_, _ = e.bot.SetMyCommands(userCmds, nil)

	// Register admin commands for chat administrators scope
	adminCmds := append(userCmds, []gotgbot.BotCommand{
		{Command: "genkey", Description: "[Admin] Generate VIP license keys"},
		{Command: "revokekey", Description: "[Admin] Revoke a license key"},
		{Command: "listkeys", Description: "[Admin] List recent license keys"},
		{Command: "ban", Description: "[Admin] Ban user by ID/username/reply"},
		{Command: "unban", Description: "[Admin] Unban user by ID or username"},
		{Command: "user", Description: "[Admin] Inspect user profile"},
		{Command: "setquota", Description: "[Admin] Update user daily quota"},
		{Command: "broadcast", Description: "[Admin] Broadcast message to all users"},
		{Command: "stats_admin", Description: "[Admin] View system analytics & memory"},
		{Command: "logs", Description: "[Admin] View recent server logs"},
		{Command: "backup", Description: "[Admin] Download full database backup"},
		{Command: "restore", Description: "[Admin] Restore database from backup file"},
		{Command: "setcookies", Description: "[Admin] Install cookies.txt for YouTube"},
		{Command: "checkcookies", Description: "[Admin] Check active cookies.txt status"},
	}...)
	_, _ = e.bot.SetMyCommands(adminCmds, &gotgbot.SetMyCommandsOpts{
		Scope: gotgbot.BotCommandScopeAllChatAdministrators{},
	})
}

// Start begins long-polling updates and initiates background jobs
func (e *BotEngine) Start(ctx context.Context) error {
	log.Printf("Starting bot @%s (ID: %d)...", e.bot.Username, e.bot.Id)
	GlobalLogBuffer.Append(fmt.Sprintf("Bot started as @%s", e.bot.Username))

	// Register bot command list in Telegram UI
	e.registerTelegramCommands()

	// Launch background jobs
	jobs := NewBackgroundJobs(e.cfg, e.database, e.bot)
	jobs.Start(ctx)

	// Start long polling
	err := e.updater.StartPolling(e.bot, &ext.PollingOpts{
		DropPendingUpdates: true,
		GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
			Timeout:        30,
			AllowedUpdates: []string{"message", "callback_query"},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to start polling: %w", err)
	}

	log.Printf("Bot @%s is active and polling for updates.", e.bot.Username)

	// Block until context cancellation
	<-ctx.Done()

	log.Println("Stopping bot polling...")
	return e.updater.Stop()
}
