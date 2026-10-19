package bot

import (
	"fmt"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"

	"tg-media-bot/internal/config"
	"tg-media-bot/internal/db"
)

// MiddlewareManager handles auth, rate-limiting, and quota enforcement
type MiddlewareManager struct {
	cfg            *config.Config
	database       *db.DB
	rateLimits     sync.Map // map[int64]time.Time
	cooldownPeriod time.Duration
}

// NewMiddlewareManager creates a new middleware manager
func NewMiddlewareManager(cfg *config.Config, database *db.DB) *MiddlewareManager {
	return &MiddlewareManager{
		cfg:            cfg,
		database:       database,
		cooldownPeriod: time.Duration(cfg.RateLimitCooldownSec) * time.Second,
	}
}

// AuthAndRateLimit wraps a handler with user registration, ban checks, and rate limiting
func (m *MiddlewareManager) AuthAndRateLimit(next func(b *gotgbot.Bot, ctx *ext.Context) error) func(b *gotgbot.Bot, ctx *ext.Context) error {
	return func(b *gotgbot.Bot, ctx *ext.Context) error {
		user := ctx.EffectiveUser
		if user == nil {
			return nil
		}

		// 1. Auth & Registration
		u, err := m.database.GetOrCreateUser(user.Id, user.Username, m.cfg.FreeDailyQuota)
		if err != nil {
			_, _ = ctx.EffectiveMessage.Reply(b, ErrInternalServer, nil)
			return err
		}

		// Ban check
		if u.Status == "banned" {
			_, _ = ctx.EffectiveMessage.Reply(b, ErrBanned, nil)
			return nil
		}

		// 2. Rate Limiting (Admins bypass rate limits)
		if !m.cfg.IsAdmin(user.Id) {
			now := time.Now()
			if lastVal, ok := m.rateLimits.Load(user.Id); ok {
				lastTime := lastVal.(time.Time)
				if now.Sub(lastTime) < m.cooldownPeriod {
					_, _ = ctx.EffectiveMessage.Reply(b, ErrRateLimited, nil)
					return nil
				}
			}
			m.rateLimits.Store(user.Id, now)
		}

		return next(b, ctx)
	}
}

// CheckQuota verifies if user has downloads remaining today
func (m *MiddlewareManager) CheckQuota(telegramID int64) (*db.User, error) {
	u, err := m.database.GetUser(telegramID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("user not found")
	}

	// 1. Check expiration for paid plans
	if u.Plan != "free" && u.Plan != "lifetime" && u.ExpiresAt != nil {
		if time.Now().UTC().After(*u.ExpiresAt) {
			// Expired: downgrade to free
			_ = m.database.UpdateUserPlan(telegramID, "free", "", nil, m.cfg.FreeDailyQuota)
			u.Plan = "free"
			u.DailyQuota = m.cfg.FreeDailyQuota
		}
	}

	// 2. Check daily quota
	if u.DailyDownloads >= u.DailyQuota {
		return u, fmt.Errorf("ERR_QUOTA_EXCEEDED")
	}

	return u, nil
}

// RequireAdmin verifies that the invoking user is in the configured admin list
func (m *MiddlewareManager) RequireAdmin(next func(b *gotgbot.Bot, ctx *ext.Context) error) func(b *gotgbot.Bot, ctx *ext.Context) error {
	return func(b *gotgbot.Bot, ctx *ext.Context) error {
		user := ctx.EffectiveUser
		if user == nil || !m.cfg.IsAdmin(user.Id) {
			if ctx.EffectiveMessage != nil {
				_, _ = ctx.EffectiveMessage.Reply(b, ErrUnauthorized, nil)
			}
			return nil
		}
		return next(b, ctx)
	}
}
