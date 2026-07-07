package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimit returns a per-client-IP token-bucket limiter allowing `rps`
// requests/second with burst `burst`. Exceeding the bucket returns 429.
//
// The per-IP limiter map is bounded: when it exceeds maxEntries, the whole map
// is reset (crude but prevents unbounded growth under IP-spoofing attack).
// For high-scale deployments, replace with a TTL-based cache.
func RateLimit(rps, burst int) gin.HandlerFunc {
	const maxEntries = 10000
	var (
		mu       sync.Mutex
		limiters = make(map[string]*rate.Limiter)
	)
	get := func(ip string) *rate.Limiter {
		mu.Lock()
		defer mu.Unlock()
		if len(limiters) > maxEntries {
			limiters = make(map[string]*rate.Limiter)
		}
		l, ok := limiters[ip]
		if !ok {
			l = rate.NewLimiter(rate.Limit(rps), burst)
			limiters[ip] = l
		}
		return l
	}
	return func(c *gin.Context) {
		if !get(c.ClientIP()).Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			return
		}
		c.Next()
	}
}
