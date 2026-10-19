package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// User represents a record in the users table
type User struct {
	TelegramID     int64
	Username       string
	LicenseKey     sql.NullString
	Plan           string
	Status         string
	DailyQuota     int
	DailyDownloads int
	LastDownloadAt *time.Time
	QuotaResetAt   *time.Time
	RegisteredAt   time.Time
	ExpiresAt      *time.Time
}

// GetOrCreateUser fetches an existing user or creates a new one with free plan
func (db *DB) GetOrCreateUser(telegramID int64, username string, defaultQuota int) (*User, error) {
	u, err := db.GetUser(telegramID)
	if err != nil {
		return nil, err
	}
	if u != nil {
		// Update username if it changed
		if u.Username != username && username != "" {
			_, _ = db.Exec(`UPDATE users SET username = ? WHERE telegram_id = ?`, username, telegramID)
			u.Username = username
		}
		return u, nil
	}

	// Insert new user
	now := time.Now().UTC()
	tomorrowMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)

	insertQuery := `
	INSERT INTO users (telegram_id, username, plan, status, daily_quota, daily_downloads, quota_reset_at)
	VALUES (?, ?, 'free', 'active', ?, 0, ?)
	`
	_, err = db.Exec(insertQuery, telegramID, username, defaultQuota, tomorrowMidnight.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to register user: %w", err)
	}

	return db.GetUser(telegramID)
}

// GetUser retrieves a user by Telegram ID
func (db *DB) GetUser(telegramID int64) (*User, error) {
	query := `
	SELECT telegram_id, username, license_key, plan, status, daily_quota, daily_downloads, 
	       last_download_at, quota_reset_at, registered_at, expires_at
	FROM users WHERE telegram_id = ?
	`
	row := db.QueryRow(query, telegramID)

	var u User
	var lastDownloadStr, quotaResetStr, registeredStr, expiresStr sql.NullString

	err := row.Scan(
		&u.TelegramID,
		&u.Username,
		&u.LicenseKey,
		&u.Plan,
		&u.Status,
		&u.DailyQuota,
		&u.DailyDownloads,
		&lastDownloadStr,
		&quotaResetStr,
		&registeredStr,
		&expiresStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if lastDownloadStr.Valid && lastDownloadStr.String != "" {
		t, _ := time.Parse(time.RFC3339, lastDownloadStr.String)
		u.LastDownloadAt = &t
	}
	if quotaResetStr.Valid && quotaResetStr.String != "" {
		t, _ := time.Parse(time.RFC3339, quotaResetStr.String)
		u.QuotaResetAt = &t
	}
	if registeredStr.Valid && registeredStr.String != "" {
		t, _ := time.Parse(time.RFC3339, registeredStr.String)
		u.RegisteredAt = t
	}
	if expiresStr.Valid && expiresStr.String != "" {
		t, _ := time.Parse(time.RFC3339, expiresStr.String)
		u.ExpiresAt = &t
	}

	return &u, nil
}

// UpdateUserPlan updates the user's plan, license key, expiration time, and quota
func (db *DB) UpdateUserPlan(telegramID int64, plan string, licenseKey string, expiresAt *time.Time, quota int) error {
	var expiresStr *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339)
		expiresStr = &s
	}

	query := `
	UPDATE users 
	SET plan = ?, license_key = ?, expires_at = ?, daily_quota = ?
	WHERE telegram_id = ?
	`
	_, err := db.Exec(query, plan, licenseKey, expiresStr, quota, telegramID)
	return err
}

// IncrementUserDownload increments daily download count and updates timestamp
func (db *DB) IncrementUserDownload(telegramID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	query := `
	UPDATE users 
	SET daily_downloads = daily_downloads + 1, last_download_at = ?
	WHERE telegram_id = ?
	`
	_, err := db.Exec(query, now, telegramID)
	return err
}

// SetUserStatus updates user status (e.g., 'active' or 'banned')
func (db *DB) SetUserStatus(telegramID int64, status string) error {
	query := `UPDATE users SET status = ? WHERE telegram_id = ?`
	res, err := db.Exec(query, status, telegramID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("user not found")
	}
	return nil
}

// SetUserQuota updates a user's daily quota
func (db *DB) SetUserQuota(telegramID int64, quota int) error {
	query := `UPDATE users SET daily_quota = ? WHERE telegram_id = ?`
	res, err := db.Exec(query, quota, telegramID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("user not found")
	}
	return nil
}

