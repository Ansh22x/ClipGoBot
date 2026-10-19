package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"tg-media-bot/internal/bot"
	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
)

func main() {
	log.Println("Initializing Telegram Media Downloader Bot...")

	// 1. Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	if cfg.BotToken == "" {
		log.Fatal("ERROR: BOT_TOKEN environment variable is required. Please set it in .env or environment.")
	}

	// 2. Initialize SQLite Database
	database, err := db.InitDB(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()
	log.Printf("SQLite database connected at %s", cfg.DBPath)

	// 3. Initialize Bot Engine
	engine, err := bot.NewBotEngine(cfg, database)
	if err != nil {
		log.Fatalf("Failed to initialize bot engine: %v", err)
	}

	// 4. Setup graceful termination context
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 5. Run bot
	if err := engine.Start(ctx); err != nil {
		log.Fatalf("Bot encountered fatal error: %v", err)
	}

	log.Println("Bot stopped gracefully.")
}
