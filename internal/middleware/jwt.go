package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

// extractBearer pulls the token out of an "Authorization: Bearer <token>" header.
func extractBearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// verifyBearer extracts + verifies the bearer token, aborting with 401 on
// failure. Returns nil claims if aborted (caller should return immediately).
func verifyBearer(svc *service.AuthService, c *gin.Context) *service.Claims {
	tok := extractBearer(c)
	if tok == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid Authorization header"})
		return nil
	}
	claims, err := svc.VerifyToken(tok)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return nil
	}
	return claims
}

// RequireAdmin verifies an admin JWT and stores admin_id/role/username in context.
func RequireAdmin(svc *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := verifyBearer(svc, c)
		if claims == nil {
			return
		}
		if claims.Type != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin access required"})
			return
		}
		c.Set("admin_id", claims.AdminID)
		c.Set("role", claims.Role)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// RequireSuperAdmin verifies an admin JWT with role "super_admin".
func RequireSuperAdmin(svc *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := verifyBearer(svc, c)
		if claims == nil {
			return
		}
		if claims.Type != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin access required"})
			return
		}
		if claims.Role != "super_admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "super admin access required"})
			return
		}
		c.Set("admin_id", claims.AdminID)
		c.Set("role", claims.Role)
		c.Set("username", claims.Username)
		c.Next()
	}
}

// RequireUser verifies a user JWT and stores user_id in context.
func RequireUser(svc *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := verifyBearer(svc, c)
		if claims == nil {
			return
		}
		if claims.Type != "user" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "user access required"})
			return
		}
		c.Set("user_id", claims.UserID)
		c.Set("user_level", claims.Level)
		c.Next()
	}
}
