package resolver

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Media represents direct media information returned by a Resolver
type Media struct {
	URL      string
	Title    string
	Referer  string
	Duration int
	Width    int
	Height   int
}

// Resolver resolves a media URL to direct downloadable media without downloading
type Resolver interface {
	Name() string
	Platform() string
	Resolve(ctx context.Context, rawURL string) (*Media, error)
}

type health struct {
	fails       int
	lastFail    time.Time
	brokenUntil time.Time
}

// Chain coordinates resolver execution and health tracking per platform
type Chain struct {
	byPlatform map[string][]Resolver
	health     map[string]*health
	mu         sync.RWMutex
}

// NewChain creates a new Chain from the provided resolvers
func NewChain(resolvers ...Resolver) *Chain {
	c := &Chain{
		byPlatform: make(map[string][]Resolver),
		health:     make(map[string]*health),
	}
	for _, r := range resolvers {
		p := r.Platform()
		c.byPlatform[p] = append(c.byPlatform[p], r)
	}
	return c
}

// Resolve iterates through byPlatform[platform], skips broken resolvers,
// marks OK on success, markFail on error, and returns the media or last error.
func (c *Chain) Resolve(ctx context.Context, platform, rawURL string) (*Media, error) {
	c.mu.RLock()
	resolvers := c.byPlatform[platform]
	c.mu.RUnlock()

	if len(resolvers) == 0 {
		return nil, fmt.Errorf("no resolvers registered for platform %s", platform)
	}

	var lastErr error
	for _, r := range resolvers {
		if c.isBroken(r.Name()) {
			continue
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		m, err := r.Resolve(ctx, rawURL)
		if err == nil && m != nil && m.URL != "" {
			c.markOK(r.Name())
			c.markOK(platform)
			return m, nil
		}

		lastErr = err
		c.markFail(r.Name())
	}

	c.markFail(platform)
	if lastErr == nil {
		lastErr = fmt.Errorf("all resolvers failed or broken for platform %s", platform)
	}
	return nil, lastErr
}

func (c *Chain) markFail(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	h, ok := c.health[key]
	if !ok {
		h = &health{}
		c.health[key] = h
	}

	now := time.Now()
	if now.Sub(h.lastFail) > 10*time.Minute {
		h.fails = 1
	} else {
		h.fails++
	}
	h.lastFail = now

	// 3 failures within 10 minutes sets broken for 15 minutes
	if h.fails >= 3 {
		h.brokenUntil = now.Add(15 * time.Minute)
	}
}

func (c *Chain) markOK(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if h, ok := c.health[key]; ok {
		h.fails = 0
		h.brokenUntil = time.Time{}
	}
}

func (c *Chain) isBroken(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if h, ok := c.health[key]; ok {
		return h.brokenUntil.After(time.Now())
	}
	return false
}

// Broken returns whether a platform or resolver is currently broken
func (c *Chain) Broken(platform string) bool {
	return c.isBroken(platform)
}

// Fails returns the current failure count for a platform or resolver
func (c *Chain) Fails(platform string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if h, ok := c.health[platform]; ok {
		return h.fails
	}
	return 0
}
