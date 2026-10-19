package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// 5. Start lightweight HTTP health server for Render Web Service ($PORT)
	port := os.Getenv("PORT")
	if port == "" {
		port = "10000" // default Render web service port
	}

	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","bot":"@ClipGo1Bot","service":"ClipGo Downloader"}`))
	})
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: httpMux,
	}

	go func() {
		log.Printf("HTTP health check server listening on :%s for Render...", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server warning: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer sCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	// 6. Run bot polling
	if err := engine.Start(ctx); err != nil {
		log.Fatalf("Bot encountered fatal error: %v", err)
	}

	log.Println("Bot stopped gracefully.")
}
