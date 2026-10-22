package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Group represents a record in the groups table
type Group struct {
	ChatID         int64
	Title          string
	LicenseKey     sql.NullString
	Plan           string
	Status         string
	DailyQuota     int
	DailyDownloads int
	LastDownloadAt *time.Time
	QuotaResetAt   *time.Time
	ExpiresAt      *time.Time
	ActivatedBy    sql.NullInt64
	RegisteredAt   time.Time
}

// GetOrCreateGroup fetches an existing group or registers it with default free quota
func (db *DB) GetOrCreateGroup(chatID int64, title string) (*Group, error) {
	g, err := db.GetGroup(chatID)
	if err != nil {
		return nil, err
	}
	if g != nil {
		if g.Title != title && title != "" {
			_, _ = db.Exec(`UPDATE groups SET title = ? WHERE chat_id = ?`, title, chatID)
			g.Title = title
		}
		return g, nil
	}

	now := time.Now().UTC()
	tomorrowMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)

	insertQuery := `
	INSERT INTO groups (chat_id, title, plan, status, daily_quota, daily_downloads, quota_reset_at)
	VALUES (?, ?, 'free', 'active', 20, 0, ?)
	`
	_, err = db.Exec(insertQuery, chatID, title, tomorrowMidnight.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("failed to register group: %w", err)
	}

	return db.GetGroup(chatID)
}

// GetGroup retrieves a group by Chat ID
func (db *DB) GetGroup(chatID int64) (*Group, error) {
	query := `
	SELECT chat_id, title, license_key, plan, status, daily_quota, daily_downloads,
	       last_download_at, quota_reset_at, expires_at, activated_by, registered_at
	FROM groups WHERE chat_id = ?
	`
	row := db.QueryRow(query, chatID)

	var g Group
	var lastDownloadStr, quotaResetStr, expiresStr, registeredStr sql.NullString

	err := row.Scan(
		&g.ChatID,
		&g.Title,
		&g.LicenseKey,
		&g.Plan,
		&g.Status,
		&g.DailyQuota,
		&g.DailyDownloads,
		&lastDownloadStr,
		&quotaResetStr,
		&expiresStr,
		&g.ActivatedBy,
		&registeredStr,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query group: %w", err)
	}

	if lastDownloadStr.Valid {
		g.LastDownloadAt = ParseFlexibleTime(lastDownloadStr.String)
	}
	if quotaResetStr.Valid {
		g.QuotaResetAt = ParseFlexibleTime(quotaResetStr.String)
	}
	if expiresStr.Valid {
		g.ExpiresAt = ParseFlexibleTime(expiresStr.String)
	}
	if registeredStr.Valid {
		if t := ParseFlexibleTime(registeredStr.String); t != nil {
			g.RegisteredAt = *t
		}
	}

	return &g, nil
}

// UpdateGroupPlan updates plan, license key, expiration time, and quota for a group
func (db *DB) UpdateGroupPlan(chatID int64, plan string, licenseKey string, expiresAt *time.Time, quota int, activatedBy int64) error {
	var expiresStr *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339)
		expiresStr = &s
	}

	query := `
	UPDATE groups 
	SET plan = ?, license_key = ?, expires_at = ?, daily_quota = ?, activated_by = ?, status = 'active'
	WHERE chat_id = ?
	`
	_, err := db.Exec(query, plan, licenseKey, expiresStr, quota, activatedBy, chatID)
	return err
}

// IncrementGroupDownload increments daily downloads counter for a group
func (db *DB) IncrementGroupDownload(chatID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	query := `
	UPDATE groups 
	SET daily_downloads = daily_downloads + 1, last_download_at = ?
	WHERE chat_id = ?
	`
	_, err := db.Exec(query, now, chatID)
	return err
}

// ResetExpiredGroups resets groups whose plan has expired to free
func (db *DB) ResetExpiredGroups() ([]int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	querySelect := `
	SELECT chat_id FROM groups 
	WHERE plan != 'free' AND plan != 'lifetime' AND expires_at IS NOT NULL AND datetime(expires_at) <= datetime(?)
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
		UPDATE groups 
		SET plan = 'free', license_key = NULL, expires_at = NULL, daily_quota = 20 
		WHERE plan != 'free' AND plan != 'lifetime' AND expires_at IS NOT NULL AND datetime(expires_at) <= datetime(?)
		`
		_, err = db.Exec(queryUpdate, now)
		if err != nil {
			return nil, err
		}
	}

	return expiredIDs, nil
}

// ResetGroupDailyQuotas resets daily download counters for groups
func (db *DB) ResetGroupDailyQuotas() (int64, error) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	tomorrowMidnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	query := `
	UPDATE groups 
	SET daily_downloads = 0, quota_reset_at = ?
	WHERE quota_reset_at IS NULL OR quota_reset_at <= ?
	`
	res, err := db.Exec(query, tomorrowMidnight, nowStr)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
