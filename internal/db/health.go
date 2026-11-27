package db

import (
	"database/sql"
	"time"
)

// PlatformHealthStatus represents health information for a platform
type PlatformHealthStatus struct {
	Platform      string
	Fails         int
	LastFail      *time.Time
	DisabledUntil *time.Time
	LastError     string
}

// RecordPlatformFail records a failure for a platform and disables it for 15 minutes after 3 failures
func (db *DB) RecordPlatformFail(platform string) error {
	return db.RecordPlatformFailWithErr(platform, nil)
}

// RecordPlatformFailWithErr records a failure with error message for a platform
func (db *DB) RecordPlatformFailWithErr(platform string, lastErr error) error {
	var errStr string
	if lastErr != nil {
		errStr = lastErr.Error()
	}

	query := `
	INSERT INTO platform_health (platform, fails, last_fail, disabled_until, last_error)
	VALUES (?, 1, CURRENT_TIMESTAMP, NULL, ?)
	ON CONFLICT(platform) DO UPDATE SET
		fails = platform_health.fails + 1,
		last_fail = CURRENT_TIMESTAMP,
		last_error = CASE WHEN ? != '' THEN ? ELSE platform_health.last_error END,
		disabled_until = CASE 
			WHEN platform_health.fails + 1 >= 3 THEN datetime('now', '+15 minutes')
			ELSE platform_health.disabled_until 
		END;
	`
	_, err := db.Exec(query, platform, errStr, errStr, errStr)
	return err
}

// ClearPlatformFails resets failure count and disabled status for a platform
func (db *DB) ClearPlatformFails(platform string) error {
	query := `
	INSERT INTO platform_health (platform, fails, last_fail, disabled_until, last_error)
	VALUES (?, 0, NULL, NULL, '')
	ON CONFLICT(platform) DO UPDATE SET
		fails = 0,
		disabled_until = NULL,
		last_error = '';
	`
	_, err := db.Exec(query, platform)
	return err
}

// DisabledPlatforms returns a map of platforms where disabled_until is in the future
func (db *DB) DisabledPlatforms() (map[string]bool, error) {
	query := `
	SELECT platform, disabled_until 
	FROM platform_health 
	WHERE disabled_until IS NOT NULL AND disabled_until > datetime('now');
	`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]bool)
	for rows.Next() {
		var platform string
		var disabledUntil sql.NullString
		if err := rows.Scan(&platform, &disabledUntil); err == nil {
			res[platform] = true
		}
	}
	return res, rows.Err()
}

// GetAllPlatformHealth returns all rows from platform_health
func (db *DB) GetAllPlatformHealth() ([]PlatformHealthStatus, error) {
	query := `SELECT platform, fails, last_fail, disabled_until, COALESCE(last_error, '') FROM platform_health;`
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PlatformHealthStatus
	for rows.Next() {
		var p PlatformHealthStatus
		var lastFailStr, disabledUntilStr sql.NullString
		if err := rows.Scan(&p.Platform, &p.Fails, &lastFailStr, &disabledUntilStr, &p.LastError); err != nil {
			return nil, err
		}
		if lastFailStr.Valid && lastFailStr.String != "" {
			if t, err := parseSQLiteTime(lastFailStr.String); err == nil {
				p.LastFail = &t
			}
		}
		if disabledUntilStr.Valid && disabledUntilStr.String != "" {
			if t, err := parseSQLiteTime(disabledUntilStr.String); err == nil {
				p.DisabledUntil = &t
			}
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// GetPlatformHealth returns health status for a specific platform
func (db *DB) GetPlatformHealth(platform string) (*PlatformHealthStatus, error) {
	query := `SELECT platform, fails, last_fail, disabled_until, COALESCE(last_error, '') FROM platform_health WHERE platform = ?;`
	row := db.QueryRow(query, platform)
	var p PlatformHealthStatus
	var lastFailStr, disabledUntilStr sql.NullString
	if err := row.Scan(&p.Platform, &p.Fails, &lastFailStr, &disabledUntilStr, &p.LastError); err != nil {
		return nil, err
	}
	if lastFailStr.Valid && lastFailStr.String != "" {
		if t, err := parseSQLiteTime(lastFailStr.String); err == nil {
			p.LastFail = &t
		}
	}
	if disabledUntilStr.Valid && disabledUntilStr.String != "" {
		if t, err := parseSQLiteTime(disabledUntilStr.String); err == nil {
			p.DisabledUntil = &t
		}
	}
	return &p, nil
}

func parseSQLiteTime(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, sql.ErrNoRows
}
