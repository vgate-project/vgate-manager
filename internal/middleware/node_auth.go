package middleware

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// NodeAuth authenticates a vgate server (node) via the node_id (public) and
// token (secret) query params. The node is looked up by node_id, then the
// token is compared in constant time. On success the *model.Node is stored in
// the gin context under the "node" key.
func NodeAuth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		nodeID := c.Query("node_id")
		token := c.Query("token")
		if nodeID == "" || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing node_id or token"})
			return
		}
		var node model.Node
		if err := db.Where("id = ? AND enabled = ?", nodeID, true).First(&node).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(node.Token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Set("node", &node)
		c.Next()
		// Any successful node request refreshes liveness, not just traffic
		// reports — so a node that polls /config or /users without generating
		// traffic is still considered online.
		if c.Writer.Status() < 400 {
			now := time.Now()
			if err := db.Model(&model.Node{}).Where("id = ?", node.ID).
				Update("last_seen_at", now).Error; err != nil {
				log.Warnf("refresh node last_seen failed (node=%s): %v", node.ID, err)
			}
		}
	}
}