// ResetExpiredUsers finds users whose paid plan has passed expires_at and resets them to free
func (db *DB) ResetExpiredUsers() ([]int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	querySelect := `
	SELECT telegram_id FROM users 
	WHERE plan != 'free' AND plan != 'lifetime' AND expires_at IS NOT NULL AND expires_at <= ?
	`
	rows, err := db.Query(querySelect, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expiredIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			expiredIDs = append(expiredIDs, id)
		}
	}

	if len(expiredIDs) > 0 {
		queryUpdate := `
		UPDATE users 
		SET plan = 'free', license_key = NULL, expires_at = NULL, daily_quota = 5 
		WHERE plan != 'free' AND plan != 'lifetime' AND expires_at IS NOT NULL AND expires_at <= ?
		`
		_, err = db.Exec(queryUpdate, now)
		if err != nil {
			return nil, err
		}
	}

	return expiredIDs, nil
}

// ResetDailyQuotas resets daily download counters for users whose quota_reset_at has passed
func (db *DB) ResetDailyQuotas() (int64, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	tomorrowMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	query := `
	UPDATE users 
	SET daily_downloads = 0, quota_reset_at = ?
	WHERE quota_reset_at IS NULL OR quota_reset_at <= ?
	`
	res, err := db.Exec(query, tomorrowMidnight, nowStr)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetAllActiveUserIDs returns list of all non-banned user IDs for broadcasting
func (db *DB) GetAllActiveUserIDs() ([]int64, error) {
	query := `SELECT telegram_id FROM users WHERE status != 'banned'`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// GetUserByUsername finds a user record by username (case-insensitive)
func (db *DB) GetUserByUsername(username string) (*User, error) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return nil, errors.New("empty username")
	}

	query := `
	SELECT telegram_id, username, license_key, plan, status, daily_quota, daily_downloads, 
	       last_download_at, quota_reset_at, registered_at, expires_at
	FROM users WHERE LOWER(username) = LOWER(?) LIMIT 1
	`
	row := db.QueryRow(query, username)

	var u User
	var lastDownloadStr, quotaResetStr, registeredStr, expiresStr sql.NullString

	err := row.Scan(
		&u.TelegramID,
		&u.Username,
		&u.LicenseKey,
		&u.Plan,
		&u.Status,
		&u.DailyQuota,
		&u.DailyDownloads,
		&lastDownloadStr,
		&quotaResetStr,
		&registeredStr,
		&expiresStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query user by username: %w", err)
	}

	if lastDownloadStr.Valid && lastDownloadStr.String != "" {
		t, _ := time.Parse(time.RFC3339, lastDownloadStr.String)
		u.LastDownloadAt = &t
	}
	if quotaResetStr.Valid && quotaResetStr.String != "" {
		t, _ := time.Parse(time.RFC3339, quotaResetStr.String)
		u.QuotaResetAt = &t
	}
	if registeredStr.Valid && registeredStr.String != "" {
		t, _ := time.Parse(time.RFC3339, registeredStr.String)
		u.RegisteredAt = t
	}
	if expiresStr.Valid && expiresStr.String != "" {
		t, _ := time.Parse(time.RFC3339, expiresStr.String)
		u.ExpiresAt = &t
	}

	return &u, nil
}

// BanUserFlexible bans a user by ID or by username
func (db *DB) BanUserFlexible(targetID int64, username string) (*User, error) {
	var u *User
	var err error

	if targetID != 0 {
		u, err = db.GetUser(targetID)
		if err != nil {
			return nil, err
		}
		if u == nil {
			// Record user in DB as banned
			_, _ = db.Exec(`INSERT INTO users (telegram_id, username, status) VALUES (?, ?, 'banned')`, targetID, username)
			return db.GetUser(targetID)
		}
		if err := db.SetUserStatus(targetID, "banned"); err != nil {
			return nil, err
		}
		u.Status = "banned"
		return u, nil
	}

	if username != "" {
		u, err = db.GetUserByUsername(username)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("user with username @%s not found in bot database", username)
		}
		if err := db.SetUserStatus(u.TelegramID, "banned"); err != nil {
			return nil, err
		}
		u.Status = "banned"
		return u, nil
	}

	return nil, errors.New("no user ID or username specified")
}

// UnbanUserFlexible unbans a user by ID or by username
func (db *DB) UnbanUserFlexible(targetID int64, username string) (*User, error) {
	var u *User
	var err error

	if targetID != 0 {
		u, err = db.GetUser(targetID)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, errors.New("user not found")
		}
		if err := db.SetUserStatus(targetID, "active"); err != nil {
			return nil, err
		}
		u.Status = "active"
		return u, nil
	}

	if username != "" {
		u, err = db.GetUserByUsername(username)
		if err != nil {
			return nil, err
		}
		if u == nil {
			return nil, fmt.Errorf("user with username @%s not found in bot database", username)
		}
		if err := db.SetUserStatus(u.TelegramID, "active"); err != nil {
			return nil, err
		}
		u.Status = "active"
		return u, nil
	}

	return nil, errors.New("no user ID or username specified")
}
