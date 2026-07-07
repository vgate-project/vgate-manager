package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// CORS returns a gin middleware that enforces CORS based on the
// cors.allowed_origins SystemConfig value, read from the database on every
// request so admin edits take effect immediately (no restart). If the key is
// missing or the DB errors, it falls back to ["*"] (allow all, no credentials),
// matching config.DefaultConfig().
func CORS(sysCfg *service.SystemConfigService) gin.HandlerFunc {
	return func(c *gin.Context) {
		origins := loadAllowedOrigins(sysCfg)

		origin := c.GetHeader("Origin")
		if len(origins) == 0 || (len(origins) == 1 && origins[0] == "*") {
			// Wildcard: per CORS spec we must not echo a credentialed origin.
			c.Header("Access-Control-Allow-Origin", "*")
		} else if origin != "" && originAllowed(origin, origins) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func loadAllowedOrigins(sysCfg *service.SystemConfigService) []string {
	v, err := sysCfg.Get(service.CfgKeyCORSAllowedOrigins)
	if err != nil || v == "" {
		return config.DefaultConfig().CORS.AllowedOrigins
	}
	var origins []string
	if err := json.Unmarshal([]byte(v), &origins); err != nil {
		log.Warnf("cors: invalid %s=%q, fall back to allow-all: %v", service.CfgKeyCORSAllowedOrigins, v, err)
		return config.DefaultConfig().CORS.AllowedOrigins
	}
	return origins
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}
