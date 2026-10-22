package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// License represents a record in the licenses table
type License struct {
	ID              int64
	Key             string
	Plan            string
	DurationDays    int
	MaxActivations  int
	ActivationCount int
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	Status          string
}

// CreateLicense inserts a new license key into the database
func (db *DB) CreateLicense(key, plan string, durationDays, maxActivations int) error {
	query := `
	INSERT INTO licenses (key, plan, duration_days, max_activations, activation_count, status)
	VALUES (?, ?, ?, ?, 0, 'active')
	`
	_, err := db.Exec(query, key, plan, durationDays, maxActivations)
	return err
}

// GetLicenseByKey retrieves a license record by its key string
func (db *DB) GetLicenseByKey(key string) (*License, error) {
	query := `
	SELECT id, key, plan, duration_days, max_activations, activation_count, created_at, expires_at, status
	FROM licenses WHERE key = ?
	`
	row := db.QueryRow(query, key)

	var lic License
	var createdAtStr string
	var expiresAtStr sql.NullString

	err := row.Scan(
		&lic.ID,
		&lic.Key,
		&lic.Plan,
		&lic.DurationDays,
		&lic.MaxActivations,
		&lic.ActivationCount,
		&createdAtStr,
		&expiresAtStr,
		&lic.Status,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query license: %w", err)
	}

	if t := ParseFlexibleTime(createdAtStr); t != nil {
		lic.CreatedAt = *t
	}
	if expiresAtStr.Valid {
		lic.ExpiresAt = ParseFlexibleTime(expiresAtStr.String)
	}

	return &lic, nil
}

// IncrementLicenseActivation increments the activation count and marks as redeemed if max reached
func (db *DB) IncrementLicenseActivation(key string) error {
	query := `
	UPDATE licenses 
	SET activation_count = activation_count + 1,
	    status = CASE WHEN activation_count + 1 >= max_activations THEN 'redeemed' ELSE status END
	WHERE key = ?
	`
	_, err := db.Exec(query, key)
	return err
}

// RevokeLicense marks a license key as revoked
func (db *DB) RevokeLicense(key string) error {
	query := `UPDATE licenses SET status = 'revoked' WHERE key = ?`
	res, err := db.Exec(query, key)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("license key not found")
	}
	return nil
}

// ListLicenses retrieves licenses filtered by status (or all if status is "")
func (db *DB) ListLicenses(status string, limit int) ([]*License, error) {
	if limit <= 0 {
		limit = 50
	}

	var rows *sql.Rows
	var err error

	if status != "" && status != "all" {
		query := `
		SELECT id, key, plan, duration_days, max_activations, activation_count, created_at, expires_at, status
		FROM licenses WHERE status = ? ORDER BY id DESC LIMIT ?
		`
		rows, err = db.Query(query, status, limit)
	} else {
		query := `
		SELECT id, key, plan, duration_days, max_activations, activation_count, created_at, expires_at, status
		FROM licenses ORDER BY id DESC LIMIT ?
		`
		rows, err = db.Query(query, limit)
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*License
	for rows.Next() {
		var lic License
		var createdAtStr string
		var expiresAtStr sql.NullString

		if err := rows.Scan(
			&lic.ID,
			&lic.Key,
			&lic.Plan,
			&lic.DurationDays,
			&lic.MaxActivations,
			&lic.ActivationCount,
			&createdAtStr,
			&expiresAtStr,
			&lic.Status,
		); err != nil {
			return nil, err
		}
		lic.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		if expiresAtStr.Valid && expiresAtStr.String != "" {
			t, _ := time.Parse(time.RFC3339, expiresAtStr.String)
			lic.ExpiresAt = &t
		}
		list = append(list, &lic)
	}

	return list, nil
}
