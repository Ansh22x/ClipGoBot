package license

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"tg-media-bot/internal/db"
)

// Standard plan configurations
type PlanInfo struct {
	Name         string
	DurationDays int
	DailyQuota   int
	Description  string
}

var Plans = map[string]PlanInfo{
	"free": {
		Name:         "Free",
		DurationDays: 0,
		DailyQuota:   5,
		Description:  "5 downloads / day, standard speed",
	},
	"trial": {
		Name:         "Trial",
		DurationDays: 1,
		DailyQuota:   15,
		Description:  "15 downloads / 24 hours trial access",
	},
	"daily": {
		Name:         "Daily Pass",
		DurationDays: 1,
		DailyQuota:   30,
		Description:  "30 downloads / 24 hours",
	},
	"weekly": {
		Name:         "Weekly VIP",
		DurationDays: 7,
		DailyQuota:   60,
		Description:  "60 downloads / day for 7 days",
	},
	"monthly": {
		Name:         "Monthly Pro",
		DurationDays: 30,
		DailyQuota:   120,
		Description:  "120 downloads / day for 30 days",
	},
	"lifetime": {
		Name:         "Lifetime Ultra",
		DurationDays: 36500, // 100 years
		DailyQuota:   999999,
		Description:  "Unlimited downloads forever",
	},
}

// GenerateKey generates a cryptographically secure license key
// Format: PREM-XXXX-XXXX-XXXX
func GenerateKey(prefix string) (string, error) {
	if prefix == "" {
		prefix = "PREM"
	}
	prefix = strings.ToUpper(prefix)

	const charset = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ" // unambiguous characters
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to read crypto rand: %w", err)
	}

	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}

	return fmt.Sprintf("%s-%s-%s-%s", prefix, string(b[0:4]), string(b[4:8]), string(b[8:12])), nil
}

// RedeemResult contains the outcome of redeeming a license key
type RedeemResult struct {
	Plan         string
	DurationDays int
	DailyQuota   int
	ExpiresAt    *time.Time
}

// RedeemKey handles validation, expiration calculation, user plan binding, and key lifecycle update
func RedeemKey(database *db.DB, telegramID int64, rawKey string) (*RedeemResult, error) {
	key := strings.TrimSpace(rawKey)
	if key == "" {
		return nil, errors.New("empty license key")
	}

	lic, err := database.GetLicenseByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup key: %w", err)
	}
	if lic == nil {
		return nil, errors.New("ERR_INVALID_KEY")
	}

	// Validate status
	if lic.Status != "active" {
		return nil, errors.New("ERR_KEY_EXPIRED")
	}

	if lic.ActivationCount >= lic.MaxActivations {
		return nil, errors.New("ERR_KEY_EXPIRED")
	}

	// Fetch current user
	usr, err := database.GetUser(telegramID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}
	if usr == nil {
		return nil, errors.New("user not found")
	}

	planCfg, ok := Plans[lic.Plan]
	if !ok {
		// Custom plan fallback
		planCfg = PlanInfo{
			Name:         strings.Title(lic.Plan),
			DurationDays: lic.DurationDays,
			DailyQuota:   100,
		}
	}

	// Calculate new expires_at
	now := time.Now().UTC()
	var newExpiresAt *time.Time

	if lic.Plan == "lifetime" {
		newExpiresAt = nil
	} else {
		baseTime := now
		// If current subscription is active and not expired, stack duration
		if usr.ExpiresAt != nil && usr.ExpiresAt.After(now) {
			baseTime = *usr.ExpiresAt
		}
		computed := baseTime.AddDate(0, 0, lic.DurationDays)
		newExpiresAt = &computed
	}

	// Update user record
	if err := database.UpdateUserPlan(telegramID, lic.Plan, lic.Key, newExpiresAt, planCfg.DailyQuota); err != nil {
		return nil, fmt.Errorf("failed to update user plan: %w", err)
	}

	// Increment license activations
	if err := database.IncrementLicenseActivation(lic.Key); err != nil {
		return nil, fmt.Errorf("failed to update license activation: %w", err)
	}

	return &RedeemResult{
		Plan:         lic.Plan,
		DurationDays: lic.DurationDays,
		DailyQuota:   planCfg.DailyQuota,
		ExpiresAt:    newExpiresAt,
	}, nil
}

// RedeemGroupKey activates a license key for a whole Telegram group chat
func RedeemGroupKey(database *db.DB, chatID int64, chatTitle string, activatedBy int64, rawKey string) (*RedeemResult, error) {
	key := strings.TrimSpace(rawKey)
	if key == "" {
		return nil, errors.New("empty license key")
	}

	lic, err := database.GetLicenseByKey(key)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup key: %w", err)
	}
	if lic == nil {
		return nil, errors.New("ERR_INVALID_KEY")
	}

	// Validate status
	if lic.Status != "active" {
		return nil, errors.New("ERR_KEY_EXPIRED")
	}

	if lic.ActivationCount >= lic.MaxActivations {
		return nil, errors.New("ERR_KEY_EXPIRED")
	}

	// Get or register group
	grp, err := database.GetOrCreateGroup(chatID, chatTitle)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch group: %w", err)
	}

	planCfg, ok := Plans[lic.Plan]
	groupQuota := 100
	if ok {
		groupQuota = planCfg.DailyQuota * 5
		if groupQuota < 100 {
			groupQuota = 100
		}
		if lic.Plan == "lifetime" {
			groupQuota = 999999
		}
	}

	// Calculate new expires_at
	now := time.Now().UTC()
	var newExpiresAt *time.Time

	if lic.Plan == "lifetime" {
		newExpiresAt = nil
	} else {
		baseTime := now
		if grp.ExpiresAt != nil && grp.ExpiresAt.After(now) {
			baseTime = *grp.ExpiresAt
		}
		computed := baseTime.AddDate(0, 0, lic.DurationDays)
		newExpiresAt = &computed
	}

	// Update group record
	if err := database.UpdateGroupPlan(chatID, lic.Plan, lic.Key, newExpiresAt, groupQuota, activatedBy); err != nil {
		return nil, fmt.Errorf("failed to update group plan: %w", err)
	}

	// Increment license activations
	if err := database.IncrementLicenseActivation(lic.Key); err != nil {
		return nil, fmt.Errorf("failed to update license activation: %w", err)
	}

	return &RedeemResult{
		Plan:         lic.Plan,
		DurationDays: lic.DurationDays,
		DailyQuota:   groupQuota,
		ExpiresAt:    newExpiresAt,
	}, nil
}
