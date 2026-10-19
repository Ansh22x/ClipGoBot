package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps standard database/sql DB instance
type DB struct {
	*sql.DB
}

// InitDB initializes SQLite database and creates the 3 specified tables
func InitDB(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	database, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	database.SetMaxOpenConns(1) // SQLite single-writer pattern

	if err := database.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping sqlite database: %w", err)
	}

	db := &DB{DB: database}
	if err := db.createSchema(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return db, nil
}

func (db *DB) createSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS licenses (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key TEXT UNIQUE NOT NULL,
		plan TEXT NOT NULL,
		duration_days INTEGER NOT NULL,
		max_activations INTEGER DEFAULT 1,
		activation_count INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		status TEXT DEFAULT 'active'
	);

	CREATE TABLE IF NOT EXISTS users (
		telegram_id INTEGER PRIMARY KEY,
		username TEXT,
		license_key TEXT,
		plan TEXT DEFAULT 'free',
		status TEXT DEFAULT 'active',
		daily_quota INTEGER DEFAULT 5,
		daily_downloads INTEGER DEFAULT 0,
		last_download_at DATETIME,
		quota_reset_at DATETIME,
		registered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME,
		FOREIGN KEY(license_key) REFERENCES licenses(key)
	);

	CREATE TABLE IF NOT EXISTS downloads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		telegram_id INTEGER NOT NULL,
		url TEXT NOT NULL,
		platform TEXT NOT NULL,
		title TEXT,
		file_size INTEGER,
		duration INTEGER,
		status TEXT NOT NULL,
		error_message TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(telegram_id) REFERENCES users(telegram_id)
	);

	CREATE TABLE IF NOT EXISTS groups (
		chat_id INTEGER PRIMARY KEY,
		title TEXT,
		license_key TEXT,
		plan TEXT DEFAULT 'free',
		status TEXT DEFAULT 'active',
		daily_quota INTEGER DEFAULT 50,
		daily_downloads INTEGER DEFAULT 0,
		last_download_at DATETIME,
		quota_reset_at DATETIME,
		expires_at DATETIME,
		activated_by INTEGER,
		registered_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY(license_key) REFERENCES licenses(key)
	);

	CREATE INDEX IF NOT EXISTS idx_licenses_key ON licenses(key);
	CREATE INDEX IF NOT EXISTS idx_users_telegram_id ON users(telegram_id);
	CREATE INDEX IF NOT EXISTS idx_downloads_telegram_id ON downloads(telegram_id);
	CREATE INDEX IF NOT EXISTS idx_users_expires_at ON users(expires_at);
	CREATE INDEX IF NOT EXISTS idx_groups_chat_id ON groups(chat_id);
	`
	_, err := db.Exec(schema)
	return err
}
