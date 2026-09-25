package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/utils/clock"
)

// CachedAuthenticator wraps a KubeAuthenticator with TTL-based caching
// for both TokenReview and SubjectAccessReview results to reduce API server load.
type CachedAuthenticator struct {
	authenticator *KubeAuthenticator
	tokenCache    *ttlCache
	authzCache    *ttlCache
	cacheTTL      time.Duration
}

// NewCachedAuthenticator creates a CachedAuthenticator with the specified TTL.
func NewCachedAuthenticator(auth *KubeAuthenticator, cacheTTL time.Duration) *CachedAuthenticator {
	return &CachedAuthenticator{
		authenticator: auth,
		tokenCache:    newTTLCache(cacheTTL),
		authzCache:    newTTLCache(cacheTTL),
		cacheTTL:      cacheTTL,
	}
}

// Authenticate validates a bearer token using the cache.
func (c *CachedAuthenticator) Authenticate(ctx context.Context, token string) (*authenticationv1.TokenReviewStatus, error) {
	key := tokenCacheKey(token)

	if cached, ok := c.tokenCache.get(key); ok {
		entry := cached.(authnResult)
		return entry.status, entry.err
	}

	status, err := c.authenticator.Authenticate(ctx, token)
	c.tokenCache.set(key, authnResult{status: status, err: err})
	return status, err
}

// Authorize checks authorization using the cache.
func (c *CachedAuthenticator) Authorize(ctx context.Context, username string, groups []string, verb string, path string) (bool, string, error) {
	key := authzCacheKey(username, groups, verb, path)

	if cached, ok := c.authzCache.get(key); ok {
		result := cached.(authzResult)
		return result.allowed, result.reason, nil
	}

	allowed, reason, err := c.authenticator.Authorize(ctx, username, groups, verb, path)
	if err != nil {
		return false, "", err
	}

	c.authzCache.set(key, authzResult{allowed: allowed, reason: reason})
	return allowed, reason, nil
}

// authnResult holds cached authentication results (including failures).
type authnResult struct {
	status *authenticationv1.TokenReviewStatus
	err    error
}

// authzResult holds cached authorization results.
type authzResult struct {
	allowed bool
	reason  string
}

// tokenCacheKey generates a cache key for tokens.
func tokenCacheKey(token string) string {
	h := sha256.New()
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))
}

// authzCacheKey generates a cache key for authorization requests.
func authzCacheKey(username string, groups []string, verb string, path string) string {
	h := sha256.New()
	h.Write([]byte(username))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(groups, ",")))
	h.Write([]byte{0})
	h.Write([]byte(verb))
	h.Write([]byte{0})
	h.Write([]byte(path))
	return hex.EncodeToString(h.Sum(nil))
}

const maxCacheSize = 4096

// ttlCache is a simple thread-safe TTL cache with a size cap.
type ttlCache struct {
	mu    sync.RWMutex
	items map[string]*ttlCacheItem
	ttl   time.Duration
	clock clock.Clock
}

type ttlCacheItem struct {
	value      interface{}
	expiration time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{
		items: make(map[string]*ttlCacheItem),
		ttl:   ttl,
		clock: clock.RealClock{},
	}
}

func (c *ttlCache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[key]
	if !ok {
		return nil, false
	}

	if c.clock.Now().After(item.expiration) {
		return nil, false
	}

	return item.value, true
}

func (c *ttlCache) set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = &ttlCacheItem{
		value:      value,
		expiration: c.clock.Now().Add(c.ttl),
	}

	if len(c.items) > maxCacheSize {
		c.evictExpiredLocked()
	}
}

func (c *ttlCache) evictExpiredLocked() {
	now := c.clock.Now()
	for k, v := range c.items {
		if now.After(v.expiration) {
			delete(c.items, k)
		}
	}
	// If still over capacity after evicting expired entries, drop oldest half
	if len(c.items) > maxCacheSize {
		count := 0
		target := len(c.items) / 2
		for k := range c.items {
			delete(c.items, k)
			count++
			if count >= target {
				break
			}
		}
	}
}
