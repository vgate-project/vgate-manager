package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// Logger logs each request via logrus (method, path, status, latency, client).
// When the request carries an authenticated node (set by NodeAuth on the
// /api/v1/server group), the node's id is included as node_id.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)
		fields := log.Fields{
			"method":  c.Request.Method,
			"path":    c.Request.URL.Path,
			"status":  c.Writer.Status(),
			"latency": latency.Round(time.Millisecond),
			"client":  c.ClientIP(),
		}
		if v, ok := c.Get("node"); ok {
			if n, ok := v.(*model.Node); ok {
				fields["node_id"] = n.ID
			}
		}
		log.WithFields(fields).Info("request")
	}
}
