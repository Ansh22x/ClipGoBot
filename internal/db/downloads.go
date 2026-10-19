package db

import (
	"database/sql"
	"time"
)

// DownloadRecord represents a row in the downloads table
type DownloadRecord struct {
	ID           int64
	TelegramID   int64
	URL          string
	Platform     string
	Title        sql.NullString
	FileSize     sql.NullInt64
	Duration     sql.NullInt64
	Status       string
	ErrorMessage sql.NullString
	CreatedAt    time.Time
}

// UserStats contains aggregated download statistics for a user
type UserStats struct {
	TotalDownloads   int
	TodayDownloads   int
	FavoritePlatform string
	TotalBytes       int64
}

// GlobalStats contains system-wide bot statistics
type GlobalStats struct {
	TotalUsers            int
	ActivePremiumUsers    int
	TotalDownloadsAllTime int
	TotalDownloadsToday   int
}

// InsertDownload creates an initial record for a download attempt
func (db *DB) InsertDownload(telegramID int64, url, platform string) (int64, error) {
	query := `
	INSERT INTO downloads (telegram_id, url, platform, status)
	VALUES (?, ?, ?, 'downloading')
	`
	res, err := db.Exec(query, telegramID, url, platform)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDownloadSuccess marks a download record as completed
func (db *DB) UpdateDownloadSuccess(id int64, title string, fileSize int64, duration int) error {
	query := `
	UPDATE downloads
	SET status = 'completed', title = ?, file_size = ?, duration = ?
	WHERE id = ?
	`
	_, err := db.Exec(query, title, fileSize, duration, id)
	return err
}

// UpdateDownloadFailed marks a download record as failed
func (db *DB) UpdateDownloadFailed(id int64, errMsg string) error {
	query := `
	UPDATE downloads
	SET status = 'failed', error_message = ?
	WHERE id = ?
	`
	_, err := db.Exec(query, errMsg, id)
	return err
}

// GetUserStats returns summary statistics for a given user
func (db *DB) GetUserStats(telegramID int64) (*UserStats, error) {
	stats := &UserStats{
		FavoritePlatform: "None",
	}

	// Total downloads and total bytes
	queryTotals := `
	SELECT COUNT(*), COALESCE(SUM(file_size), 0)
	FROM downloads
	WHERE telegram_id = ? AND status = 'completed'
	`
	row := db.QueryRow(queryTotals, telegramID)
	if err := row.Scan(&stats.TotalDownloads, &stats.TotalBytes); err != nil {
		return nil, err
	}

	// Today's downloads
	todayStart := time.Now().UTC().Format("2006-01-02 00:00:00")
	queryToday := `
	SELECT COUNT(*)
	FROM downloads
	WHERE telegram_id = ? AND status = 'completed' AND created_at >= ?
	`
	_ = db.QueryRow(queryToday, telegramID, todayStart).Scan(&stats.TodayDownloads)

	// Favorite platform
	queryFav := `
	SELECT platform
	FROM downloads
	WHERE telegram_id = ? AND status = 'completed'
	GROUP BY platform
	ORDER BY COUNT(*) DESC
	LIMIT 1
	`
	var fav string
	if err := db.QueryRow(queryFav, telegramID).Scan(&fav); err == nil && fav != "" {
		stats.FavoritePlatform = fav
	}

	return stats, nil
}

// GetGlobalStats calculates global platform statistics
func (db *DB) GetGlobalStats() (*GlobalStats, error) {
	stats := &GlobalStats{}

	// Total registered users
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&stats.TotalUsers)

	// Active premium users
	now := time.Now().UTC().Format(time.RFC3339)
	_ = db.QueryRow(`
		SELECT COUNT(*) FROM users 
		WHERE plan != 'free' AND status != 'banned' 
		  AND (expires_at IS NULL OR expires_at > ?)
	`, now).Scan(&stats.ActivePremiumUsers)

	// Total downloads all time
	_ = db.QueryRow(`SELECT COUNT(*) FROM downloads WHERE status = 'completed'`).Scan(&stats.TotalDownloadsAllTime)

	// Total downloads today
	todayStart := time.Now().UTC().Format("2006-01-02 00:00:00")
	_ = db.QueryRow(`SELECT COUNT(*) FROM downloads WHERE status = 'completed' AND created_at >= ?`, todayStart).Scan(&stats.TotalDownloadsToday)

	return stats, nil
}
